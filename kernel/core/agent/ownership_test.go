package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

func waitTestSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier did not complete")
	}
}

func awaitTestAgentIdle(t *testing.T, a Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
}

func newTestAgentLoop(
	t *testing.T,
	persistent bool,
	chunks []protocol.StreamChunk,
	preStep func(context.Context, agentmiddleware.StepInput) (agentmiddleware.StepInput, error),
) (*fixtureController, *agentloop.Loop, session.Session) {
	t.Helper()
	var store session.Store = session.NewRuntime(session.RuntimeOptions{})

	current, err := store.Create(context.Background(), session.CreateRequest{ID: "ownership-fixture"})
	if err != nil {
		t.Fatal(err)
	}

	prompts, toolRuntime, llmRuntime, middleware := prompt.NewRuntime(), tools.NewRuntime(), llm.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() {
		middleware.Close()
		llmRuntime.Close()
		toolRuntime.Close()
		prompts.Close()
	})
	owned := newTestOwner("ownership-fixture", 1)
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  &scriptedAdapter{Scripts: []scriptedScript{{Chunks: chunks}}},
	}); err != nil {
		t.Fatal(err)
	}

	if preStep != nil {
		if _, err := middleware.Register(owned, agentmiddleware.Middleware{ID: "barrier", PreStep: preStep}); err != nil {
			t.Fatal(err)
		}
	}

	loop := agentloop.New(runprep.Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: middleware,
	})
	service := newFixtureController(loop, store)
	t.Cleanup(service.Close)

	return service, loop, current
}

func createTestAgent(t *testing.T, service *fixtureController, id protocol.SessionID) Agent {
	t.Helper()
	a, err := service.Create(context.Background(), CreateRequest{
		ID:        "caller-label",
		SessionID: id,
		Config:    Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	return a
}

func largeTextStream() []protocol.StreamChunk {
	chunks := []protocol.StreamChunk{}

	for i := 0; i < 2048; i++ {
		chunks = append(chunks, protocol.StreamChunk{
			Type:  protocol.ChunkTextDelta,
			Index: 0,
			Text:  "x",
		})
	}

	return append(chunks, protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}})
}

func sessionEvents(t *testing.T, current session.Session) []protocol.SessionEvent {
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

func TestDetachedAbsentAndSlowObservationStillSettles(t *testing.T) {
	for _, persistent := range []bool{false} {
		for _, mode := range []string{"absent", "detached", "slow"} {
			t.Run(fmt.Sprintf("jsonl=%t/%s", persistent, mode), func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				service, _, current := newTestAgentLoop(
					t,
					persistent,
					largeTextStream(),
					func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
						close(entered)

						select {
						case <-release:
						case <-ctx.Done():
							return in, ctx.Err()
						}

						return in, nil
					},
				)
				a := createTestAgent(t, service, current.ID())
				if err := a.CancelActivity(protocol.AgentCancelCause{Kind: "user"}); err != nil {
					t.Fatal(err)
				}

				caller, cancel := context.WithCancel(context.Background())
				defer cancel()

				_, stream, err := a.Submit(caller, Input{Text: "hello"})
				if err != nil {
					t.Fatal(err)
				}

				waitTestSignal(t, entered)
				// Keep this observation context live to exercise bounded preview delivery.
				// Independent observation cancellation is covered by the raw subscription tests.
				slowEntered, slowRelease, observerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				if mode == "detached" {
					for range stream {
						break
					}
				}

				if mode == "slow" {
					go func() {
						defer close(observerDone)

						first := true

						for range stream {
							if first {
								first = false
								close(slowEntered)
								<-slowRelease
							}
						}
					}()
					waitTestSignal(t, slowEntered)
				}

				close(release)
				awaitTestAgentIdle(t, a)
				events := sessionEvents(t, current)
				end := protocol.EventPayload[protocol.TurnEndData](events[len(events)-1])
				if events[len(events)-1].Type != protocol.TurnEnded || end.Reason.Kind != "completed" {
					t.Fatalf("unclosed or canceled: %+v", events)
				}

				messages, err := current.Messages(context.Background())
				if err != nil {
					t.Fatal(err)
				}

				last := messages[len(messages)-1]
				if last.Role != protocol.RoleAssistant || last.Content[0].(protocol.TextBlock).Text != strings.Repeat("x", 2048) {
					t.Fatal("observation lost durable model text")
				}

				if mode == "slow" {
					close(slowRelease)
					waitTestSignal(t, observerDone)
				}

				if mode == "absent" {
					if a.State().Dropped == 0 {
						t.Fatal("test did not exercise bounded delivery")
					}

					terminal := false

					for event, err := range stream {
						if err != nil {
							t.Fatal(err)
						}

						terminal = terminal || event.Type == "ended" && event.Reason.Kind == "completed"
					}

					if !terminal {
						t.Fatal("bounded subscription lost terminal event")
					}
				}
			})
		}
	}
}

