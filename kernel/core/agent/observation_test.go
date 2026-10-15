package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
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

type testInitializer func(context.Context, Initialization, CleanupRegistrar) error

func (f testInitializer) Initialize(c context.Context, i Initialization, r CleanupRegistrar) error {
	return f(c, i, r)
}

type panickingDispatcher struct{}

func (panickingDispatcher) SessionAppended(protocol.SessionID, protocol.SessionEvent) {
	panic("Session passive observer")
}
func (panickingDispatcher) DriverEvent(contracts.DriverEvent) { panic("Driver passive observer") }
func (panickingDispatcher) AssistantStream(contracts.AssistantStreamObservation) {
	panic("stream passive observer")
}

func TestPassiveDispatcherPanicsPreserveAcceptedResult(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{Observer: panickingDispatcher{}})
	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: []protocol.StreamChunk{{Type: protocol.ChunkTextDelta, Text: "accepted"}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}}}}
	_, err := llmRuntime.RegisterAdapter(newTestOwner("panic-runtime", 1), llm.AdapterRegistration{Provider: "fake", Route: "chat", Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	f := standaloneFactory(t, FactoryOptions{Registry: NewRegistry(), Store: store, Driver: agentloop.New(runprep.Coordinator{Prompts: prompts, LLM: llmRuntime, Tools: toolRuntime, Middleware: middleware}, panickingDispatcher{})})
	h, err := f.Create(ctx, CreateOptions{Agent: AgentOptions{Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	native, _ := h.Agent().Subscribe(ctx)
	live, _ := h.Agent().SubscribeAssistant(ctx)
	_, stream, err := h.Agent().Submit(ctx, Input{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for e, err := range stream {
		if err != nil {
			t.Fatal("observer vetoed execution", err)
		}
		if e.Type == "ended" {
			completed = e.Reason.Kind == "completed"
		}
	}
	messages, err := h.Session().Messages(ctx)
	if err != nil || len(messages) != 2 || messages[1].Content[0].(protocol.TextBlock).Text != "accepted" || !completed {
		t.Fatal("accepted result changed", messages, err)
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	diagnostics := 0
	for e, err := range native.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Error != nil {
			diagnostics++
		}
	}
	frames := 0
	for _, err := range live.Events() {
		if err != nil {
			t.Fatal(err)
		}
		frames++
	}
	if diagnostics == 0 || frames != 4 {
		t.Fatal("panic diagnostics or complete preview missing", diagnostics, frames)
	}
}

func TestInitializerRegistryOrdersExpiresAndCompensatesPartialFailure(t *testing.T) {
	ctx := observationTestContext(t)
	initializers := NewInitializers()
	defer initializers.Close()
	owned := newTestOwner("initializers", 1)
	var retained InitialAgent
	var retainedCleanup CleanupRegistrar
	original, teardown := errors.New("second initializer"), errors.New("cleanup panic")
	var sequence []string
	_, err := initializers.Register(owned, InitializationHandler{ID: "second", Order: 20, Initialize: func(c context.Context, i Initialization, r CleanupRegistrar) error {
		sequence = append(sequence, "second")
		if err := retained.Send(c, inboxUser("late"), contracts.NextTurn, true); !errors.Is(err, ErrClosed) {
			t.Error("prior port remained valid", err)
		}
		if err := retainedCleanup.Defer("late", func(context.Context) error { return nil }); !errors.Is(err, ErrClosed) {
			t.Error("prior registrar remained valid", err)
		}
		_ = r.Defer("panic", func(context.Context) error { sequence = append(sequence, "cleanup-second"); panic(teardown) })
		return original
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = initializers.Register(owned, InitializationHandler{ID: "first", Order: 10, Initialize: func(c context.Context, i Initialization, r CleanupRegistrar) error {
		sequence = append(sequence, "first")
		retained = i.Agent
		retainedCleanup = r
		_ = r.Defer("first", func(context.Context) error { sequence = append(sequence, "cleanup-first"); return nil })
		return i.Agent.Send(c, inboxUser("prepared"), contracts.NextTurn, true)
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = initializers.Register(owned, InitializationHandler{ID: "never", Order: 30, Initialize: func(context.Context, Initialization, CleanupRegistrar) error {
		t.Error("initializer continued after failure")
		return nil
	}})
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Initialize: initializers, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error {
		t.Error("failed initializer woke worker")
		return nil
	})})
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "partial"}}); !errors.Is(err, original) || !errors.Is(err, teardown) {
		t.Fatal("partial cleanup errors", err)
	}
	if !reflect.DeepEqual(sequence, []string{"first", "second", "cleanup-second", "cleanup-first"}) {
		t.Fatal(sequence)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("partial initialization published")
	}
	if err := owned.Stop(ctx); err != nil {
		t.Fatal("initializer lease remained", err)
	}
}

func TestObserverPanicDiagnosisAndStatusReentry(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	events := testAgentEvents(t, ObserverHandlers{})
	entered, release := make(chan struct{}), make(chan struct{})
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Observe: events, Driver: rawDriver(func(c context.Context, r agentloop.Run, _ agentloop.EmitFunc) error {
		_, _ = r.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1)
		close(entered)
		<-release
		return nil
	})})
	h := factoryCreate(t, f, "observer-panic")
	sub, _ := h.Agent().Subscribe(ctx)
	diagnosis := make(chan error, 2)
	finished := make(chan error, 1)
	_, _ = events.Register(newTestOwner("panic", 1), ObserverHandlers{ID: "panic", Status: func(e contracts.AgentStatusObservation) {
		if e.Status == StatusRunning {
			panic("observer boom")
		}
	}, Error: func(e contracts.AgentErrorObservation) { diagnosis <- e.Error }})
	_, _ = events.Register(newTestOwner("reenter", 1), ObserverHandlers{ID: "reenter", Order: 1, Status: func(e contracts.AgentStatusObservation) {
		if e.Status == StatusRunning {
			finished <- errors.Join(e.Agent.WhenIdle(ctx), h.Dispose(ctx))
		}
	}})
	_, stream, err := h.Agent().Submit(ctx, Input{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Status callback resource cycle")
	}
	select {
	case err := <-diagnosis:
		if err == nil {
			t.Fatal("panic was hidden")
		}
	case <-ctx.Done():
		t.Fatal("panic diagnosis missing")
	}
	for range stream {
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("panic retained registration")
	}
	// Diagnostics remain typed local errors; completion is still from Session.
	if sub.Dropped() != 0 {
		t.Fatal("unexpected local overflow")
	}
}

func TestAppendFailureAbandonedStreamAndOriginalLocalError(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
	failure := errors.New("settlement storage failure")
	header, _ := session.CreateHeader("append-failure", nil)
	current, err := session.Restore(header, nil, nil, func(raw []byte) error {
		var event protocol.SessionEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return err
		}
		if event.Type == protocol.AssistantMessage || event.Type == protocol.AssistantAttempt || event.Type == protocol.TurnEnded {
			return failure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: []protocol.StreamChunk{{Type: protocol.ChunkTextDelta, Text: "preview"}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}}}}
	_, err = llmRuntime.RegisterAdapter(newTestOwner("failure", 1), llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: agentloop.New(runprep.Coordinator{Prompts: prompts, Tools: toolRuntime, LLM: llmRuntime, Middleware: middleware})})
	h, err := f.Attach(ctx, AttachOptions{Session: current, Agent: AgentOptions{Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "text"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	live, _ := h.Agent().SubscribeAssistant(ctx)
	native, _ := h.Agent().Subscribe(ctx)
	factsSub, _ := current.Subscribe(ctx)
	defer factsSub.Close()
	_, output, err := h.Agent().Submit(ctx, Input{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	var driverErr error
	for _, err := range output {
		driverErr = errors.Join(driverErr, err)
	}
	if !errors.Is(driverErr, failure) {
		t.Fatal("original Driver error", driverErr)
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	ended := false
	for e, err := range live.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Frame.Type == "end" {
			ended = true
			if e.Frame.Outcome.Kind != "abandoned" || e.Frame.Outcome.Seq != nil {
				t.Fatal("false stream commit", e)
			}
		}
	}
	if !ended {
		t.Fatal("abandoned end missing")
	}
	observed := false
	for e, err := range native.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Error != nil {
			observed = errors.Is(e.Error.Error, failure) && e.Error.InstanceID == h.Agent().InstanceID()
			if !observed {
				t.Fatal(e)
			}
		}
	}
	if !observed {
		t.Fatal("unpersistable error invisible")
	}
	facts := sessionEvents(t, current)
	for _, e := range facts {
		if e.Type == protocol.AssistantMessage || e.Type == protocol.AssistantAttempt || e.Type == protocol.TurnEnded {
			t.Fatal("failed append was published", e)
		}
	}
	count := 0
	for e, err := range factsSub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e, facts[count]) {
			t.Fatal("false Session observation")
		}
		count++
		if count == len(facts) {
			break
		}
	}
}

func TestAssistantOverflowAndDetachedObservationDoNotCancelActivity(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	entered, release := make(chan struct{}), make(chan struct{})
	f := standaloneFactory(t, FactoryOptions{Registry: NewRegistry(), Store: store, Driver: rawDriver(func(c context.Context, r agentloop.Run, emit agentloop.EmitFunc) error {
		_, _ = r.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1)
		close(entered)
		<-release
		if c.Err() != nil {
			t.Error("observation canceled activity")
		}
		for i := 0; i < 302; i++ {
			frame := protocol.AssistantStreamFrame{Type: "chunk", AttemptID: "local:1", Revision: int64(i + 1), Index: int64(i), Time: 17, Chunk: &protocol.StreamChunk{Type: protocol.ChunkTextDelta, Text: "original"}}
			if err := emit(contracts.DriverEvent{Type: "stream", SessionID: r.Agent.SessionID(), InvocationID: r.InvocationID, Stream: &frame}); err != nil {
				return err
			}
			frame.Chunk.Text = "mutated"
		}
		return nil
	})})
	h := factoryCreate(t, f, "overflow")
	slow, _ := h.Agent().SubscribeAssistant(ctx)
	local, _ := h.Agent().Subscribe(ctx)
	canceledCtx, cancel := context.WithCancel(ctx)
	detached, _ := h.Agent().SubscribeAssistant(canceledCtx)
	_, output, err := h.Agent().Submit(ctx, Input{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	cancel()
	detached.Close()
	local.Close()
	if h.Agent().Status() != StatusRunning {
		t.Fatal("detached observer stopped work")
	}
	close(release)
	for _, err := range output {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	count := 0
	for e, err := range slow.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Frame.Chunk.Text != "original" {
			t.Fatal("preview clone corrupted")
		}
		count++
	}
	if count != 256 || slow.Dropped() != 46 {
		t.Fatal(count, slow.Dropped())
	}
}

func observationTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func testAgentEvents(t *testing.T, h ObserverHandlers) *agentEvents {
	t.Helper()
	r := NewAgentEvents().(*agentEvents)
	if h.ID != "" {
		if _, err := r.Register(newTestOwner("test-observer", 1), h); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := r.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return r
}

func TestInitializePortWakeCleanupAndPassivePair(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			ctx := observationTestContext(t)
			store := session.NewRuntime(session.RuntimeOptions{})
			registry := NewRegistry()
			lifecycle := make(chan AgentNotification, 2)
			var observed []string
			events := testAgentEvents(t, ObserverHandlers{ID: "pair", Created: func(e contracts.AgentCreated) {
				observed = append(observed, "created")
				lifecycle <- AgentNotification{Created: &e}
			}, Inbox: func(e contracts.AgentInboxObservation) { observed = append(observed, e.Kind) }, Status: func(e contracts.AgentStatusObservation) { observed = append(observed, string(e.Status)) },
				Disposed: func(e contracts.AgentDisposed) {
					observed = append(observed, "disposed")
					lifecycle <- AgentNotification{Disposed: &e}
				}})
			entered, release, driven := make(chan struct{}), make(chan struct{}), make(chan agentloop.Run, 1)
			original, cleanupErr := errors.New("initialize failed"), errors.New("cleanup failed")
			var retained InitialAgent
			var registrar CleanupRegistrar
			var order []string
			f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Observe: events,
				Driver: rawDriver(func(_ context.Context, r agentloop.Run, _ agentloop.EmitFunc) error {
					if len(registry.Snapshot()) != 1 {
						t.Error("wake preceded publication")
					}
					if _, err := r.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1); err != nil {
						return err
					}
					driven <- r
					return nil
				}),
				Compose: func(context.Context, Creation) (Config, Cleanup, error) {
					return Config{}, func(context.Context) error { order = append(order, "config"); return nil }, nil
				},
				Initialize: testInitializer(func(c context.Context, i Initialization, r CleanupRegistrar) error {
					retained, registrar = i.Agent, r
					if _, err := registry.Get(c, i.Agent.ID()); !errors.Is(err, ErrNotFound) {
						t.Error("initializer visible")
					}
					if err := r.Defer("first", func(context.Context) error { order = append(order, "first"); return cleanupErr }); err != nil {
						return err
					}
					_ = r.Defer("second", func(context.Context) error { order = append(order, "second"); return nil })
					if err := i.Agent.Configure(c, Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "local", Model: "text"}}}); err != nil {
						return err
					}
					if err := i.Agent.Send(c, inboxUser("seed"), contracts.NextTurn, true); err != nil {
						return err
					}
					pending, err := i.Agent.Pending(c)
					if err != nil || len(pending[contracts.NextTurn]) != 1 {
						t.Error("input not prepared", pending, err)
					}
					close(entered)
					<-release
					if fail {
						return original
					}
					return nil
				})})
			result := make(chan struct {
				h   Handle
				err error
			}, 1)
			go func() {
				h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "initialized"}})
				result <- struct {
					h   Handle
					err error
				}{h, err}
			}()
			<-entered
			select {
			case <-driven:
				t.Fatal("initializing worker ran")
			default:
			}
			select {
			case <-lifecycle:
				t.Fatal("unpublished lifecycle notification")
			default:
			}
			close(release)
			got := <-result
			if err := retained.Send(ctx, inboxUser("late"), contracts.NextTurn, true); !errors.Is(err, ErrClosed) {
				t.Fatal("retained port", err)
			}
			if err := registrar.Defer("late", func(context.Context) error { return nil }); !errors.Is(err, ErrClosed) {
				t.Fatal("retained registrar", err)
			}
			if fail {
				if got.h != nil || !errors.Is(got.err, original) || !errors.Is(got.err, cleanupErr) {
					t.Fatal(got)
				}
				if _, err := store.Get(ctx, "initialized"); !errors.Is(err, session.ErrNotFound) {
					t.Fatal("failed owned resource", err)
				}
				if len(registry.Snapshot()) != 0 || events.reserved != 0 {
					t.Fatal("failed transaction leaked")
				}
				select {
				case <-lifecycle:
					t.Fatal("failure fabricated lifecycle")
				default:
				}
			} else {
				if got.err != nil {
					t.Fatal(got.err)
				}
				select {
				case r := <-driven:
					if r.Config.LLM.Provider != "local" {
						t.Fatal(r.Config)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if err := got.h.Dispose(ctx); !errors.Is(err, cleanupErr) {
					t.Fatal(err)
				}
				created, disposed := <-lifecycle, <-lifecycle
				if created.Created == nil || disposed.Disposed == nil || created.Created.Snapshot.InstanceID != disposed.Disposed.Snapshot.InstanceID || !disposed.Disposed.Snapshot.Disposed || !errors.Is(disposed.Disposed.Err, cleanupErr) {
					t.Fatal(created, disposed)
				}
				if _, err := store.Get(ctx, "initialized"); !errors.Is(err, session.ErrNotFound) {
					t.Fatal("Disposed preceded release", err)
				}
				if !reflect.DeepEqual(observed, []string{"created", "inserted", "running", "claimed", "idle", "disposed"}) {
					t.Fatal("initial notifications order", observed)
				}
			}
			if !reflect.DeepEqual(order, []string{"second", "first", "config"}) {
				t.Fatal("cleanup order", order)
			}
		})
	}
}

