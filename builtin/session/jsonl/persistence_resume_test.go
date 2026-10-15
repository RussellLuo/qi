package jsonl_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/RussellLuo/qi/kernel/core/agent"
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

func TestResumeInitializesBeforeWakeAndKeepsPendingWithoutToolReplay(t *testing.T) {

	for _, settled := range []bool{false, true} {
		t.Run(fmt.Sprintf("settled=%t", settled), func(t *testing.T) {
			ctx := context.Background()
			var store session.Store
			dir := t.TempDir()

			disk, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			store = disk

			input := struct {
				Header protocol.SessionHeader
				Events []protocol.SessionEvent
			}{
				Header: protocol.SessionHeader{Version: protocol.SessionFormatVersion, ID: "s1", CreatedAt: 1000},
				Events: []protocol.SessionEvent{
					{Type: "agent/inbox/spliced", Seq: 0, Time: 1001, Data: json.RawMessage(`{"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}`)},
					{Type: "turn/start", Seq: 1, Time: 1002, Data: json.RawMessage(`{"turn":1}`)},
					{Type: "agent/inbox/spliced", Seq: 2, Time: 1003, Data: json.RawMessage(`{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}`)},
					{Type: "step/start", Seq: 3, Time: 1004, Data: json.RawMessage(`{"turn":1,"step":1}`)},
					{Type: "system/message", Seq: 4, Time: 1005, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}}`), SurfaceOp: protocol.AppendSurface()},
					{Type: "user/message", Seq: 5, Time: 1006, Data: json.RawMessage(`{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}`), SurfaceOp: protocol.AppendSurface()},
					{Type: "request/header", Seq: 6, Time: 1007, Data: json.RawMessage(`{"header":{"config":{"provider":"p","model":"m"},"tools":[{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]},"reason":"initial"}`)},
					{Type: "request/context", Seq: 7, Time: 1007, Data: json.RawMessage(`{"provider":"p","model":"m"}`)},
					{Type: "assistant/message", Seq: 8, Time: 1008, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"a-tool","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},{"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}]},"stream":[{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}},{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}]}`), SurfaceOp: protocol.AppendSurface()},
					{Type: "tool/call", Seq: 9, Time: 1009, Data: json.RawMessage(`{"turn":1,"step":1,"callId":"c2","name":"echo","arguments":"{broken"}`)},
				},
			}
			current, err := store.Create(ctx, session.CreateRequest{ID: input.Header.ID, Header: &input.Header})
			if err != nil {
				t.Fatal(err)
			}
			prefix := input.Events
			if !settled {
				prefix = prefix[:4]
			} // Claimed u1, open Step, no successful assistant settlement.
			for _, e := range prefix {
				if _, err := current.Append(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			inbox := session.NewInbox(current)
			// The removal is durable, but this batch never entered a request.
			if _, err := inbox.Splice(ctx, contracts.NextStep, 0, 0, []protocol.Message{inboxUser("claimed-lost")}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := inbox.Claim(ctx, contracts.NextStep, 1); err != nil {
				t.Fatal(err)
			}
			if !settled {
				record, err := protocol.NewAssistantStreamRecord(1010, protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: "uncommitted prefix"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := current.Append(ctx, protocol.NewAttemptEvent(1, 1, []protocol.AssistantStreamRecord{record})); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := inbox.Splice(ctx, contracts.NextTurn, 0, 0, []protocol.Message{inboxUser("pending-1"), inboxUser("pending-2")}, false); err != nil {
				t.Fatal(err)
			}
			if _, err := inbox.Splice(ctx, contracts.NextStep, 0, 0, []protocol.Message{inboxUser("pending-step")}, false); err != nil {
				t.Fatal(err)
			}

			if err := store.(*runtimeFixture).Close(); err != nil {
				t.Fatal(err)
			}
			disk, err = newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			store = disk
			t.Cleanup(func() { _ = disk.Close() })
			current, err = store.(session.StoreOpener).Open(ctx, input.Header.ID)
			if err != nil {
				t.Fatal(err)
			}

			prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
			t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
			barrier := &inboxBarrierLLM{requests: make(chan llm.Request, 16), release: make(chan []protocol.StreamChunk, 16)}
			owned := newTestOwner("resume", 1)
			if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{Route: "chat", Provider: "p", Adapter: barrier}); err != nil {
				t.Fatal(err)
			}
			var sideEffects atomic.Int64
			if _, err := toolRuntime.Register(owned, tools.Definition{Name: "echo", Execute: func(context.Context, tools.Execution) (tools.ExecutionResult, error) {
				sideEffects.Add(1)
				return tools.ExecutionResult{Value: json.RawMessage(`true`), Content: []protocol.ContentBlock{protocol.TextBlock{Text: "executed"}}}, nil
			}}); err != nil {
				t.Fatal(err)
			}
			service := newFixtureOwner(agentloop.New(runprep.Coordinator{Prompts: prompts, LLM: llmRuntime, Tools: toolRuntime, Middleware: middleware}), store)
			lifecycle := service.Initializers
			t.Cleanup(lifecycle.Close)
			t.Cleanup(service.Close)
			created, initialize := make(chan InitialAgent, 1), make(chan struct{})
			if _, err := lifecycle.Register(owned, InitializationHandler{ID: "resume-init", Initialize: func(ctx context.Context, e Initialization, _ CleanupRegistrar) error {
				if e.Source != "resume" {
					return fmt.Errorf("creation source=%s", e.Source)
				}
				created <- e.Agent
				select {
				case <-initialize:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				_, err := service.Attach(ctx, CreateRequest{SessionID: current.ID(), Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "p", Model: "m"}}}})
				finished <- err
			}()
			var initial InitialAgent
			select {
			case initial = <-created:
			case <-time.After(5 * time.Second):
				t.Fatal("initialization did not start")
			}
			if _, err := service.Get(ctx, ID(current.ID())); !errors.Is(err, ErrNotFound) {
				t.Fatal("unpublished Agent visible", err)
			}
			if len(barrier.requests) != 0 {
				t.Fatal("recovery woke model before initialization")
			}
			pending, err := initial.Pending(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ids := func(ms []protocol.Message) []string {
				out := []string{}
				for _, m := range ms {
					out = append(out, string(m.ID))
				}
				return out
			}
			if got := ids(pending[contracts.NextTurn]); !reflect.DeepEqual(got, []string{"pending-1", "pending-2"}) {
				t.Fatal(got)
			}
			if got := ids(pending[contracts.NextStep]); !reflect.DeepEqual(got, []string{"pending-step"}) {
				t.Fatal(got)
			}
			// Recovery has committed before agent/created, while the attach is exclusive.
			facts := collectAgentEvents(t, current)
			if facts[len(facts)-1].Type != protocol.SessionEndSeed || current.LastTurn() != 1 {
				t.Fatal("initialization preceded repair", facts)
			}
			if _, err := current.Attach(); !errors.Is(err, session.ErrAttached) {
				t.Fatal("resume owner not exclusive", err)
			}
			close(initialize)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			a, err := service.Get(ctx, ID(current.ID()))
			if err != nil {
				t.Fatal(err)
			}
			h := &inboxQi{a: a, service: service, current: current, llm: barrier, tools: toolRuntime, middleware: middleware}
			if a.Status() != StatusIdle || len(barrier.requests) != 0 || sideEffects.Load() != 0 {
				t.Fatal("repair/initialization dispatched work")
			}
			// A new explicit wake drains restored next-step and next-turn in normal order.
			if err := a.Followup(ctx, inboxUser("new-wake")); err != nil {
				t.Fatal(err)
			}
			var wantUsers []string
			if settled {
				wantUsers = append(wantUsers, "u1")
			}
			wantUsers = append(wantUsers, "pending-step", "pending-1")
			for i := 0; i < 3; i++ {
				r := nextInboxRequest(t, h)
				if !reflect.DeepEqual(messageIDs(r.Messages), wantUsers) {
					t.Fatal(messageIDs(r.Messages), wantUsers)
				}
				if i == 0 && settled {
					codes := []string{}
					for _, e := range facts {
						if e.Type == protocol.ToolResulted {
							codes = append(codes, protocol.EventPayload[protocol.ToolResultData](e).Error.Code)
						}
					}
					if !reflect.DeepEqual(codes, []string{"TOOL_NOT_STARTED", "TOOL_OUTCOME_UNKNOWN"}) {
						t.Fatal(codes)
					}
					toolMessages := 0
					for _, m := range r.Messages {
						if m.Role == protocol.RoleTool {
							toolMessages++
							if m.IsError == nil || !*m.IsError {
								t.Fatal("repair result treated as success")
							}
						}
					}
					if toolMessages != 2 || len(r.ToolHistory.Tools) != 1 {
						t.Fatal("lost recovered tool context", r)
					}
				}
				barrier.release <- inboxStop()
				if i == 0 {
					wantUsers = append(wantUsers, "pending-2")
				}
				if i == 1 {
					wantUsers = append(wantUsers, "new-wake")
				}
			}
			waitForAgentIdle(t, a)
			if sideEffects.Load() != 0 {
				t.Fatal("original tool replayed")
			}
			turns := []protocol.TurnID{}
			steps := []protocol.StepData{}
			headers := []string{}
			for _, e := range collectAgentEvents(t, current)[len(facts):] {
				switch e.Type {
				case protocol.TurnStarted:
					turns = append(turns, protocol.EventPayload[protocol.TurnData](e).Turn)
				case protocol.StepStarted:
					steps = append(steps, protocol.EventPayload[protocol.StepData](e))
				case protocol.RequestHeader:
					headers = append(headers, protocol.EventPayload[protocol.RequestHeaderData](e).Reason)
				}
			}
			if !reflect.DeepEqual(turns, []protocol.TurnID{2, 3, 4}) || !reflect.DeepEqual(steps, []protocol.StepData{{Turn: 2, Step: 1}, {Turn: 3, Step: 1}, {Turn: 4, Step: 1}}) {
				t.Fatal(turns, steps)
			}
			wantHeader := "initial"
			if settled {
				wantHeader = "resume"
			}
			if len(headers) == 0 || headers[0] != wantHeader {
				t.Fatal(headers)
			}
			if got := inboxPending(t, a, contracts.NextTurn); len(got) != 0 {
				t.Fatal(got)
			}
			// Counters belong to the new Agent, independent of durable Turn numbering.
			if a.NextAssistantAttempt() != 4 {
				t.Fatal("new Agent attempt counter did not restart")
			}
		})
	}

}

// Refuse the result after the tool has returned: the external side effect is
// real, but only its call start is durable. The normal S10 failure path leaves
// this prefix for S11 recovery.
type resultFailStore struct{ session.Store }
type resultFailSession struct{ session.Session }

func (s resultFailStore) Get(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	current, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return resultFailSession{current}, nil
}
func (s resultFailSession) Append(ctx context.Context, event protocol.SessionEvent) (protocol.Sequence, error) {
	if event.Type == protocol.ToolResulted {
		return -1, errors.New("injected durable result failure")
	}
	return s.Session.Append(ctx, event)
}

func TestExecutedSideEffectWithLostResultIsNotReplayed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	current, err := store.Create(ctx, session.CreateRequest{ID: "side-effect"})
	if err != nil {
		t.Fatal(err)
	}
	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
	barrier := &inboxBarrierLLM{requests: make(chan llm.Request, 4), release: make(chan []protocol.StreamChunk, 4)}
	owned := newTestOwner("side-effect", 1)
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: barrier}); err != nil {
		t.Fatal(err)
	}
	var sideEffects atomic.Int64
	if _, err := toolRuntime.Register(owned, tools.Definition{Name: "write", Execute: func(context.Context, tools.Execution) (tools.ExecutionResult, error) {
		sideEffects.Add(1)
		return tools.ExecutionResult{Value: json.RawMessage(`true`), Content: []protocol.ContentBlock{protocol.TextBlock{Text: "written"}}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	coordinator := runprep.Coordinator{Prompts: prompts, LLM: llmRuntime, Tools: toolRuntime, Middleware: middleware}
	originalService := newFixtureOwner(agentloop.New(coordinator), resultFailStore{store})
	t.Cleanup(originalService.Close)
	a := attachAgent(t, originalService, current.ID())
	h := &inboxQi{a: a, service: originalService, current: current, llm: barrier}
	if err := a.Followup(ctx, inboxUser("perform-write")); err != nil {
		t.Fatal(err)
	}
	_ = nextInboxRequest(t, h)
	call := protocol.ToolCall{ID: "write-once", Name: "write", Arguments: `{}`}
	barrier.release <- []protocol.StreamChunk{{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.ToolCallBlock{ToolCall: call}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}}}
	waitForAgentIdle(t, a)
	if sideEffects.Load() != 1 {
		t.Fatal("tool never executed")
	}
	before := collectAgentEvents(t, current)
	if before[len(before)-1].Type != protocol.ToolCalled {
		t.Fatal("not a crash-equivalent lost-result prefix", before)
	}
	originalService.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored, err := openRecovered(ctx, reopened, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	repaired := collectAgentEvents(t, restored)
	result := protocol.EventPayload[protocol.ToolResultData](repaired[len(before)])
	if repaired[len(before)].Type != protocol.ToolResulted || result.Error == nil || result.Error.Code != "TOOL_OUTCOME_UNKNOWN" {
		t.Fatal("side effect presented as known result", repaired)
	}
	service := newFixtureOwner(agentloop.New(coordinator), reopened)
	t.Cleanup(service.Close)
	resumed := attachAgent(t, service, current.ID())
	if sideEffects.Load() != 1 || len(barrier.requests) != 0 {
		t.Fatal("resume replayed the tool")
	}
	h = &inboxQi{a: resumed, service: service, current: restored, llm: barrier}
	if err := resumed.Followup(ctx, inboxUser("after-crash")); err != nil {
		t.Fatal(err)
	}
	r := nextInboxRequest(t, h)
	found := false
	for _, m := range r.Messages {
		if m.Role == protocol.RoleTool && m.ToolCallID == call.ID {
			found = true
			if m.IsError == nil || !*m.IsError {
				t.Fatal("unknown outcome treated as success")
			}
		}
	}
	if !found {
		t.Fatal("recovery missing from next model request")
	}
	barrier.release <- inboxStop()
	waitForAgentIdle(t, resumed)
	if sideEffects.Load() != 1 || restored.LastTurn() != 2 {
		t.Fatal("side effect repeated or Turn lost", sideEffects.Load(), restored.LastTurn())
	}
}

// Every model boundary is held until the test explicitly permits completion.
type inboxBarrierLLM struct {
	requests chan llm.Request
	release  chan []protocol.StreamChunk
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
			yield(protocol.StreamChunk{}, ctx.Err())
		}
	}
}

type inboxQi struct {
	a          Agent
	service    *fixtureOwner
	current    session.Session
	llm        *inboxBarrierLLM
	middleware *agentmiddleware.Runtime
	tools      *tools.Runtime
}

func inboxUser(id string) protocol.Message {
	return protocol.NewTextMessage(protocol.MessageID(id), protocol.RoleUser, protocol.SourceUser, id)
}

func inboxStop() []protocol.StreamChunk {
	return []protocol.StreamChunk{{Type: protocol.ChunkTextDelta, Index: 0, Text: "ok"}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}
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

func TestReattachedAgentReadsPendingWithoutAutomaticWake(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(ctx, session.CreateRequest{ID: "pending-agent"})
	if err != nil {
		t.Fatal(err)
	}
	pending := session.NewInbox(current)
	for _, target := range []string{contracts.NextTurn, contracts.NextStep} {
		if _, err := pending.Splice(ctx, target, 0, 0, []protocol.Message{inboxUser(target)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.Open(ctx, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	service := newFixtureOwner(agentloop.New(runprep.Coordinator{}), reopened)
	defer service.Close()
	a, err := service.Attach(ctx, CreateRequest{SessionID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}
	waitForAgentIdle(t, a)
	if !reflect.DeepEqual(inboxPending(t, a, contracts.NextTurn), []string{contracts.NextTurn}) || !reflect.DeepEqual(inboxPending(t, a, contracts.NextStep), []string{contracts.NextStep}) {
		t.Fatal("pending lost on attach")
	}
	if restored.LastTurn() != 0 {
		t.Fatal("attach auto-ran pending")
	}
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

func waitForAgentIdle(t *testing.T, a Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
}

func attachAgent(t *testing.T, service *fixtureOwner, id protocol.SessionID) Agent {
	t.Helper()
	a, err := service.Attach(context.Background(), CreateRequest{ID: "caller-label", SessionID: id, Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func collectAgentEvents(t *testing.T, current session.Session) []protocol.SessionEvent {
	t.Helper()
	result := []protocol.SessionEvent{}
	for event, err := range current.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, event)
	}
	return result
}

// explicitStore lends one already-opened Session for a second-owner rejection test.
type explicitStore struct{ current session.Session }

func (s explicitStore) Get(context.Context, protocol.SessionID) (session.Session, error) {
	return s.current, nil
}
func (s explicitStore) Create(context.Context, session.CreateRequest) (session.Session, error) {
	return nil, errors.New("fixture is borrowing-only")
}
func (s explicitStore) Delete(context.Context, protocol.SessionID) error {
	return errors.New("fixture is borrowing-only")
}