func TestStructuredCancelFirstWinsAtPreStep(t *testing.T) {
	for _, cause := range []protocol.AgentCancelCause{
		{Kind: "user"},
		{Kind: "parent"},
		{Kind: "hook", Reason: "policy"},
		{Kind: "hook"},
		{Kind: "disposed"},
	} {
		t.Run(cause.Kind, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			activityCtx := make(chan context.Context, 1)
			service, _, current := newTestAgentLoop(t, false, largeTextStream(), func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
				activityCtx <- ctx
				close(entered)
				<-release
				return in, ctx.Err()
			})
			a := createTestAgent(t, service, current.ID())
			invocation, stream, err := a.Submit(context.Background(), Input{Text: "hello"})
			if err != nil {
				t.Fatal(err)
			}

			waitTestSignal(t, entered)
			if a.Status() != StatusRunning {
				t.Fatal("pre-step was not running")
			}

			if err := a.Cancel(context.Background(), invocation, protocol.AgentCancelCause{Kind: "legacy"}); err == nil {
				t.Fatal("accepted invalid new cause")
			}

			if err := a.Cancel(context.Background(), invocation, cause); err != nil {
				t.Fatal(err)
			}

			if err := a.CancelActivity(protocol.AgentCancelCause{Kind: "hook", Reason: "second"}); err != nil {
				t.Fatal(err)
			}

			if got := contracts.CancellationCause(context.Cause(<-activityCtx)); got != cause {
				t.Fatalf("context cause=%+v", got)
			}

			close(release)

			for _, err := range stream {
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}

			awaitTestAgentIdle(t, a)
			events := sessionEvents(t, current)
			if len(events) != 4 ||
				events[0].Type != protocol.InboxSpliced ||
				events[1].Type != protocol.TurnStarted ||
				events[2].Type != protocol.InboxSpliced ||
				events[3].Type != protocol.TurnEnded {
				t.Fatalf("pre-step cancellation admitted messages/Step: %+v", events)
			}

			end := protocol.EventPayload[protocol.TurnEndData](events[3])
			if end.Reason.Kind != "aborted" || end.Reason.Reason == nil || *end.Reason.Reason != cause {
				t.Fatalf("durable cause=%+v", end.Reason)
			}
		})
	}
}

