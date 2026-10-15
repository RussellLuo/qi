package jsonl_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/builtin/session/jsonl"
	jsonlplugin "github.com/RussellLuo/qi/builtin/session/jsonl/plugin"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	runtimeplugin "github.com/RussellLuo/qi/kernel/core/session/plugin"
)

type lookupProbe struct {
	scope **gordis.Scope
	store *session.Store
}

func (p *lookupProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.lookup-probe", Requires: []gordis.ServiceSpec{session.StoreKey.Spec()}, New: func() gordis.Plugin { return &lookupProbe{p.scope, p.store} }}
}
func (p *lookupProbe) Activate(_ context.Context, s *gordis.Scope) error {
	store, err := s.Get(session.StoreKey)
	if err == nil {
		*p.scope = s
		*p.store = store
	}
	return err
}

// This wrapper runs the production JSONL adapter unchanged and observes the
// point after its Defer releases the plugin reference (callbacks run LIFO).
type observedJSONL struct {
	Directory string `json:"directory"`
	after     func() error
}

func (p *observedJSONL) Spec() gordis.PluginSpec {
	spec := (&jsonlplugin.Plugin{}).Spec()
	spec.New = func() gordis.Plugin { return &observedJSONL{after: p.after} }
	return spec
}
func (p *observedJSONL) Activate(ctx context.Context, s *gordis.Scope) error {
	if err := s.Defer("observe completed backend cleanup", func(context.Context) error { return p.after() }); err != nil {
		return err
	}
	return (&jsonlplugin.Plugin{Directory: p.Directory}).Activate(ctx, s)
}

func TestHostCleanupOrdersKeepOriginalHandlesAndLocks(t *testing.T) {
	for _, backendID := range []string{"aa-backend", "zz-backend"} {
		t.Run(backendID, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			var scope *gordis.Scope
			var store session.Store
			var current session.Session
			var afterCalled bool
			var runtimeAlive bool
			after := func() error {
				afterCalled = true
				_, err := store.Get(ctx, "saved")
				runtimeAlive = err == nil
				if runtimeAlive {
					owner, err := jsonl.NewOwner(directory)
					if owner != nil {
						_ = owner.Close()
					}
					if !errors.Is(err, jsonl.ErrStoreLocked) {
						return errors.New("backend Scope released lock before Runtime")
					}
					message := protocol.NewTextMessage("after-stop", protocol.RoleUser, protocol.SourceUser, "still durable")
					if _, err := session.NewInbox(current).Splice(ctx, contracts.NextTurn, 0, 0, []protocol.Message{message}, false); err != nil {
						return err
					}
					// New acquisition sees Host stopping; it must not create a memory entry.
					if _, err := store.Create(ctx, session.CreateRequest{ID: "after-stop"}); !errors.Is(err, gordis.ErrClosed) {
						return errors.New("stopping discovery silently selected memory")
					}
					if _, err := store.Get(ctx, "after-stop"); !errors.Is(err, session.ErrNotFound) {
						return errors.New("failed discovery published Session")
					}
				} else if !errors.Is(err, contracts.ErrClosed) {
					return err
				}
				return nil
			}
			host, err := gordis.NewHost(&events.Plugin{}, &runtimeplugin.Plugin{}, &observedJSONL{after: after}, &lookupProbe{&scope, &store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Shutdown(ctx) })
			for _, spec := range []gordis.InstanceSpec{{ID: "events", Plugin: "gordis/events"}, {ID: "session", Plugin: runtimeplugin.PluginID}, {ID: backendID, Plugin: jsonlplugin.PluginID, Config: []byte(`{"directory":` + strconv.Quote(directory) + `}`)}, {ID: "probe", Plugin: "test.lookup-probe"}} {
				if _, err := host.Env().MountReady(ctx, spec); err != nil {
					t.Fatal(err)
				}
			}
			current, err = store.Create(ctx, session.CreateRequest{ID: "saved"})
			if err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := host.Shutdown(deadline); err != nil {
				t.Fatal(err)
			}
			if !afterCalled || runtimeAlive != (backendID == "zz-backend") {
				t.Fatal("did not exercise both actual cleanup orders", afterCalled, runtimeAlive)
			}
			owner, err := jsonl.NewOwner(directory)
			if err != nil {
				t.Fatal("shutdown left writer lock", err)
			}
			defer owner.Close()
			reopened := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: func(ctx context.Context) (session.PersistenceLease, bool, error) {
				lease, err := owner.Acquire(ctx)
				return lease, true, err
			}})
			defer reopened.Close(ctx)
			restored, err := reopened.Open(ctx, "saved")
			if err != nil {
				t.Fatal(err)
			}
			pending, err := session.NewInbox(restored).Pending(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if runtimeAlive && len(pending[contracts.NextTurn]) != 1 {
				t.Fatal("post-Scope Commit lost", pending)
			}
		})
	}
}

// A differently named plugin provides the same contract; no JSONL ID is used
// by the Session resolver. Its resource Acquire can fail after discovery.
type alternateProvider struct {
	provider session.PersistenceProvider
	calls    *int
}

