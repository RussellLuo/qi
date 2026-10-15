package gordisbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
)

var _ contracts.ContributionOwner = (*ScopeOwner)(nil)

func TestScopeOwnerUnavailable(t *testing.T) {
	for name, owner := range map[string]*ScopeOwner{"nil": nil, "zero": {}, "nil scope": NewScopeOwner(nil), "invalid scope": NewScopeOwner(&gordis.Scope{})} {
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

type publicOwnerProbe struct{ ready chan<- *ScopeOwner }

func (p *publicOwnerProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.public-owner", New: func() gordis.Plugin { return &publicOwnerProbe{ready: p.ready} }}
}

func (p *publicOwnerProbe) Activate(_ context.Context, scope *gordis.Scope) error {
	p.ready <- NewScopeOwner(scope)
	return nil
}

// Verify the public wrapper's delegation; generation and drain behavior live in
// the internal helper's tests.
func TestScopeOwnerPublicContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan *ScopeOwner, 1)
	host, err := gordis.NewHost(&publicOwnerProbe{ready: ready})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(context.Background())
	instance, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "public-owner", Plugin: "test.public-owner"})
	if err != nil {
		t.Fatal(err)
	}
	owner := <-ready
	if identity := owner.Identity(); identity.ID != "public-owner" || !identity.Valid() {
		t.Fatalf("identity = %+v", identity)
	}
	release, err := owner.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	cleaned := false
	if err := owner.DeferCleanup("public cleanup", func() { cleaned = true }); err != nil {
		t.Fatal(err)
	}
	if err := instance.Unmount(ctx); err != nil {
		t.Fatal(err)
	}
	if !cleaned {
		t.Fatal("public cleanup was not delegated")
	}
}