func TestObserverReentryCapacityAndPanicIsolation(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	handles := make(chan Handle, 1)
	callbackEntered, callbackDone, disposed := make(chan struct{}), make(chan struct{}), make(chan contracts.AgentDisposed, 1)
	var reentryErr error
	events := testAgentEvents(t, ObserverHandlers{ID: "reentrant", Created: func(e contracts.AgentCreated) {
		if _, err := registry.Get(ctx, ID(e.Snapshot.SessionID)); err != nil {
			t.Error("Created before visible", err)
		}
		close(callbackEntered)
		h := <-handles
		reentryErr = errors.Join(h.Agent().WhenIdle(ctx), h.Dispose(ctx))
		close(callbackDone)
	}, Disposed: func(e contracts.AgentDisposed) { disposed <- e }})
	events.capacity = 1 // deterministic single reserved lifecycle
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Observe: events, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil })})
	h := factoryCreate(t, f, "one")
	<-callbackEntered
	// The worker is blocked in Created, but disposal never waits for it.
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "full"}}); !errors.Is(err, ErrObservationCapacity) {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "full"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("capacity rollback", err)
	}
	for i := 0; i < 261; i++ {
		events.Status(contracts.AgentStatusObservation{Agent: h.Agent(), InstanceID: h.Agent().InstanceID(), Status: StatusIdle})
	}
	if events.Dropped() != 5 {
		t.Fatal(events.Dropped())
	}
	handles <- h
	select {
	case <-callbackDone:
	case <-ctx.Done():
		t.Fatal("callback Dispose cycle")
	}
	if reentryErr != nil {
		t.Fatal(reentryErr)
	}
	select {
	case e := <-disposed:
		if !e.Snapshot.Disposed {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("paired Disposed was dropped")
	}
}

