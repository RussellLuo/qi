package gordisowner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
)

type ownerProbe struct{ ready chan<- ownerProbeResult }
type ownerProbeResult struct {
	owner       *Owner
	scopeID     string
	generation  uint64
	stopped     <-chan struct{}
	cleaned     <-chan struct{}
	activateErr error
}

func (p *ownerProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.owner-probe", New: func() gordis.Plugin { return &ownerProbe{ready: p.ready} }}
}

func (p *ownerProbe) Activate(_ context.Context, scope *gordis.Scope) error {
	owner := New(scope)
	release, activateErr := owner.Acquire()
	if release != nil {
		release()
	}
	stopped, cleaned := make(chan struct{}), make(chan struct{})
	if err := owner.DeferCleanup("withdraw contribution", func() { close(stopped) }); err != nil {
		return err
	}
	if err := scope.Defer("close owned resource", func(context.Context) error { close(cleaned); return nil }); err != nil {
		return err
	}
	p.ready <- ownerProbeResult{owner: owner, scopeID: scope.ID(), generation: scope.Generation(), stopped: stopped, cleaned: cleaned, activateErr: activateErr}
	return nil
}

func TestScopeOwnerUsesAndDrainsExactGordisGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan ownerProbeResult, 1)
	host, err := gordis.NewHost(&ownerProbe{ready: ready})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := host.Shutdown(shutdownCtx); err != nil {
			t.Error(err)
		}
	})
	instance, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "owner", Plugin: "test.owner-probe"})
	if err != nil {
		t.Fatal(err)
	}
	result := <-ready
	if !errors.Is(result.activateErr, gordis.ErrNotReady) {
		t.Fatalf("Acquire during Activate = %v, want ErrNotReady", result.activateErr)
	}
	identity := result.owner.Identity()
	if identity.ID != result.scopeID || identity.Generation != result.generation {
		t.Fatalf("owner identity = %+v, scope = %s/%d", identity, result.scopeID, result.generation)
	}
	release, err := result.owner.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "other", Plugin: "test.owner-probe"}); err != nil {
		t.Fatal(err)
	}
	other := <-ready
	done := make(chan error, 1)
	go func() { done <- instance.Unmount(ctx) }()
	select {
	case <-result.stopped:
	case <-ctx.Done():
		t.Fatal("OnStop was not reached:", ctx.Err())
	}
	select {
	case <-result.cleaned:
		t.Fatal("final resource cleanup overtook its lease")
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("generation stop overtook its lease: %v", err)
	default:
	}
	if release, err := result.owner.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		if release != nil {
			release()
		}
		t.Fatalf("frozen owner Acquire = %v, want ErrClosed", err)
	}
	if err := result.owner.DeferCleanup("late cleanup", func() { t.Error("late cleanup ran") }); !errors.Is(err, gordis.ErrClosed) {
		t.Fatalf("frozen owner DeferCleanup = %v, want ErrClosed", err)
	}
	otherRelease, err := other.owner.Acquire()
	if err != nil {
		t.Fatal("other owner stopped:", err)
	}
	otherRelease()
	select {
	case <-other.stopped:
		t.Fatal("stopping one owner withdrew another Scope")
	default:
	}
	release()
	release() // Gordis lease release remains idempotent.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("generation cleanup after release: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not drain after release:", ctx.Err())
	}
	select {
	case <-result.cleaned:
	default:
		t.Fatal("final resource cleanup did not run")
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "owner", Plugin: "test.owner-probe"}); err != nil {
		t.Fatal(err)
	}
	replacement := <-ready
	if replacement.owner.Identity().ID != identity.ID || replacement.owner.Identity().Generation == identity.Generation {
		t.Fatalf("replacement identity = %+v, previous = %+v", replacement.owner.Identity(), identity)
	}
	if result.owner.Identity() != identity {
		t.Fatal("old owner identity followed replacement")
	}
	if release, err := result.owner.Acquire(); !errors.Is(err, gordis.ErrClosed) {
		if release != nil {
			release()
		}
		t.Fatalf("old generation acquired replacement: %v", err)
	}
	newRelease, err := replacement.owner.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	newRelease()
	select {
	case <-replacement.stopped:
		t.Fatal("old generation cleanup withdrew replacement")
	default:
	}
}

func TestUnavailableOwner(t *testing.T) {
	for name, owner := range map[string]*Owner{"nil": nil, "zero": {}, "nil scope": New(nil), "invalid scope": New(&gordis.Scope{})} {
		t.Run(name, func(t *testing.T) {
			if identity := owner.Identity(); identity != (contracts.OwnerIdentity{}) {
				t.Fatalf("identity = %+v", identity)
			}
			if release, err := owner.Acquire(); release != nil || !errors.Is(err, contracts.ErrOwnerUnavailable) {
				t.Fatalf("Acquire returned release: %t, error: %v", release != nil, err)
			}
			if err := owner.DeferCleanup("cleanup", func() { t.Error("unavailable cleanup ran") }); !errors.Is(err, contracts.ErrOwnerUnavailable) {
				t.Fatalf("DeferCleanup = %v", err)
			}
		})
	}
}
