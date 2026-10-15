package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestMemoryAgentSubmitCancelWaitIdleAndState(t *testing.T) {
	driver := &blockingDriver{entered: make(chan struct{})}
	service := newTestService(driver)
	defer service.Close()

	created, err := service.Create(context.Background(), CreateRequest{
		ID: "agent-1", SessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	invocation, stream, err := created.Submit(context.Background(), Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-driver.entered:
	case <-time.After(time.Second):
		t.Fatal("driver did not start")
	}

	if state := created.State(); state.Status != StatusRunning || state.ActiveInvocation != invocation {
		t.Fatalf("running state = %+v", state)
	}

	if _, _, err := created.Submit(context.Background(), Input{Text: "queued"}); err != nil {
		t.Fatalf("second Submit error = %v", err)
	}

	if err := created.Cancel(context.Background(), invocation, CancelRequested); err != nil {
		t.Fatal(err)
	}

	var streamErr error

	for _, err := range stream {
		if err != nil {
			streamErr = err
		}
	}

	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error = %v", streamErr)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := created.WaitIdle(waitCtx); err != nil {
		t.Fatal(err)
	}

	if state := created.State(); state.Status != StatusIdle || state.ActiveInvocation != "" {
		t.Fatalf("idle state = %+v", state)
	}

	if err := created.Cancel(context.Background(), invocation, CancelRequested); !errors.Is(err, ErrNotActive) {
		t.Fatalf("Cancel completed invocation error = %v", err)
	}
}

func TestConfigureAppliesToNextInvocationOnly(t *testing.T) {
	entered := make(chan agentloop.Run, 2)
	release := make(chan struct{}, 2)
	service := newTestService(driverFunc(func(_ context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
		entered <- run
		<-release
		return nil
	}))
	defer service.Close()

	created, err := service.Create(context.Background(), CreateRequest{
		ID:        "agent-model",
		SessionID: "session-model",
		Config:    Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "one"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, firstStream, err := created.Submit(context.Background(), Input{Text: "first"})
	if err != nil {
		t.Fatal(err)
	}

	first := <-entered
	if err := created.Configure(context.Background(), Config{
		Loop: agentloop.Config{
			LLM: protocol.CallConfig{Provider: "openai", Model: "two"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if first.Config.LLM.Model != "one" {
		t.Fatalf("active invocation model = %q", first.Config.LLM.Model)
	}

	release <- struct{}{}

	for range firstStream {
	}

	if err := created.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, secondStream, err := created.Submit(context.Background(), Input{Text: "second"})
	if err != nil {
		t.Fatal(err)
	}

	second := <-entered
	if second.Config.LLM.Provider != "openai" || second.Config.LLM.Model != "two" {
		t.Fatalf("next invocation config = %+v", second.Config.LLM)
	}

	release <- struct{}{}

	for range secondStream {
	}
}

func TestMemoryAgentRegistryAndCompletedRun(t *testing.T) {
	service := newTestService(driverFunc(func(_ context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
		started := contracts.DriverEvent{
			Type:         "started",
			SessionID:    run.Attached.Session.ID(),
			InvocationID: run.InvocationID,
		}
		if err := emit(started); err != nil {
			return err
		}

		completed := contracts.DriverEvent{
			Type:         "ended",
			SessionID:    run.Attached.Session.ID(),
			InvocationID: run.InvocationID,
			Reason:       protocol.TurnEndReason{Kind: "completed"},
		}

		return emit(completed)
	}))
	created, err := service.Create(context.Background(), CreateRequest{ID: "agent-1", SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Create(context.Background(), CreateRequest{SessionID: "session-1"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Create error = %v", err)
	}

	got, err := service.Get(context.Background(), "agent-1")
	if err != nil || got != created {
		t.Fatalf("Get = %v/%v", got, err)
	}

	_, stream, err := created.Submit(context.Background(), Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string

	for event, err := range stream {
		if err != nil {
			t.Fatal(err)
		}

		kinds = append(kinds, event.Type)
	}

	if len(kinds) != 2 || kinds[0] != "started" || kinds[1] != "ended" {
		t.Fatalf("event kinds = %v", kinds)
	}

	service.Close()
	if _, err := service.Create(context.Background(), CreateRequest{SessionID: "session-3"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Create after Close error = %v", err)
	}
}

func (d *blockingDriver) Drive(ctx context.Context, _ agentloop.Run, _ agentloop.EmitFunc) error {
	d.once.Do(func() { close(d.entered) })
	<-ctx.Done()
	return context.Cause(ctx)
}

type driverFunc func(context.Context, agentloop.Run, agentloop.EmitFunc) error

func (f driverFunc) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	if _, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, run.Attached.Session.LastTurn()+1); err != nil {
		return err
	}
	return f(ctx, run, emit)
}

func TestAgentOwnsCountersAcrossSubmissionsAndRecreation(t *testing.T) {
	var reference contracts.AgentRuntime
	attempts, revisions := []int64{}, []int64{}
	service := newTestService(driverFunc(func(_ context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
		if run.Agent == nil || run.Agent.SessionID() != run.Attached.Session.ID() {
			t.Fatal("Driver lost Agent reference")
		}

		if reference != nil && reference != run.Agent {
			t.Fatal("Submit replaced Agent runtime")
		}

		reference = run.Agent
		attempts = append(attempts, run.Agent.NextAssistantAttempt())

		for i := 0; i < 3; i++ {
			revisions = append(revisions, run.Agent.NextAssistantStreamRevision())
		}

		return nil
	}))
	defer service.Close()

	created, err := service.Create(context.Background(), CreateRequest{ID: "a", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		_, stream, err := created.Submit(context.Background(), Input{Text: "hello"})
		if err != nil {
			t.Fatal(err)
		}

		for _, err := range stream {
			if err != nil {
				t.Fatal(err)
			}
		}

		if err := created.WaitIdle(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if reference != created.(contracts.AgentRuntime) ||
		attempts[0] != 1 ||
		attempts[1] != 2 ||
		revisions[0] != 1 ||
		revisions[5] != 6 {
		t.Fatal("counters reset across submissions")
	}

	if err := service.Delete(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}

	recreated, err := service.Create(context.Background(), CreateRequest{ID: "a", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}

	runtime := recreated.(contracts.AgentRuntime)
	if runtime.NextAssistantAttempt() != 1 || runtime.NextAssistantStreamRevision() != 1 {
		t.Fatal("new Agent inherited old counters")
	}
}

type blockingDriver struct {
	entered chan struct{}
	once    sync.Once
}

func TestAgentRequestStateSurvivesCancellationAndResetsOnReattach(t *testing.T) {
	ctx := context.Background()
	h := newInboxQi(t, false, nil)
	first := h.a.(*loopAgent)
	state := first.requestState
	if state == nil {
		t.Fatal("attached Agent has no request state")
	}

	if err := h.a.Followup(ctx, inboxUser("cancel")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	if err := h.a.CancelActivity(CancelCause{Kind: "user"}); err != nil {
		t.Fatal(err)
	}

	awaitTestAgentIdle(t, h.a)
	if first.requestState != state {
		t.Fatal("activity cancellation discarded attachment request state")
	}

	if err := h.a.Followup(ctx, inboxUser("next-turn")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	if err := h.service.Delete(ctx, h.a.ID()); err != nil {
		t.Fatal(err)
	}

	if first.requestState != nil {
		t.Fatal("detached Agent retained request state")
	}

	h.a = createTestAgent(t, h.service, h.current.ID())
	second := h.a.(*loopAgent)
	if second.requestState == nil || second.requestState == state {
		t.Fatal("reattached Agent reused old request state")
	}

	if err := h.a.Followup(ctx, inboxUser("reattach")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	reasons := []string{}
	for _, event := range sessionEvents(t, h.current) {
		if event.Type == protocol.RequestHeader {
			reasons = append(reasons, protocol.EventPayload[protocol.RequestHeaderData](event).Reason)
		}
	}

	if !reflect.DeepEqual(reasons, []string{"initial", "resume"}) {
		t.Fatalf("cancel, next Turn, reattach request headers = %v", reasons)
	}

	h.service.Close()
	h.service.Close()
	if second.requestState != nil {
		t.Fatal("repeated Service.Close retained request state")
	}
}

func TestFailedAttachReleasesRequestStateAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	h := newInboxQi(t, false, nil)
	if err := h.a.Followup(ctx, inboxUser("history")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	if err := h.service.Delete(ctx, h.a.ID()); err != nil {
		t.Fatal(err)
	}

	watermark := h.current.Watermark()
	lifecycle := newFixtureEvents()
	t.Cleanup(func() {
		h.service.Close()
		lifecycle.Close()
	})
	h.service.SetLifecycle(lifecycle)
	failure := errors.New("reject attachment")
	var rejected *loopAgent
	unregister, err := lifecycle.Register(newTestOwner("reject", 1), fixtureHandlers{
		ID: "reject",
		Created: func(_ context.Context, event contracts.AgentCreated) error {
			rejected = event.Agent.(*loopAgent)
			if rejected.requestState == nil {
				t.Error("request state missing during attachment initialization")
			}

			return failure
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.service.Create(ctx, CreateRequest{SessionID: h.current.ID()}); !errors.Is(err, failure) {
		t.Fatal(err)
	}

	if rejected == nil || rejected.requestState != nil || h.current.Watermark() != watermark || len(h.service.Snapshot()) != 0 {
		t.Fatal("failed attachment retained state or changed committed history")
	}

	unregister()
	h.a = createTestAgent(t, h.service, h.current.ID())
	if err := h.a.Followup(ctx, inboxUser("after-rejection")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	reasons := []string{}
	for _, event := range sessionEvents(t, h.current) {
		if event.Type == protocol.RequestHeader {
			reasons = append(reasons, protocol.EventPayload[protocol.RequestHeaderData](event).Reason)
		}
	}

	if !reflect.DeepEqual(reasons, []string{"initial", "resume"}) {
		t.Fatalf("request headers after failed attachment = %v", reasons)
	}
}