func TestDisposeDrainsDriverAndSharesAggregateResult(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cleanupErr := errors.New("teardown failure")
	driveErr := errors.New("drained settlement failure")
	var cleaned atomic.Int32
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store,
		Initialize: testInitializer(func(_ context.Context, _ Initialization, r CleanupRegistrar) error {
			return r.Defer("owned", func(context.Context) error { cleaned.Add(1); return cleanupErr })
		}),
		Driver: rawDriver(func(c context.Context, r agentloop.Run, _ agentloop.EmitFunc) error {
			_, _ = r.Attached.Inbox.Claim(context.Background(), contracts.NextTurn, 1)
			close(entered)
			<-c.Done()
			close(canceled)
			<-release
			return errors.Join(c.Err(), driveErr)
		})})
	h := factoryCreate(t, f, "slow")
	_, stream, err := h.Agent().Submit(ctx, Input{Text: "run"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	waiting, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Dispose(waiting); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-canceled
	if cleaned.Load() != 0 || len(registry.Snapshot()) != 0 {
		t.Fatal("premature cleanup or still visible")
	}
	if _, err := h.Session().Attach(); !errors.Is(err, session.ErrAttached) {
		t.Fatal("early detach", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- h.Dispose(ctx) }()
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, cleanupErr) || !errors.Is(err, driveErr) {
			t.Fatal(err)
		}
	}
	for _, err := range stream {
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if cleaned.Load() != 1 {
		t.Fatal("cleanup repeated")
	}
	if err := f.Close(ctx); !errors.Is(err, cleanupErr) || !errors.Is(err, driveErr) {
		t.Fatal("Factory lost same result", err)
	}
	if _, err := store.Get(ctx, "slow"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("owned instance remained", err)
	}
}

// The real Loop covers every stream chunk, retry in the same step, two turns,
// tool execution, durable settlement, input outside the Driver and replacement.
func TestLocalSessionAssistantAndAgentCoverage(t *testing.T) {
	ctx := observationTestContext(t)
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	owned := newTestOwner("observation-runtime", 1)
	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
	_, _ = prompts.Register(owned, prompt.Section{ID: "system", Render: func(context.Context, prompt.Context) (string, error) { return "system prompt", nil }})
	_, _ = middleware.Register(owned, agentmiddleware.Middleware{ID: "retry", RequestError: func(_ context.Context, in agentmiddleware.RequestError, _ *agentmiddleware.RequestErrorAction) (*agentmiddleware.RequestErrorAction, error) {
		return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
	}})
	var calls atomic.Int32
	_, _ = toolRuntime.Register(owned, tools.Definition{Name: "echo", Description: "echo", Parameters: tools.JSONSchema(`{"type":"object"}`), Execute: func(context.Context, tools.Execution) (tools.ExecutionResult, error) {
		calls.Add(1)
		return tools.ExecutionResult{Content: []protocol.ContentBlock{protocol.TextBlock{Text: "result"}}}, nil
	}})
	name := "echo"
	toolChunks := []protocol.StreamChunk{
		{Type: protocol.ChunkBlockStart, Index: 37, BlockType: protocol.BlockReasoning},
		{Type: protocol.ChunkReasoningDelta, Index: 37, Text: "think"},
		{Type: protocol.ChunkBlockEnd, Index: 37, Block: protocol.ReasoningBlock{Text: "think"}},
		{Type: protocol.ChunkBlockStart, Index: 9, BlockType: protocol.BlockToolCall},
		{Type: protocol.ChunkToolCallDelta, Index: 9, ID: "call", Name: &name, ArgumentsDelta: `{}`},
		{Type: protocol.ChunkBlockEnd, Index: 9, Block: protocol.ToolCallBlock{ToolCall: protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{}`}}},
		{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 5}},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}},
	}
	text := []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, BlockType: protocol.BlockText}, {Type: protocol.ChunkTextDelta, Text: "answer"}, {Type: protocol.ChunkBlockEnd, Block: protocol.TextBlock{Text: "answer"}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: []protocol.StreamChunk{{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishError, Failure: &protocol.LlmFailure{Code: "RETRY", Message: "retry"}}}}}, {Chunks: toolChunks}, {Chunks: text}, {Chunks: text}, {Chunks: text}}}
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{Provider: "fake", Route: "chat", Adapter: adapter}); err != nil {
		t.Fatal(err)
	}
	loop := agentloop.New(runprep.Coordinator{Prompts: prompts, Tools: toolRuntime, LLM: llmRuntime, Middleware: middleware})
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: driveOnly{loop}})
	current, err := store.Create(ctx, session.CreateRequest{ID: "coverage"})
	if err != nil {
		t.Fatal(err)
	}
	// A non-Driver writer leaves an unclosed prefix; Attach commits repair/seed.
	_, _ = current.Append(ctx, protocol.NewSessionEvent(protocol.TurnStarted, protocol.TurnData{Turn: 1}))
	_, _ = current.Append(ctx, protocol.NewSessionEvent(protocol.StepStarted, protocol.StepData{Turn: 1, Step: 1}))
	sub, err := current.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	config := AgentOptions{Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}}
	h, err := f.Attach(ctx, AttachOptions{Session: current, Agent: config})
	if err != nil {
		t.Fatal(err)
	}
	a := h.Agent()
	notifications, err := a.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live, err := a.SubscribeAssistant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Ignorable opaque surface/source bytes retain their original protocol form.
	plugin, err := protocol.DecodeSessionEvent([]byte(`{"type":"plugin/fact","seq":0,"time":7,"data":{"value":[1,2]},"ignorable":true,"surfaceOp":{"future":"value"},"sourceEventSeqs":{"future":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Append(ctx, plugin); err != nil {
		t.Fatal(err)
	}
	if err := a.Inject(ctx, inboxUser("discard")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Splice(ctx, contracts.NextStep, 0, 1, nil); err != nil {
		t.Fatal(err)
	}
	driverFacts := map[protocol.Sequence]bool{}
	for i := 0; i < 2; i++ {
		_, output, err := a.Submit(ctx, Input{Text: fmt.Sprintf("turn-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		for e, err := range output {
			if err != nil {
				t.Fatal(err)
			}
			if e.Session != nil {
				driverFacts[e.Session.Seq] = true
			}
		}
		if err := a.WhenIdle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || len(adapter.Seen) != 4 {
		t.Fatal(calls.Load(), len(adapter.Seen))
	}
	watermark := current.Watermark()
	facts := sessionEvents(t, current)
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	var received []protocol.SessionEvent
	for e, err := range sub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		received = append(received, e)
		if e.Seq == watermark {
			break
		}
	}
	if !reflect.DeepEqual(received, facts[2:]) || sub.Dropped() != 0 {
		t.Fatal("Session notification differs from committed facts")
	}
	seen := map[protocol.SessionEventKind]bool{}
	outside := 0
	for _, e := range received {
		seen[e.Type] = true
		if !driverFacts[e.Seq] {
			outside++
		}
	}
	for _, kind := range []protocol.SessionEventKind{protocol.TurnStarted, protocol.TurnEnded, protocol.StepStarted, protocol.StepEnded, protocol.SystemMessage, protocol.UserMessage, protocol.AssistantMessage, protocol.AssistantAttempt, protocol.ToolCalled, protocol.ToolResulted, protocol.RequestHeader, protocol.RequestContextEvent, protocol.InboxSpliced, protocol.SessionEndSeed, "plugin/fact"} {
		if !seen[kind] {
			t.Error("missing Session fact", kind)
		}
	}
	if outside < 6 {
		t.Fatal("Driver falsely treated as complete Session source", outside)
	}
	var frames []protocol.AssistantStreamFrame
	chunks := map[protocol.StreamChunkType]bool{}
	for e, err := range live.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Agent != a || e.InstanceID != a.InstanceID() {
			t.Fatal("stream identity")
		}
		frames = append(frames, e.Frame)
		if e.Frame.Chunk != nil {
			chunks[e.Frame.Chunk.Type] = true
		}
	}
	for _, kind := range []protocol.StreamChunkType{protocol.ChunkBlockStart, protocol.ChunkReasoningDelta, protocol.ChunkTextDelta, protocol.ChunkToolCallDelta, protocol.ChunkBlockEnd, protocol.ChunkUsage, protocol.ChunkFinish} {
		if !chunks[kind] {
			t.Error("missing chunk", kind)
		}
	}
	starts, ends := 0, 0
	var timed []protocol.TimedStreamChunk
	for i, frame := range frames {
		if frame.Revision != int64(i+1) || frame.Validate() != nil {
			t.Fatal("frame fields", frame)
		}
		switch frame.Type {
		case "start":
			starts++
			timed = nil
		case "chunk":
			if frame.Index != int64(len(timed)) {
				t.Fatal("frame index")
			}
			timed = append(timed, protocol.TimedStreamChunk{Time: frame.Time, Chunk: *frame.Chunk})
		case "end":
			ends++
			if frame.Outcome.Kind != "committed" || frame.Outcome.Seq == nil {
				t.Fatal(frame)
			}
			fact := facts[*frame.Outcome.Seq]
			if fact.Type != frame.Outcome.EventType {
				t.Fatal("false committed reference")
			}
			var records []protocol.AssistantStreamRecord
			if fact.Type == protocol.AssistantAttempt {
				records = protocol.EventPayload[protocol.AttemptData](fact).Stream
			} else {
				records = protocol.EventPayload[protocol.AssistantMessageData](fact).Stream
			}
			expanded, err := protocol.ExpandAssistantStream(records)
			if err != nil {
				t.Fatal(err)
			}
			if len(timed) == 0 {
				timed = []protocol.TimedStreamChunk{}
			}
			if !reflect.DeepEqual(expanded, timed) {
				t.Fatal("live/durable mismatch", expanded, timed)
			}
		}
		if frame.AttemptID != fmt.Sprintf("%s:%d", current.ID(), starts) {
			t.Fatal("retry attempt identity", frame)
		}
	}
	if starts != 4 || ends != starts {
		t.Fatal("retry/step/turn streams", starts, ends)
	}
	kinds := map[string]bool{}
	for e, err := range notifications.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.InstanceID != a.InstanceID() {
			t.Fatal("notification identity")
		}
		if e.Inbox != nil {
			kinds[e.Inbox.Kind] = true
		}
		if e.Disposed != nil && !e.Disposed.Snapshot.Disposed {
			t.Fatal(e)
		}
	}
	for _, kind := range []string{"inserted", "claimed", "discarded"} {
		if !kinds[kind] {
			t.Error("missing Inbox notification", kind)
		}
	}
	// Replacement uses the same durable seq namespace and a new local instance.
	h2, err := f.Attach(ctx, AttachOptions{Session: current, Agent: config})
	if err != nil {
		t.Fatal(err)
	}
	if h2.Agent().InstanceID() == a.InstanceID() {
		t.Fatal("replacement reused instance")
	}
	live2, _ := h2.Agent().SubscribeAssistant(ctx)
	_, output, err := h2.Agent().Submit(ctx, Input{Text: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range output {
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = h2.Dispose(ctx)
	for e, err := range live2.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Frame.Revision != 1 || e.Frame.AttemptID != "coverage:1" || e.InstanceID != h2.Agent().InstanceID() {
			t.Fatal("replacement cursor", e)
		}
		break
	}
	if current.Watermark() <= watermark {
		t.Fatal("durable seq reset")
	}
}
