package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

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

// driveOnly proves wrappers need no Store discovery or attachment methods.
type driveOnly struct{ inner agentloop.Driver }

func (d driveOnly) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	return d.inner.Drive(ctx, run, emit)
}

type rawDriver func(context.Context, agentloop.Run, agentloop.EmitFunc) error

func (d rawDriver) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	return d(ctx, run, emit)
}

func standaloneFactory(t *testing.T, opts FactoryOptions) Factory {
	t.Helper()
	f, err := NewFactory(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(context.Background()); err != nil {
			t.Log("retained cleanup error:", err)
		}
	})
	return f
}

func TestStandaloneFactoryCreateAttachToolSettlementDispose(t *testing.T) {
	for _, attach := range []bool{false, true} {
		t.Run(fmt.Sprint("attach=", attach), func(t *testing.T) {
			ctx := context.Background()
			store := session.NewRuntime(session.RuntimeOptions{})
			registry := NewRegistry()
			owned := newTestOwner("factory-fixture", 1)
			prompts, toolRuntime, llmRuntime, middleware := prompt.NewRuntime(), tools.NewRuntime(), llm.NewRuntime(), agentmiddleware.NewRuntime()
			t.Cleanup(func() { middleware.Close(); llmRuntime.Close(); toolRuntime.Close(); prompts.Close() })
			var executed atomic.Int32
			_, err := toolRuntime.Register(owned, tools.Definition{
				Name: "echo", Description: "Echo", Parameters: tools.JSONSchema(`{"type":"object"}`),
				Execute: func(context.Context, tools.Execution) (tools.ExecutionResult, error) {
					executed.Add(1)
					return tools.ExecutionResult{Content: []protocol.ContentBlock{protocol.TextBlock{Text: "tool result"}}}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			adapter := &scriptedAdapter{Scripts: []scriptedScript{
				{Chunks: []protocol.StreamChunk{
					{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockToolCall},
					{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.ToolCallBlock{ToolCall: protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{}`}}},
					{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}},
				}},
				{Chunks: []protocol.StreamChunk{
					{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText},
					{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: "done"}},
					{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
				}},
			}}
			if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: adapter}); err != nil {
				t.Fatal(err)
			}
			loop := agentloop.New(runprep.Coordinator{Prompts: prompts, Tools: toolRuntime, LLM: llmRuntime, Middleware: middleware})
			f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: driveOnly{loop}})
			config := Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}
			creation, cancel := context.WithCancel(ctx)
			var h Handle
			if attach {
				current, err := store.Create(ctx, session.CreateRequest{ID: "borrowed"})
				if err != nil {
					t.Fatal(err)
				}
				h, err = f.Attach(creation, AttachOptions{Session: current, Agent: AgentOptions{Config: config}})
			} else {
				h, err = f.Create(creation, CreateOptions{Session: session.CreateRequest{ID: "owned"}, Agent: AgentOptions{Config: config}})
			}
			if err != nil {
				t.Fatal(err)
			}
			cancel() // Only the acquisition transaction used this context.
			id, stream, err := h.Agent().Submit(ctx, Input{Text: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			var ended bool
			facts := map[protocol.Sequence]protocol.SessionEvent{}
			for e, err := range stream {
				if err != nil {
					t.Fatal(err)
				}
				if e.InvocationID != id || e.SessionID != h.Session().ID() {
					t.Fatal("wrong output association", e)
				}
				if e.Session != nil {
					facts[e.Session.Seq] = e.Session.Clone()
				}
				if e.Type == "ended" {
					ended = e.Reason.Kind == "completed"
				}
			}
			if !ended || executed.Load() != 1 || len(adapter.Seen) != 2 {
				t.Fatal(ended, executed.Load(), len(adapter.Seen))
			}
			for e, err := range h.Session().Events(ctx, -1) {
				if err != nil {
					t.Fatal(err)
				}
				if e.Type == protocol.InboxSpliced {
					continue
				} // Inbox facts use Session observation; Submit covers Driver emissions.
				if fact, ok := facts[e.Seq]; !ok || !reflect.DeepEqual(fact, e) {
					t.Fatal("raw fact differs from committed Session", e)
				}
			}
			if got, err := registry.Get(ctx, h.Agent().ID()); err != nil || got != h.Agent() {
				t.Fatal(got, err)
			}
			if err := h.Dispose(ctx); err != nil {
				t.Fatal(err)
			}
			if len(registry.Snapshot()) != 0 {
				t.Fatal("entry leaked")
			}
			if attach {
				got, err := store.Get(ctx, h.Session().ID())
				if err != nil || got != h.Session() {
					t.Fatal("borrowed instance lost", err)
				}
				reservation, err := got.Attach()
				if err != nil {
					t.Fatal(err)
				}
				_ = reservation.Close()
			} else if _, err := store.Get(ctx, h.Session().ID()); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("owned runtime leaked", err)
			}
		})
	}
}

func TestRegistryHiddenReservationDuplicateAndOldToken(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry().(*memoryRegistry)
	store := session.NewRuntime(session.RuntimeOptions{})
	current, _ := store.Create(ctx, session.CreateRequest{ID: "reserved"})
	entered, release := make(chan struct{}), make(chan struct{})
	f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }),
		Compose: func(context.Context, Creation) (Config, Cleanup, error) {
			close(entered)
			<-release
			return Config{}, nil, nil
		},
	})
	result := make(chan Handle, 1)
	go func() {
		h, err := f.Attach(ctx, AttachOptions{Session: current})
		if err != nil {
			t.Error(err)
		}
		result <- h
	}()
	waitTestSignal(t, entered)
	if _, err := r.Get(ctx, "reserved"); !errors.Is(err, ErrNotFound) || len(r.Snapshot()) != 0 {
		t.Fatal("initializing object visible", err)
	}
	if _, err := f.Attach(ctx, AttachOptions{Session: current}); !errors.Is(err, ErrExists) {
		t.Fatal("identity reservation did not conflict", err)
	}
	r.mu.RLock()
	old := r.entries["reserved"]
	r.mu.RUnlock()
	close(release)
	h := <-result
	if h == nil {
		t.Fatal("creation failed")
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	next, _, err := r.reserve("reserved", "")
	if err != nil {
		t.Fatal(err)
	}
	r.withdraw(old)
	r.remove(old)
	if r.entries["reserved"] != next {
		t.Fatal("stale token removed new identity")
	}
	r.remove(next)
}

func TestRollbackCleanupErrorsAndBorrowedIdentity(t *testing.T) {
	for _, attach := range []bool{false, true} {
		t.Run(fmt.Sprint(attach), func(t *testing.T) {
			ctx := context.Background()
			store := session.NewRuntime(session.RuntimeOptions{})
			r := NewRegistry()
			original, cleanupErr := errors.New("composition"), errors.New("cleanup")
			var released atomic.Int32
			f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }),
				Compose: func(context.Context, Creation) (Config, Cleanup, error) {
					return Config{}, func(context.Context) error { released.Add(1); return cleanupErr }, original
				},
			})
			var borrowed session.Session
			var err error
			if attach {
				borrowed, err = store.Create(ctx, session.CreateRequest{ID: "rollback"})
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.Attach(ctx, AttachOptions{Session: borrowed})
			} else {
				_, err = f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "rollback"}})
			}
			if !errors.Is(err, original) || !errors.Is(err, cleanupErr) || released.Load() != 1 || len(r.Snapshot()) != 0 {
				t.Fatal(err, released.Load())
			}
			if attach {
				reservation, err := borrowed.Attach()
				if err != nil {
					t.Fatal(err)
				}
				_ = reservation.Close()
			} else if _, err := store.Get(ctx, "rollback"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("new resource leaked", err)
			}
		})
	}
}

func TestParentSnapshotDoesNotOwnParent(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	r := NewRegistry()
	f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil })})
	parent, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "parent"}, Agent: AgentOptions{Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "before"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan Handle, 1)
	go func() {
		h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "child"}, Agent: AgentOptions{ParentID: parent.Agent().ID(), ComposeConfig: func(_ context.Context, c Creation) (Config, Cleanup, error) {
			close(entered)
			<-release
			if c.Parent == nil || c.Parent.ID != "parent" || c.Parent.Config.Loop.LLM.Model != "before" {
				t.Error("parent snapshot changed", c.Parent)
			}
			return c.Parent.Config, nil, nil
		}}})
		if err != nil {
			t.Error(err)
		}
		result <- h
	}()
	waitTestSignal(t, entered)
	if err := parent.Agent().Configure(ctx, Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "after"}}}); err != nil {
		t.Fatal(err)
	}
	if err := parent.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	child := <-result
	if child == nil || child.Agent().Config().Loop.LLM.Model != "before" {
		t.Fatal("child did not retain independent snapshot")
	}
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "late"}, Agent: AgentOptions{ParentID: "parent"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal("disposed parent admitted", err)
	}
	if got, err := r.Get(ctx, "child"); err != nil || got != child.Agent() {
		t.Fatal("parent disposed child", err)
	}
}

type rawFailure struct{ cause error }

func (e *rawFailure) Error() string { return "raw failure" }
func (e *rawFailure) Unwrap() error { return e.cause }

func TestRawSubscriptionCloneOverflowTerminalAndOriginalError(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	r := NewRegistry()
	root := errors.New("cause")
	original := &rawFailure{root}
	var expected contracts.DriverEvent
	f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
		if _, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1); err != nil {
			return err
		}
		seq := protocol.Sequence(19)
		fact := protocol.NewTurnEnded(3, protocol.TurnEndReason{Kind: "error", Error: &protocol.LlmFailure{Code: "RATE_LIMIT", Message: "original", ProviderRetryAfterMs: func() *float64 { n := 125.0; return &n }()}})
		fact.Seq = seq
		frame := protocol.AssistantStreamFrame{Type: "end", AttemptID: "attempt", Revision: 27, Turn: 3, Step: 2, Index: 4, Time: 71, Outcome: &protocol.AssistantStreamOutcome{Kind: "committed", EventType: protocol.AssistantAttempt, Seq: &seq}}
		expected = contracts.DriverEvent{Type: "session", SessionID: run.Attached.Session.ID(), InvocationID: run.InvocationID, Session: &fact, Stream: &frame, Reason: protocol.EventPayload[protocol.TurnEndData](fact).Reason, Err: original, ErrorCode: protocol.ErrorMaxSteps}
		snapshot := expected.Clone()
		if err := emit(expected); err != nil {
			return err
		}
		fact.Seq = 777
		frame.Outcome.Kind = "abandoned"
		*frame.Outcome.Seq = 777
		expected = snapshot
		for range 1024 {
			if err := emit(contracts.DriverEvent{Type: "stream", SessionID: run.Attached.Session.ID(), InvocationID: run.InvocationID}); err != nil {
				return err
			}
		}
		if err := emit(contracts.DriverEvent{Type: "ended", SessionID: run.Attached.Session.ID(), InvocationID: run.InvocationID, Reason: protocol.TurnEndReason{Kind: "error"}, Err: original, ErrorCode: protocol.ErrorMaxSteps}); err != nil {
			return err
		}
		return original
	})})
	h, err := f.Create(ctx, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, stream, err := h.Agent().Submit(ctx, Input{Text: "test"})
	if err != nil {
		t.Fatal(err)
	}
	awaitTestAgentIdle(t, h.Agent())
	var count, ended int
	var returned error
	for e, err := range stream {
		if err != nil {
			returned = err
			continue
		}
		if e.InvocationID != id {
			t.Fatal("association lost", e)
		}
		if count == 0 && !reflect.DeepEqual(e, expected) {
			t.Fatal("raw fields changed", e, expected)
		}
		if e.Type == "ended" {
			ended++
			if count != 256 || e.Err != original || e.ErrorCode != protocol.ErrorMaxSteps {
				t.Fatal("terminal not reserved after previews", e, count)
			}
		}
		count++
	}
	var typed *rawFailure
	if count != 257 || ended != 1 || h.Agent().State().Dropped != 769 || !errors.Is(returned, root) || !errors.As(returned, &typed) || typed != original {
		t.Fatal(count, ended, h.Agent().State(), returned)
	}
}

func TestSubscriptionStopNeverCancelsActivity(t *testing.T) {
	for _, mode := range []string{"context", "early-exit"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := session.NewRuntime(session.RuntimeOptions{})
			r := NewRegistry()
			entered, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
				_, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1)
				if err != nil {
					return err
				}
				if err := emit(contracts.DriverEvent{Type: "started", SessionID: run.Attached.Session.ID(), InvocationID: run.InvocationID}); err != nil {
					return err
				}
				close(entered)
				<-release
				if ctx.Err() != nil {
					t.Error("observation canceled activity", ctx.Err())
				}
				close(settled)
				return emit(contracts.DriverEvent{Type: "ended", SessionID: run.Attached.Session.ID(), InvocationID: run.InvocationID, Reason: protocol.TurnEndReason{Kind: "completed"}})
			})})
			h, err := f.Create(ctx, CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			observing, cancel := context.WithCancel(ctx)
			defer cancel()
			_, stream, err := h.Agent().Submit(observing, Input{Text: "test"})
			if err != nil {
				t.Fatal(err)
			}
			waitTestSignal(t, entered)
			if mode == "context" {
				cancel()
				done := make(chan struct{})
				go func() {
					for range stream {
					}
					close(done)
				}()
				waitTestSignal(t, done)
			} else {
				for range stream {
					break
				}
			}
			if h.Agent().Status() != StatusRunning {
				t.Fatal("observation ended activity")
			}
			close(release)
			waitTestSignal(t, settled)
			awaitTestAgentIdle(t, h.Agent())
			if len(r.Snapshot()) != 1 {
				t.Fatal("observation disposed Agent")
			}
		})
	}
}

func TestSubscriptionDuplicateEndedIsBounded(t *testing.T) {
	s := newSubscription(context.Background())
	defer s.stop()
	if err := s.emit(contracts.DriverEvent{Type: "ended"}); err != nil {
		t.Fatal(err)
	}
	for range 1024 {
		if err := s.emit(contracts.DriverEvent{Type: "ended"}); !errors.Is(err, ErrDuplicateEnded) {
			t.Fatal(err)
		}
	}
	s.finish(nil)
	events, failures := 0, 0
	for e, err := range s.stream {
		if e.Type == "ended" {
			events++
		}
		if errors.Is(err, ErrDuplicateEnded) {
			failures++
		}
	}
	if events != 1 || failures != 1 {
		t.Fatal(events, failures)
	}
}

func TestInitializationFailureAndCancellationAreNeverPublished(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store, r := session.NewRuntime(session.RuntimeOptions{}), NewRegistry()
			original := errors.New("initialization failed")
			var releases atomic.Int32
			f := standaloneFactory(t, FactoryOptions{
				Registry: r, Store: store,
				Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }),
				Compose: func(context.Context, Creation) (Config, Cleanup, error) {
					return Config{}, func(context.Context) error { releases.Add(1); return nil }, nil
				},
				Initialize: &factoryLifecycle{created: func(context.Context) error {
					if _, err := r.Get(context.Background(), "init"); !errors.Is(err, ErrNotFound) || len(r.Snapshot()) != 0 {
						t.Error("initializer saw published Agent", err)
					}
					if canceled {
						cancel()
						return nil
					}
					return original
				}},
			})
			// A failing initialization closes the acquired attachment and new resources.
			h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "init"}})
			want := original
			if canceled {
				want = context.Canceled
			}
			if h != nil || !errors.Is(err, want) || releases.Load() != 1 || len(r.Snapshot()) != 0 {
				t.Fatal(h, err, releases.Load())
			}
			if _, err := store.Get(context.Background(), "init"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("initialization leaked runtime", err)
			}
		})
	}
}

func TestDriverReturnErrorSurvivesFullQueueWithoutEnded(t *testing.T) {
	ctx := context.Background()
	original := errors.New("append failed before closer")
	s := newSubscription(ctx)
	defer s.stop()
	for range 1024 {
		if err := s.emit(contracts.DriverEvent{Type: "stream"}); err != nil {
			t.Fatal(err)
		}
	}
	s.finish(original)
	previews, failures := 0, 0
	for e, err := range s.stream {
		if err != nil {
			if !errors.Is(err, original) {
				t.Fatal(err)
			}
			failures++
		} else {
			if e.Type != "stream" {
				t.Fatal(e)
			}
			previews++
		}
	}
	if previews != 256 || failures != 1 || s.dropped.Load() != 768 {
		t.Fatal(previews, failures, s.dropped.Load())
	}
}

type releaseBarrierStore struct {
	*session.Runtime
	entered chan struct{}
	release chan struct{}
}

func (s *releaseBarrierStore) Release(ctx context.Context, current session.Session) error {
	close(s.entered)
	<-s.release
	return s.Runtime.Release(ctx, current)
}

func TestWithdrawRetainsIdentityUntilOwnedRelease(t *testing.T) {
	ctx := context.Background()
	store := &releaseBarrierStore{session.NewRuntime(session.RuntimeOptions{}), make(chan struct{}), make(chan struct{})}
	r := NewRegistry()
	f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil })})
	h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "held"}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- h.Dispose(ctx) }()
	waitTestSignal(t, store.entered)
	if _, err := r.Get(ctx, "held"); !errors.Is(err, ErrNotFound) || len(r.Snapshot()) != 0 {
		t.Error("disposing object remains visible", err)
	}
	if _, err := f.Attach(ctx, AttachOptions{Session: h.Session()}); !errors.Is(err, ErrExists) {
		t.Error("identity reused before release", err)
	}
	close(store.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	token, _, err := r.(*memoryRegistry).reserve("held", "")
	if err != nil {
		t.Fatal("identity retained after release", err)
	}
	r.(*memoryRegistry).remove(token)
}

func TestComposerAndCleanupPanicStillRollbackResources(t *testing.T) {
	for _, phase := range []string{"compose", "cleanup"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			store, r := session.NewRuntime(session.RuntimeOptions{}), NewRegistry()
			original, panicErr := errors.New("original composition failure"), errors.New("panic cause")
			f := standaloneFactory(t, FactoryOptions{Registry: r, Store: store, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }),
				Compose: func(context.Context, Creation) (Config, Cleanup, error) {
					if phase == "compose" {
						panic(panicErr)
					}
					return Config{}, func(context.Context) error { panic(panicErr) }, original
				},
			})
			if h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "panic"}}); h != nil || !errors.Is(err, panicErr) || phase == "cleanup" && !errors.Is(err, original) {
				t.Fatal(h, err)
			}
			if _, err := store.Get(ctx, "panic"); !errors.Is(err, session.ErrNotFound) || len(r.Snapshot()) != 0 {
				t.Fatal("panic leaked resources", err)
			}
		})
	}
}