func TestSingleAttachmentAndConcurrentDrive(t *testing.T) {
	for _, persistent := range []bool{false} {
		t.Run(fmt.Sprintf("jsonl=%t", persistent), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			service, loop, current := newTestAgentLoop(t, persistent, nil, func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
				close(entered)
				<-release
				return in, ctx.Err()
			})
			a := createTestAgent(t, service, current.ID())
			if a.ID() != ID(current.ID()) {
				t.Fatal("core id is not Session id")
			}

			if got, err := service.Get(context.Background(), "caller-label"); err != nil || got != a {
				t.Fatal("caller mapping lost")
			}

			other := newFixtureController(loop, service.store)
			defer other.Close()

			if _, err := other.Create(context.Background(), CreateRequest{SessionID: current.ID()}); !errors.Is(err, session.ErrAttached) {
				t.Fatalf("double attach=%v", err)
			}

			if _, err := current.BeginRun(); !errors.Is(err, session.ErrAttached) {
				t.Fatalf("unowned Drive=%v", err)
			}

			_, stream, err := a.Submit(context.Background(), Input{Text: "hello"})
			if err != nil {
				t.Fatal(err)
			}

			waitTestSignal(t, entered)
			if _, _, err := a.Submit(context.Background(), Input{Text: "again"}); err != nil {
				t.Fatalf("concurrent Submit=%v", err)
			}

			reserved := a.(*loopAgent).reservation
			err = loop.Drive(context.Background(), agentloop.Run{
				Agent:        a,
				Attached:     agentloop.AttachedRun{Session: current, Reservation: reserved, Inbox: a.(*loopAgent), RequestState: &agentloop.RequestState{}},
				InvocationID: "parallel",
				Config:       a.Config().Loop,
			}, func(contracts.DriverEvent) error { return nil })
			if !errors.Is(err, session.ErrRunActive) {
				t.Fatalf("parallel Drive=%v", err)
			}

			deleted := make(chan struct{})
			go func() {
				defer close(deleted)

				if err := service.Delete(context.Background(), a.ID()); err != nil {
					t.Error(err)
				}
			}()
			// Wait for cancellation rather than relying on goroutine scheduling.
			select {
			case <-a.(*loopAgent).ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("dispose did not cancel")
			}

			if _, _, err := a.Submit(context.Background(), Input{Text: "late"}); !errors.Is(err, ErrClosed) {
				t.Fatalf("disposed admission=%v", err)
			}

			if !a.Disposed() || a.Status() != StatusRunning {
				t.Fatal("disposed became a third activity status")
			}

			close(release)

			for range stream {
			}

			waitTestSignal(t, deleted)
			if _, err := other.Create(context.Background(), CreateRequest{SessionID: current.ID()}); err != nil {
				t.Fatalf("reattach after drain=%v", err)
			}
		})
	}
}

