package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Every model boundary is held until the test explicitly permits completion.
type inboxBarrierLLM struct {
	requests      chan llm.Request
	release       chan []protocol.StreamChunk
	canceled      chan struct{}
	cancelRelease chan struct{}
}

func (*inboxBarrierLLM) ResolveModel(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	return llm.ModelInfo{Ref: ref, ContextWindow: 4096}, nil
}

func (a *inboxBarrierLLM) Stream(ctx context.Context, r llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	return func(yield func(protocol.StreamChunk, error) bool) {
		a.requests <- r.Clone()

		select {
		case chunks := <-a.release:
			for _, chunk := range chunks {
				if !yield(chunk, nil) {
					return
				}
			}
		case <-ctx.Done():
			if a.canceled != nil {
				close(a.canceled)
				<-a.cancelRelease
			}
			yield(protocol.StreamChunk{}, ctx.Err())
		}
	}
}

type inboxQi struct {
	a          Agent
	service    *fixtureController
	current    session.Session
	llm        *inboxBarrierLLM
	middleware *agentmiddleware.Runtime
	tools      *tools.Runtime
}

func newInboxQi(t *testing.T, disk bool, pre func(context.Context, agentmiddleware.StepInput) (agentmiddleware.StepInput, error)) *inboxQi {
	t.Helper()
	var store session.Store = session.NewRuntime(session.RuntimeOptions{})

	current, err := store.Create(context.Background(), session.CreateRequest{ID: "inbox-fixture"})
	if err != nil {
		t.Fatal(err)
	}

	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() {
		middleware.Close()
		toolRuntime.Close()
		llmRuntime.Close()
		prompts.Close()
	})
	barrier := &inboxBarrierLLM{
		requests: make(chan llm.Request, 16),
		release:  make(chan []protocol.StreamChunk, 16),
	}
	owned := newTestOwner("inbox-fixture", 1)
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  barrier,
	}); err != nil {
		t.Fatal(err)
	}

	if pre != nil {
		if _, err := middleware.Register(owned, agentmiddleware.Middleware{ID: "pre", PreStep: pre}); err != nil {
			t.Fatal(err)
		}
	}

	loop := agentloop.New(runprep.Coordinator{
		Prompts:    prompts,
		LLM:        llmRuntime,
		Tools:      toolRuntime,
		Middleware: middleware,
	})
	service := newFixtureController(loop, store)
	t.Cleanup(service.Close)

	return &inboxQi{
		a:          createTestAgent(t, service, current.ID()),
		service:    service,
		current:    current,
		llm:        barrier,
		middleware: middleware,
		tools:      toolRuntime,
	}
}

func inboxUser(id string) protocol.Message {
	return protocol.NewTextMessage(protocol.MessageID(id), protocol.RoleUser, protocol.SourceUser, id)
}

func messageIDs(messages []protocol.Message) []string {
	ids := []string{}

	for _, m := range messages {
		if m.Role == protocol.RoleUser {
			ids = append(ids, string(m.ID))
		}
	}

	return ids
}

func inboxStop() []protocol.StreamChunk {
	return []protocol.StreamChunk{
		{
			Type:  protocol.ChunkTextDelta,
			Index: 0,
			Text:  "ok",
		},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
	}
}

func nextInboxRequest(t *testing.T, h *inboxQi) llm.Request {
	t.Helper()

	select {
	case r := <-h.llm.requests:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no model request")
		return llm.Request{}
	}
}

func inboxPending(t *testing.T, a Agent, target string) []string {
	t.Helper()
	p, err := a.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return messageIDs(p[target])
}

