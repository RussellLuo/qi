package gordisbridge

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type factoryScopeProbe struct{ scopes chan *gordis.Scope }

func (p *factoryScopeProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "factory.scope-probe", New: func() gordis.Plugin { return p }}
}
func (p *factoryScopeProbe) Activate(_ context.Context, s *gordis.Scope) error {
	p.scopes <- s
	return nil
}

type idleDriver struct{}

func (idleDriver) Drive(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }

// Wrap only the real Ready acquisition, allowing barriers around its exact
// position. These private wrappers are test fixtures, not plugin gate callbacks.
type readyAcquisitionBarrier struct {
	scope  *gordis.Scope
	before func()
	after  func()
	active *atomic.Int32
}

func (b readyAcquisitionBarrier) AcquireReady() (func(), error) {
	if b.before != nil {
		b.before()
	}
	release, err := b.scope.AcquireReady()
	if err != nil {
		return nil, err
	}
	b.active.Add(1)
	if b.after != nil {
		b.after()
	}
	return func() { b.active.Add(-1); release() }, nil
}

func TestFinalReadyLeasesLinearizePublicationAndFreeze(t *testing.T) {
	for _, phase := range []string{"before-provider", "before-caller", "between-acquires", "after-acquires"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			probe := &factoryScopeProbe{scopes: make(chan *gordis.Scope, 2)}
			host, err := gordis.NewHost(probe)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Shutdown(context.Background())
			provider, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "provider", Plugin: "factory.scope-probe"})
			if err != nil {
				t.Fatal(err)
			}
			providerScope := <-probe.scopes
			caller, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "caller", Plugin: "factory.scope-probe"})
			if err != nil {
				t.Fatal(err)
			}
			callerScope := <-probe.scopes
			store := session.NewRuntime(session.RuntimeOptions{})
			registry := agent.NewRegistry()
			events := agent.NewAgentEvents()
			defer events.Close(ctx)
			created, disposed := make(chan contracts.AgentCreated, 1), make(chan contracts.AgentDisposed, 1)
			_, err = events.Register(NewScopeOwner(callerScope), agent.ObserverHandlers{ID: "pair", Created: func(e contracts.AgentCreated) { created <- e }, Disposed: func(e contracts.AgentDisposed) { disposed <- e }})
			if err != nil {
				t.Fatal(err)
			}
			arrived, proceed, frozen := make(chan struct{}), make(chan struct{}), make(chan struct{})
			barrier := func() { close(arrived); <-proceed }
			var active atomic.Int32
			p, c := readyAcquisitionBarrier{scope: providerScope, active: &active}, readyAcquisitionBarrier{scope: callerScope, active: &active}
			switch phase {
			case "before-provider":
				p.before = barrier
			case "before-caller":
				c.before = barrier
			case "between-acquires":
				p.after = barrier
			case "after-acquires":
				c.after = barrier
			}
			f, err := agent.NewBoundFactory(agent.FactoryOptions{Registry: registry, Store: store, Driver: idleDriver{}, Observe: events}, func() (func(), error) { return acquireReadyPair(p, c) })
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(context.Background())
			// Use the same ownership shutdown seam as the hub. OnStop's last-added
			// barrier proves Freeze happened before requesting factory drain.
			stop := provider
			scope := providerScope
			if phase == "before-caller" {
				stop = caller
				scope = callerScope
			}
			if err := scope.OnStop("drain bound Factory", f.Close); err != nil {
				t.Fatal(err)
			}
			if err := scope.OnStop("observe Freeze", func(context.Context) error { close(frozen); return nil }); err != nil {
				t.Fatal(err)
			}
			// Earlier leases alone must not authorize final publication.
			early, err := acquireReadyPair(providerScope, callerScope)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan struct {
				h   agent.Handle
				err error
			}, 1)
			go func() {
				h, err := f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "race"}})
				result <- struct {
					h   agent.Handle
					err error
				}{h, err}
			}()
			<-arrived
			stopped := make(chan error, 1)
			go func() { stopped <- stop.Unmount(ctx) }()
			select {
			case <-frozen:
			case <-ctx.Done():
				t.Fatal("Freeze not reached")
			}
			if release, err := scope.AcquireReady(); err == nil {
				release()
				t.Fatal("OnStop without Freeze")
			}
			close(proceed)
			got := <-result
			early()
			if phase == "before-provider" || phase == "before-caller" {
				if got.h != nil || !errors.Is(got.err, agent.ErrClosed) {
					t.Fatal("Freeze should roll back", got.err)
				}
				select {
				case <-created:
					t.Fatal("failed creation announced")
				default:
				}
			} else {
				if got.err != nil || got.h == nil {
					t.Fatal("already linearized creation rejected", got.err)
				}
				if err := got.h.Dispose(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case e := <-created:
					if e.Snapshot.InstanceID != got.h.Agent().InstanceID() {
						t.Fatal(e)
					}
				case <-ctx.Done():
					t.Fatal("Created missing")
				}
				select {
				case e := <-disposed:
					if !e.Snapshot.Disposed {
						t.Fatal(e)
					}
				case <-ctx.Done():
					t.Fatal("Disposed missing")
				}
			}
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if active.Load() != 0 || len(registry.Snapshot()) != 0 {
				t.Fatal("lease or entry retained", active.Load())
			}
			if _, err := store.Get(ctx, "race"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("runtime retained", err)
			}
			if _, err := f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "late"}}); !errors.Is(err, agent.ErrClosed) {
				t.Fatal("old Factory reopened", err)
			}
		})
	}
}

