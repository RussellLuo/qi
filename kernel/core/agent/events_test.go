package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestLifecycleObserverPanics(t *testing.T) {
	for _, kind := range []string{"created", "status", "inbox", "error", "disposed"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			observed := NewAgentEvents()
			var healthy, diagnostics atomic.Int32
			panicHandler := ObserverHandlers{ID: "panic"}
			good := ObserverHandlers{ID: "healthy", Order: 1, Error: func(e contracts.AgentErrorObservation) {
				if e.Error != nil {
					diagnostics.Add(1)
				}
			}}
			switch kind {
			case "created":
				panicHandler.Created = func(contracts.AgentCreated) { panic("created") }
				good.Created = func(contracts.AgentCreated) { healthy.Add(1) }
			case "status":
				panicHandler.Status = func(contracts.AgentStatusObservation) { panic("status") }
				good.Status = func(contracts.AgentStatusObservation) { healthy.Add(1) }
			case "inbox":
				panicHandler.Inbox = func(contracts.AgentInboxObservation) { panic("inbox") }
				good.Inbox = func(contracts.AgentInboxObservation) { healthy.Add(1) }
			case "error":
				panicHandler.Error = func(contracts.AgentErrorObservation) { panic("error") }
				good.Error = func(contracts.AgentErrorObservation) { healthy.Add(1) }
			case "disposed":
				panicHandler.Disposed = func(contracts.AgentDisposed) { panic("disposed") }
				good.Disposed = func(contracts.AgentDisposed) { healthy.Add(1) }
			}
			for _, h := range []ObserverHandlers{panicHandler, good} {
				if _, err := observed.Register(newTestOwner(h.ID, 1), h); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "created":
				observed.Created(contracts.AgentCreated{})
			case "status":
				observed.Status(contracts.AgentStatusObservation{})
			case "inbox":
				observed.Inbox(contracts.AgentInboxObservation{})
			case "error":
				observed.AgentError(contracts.AgentErrorObservation{Error: errors.New("original")})
			case "disposed":
				observed.Disposed(contracts.AgentDisposed{})
			}
			if err := observed.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if healthy.Load() != 1 || observed.Dropped() != 0 {
				t.Fatal(healthy.Load(), observed.Dropped())
			}
			if kind != "error" && diagnostics.Load() != 1 {
				t.Fatal("panic diagnostic", diagnostics.Load())
			}
		})
	}
}
func TestInitializerVetoNeverPublishesLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	init := NewInitializers()
	defer init.Close()
	events := NewAgentEvents()
	var facts, cleanups atomic.Int32
	_, err := events.Register(newTestOwner("observer", 1), ObserverHandlers{ID: "facts", Created: func(contracts.AgentCreated) { facts.Add(1) }, Disposed: func(contracts.AgentDisposed) { facts.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("veto")
	_, err = init.Register(newTestOwner("initializer", 1), InitializationHandler{ID: "veto", Initialize: func(_ context.Context, _ Initialization, r CleanupRegistrar) error {
		if err := r.Defer("resource", func(context.Context) error { cleanups.Add(1); return nil }); err != nil {
			return err
		}
		return cause
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewRuntime(session.RuntimeOptions{})
	registry := NewRegistry()
	f, err := NewFactory(FactoryOptions{Store: store, Registry: registry, Driver: rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }), Initialize: init, Observe: events})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "veto"}})
	if h != nil || !errors.Is(err, cause) {
		t.Fatal(h, err)
	}
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := events.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if facts.Load() != 0 || cleanups.Load() != 1 || len(registry.Snapshot()) != 0 {
		t.Fatal(facts.Load(), cleanups.Load(), registry.Snapshot())
	}
	if _, err := store.Get(ctx, "veto"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
}
