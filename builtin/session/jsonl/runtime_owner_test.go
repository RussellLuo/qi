package jsonl

import (
	"context"
	"errors"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestRuntimeJSONLFinalLeaseCloseFailureRetainsBothRetryOwners(t *testing.T) {
	for _, kind := range []string{"release", "delete", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			o := testResourceOwner(t, dir)
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolveOwner(o)})
			current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			fault := &faultLockCloser{Closer: o.backend.writerLock, err: errors.New("final close failure")}
			o.backend.writerLock = fault
			t.Cleanup(func() { fault.err = nil; _ = r.Close(ctx) })
			if err := o.Close(); err != nil {
				t.Fatal("plugin reference waited for Runtime", err)
			}
			switch kind {
			case "release":
				err = r.Release(ctx, current)
			case "delete":
				err = r.Delete(ctx, current.ID())
			case "rollback":
				err = r.RollbackCreate(ctx, current)
			}
			var runtimeCleanup *session.RuntimeCleanupError
			var ownerCleanup *OwnerCloseError
			if !errors.Is(err, fault.err) || !errors.As(err, &runtimeCleanup) || runtimeCleanup.Runtime != r || !errors.As(err, &ownerCleanup) || ownerCleanup.Owner != o {
				t.Fatal("final close lost retry owners", err)
			}
			expectLocked(t, dir)
			if got, err := r.Get(ctx, current.ID()); err != nil || got != current {
				t.Fatal("Runtime lost exact release credential", err)
			}
			fault.err = nil
			// Either physical owner or Runtime may be explicitly retried first.
			if kind == "delete" {
				if err := ownerCleanup.Owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtimeCleanup.Runtime.Close(ctx); err != nil {
				t.Fatal(err)
			}
			reopened := expectUnlocked(t, dir)
			handle, err := reopened.Open(ctx, current.ID())
			if kind == "release" {
				if err != nil {
					t.Fatal("release removed history", err)
				}
				if err := handle.Close(); err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, session.ErrNotFound) {
				t.Fatal("delete/rollback retained history or was repeated incorrectly", err)
			}
		})
	}
}