func TestInjectFollowupAndSteerBatchOrder(t *testing.T) {
	for _, disk := range []bool{false} {
		t.Run(fmt.Sprintf("jsonl=%t", disk), func(t *testing.T) {
			h := newInboxQi(t, disk, nil)
			ctx := context.Background()
			if err := h.a.Inject(ctx, inboxUser("injected")); err != nil {
				t.Fatal(err)
			}

			if h.a.Status() != StatusIdle || len(h.llm.requests) != 0 {
				t.Fatal("inject woke idle")
			}

			if err := h.a.Followup(ctx, inboxUser("first")); err != nil {
				t.Fatal(err)
			}

			first := nextInboxRequest(t, h)
			if got := messageIDs(first.Messages); !reflect.DeepEqual(got, []string{"injected", "first"}) {
				t.Fatal(got)
			}

			if err := h.a.Followup(ctx, inboxUser("second")); err != nil {
				t.Fatal(err)
			}

			if err := h.a.Steer(ctx, inboxUser("steering")); err != nil {
				t.Fatal(err)
			}

			if got := messageIDs(first.Messages); !reflect.DeepEqual(got, []string{"injected", "first"}) {
				t.Fatal("changed in-flight request", got)
			}

			h.llm.release <- inboxStop()
			step2 := nextInboxRequest(t, h)
			if got := messageIDs(step2.Messages); !reflect.DeepEqual(got, []string{"injected", "first", "steering"}) {
				t.Fatal(got)
			}

			h.llm.release <- inboxStop()
			turn2 := nextInboxRequest(t, h)
			if got := messageIDs(turn2.Messages); !reflect.DeepEqual(got, []string{"injected", "first", "steering", "second"}) {
				t.Fatal(got)
			}

			h.llm.release <- inboxStop()
			awaitTestAgentIdle(t, h.a)
			starts, steps := []protocol.TurnID{}, []protocol.StepData{}

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.TurnStarted {
					starts = append(starts, protocol.EventPayload[protocol.TurnData](e).Turn)
				}

				if e.Type == protocol.StepStarted {
					steps = append(steps, protocol.EventPayload[protocol.StepData](e))
				}
			}

			if !reflect.DeepEqual(starts, []protocol.TurnID{1, 2}) ||
				!reflect.DeepEqual(steps, []protocol.StepData{{Turn: 1, Step: 1}, {Turn: 1, Step: 2}, {Turn: 2, Step: 1}}) {
				t.Fatal(starts, steps)
			}

			p, err := h.a.Pending(ctx)
			if err != nil || len(p[contracts.NextStep])+len(p[contracts.NextTurn]) != 0 {
				t.Fatal(p, err)
			}
		})
	}
}

func TestSteerIdleAndPreStepBoundary(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	batches := make(chan []string, 4)
	h := newInboxQi(t, false, func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
		batches <- messageIDs(in.Messages)
		once.Do(func() {
			close(entered)

			select {
			case <-release:
			case <-ctx.Done():
			}
		})

		return in, ctx.Err()
	})
	ctx := context.Background()
	if err := h.a.Steer(ctx, inboxUser("first")); err != nil {
		t.Fatal(err)
	}

	waitTestSignal(t, entered)
	if err := h.a.Inject(ctx, inboxUser("during-pre-step")); err != nil {
		t.Fatal(err)
	}

	close(release)
	if got := messageIDs(nextInboxRequest(t, h).Messages); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatal(got)
	}

	h.llm.release <- inboxStop()
	if got := messageIDs(nextInboxRequest(t, h).Messages); !reflect.DeepEqual(got, []string{"first", "during-pre-step"}) {
		t.Fatal(got)
	}

	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	if !reflect.DeepEqual(<-batches, []string{"first"}) || !reflect.DeepEqual(<-batches, []string{"during-pre-step"}) {
		t.Fatal("pre-step batch was not frozen")
	}
}

func TestRejectConsumesClaimWithoutContextOrDiscard(t *testing.T) {
	for _, disk := range []bool{false} {
		t.Run(fmt.Sprintf("jsonl=%t", disk), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			h := newInboxQi(t, disk, func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
				close(entered)

				select {
				case <-release:
				case <-ctx.Done():
				}

				in.Kind = "reject"

				return in, ctx.Err()
			})
			if err := h.a.Followup(context.Background(), inboxUser("claimed")); err != nil {
				t.Fatal(err)
			}

			waitTestSignal(t, entered)
			if err := h.a.Inject(context.Background(), inboxUser("unclaimed")); err != nil {
				t.Fatal(err)
			}

			close(release)
			awaitTestAgentIdle(t, h.a)
			if got := inboxPending(t, h.a, contracts.NextStep); !reflect.DeepEqual(got, []string{"unclaimed"}) {
				t.Fatal(got)
			}

			events := sessionEvents(t, h.current)

			for _, e := range events {
				if e.Type == protocol.UserMessage || e.Type == protocol.StepStarted {
					t.Fatal("rejected batch entered context")
				}

				if e.Type == protocol.InboxSpliced && protocol.EventPayload[protocol.InboxSpliceData](e).Outcome != "" {
					t.Fatal("claim was discarded")
				}
			}

			end := protocol.EventPayload[protocol.TurnEndData](events[len(events)-1])
			if end.Reason.Kind != "blocked" {
				t.Fatal(end)
			}
		})
	}
}