func (p *alternateProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.alternative-persistence", Provides: []gordis.ServiceSpec{session.PersistenceKey.Spec()}, New: func() gordis.Plugin { return &alternateProvider{p.provider, p.calls} }}
}
func (p *alternateProvider) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide[session.PersistenceProvider](session.PersistenceKey, p)
}
func (p *alternateProvider) Acquire(ctx context.Context) (session.PersistenceLease, error) {
	*p.calls++
	return p.provider.Acquire(ctx)
}

func TestLookupAlternativeProviderViewAndAcquireFailure(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible", true: "isolated"}[isolated], func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			owner, err := jsonl.NewOwner(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			calls := 0
			var scope *gordis.Scope
			var store session.Store
			host, err := gordis.NewHost(&events.Plugin{}, &runtimeplugin.Plugin{}, &alternateProvider{owner, &calls}, &lookupProbe{&scope, &store})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Shutdown(ctx)
			specs := []gordis.InstanceSpec{{ID: "events", Plugin: "gordis/events"}, {ID: "session", Plugin: runtimeplugin.PluginID}, {ID: "backend", Plugin: "test.alternative-persistence"}, {ID: "probe", Plugin: "test.lookup-probe"}}
			for _, spec := range specs {
				env := host.Env()
				if isolated && spec.ID == "backend" {
					env = env.Isolate(session.PersistenceKey, "private-backend")
				}
				if _, err := env.MountReady(ctx, spec); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 0 {
				t.Fatal("Activate acquired backend")
			}
			current, err := store.Create(ctx, session.CreateRequest{ID: "saved"})
			if err != nil {
				t.Fatal(err)
			}
			if calls != map[bool]int{false: 1, true: 0}[isolated] {
				t.Fatal("wrong View/acquisition", calls)
			}
			if err := store.(session.ResourceStore).Release(ctx, current); err != nil {
				t.Fatal(err)
			}
			owner.Stop()
			_, err = store.(session.StoreOpener).Open(ctx, "saved")
			if isolated {
				if !errors.Is(err, session.ErrResumeUnsupported) {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, contracts.ErrClosed) {
					t.Fatal("Acquire failed but downgraded", err)
				}
			}
			if _, err := store.Create(ctx, session.CreateRequest{ID: "new"}); !isolated && !errors.Is(err, contracts.ErrClosed) {
				t.Fatal("Acquire failure became memory", err)
			}
		})
	}
}

type wrongContract struct{}

func (*wrongContract) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.wrong-contract", Provides: []gordis.ServiceSpec{gordis.NewKey[string](session.PersistenceKey.Name()).Spec()}, New: func() gordis.Plugin { return &wrongContract{} }}
}
func (*wrongContract) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(gordis.NewKey[string](session.PersistenceKey.Name()), "wrong")
}
func TestLookupContractErrorsNeverBecomeMemory(t *testing.T) {
	ctx := context.Background()
	var scope *gordis.Scope
	var store session.Store
	host, err := gordis.NewHost(&events.Plugin{}, &runtimeplugin.Plugin{}, &wrongContract{}, &lookupProbe{&scope, &store})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(ctx)
	for _, spec := range []gordis.InstanceSpec{{ID: "events", Plugin: "gordis/events"}, {ID: "session", Plugin: runtimeplugin.PluginID}, {ID: "backend", Plugin: "test.wrong-contract"}, {ID: "probe", Plugin: "test.lookup-probe"}} {
		if _, err := host.Env().MountReady(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(ctx, session.CreateRequest{ID: "wrong"}); err == nil || !strings.Contains(err.Error(), "contract mismatch") {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "wrong"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("discovery error published Session", err)
	}
}

func TestHostFinalBackendFailureExposesRetryOwner(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	var scope *gordis.Scope
	var store session.Store
	host, err := gordis.NewHost(&events.Plugin{}, &runtimeplugin.Plugin{}, &jsonlplugin.Plugin{}, &lookupProbe{&scope, &store})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(ctx)
	for _, spec := range []gordis.InstanceSpec{{ID: "events", Plugin: "gordis/events"}, {ID: "session", Plugin: runtimeplugin.PluginID}, {ID: "zz-backend", Plugin: jsonlplugin.PluginID, Config: []byte(`{"directory":` + strconv.Quote(directory) + `}`)}, {ID: "probe", Plugin: "test.lookup-probe"}} {
		if _, err := host.Env().MountReady(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	provider, found, err := scope.Lookup(session.PersistenceKey)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	lease, err := provider.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Retain a transferred physical handle so the real Backend.Close rejects
	// final shutdown. The error must preserve the owner, not just its text.
	handle, err := lease.Backend().Create(ctx, protocol.SessionHeader{Version: 4, ID: "stray"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, session.CreateRequest{ID: "normal"}); err != nil {
		t.Fatal(err)
	}
	err = host.Shutdown(ctx)
	var cleanup *jsonl.OwnerCloseError
	var runtimeCleanup *session.RuntimeCleanupError
	if !errors.As(err, &cleanup) || cleanup.Owner != provider || !errors.As(err, &runtimeCleanup) {
		t.Fatal("Host lost explicit retry owners", err)
	}
	competing, lockErr := jsonl.NewOwner(directory)
	if competing != nil {
		_ = competing.Close()
	}
	if !errors.Is(lockErr, jsonl.ErrStoreLocked) {
		t.Fatal("failed cleanup lost lock", lockErr)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup.Owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtimeCleanup.Runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := jsonl.NewOwner(directory)
	if err != nil {
		t.Fatal("explicit retry left lock", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