func TestInitializationAndDisposeOrdering(t *testing.T) {
	service, _, current := newTestAgentLoop(t, false, nil, nil)
	lifecycle := newFixtureEvents()
	defer lifecycle.Close()

	service.SetLifecycle(lifecycle)
	entered, release, disposed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	seen := make(chan contracts.AgentControl, 1)
	status := []contracts.AgentStatus{}
	var initOrder []int

	for i := 0; i < 2; i++ {
		i := i
		_, err := lifecycle.Register(newTestOwner(fmt.Sprint(i), 1), fixtureHandlers{
			ID:    fmt.Sprint(i),
			Order: i,
			Created: func(ctx context.Context, event contracts.AgentCreated) error {
				initOrder = append(initOrder, i)
				if i == 0 {
					if event.Source != "startup" {
						t.Error(event.Source)
					}

					seen <- event.Agent
					close(entered)

					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}

				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	_, err := lifecycle.Register(newTestOwner("observer", 1), fixtureHandlers{
		ID: "observer",
		Status: func(event contracts.AgentStatusObservation) {
			status = append(status, event.Status)
		},
		Disposed: func(_ context.Context, event contracts.AgentDisposed) {
			if event.Agent.Status() != StatusIdle || !event.Agent.Disposed() {
				t.Error("disposed before quiescence")
			}

			if _, err := service.Get(context.Background(), ID(current.ID())); !errors.Is(err, ErrNotFound) {
				t.Error("registration not withdrawn")
			}

			reservation, err := current.Attach()
			if err != nil {
				t.Error("Disposed before attachment release", err)
			} else {
				_ = reservation.Close()
			}

			close(disposed)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	created := make(chan Agent, 1)
	go func() {
		a, err := service.Create(context.Background(), CreateRequest{
			SessionID: current.ID(),
			Config:    Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}},
		})
		if err != nil {
			t.Error(err)
		}

		created <- a
	}()
	waitTestSignal(t, entered)
	pending := (<-seen).(Agent)
	if _, _, err := pending.Submit(context.Background(), Input{Text: "early"}); !errors.Is(err, ErrInitializing) {
		t.Fatalf("initialization admission=%v", err)
	}

	close(release)
	a := <-created
	if !reflect.DeepEqual(initOrder, []int{0, 1}) {
		t.Fatalf("initialization order=%v", initOrder)
	}

	_, stream, err := a.Submit(context.Background(), Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	for range stream {
	} // Legal empty success still closes the Turn.
	awaitTestAgentIdle(t, a)
	if err := service.Delete(context.Background(), a.ID()); err != nil {
		t.Fatal(err)
	}

	waitTestSignal(t, disposed)
	if !reflect.DeepEqual(status, []contracts.AgentStatus{StatusRunning, StatusIdle}) {
		t.Fatalf("statuses=%v", status)
	}

	reservation, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}

	_ = reservation.Close()
}

func TestFailedInitializationAndServiceCloseReleaseAttachment(t *testing.T) {
	for _, closeService := range []bool{false, true} {
		t.Run(fmt.Sprint(closeService), func(t *testing.T) {
			service, loop, current := newTestAgentLoop(t, false, nil, nil)
			lifecycle := newFixtureEvents()
			defer lifecycle.Close()

			service.SetLifecycle(lifecycle)
			entered := make(chan struct{})
			failure := errors.New("initialization failed")
			_, err := lifecycle.Register(newTestOwner("init", 1), fixtureHandlers{
				ID: "init",
				Created: func(ctx context.Context, _ contracts.AgentCreated) error {
					close(entered)
					if closeService {
						<-ctx.Done()
						return ctx.Err()
					}

					return failure
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			result := make(chan error, 1)
			go func() {
				_, err := service.Create(context.Background(), CreateRequest{SessionID: current.ID()})
				result <- err
			}()
			waitTestSignal(t, entered)
			if closeService {
				service.Close()
			}

			err = <-result
			if closeService && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}

			if !closeService && !errors.Is(err, failure) {
				t.Fatal(err)
			}

			other := newFixtureController(loop, service.store)
			defer other.Close()

			if _, err := other.Create(context.Background(), CreateRequest{SessionID: current.ID()}); err != nil {
				t.Fatal("initialization leaked attach", err)
			}
		})
	}
}

func TestClosedSessionReattachUsesResumeInitialization(t *testing.T) {
	service, _, current := newTestAgentLoop(t, false, nil, nil)
	caller, cancel := context.WithCancel(context.Background())
	first, err := service.Create(caller, CreateRequest{
		SessionID: current.ID(),
		Config:    Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cancel() // The creation caller owns initialization only.
	_, stream, err := first.Submit(context.Background(), Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	for _, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := service.Delete(context.Background(), first.ID()); err != nil {
		t.Fatal(err)
	}

	lifecycle := newFixtureEvents()
	defer lifecycle.Close()

	service.SetLifecycle(lifecycle)
	source := ""
	if _, err := lifecycle.Register(newTestOwner("resume", 1), fixtureHandlers{
		ID: "resume",
		Created: func(_ context.Context, event contracts.AgentCreated) error {
			source = event.Source
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := service.Create(context.Background(), CreateRequest{SessionID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}

	if source != "resume" || resumed.ID() != ID(current.ID()) {
		t.Fatalf("resumed Agent=%s source=%s", resumed.ID(), source)
	}

	if err := service.Delete(context.Background(), resumed.ID()); err != nil {
		t.Fatal(err)
	}
}