func TestCancelKeepClearAndWakingAfterAbort(t *testing.T) {
	for _, keep := range []bool{false, true} {
		for _, disk := range []bool{false} {
			t.Run(fmt.Sprintf("keep=%t/jsonl=%t", keep, disk), func(t *testing.T) {
				h := newInboxQi(t, disk, nil)
				ctx := context.Background()
				h.llm.canceled = make(chan struct{})
				h.llm.cancelRelease = make(chan struct{})
				if err := h.a.Followup(ctx, inboxUser("active")); err != nil {
					t.Fatal(err)
				}

				nextInboxRequest(t, h)
				if err := h.a.Inject(ctx, inboxUser("step-pending")); err != nil {
					t.Fatal(err)
				}

				if err := h.a.Followup(ctx, inboxUser("turn-pending")); err != nil {
					t.Fatal(err)
				}

				if err := h.a.CancelActivity(CancelRequested, CancelOptions{KeepInbox: keep}); err != nil {
					t.Fatal(err)
				}

				waitTestSignal(t, h.llm.canceled)
				step, turn := inboxPending(t, h.a, contracts.NextStep), inboxPending(t, h.a, contracts.NextTurn)
				if keep {
					if !reflect.DeepEqual(step, []string{"step-pending"}) || !reflect.DeepEqual(turn, []string{"turn-pending"}) {
						t.Fatal(step, turn)
					}
				} else if len(step)+len(turn) != 0 {
					t.Fatal(step, turn)
				}

				if err := h.a.Steer(ctx, inboxUser("after-abort")); err != nil {
					t.Fatal(err)
				}

				if got := inboxPending(t, h.a, contracts.NextTurn); got[len(got)-1] != "after-abort" {
					t.Fatal("abort wake did not reroute", got)
				}

				close(h.llm.cancelRelease)
				r := nextInboxRequest(t, h)
				expected := []string{"active", "after-abort"}
				if keep {
					expected = []string{"active", "step-pending", "turn-pending"}
				}

				if !reflect.DeepEqual(messageIDs(r.Messages), expected) {
					t.Fatal(messageIDs(r.Messages), expected)
				}

				h.llm.release <- inboxStop()
				if keep {
					nextInboxRequest(t, h)
					h.llm.release <- inboxStop()
				}

				awaitTestAgentIdle(t, h.a)
				clearTargets := []string{}

				for _, e := range sessionEvents(t, h.current) {
					if e.Type == protocol.InboxSpliced {
						d := protocol.EventPayload[protocol.InboxSpliceData](e)
						if d.Outcome == "canceled" {
							clearTargets = append(clearTargets, d.Target)
						}
					}
				}

				if keep && len(clearTargets) != 0 ||
					!keep && !reflect.DeepEqual(clearTargets, []string{contracts.NextStep, contracts.NextTurn}) {
					t.Fatal(clearTargets)
				}
			})
		}
	}
}

func TestKeepInboxDoesNotWakeOrPrecancelFuture(t *testing.T) {
	h := newInboxQi(t, false, nil)
	ctx := context.Background()
	if err := h.a.Inject(ctx, inboxUser("pending")); err != nil {
		t.Fatal(err)
	}

	if err := h.a.CancelActivity(CancelRequested, CancelOptions{KeepInbox: true}); err != nil {
		t.Fatal(err)
	}

	if h.a.Status() != StatusIdle || len(h.llm.requests) != 0 {
		t.Fatal("idle cancel woke work")
	}

	if err := h.a.Followup(ctx, inboxUser("wake")); err != nil {
		t.Fatal(err)
	}

	if got := messageIDs(nextInboxRequest(t, h).Messages); !reflect.DeepEqual(got, []string{"pending", "wake"}) {
		t.Fatal(got)
	}

	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
}