func TestFactoryCloseAndPublicationHaveOneWinner(t *testing.T) {
	for _, publishFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("publishFirst=", publishFirst), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := session.NewRuntime(session.RuntimeOptions{})
			registry := agent.NewRegistry()
			entered, release := make(chan struct{}), make(chan struct{})
			options := agent.FactoryOptions{Store: store, Registry: registry, Driver: idleDriver{}}
			if !publishFirst {
				options.Initialize = factoryInitializerFunc(func(c context.Context, _ agent.Initialization, _ agent.CleanupRegistrar) error {
					close(entered)
					<-release
					return c.Err()
				})
			}
			gate := func() (func(), error) {
				if publishFirst {
					close(entered)
					<-release
				}
				return func() {}, nil
			}
			f, err := agent.NewBoundFactory(options, gate)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan struct {
				h   agent.Handle
				err error
			}, 1)
			go func() {
				h, err := f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "close-race"}})
				result <- struct {
					h   agent.Handle
					err error
				}{h, err}
			}()
			<-entered
			closeStarted := make(chan struct{})
			closed := make(chan error, 1)
			go func() { close(closeStarted); closed <- f.Close(ctx) }()
			<-closeStarted
			if !publishFirst {
				// Close cancels initialization before it can enter the commit locks.
				// Observe that cancellation through the initializer itself.
				// The next gate cannot be reached until the barrier is released.
				// Close with a canceled waiting context still initiates shutdown.
				waiting, stop := context.WithCancel(ctx)
				stop()
				_ = f.Close(waiting)
			}
			close(release)
			got := <-result
			if publishFirst {
				if got.err != nil || got.h == nil {
					t.Fatal(got.err)
				}
			} else if got.h != nil || got.err == nil {
				t.Fatal("Close lost prepublication creation")
			}
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
			if len(registry.Snapshot()) != 0 {
				t.Fatal("Close retained entry")
			}
			if _, err := store.Get(ctx, "close-race"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

type factoryInitializerFunc func(context.Context, agent.Initialization, agent.CleanupRegistrar) error

func (f factoryInitializerFunc) Initialize(c context.Context, i agent.Initialization, r agent.CleanupRegistrar) error {
	return f(c, i, r)
}