func TestSubmitQueuesAndObservesEachTurn(t *testing.T) {
	h := newInboxQi(t, false, nil)
	ctx := context.Background()
	firstID, first, err := h.a.Submit(ctx, Input{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}

	nextInboxRequest(t, h)
	secondID, second, err := h.a.Submit(ctx, Input{Text: "second"})
	if err != nil || secondID == firstID {
		t.Fatal(secondID, err)
	}

	h.llm.release <- inboxStop()
	nextInboxRequest(t, h)
	h.llm.release <- inboxStop()

	for index, stream := range []iter.Seq2[contracts.DriverEvent, error]{first, second} {
		id := firstID
		if index == 1 {
			id = secondID
		}

		kinds := []string{}

		for e, err := range stream {
			if err != nil {
				t.Fatal(err)
			}

			if e.InvocationID != id {
				t.Fatal(e.InvocationID, id)
			}

			kinds = append(kinds, e.Type)
		}

		if len(kinds) < 2 || kinds[len(kinds)-1] != "ended" {
			t.Fatal(kinds)
		}
	}

	awaitTestAgentIdle(t, h.a)
}

func TestInputAtToolBoundary(t *testing.T) {
	h := newInboxQi(t, false, nil)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	_, err := h.tools.Register(newTestOwner("tool", 1), tools.Definition{
		Name:        "barrier",
		Description: "barrier",
		Parameters:  tools.JSONSchema(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ tools.Execution) (tools.ExecutionResult, error) {
			close(entered)

			select {
			case <-release:
			case <-ctx.Done():
				return tools.FailureResult("cancel", ctx.Err()), ctx.Err()
			}

			return tools.ExecutionResult{
				Value:   []byte(`null`),
				Content: []protocol.ContentBlock{protocol.TextBlock{Text: "tool done"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.a.Followup(ctx, inboxUser("first")); err != nil {
		t.Fatal(err)
	}

	nextInboxRequest(t, h)
	h.llm.release <- []protocol.StreamChunk{
		{
			Type:  protocol.ChunkBlockEnd,
			Index: 0,
			Block: protocol.ToolCallBlock{
				ToolCall: protocol.ToolCall{
					ID:        "call",
					Name:      "barrier",
					Arguments: `{}`,
				},
			},
		},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
	}
	waitTestSignal(t, entered)
	if err := h.a.Steer(ctx, inboxUser("tool-steer")); err != nil {
		t.Fatal(err)
	}

	if err := h.a.Followup(ctx, inboxUser("later-turn")); err != nil {
		t.Fatal(err)
	}

	close(release)
	request := nextInboxRequest(t, h)
	if !reflect.DeepEqual(messageIDs(request.Messages), []string{"first", "tool-steer"}) {
		t.Fatal(messageIDs(request.Messages))
	}

	toolResults := 0

	for _, m := range request.Messages {
		if m.Role == protocol.RoleTool {
			toolResults++
		}
	}

	if toolResults != 1 {
		t.Fatal("tool result absent")
	}

	h.llm.release <- inboxStop()
	nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
}

type inboxRecorder struct {
	mu      sync.Mutex
	current session.Session
	events  []contracts.AgentInboxObservation
	err     error
}

func (*inboxRecorder) Created(context.Context, contracts.AgentCreated) error { return nil }

func (*inboxRecorder) Status(contracts.AgentStatusObservation) {}

func (*inboxRecorder) Disposed(context.Context, contracts.AgentDisposed) {}

func (o *inboxRecorder) Inbox(event contracts.AgentInboxObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// The local notification must already have a corresponding committed splice.
	found := false

	for e, err := range o.current.Events(context.Background(), -1) {
		if err != nil {
			o.err = err
			return
		}

		if e.Type != protocol.InboxSpliced {
			continue
		}

		d := protocol.EventPayload[protocol.InboxSpliceData](e)
		if event.Kind == "inserted" {
			for _, m := range d.Inserted {
				if m.ID == event.Message.ID {
					found = true
				}
			}
		} else if d.RemovedCount != nil {
			if event.Kind == "claimed" && d.Outcome == "" || event.Kind == "discarded" && d.Outcome == "canceled" {
				found = true
			}
		}
	}

	if !found {
		o.err = errors.New("local notification before commit")
	}

	o.events = append(o.events, event.Clone())
}

func (o *inboxRecorder) kinds() []string {
	o.mu.Lock()
	defer o.mu.Unlock()

	result := []string{}

	for _, e := range o.events {
		result = append(result, e.Kind+":"+string(e.Message.ID))
	}

	return result
}

// Activity completion and passive delivery have separate completion signals.
// Register before Send, then consume through the actual idle notification.
func subscribeInbox(t *testing.T, a Agent) AgentSubscription {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	sub, err := a.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}
func (o *inboxRecorder) receiveUntilIdle(t *testing.T, sub AgentSubscription) {
	t.Helper()
	idle := false
	for note, err := range sub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if note.Inbox != nil {
			o.Inbox(note.Inbox.Clone())
		}
		if note.Status != nil && note.Status.Status == StatusIdle {
			idle = true
			break
		}
	}
	if !idle {
		t.Fatal("observation ended before idle")
	}
	if sub.Dropped() != 0 {
		t.Fatal("Inbox observation lost events", sub.Dropped())
	}
}

func TestNotificationsRejectAndExplicitSplice(t *testing.T) {
	h := newInboxQi(t, false, func(_ context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
		in.Kind = "reject"
		return in, nil
	})
	o := &inboxRecorder{current: h.current}
	sub := subscribeInbox(t, h.a)
	ctx := context.Background()
	if err := h.a.Inject(ctx, inboxUser("removed")); err != nil {
		t.Fatal(err)
	}

	if _, err := h.a.Splice(ctx, contracts.NextStep, -1, 99, []protocol.Message{inboxUser("claimed")}); err != nil {
		t.Fatal(err)
	}

	if err := h.a.Followup(ctx, inboxUser("turn")); err != nil {
		t.Fatal(err)
	}

	awaitTestAgentIdle(t, h.a)
	o.receiveUntilIdle(t, sub)
	if got := o.kinds(); !reflect.DeepEqual(
		got,
		[]string{
			"inserted:removed",
			"discarded:removed",
			"inserted:claimed",
			"inserted:turn",
			"claimed:claimed",
			"claimed:turn",
		},
	) {
		t.Fatal(got)
	}

	if o.err != nil {
		t.Fatal(o.err)
	}

	for _, event := range o.events {
		if event.Kind == "claimed" && event.Turn != 1 {
			t.Fatal(event)
		}
	}
}

type inboxFailWriter struct {
	contracts.InboxSession
	mu   sync.Mutex
	fail bool
}

func (s *inboxFailWriter) setFail(fail bool) {
	s.mu.Lock()
	s.fail = fail
	s.mu.Unlock()
}

func (s *inboxFailWriter) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	s.mu.Lock()
	fail := s.fail
	s.mu.Unlock()
	if fail && e.Type == protocol.InboxSpliced {
		return -1, errors.New("inbox append failed")
	}

	return s.InboxSession.Append(ctx, e)
}

func TestFailedSendClearAndClaimAreNotPublished(t *testing.T) {
	h := newInboxQi(t, false, nil)
	ctx := context.Background()
	writer := &inboxFailWriter{InboxSession: h.current}
	h.a.(*loopAgent).inputInbox = session.NewInbox(writer)
	o := &inboxRecorder{current: h.current}
	sub := subscribeInbox(t, h.a)
	writer.setFail(true)
	if err := h.a.Followup(ctx, inboxUser("failed-send")); err == nil {
		t.Fatal("send succeeded")
	}

	if h.a.Status() != StatusIdle || len(o.kinds()) != 0 || h.current.Watermark() != -1 {
		t.Fatal("failed send became visible")
	}

	writer.setFail(false)
	if err := h.a.Followup(ctx, inboxUser("active")); err != nil {
		t.Fatal(err)
	}

	nextInboxRequest(t, h)
	if err := h.a.Inject(ctx, inboxUser("pending")); err != nil {
		t.Fatal(err)
	}

	writer.setFail(true)
	if err := h.a.CancelActivity(CancelRequested); err == nil {
		t.Fatal("failed clear succeeded")
	}

	a := h.a.(*loopAgent)
	a.mu.Lock()
	canceled := a.activityCtx.Err() != nil
	a.mu.Unlock()
	if canceled {
		t.Fatal("abort preceded failed clear")
	}

	if !reflect.DeepEqual(inboxPending(t, h.a, contracts.NextStep), []string{"pending"}) {
		t.Fatal("failed clear changed state")
	}

	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	o.receiveUntilIdle(t, sub)
	if got := o.kinds(); !reflect.DeepEqual(got, []string{"inserted:active", "claimed:active", "inserted:pending"}) {
		t.Fatal(got)
	}

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.UserMessage && protocol.EventPayload[protocol.Message](e).ID == "pending" {
			t.Fatal("failed claim entered context")
		}
	}

	writer.setFail(false)
}

func TestFailedPreparationConsumesClaimWithoutContext(t *testing.T) {
	h := newInboxQi(t, false, nil)
	_, err := h.middleware.Register(newTestOwner("request-fail", 1), agentmiddleware.Middleware{
		ID: "request-fail",
		Request: func(context.Context, agentmiddleware.Request) (agentmiddleware.Request, error) {
			return agentmiddleware.Request{}, errors.New("request preparation failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	o := &inboxRecorder{current: h.current}
	sub := subscribeInbox(t, h.a)
	if err := h.a.Followup(context.Background(), inboxUser("claimed")); err != nil {
		t.Fatal(err)
	}

	awaitTestAgentIdle(t, h.a)
	o.receiveUntilIdle(t, sub)
	if !reflect.DeepEqual(o.kinds(), []string{"inserted:claimed", "claimed:claimed"}) {
		t.Fatal(o.kinds())
	}

	if len(inboxPending(t, h.a, contracts.NextTurn)) != 0 {
		t.Fatal("failed batch requeued")
	}

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.UserMessage {
			t.Fatal("failed preparation entered context")
		}
	}

	if len(h.llm.requests) != 0 {
		t.Fatal("preparation failure dispatched")
	}
}

func TestConcurrentInputAndCrossQueueDedup(t *testing.T) {
	h := newInboxQi(t, false, nil)
	ctx := context.Background()
	var workers sync.WaitGroup

	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()

			if err := h.a.Inject(ctx, inboxUser(fmt.Sprintf("item-%d", i))); err != nil {
				t.Error(err)
			}
		}(i)
	}

	workers.Wait()
	p, err := h.a.Pending(ctx)
	if err != nil || len(p[contracts.NextStep]) != 16 {
		t.Fatal(p, err)
	}

	before := h.current.Watermark()
	if err := h.a.Followup(ctx, p[contracts.NextStep][0]); err == nil || h.current.Watermark() != before || h.a.Status() != StatusIdle {
		t.Fatal("duplicate insertion woke work")
	}

	if err := h.a.CancelActivity(CancelRequested); err != nil {
		t.Fatal(err)
	}

	if len(inboxPending(t, h.a, contracts.NextStep)) != 0 {
		t.Fatal("clear retained messages")
	}
}

func TestOrdinaryReplyMatchesCoreOrder(t *testing.T) {
	h := newInboxQi(t, false, nil)
	if err := h.a.Followup(context.Background(), inboxUser("user-one")); err != nil {
		t.Fatal(err)
	}

	nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	events := sessionEvents(t, h.current)
	kinds := []protocol.SessionEventKind{}

	for index, e := range events {
		if e.Seq != protocol.Sequence(index) {
			t.Fatal("seq did not start at zero")
		}

		kinds = append(kinds, e.Type)
	}

	want := []protocol.SessionEventKind{
		protocol.InboxSpliced,
		protocol.TurnStarted,
		protocol.InboxSpliced,
		protocol.StepStarted,
		protocol.SystemMessage,
		protocol.UserMessage,
		protocol.RequestHeader,
		protocol.RequestContextEvent,
		protocol.AssistantMessage,
		protocol.StepEnded,
		protocol.TurnEnded,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatal(kinds)
	}

	inserted := protocol.EventPayload[protocol.InboxSpliceData](events[0])
	claim := protocol.EventPayload[protocol.InboxSpliceData](events[2])
	user := protocol.EventPayload[protocol.Message](events[5])
	if len(inserted.Inserted) != 1 ||
		!reflect.DeepEqual(inserted.Inserted[0], user) ||
		claim.Target != contracts.NextTurn ||
		claim.Start != 0 ||
		claim.RemovedCount == nil ||
		*claim.RemovedCount != 1 ||
		claim.Outcome != "" ||
		len(claim.Inserted) != 0 {
		t.Fatal(inserted, claim, user)
	}
}

func TestCanceledWorkOnlyReplaysANewWake(t *testing.T) {
	for _, kind := range []string{"user", "disposed"} {
		t.Run(kind, func(t *testing.T) {
			h := newInboxQi(t, false, nil)
			ctx := context.Background()
			h.llm.canceled = make(chan struct{})
			h.llm.cancelRelease = make(chan struct{})
			if err := h.a.Followup(ctx, inboxUser("active")); err != nil {
				t.Fatal(err)
			}

			nextInboxRequest(t, h)
			if err := h.a.Inject(ctx, inboxUser("kept")); err != nil {
				t.Fatal(err)
			}

			if err := h.a.CancelActivity(CancelCause{Kind: kind}, CancelOptions{KeepInbox: true}); err != nil {
				t.Fatal(err)
			}

			waitTestSignal(t, h.llm.canceled)
			if kind == "disposed" {
				if err := h.a.Steer(ctx, inboxUser("no-replay")); err != nil {
					t.Fatal(err)
				}
			}

			close(h.llm.cancelRelease)
			awaitTestAgentIdle(t, h.a)
			if len(h.llm.requests) != 0 || !reflect.DeepEqual(inboxPending(t, h.a, contracts.NextStep), []string{"kept"}) {
				t.Fatal("canceled activity restarted pending work")
			}

			if kind == "disposed" {
				if !reflect.DeepEqual(inboxPending(t, h.a, contracts.NextTurn), []string{"no-replay"}) {
					t.Fatal("disposed wake not retained")
				}

				return
			}
			// Once idle, an explicit wake uses a fresh uncanceled activity.
			if err := h.a.Followup(ctx, inboxUser("wake")); err != nil {
				t.Fatal(err)
			}

			nextInboxRequest(t, h)
			h.llm.release <- inboxStop()
			awaitTestAgentIdle(t, h.a)
		})
	}
}

func TestNativeInboxNotificationsPreserveAgentAndSnapshots(t *testing.T) {
	h := newInboxQi(t, false, nil)
	native := newFixtureEvents()
	h.service.SetLifecycle(native)
	defer func() {
		h.service.Close()
		native.Close()
	}()

	received := make(chan contracts.AgentInboxObservation, 8)
	_, err := native.Register(newTestOwner("inbox-listener", 1), fixtureHandlers{
		ID: "inbox-listener",
		Inbox: func(e contracts.AgentInboxObservation) {
			received <- e
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	message := inboxUser("native")
	if err := h.a.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}

	message.Content[0] = protocol.TextBlock{Text: "mutated"}
	if err := h.a.Followup(context.Background(), inboxUser("wake")); err != nil {
		t.Fatal(err)
	}

	nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)

	for _, want := range []string{"inserted:native", "inserted:wake", "claimed:native", "claimed:wake"} {
		select {
		case e := <-received:
			if e.Kind+":"+string(e.Message.ID) != want || e.Agent != h.a {
				t.Fatal(e, want)
			}
			if e.Message.ID == "native" && e.Message.Content[0].(protocol.TextBlock).Text != "native" {
				t.Fatal("lost snapshot")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("native notification missing")
		}
	}
}

func TestInvalidPreStepBatchDoesNotPartiallyAdmit(t *testing.T) {
	h := newInboxQi(t, false, func(_ context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
		in.Messages = append(in.Messages, protocol.NewTextMessage("invalid-role", protocol.RoleSystem, protocol.SourceSystem, "invalid batch"))
		return in, nil
	})
	if err := h.a.Followup(context.Background(), inboxUser("claimed")); err != nil {
		t.Fatal(err)
	}

	awaitTestAgentIdle(t, h.a)

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.UserMessage || e.Type == protocol.StepStarted {
			t.Fatal("invalid batch partially admitted", e)
		}
	}

	if len(h.llm.requests) != 0 || len(inboxPending(t, h.a, contracts.NextTurn)) != 0 {
		t.Fatal("invalid batch dispatched or requeued")
	}
}
