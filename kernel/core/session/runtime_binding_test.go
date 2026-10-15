package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestRuntimeResolvesOnceAndKeepsEachAcquisitionBackend(t *testing.T) {
	ctx := context.Background()
	first, second := newPersistenceStub(), newPersistenceStub()
	first.seed(t, "session-1")
	calls := 0
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: func(ctx context.Context) (session.PersistenceLease, bool, error) {
		calls++
		if calls == 1 {
			return first.resolve(ctx)
		}
		return second.resolve(ctx)
	}})
	t.Cleanup(func() { _ = r.Close(ctx) })
	a, err := r.Create(ctx, session.CreateRequest{})
	if err != nil || a.ID() != "session-2" || calls != 1 || first.acquires != 1 || second.acquires != 0 {
		t.Fatalf("automatic ID retry changed acquisition: %v, %v, calls=%d", a, err, calls)
	}
	b, err := r.Create(ctx, session.CreateRequest{ID: "other"})
	if err != nil || calls != 2 {
		t.Fatal(b, err, calls)
	}
	for _, current := range []session.Session{a, b} {
		if _, err := current.Append(ctx, protocol.NewTurnStarted(1)); err != nil {
			t.Fatal(err)
		}
		if got, err := r.Get(ctx, current.ID()); err != nil || got != current {
			t.Fatal(got, err)
		}
	}
	if calls != 2 || len(first.histories[a.ID()].events) != 1 || len(second.histories[b.ID()].events) != 1 {
		t.Fatal("Append/Get rediscovered or changed backend", calls)
	}
	if err := r.Delete(ctx, a.ID()); err != nil {
		t.Fatal(err)
	}
	if first.histories[a.ID()] != nil || calls != 2 || first.releases != 1 {
		t.Fatal("Delete did not use original acquisition", calls)
	}
	if err := r.Release(ctx, b); err != nil {
		t.Fatal(err)
	}
	restored, err := r.Open(ctx, b.ID())
	if err != nil || calls != 3 || second.acquires != 2 {
		t.Fatal(restored, err, calls)
	}
	if err := r.Release(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || second.releases != 2 {
		t.Fatal("release rediscovered or leaked backend", calls)
	}
}

func TestRuntimeMemoryInstanceDoesNotAdoptOrDeleteLaterBackendHistory(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	p.seed(t, "same")
	selected, calls := false, 0
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: func(ctx context.Context) (session.PersistenceLease, bool, error) {
		calls++
		if !selected {
			return nil, false, nil
		}
		return p.resolve(ctx)
	}})
	t.Cleanup(func() { _ = r.Close(ctx) })
	current, err := r.Create(ctx, session.CreateRequest{ID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	selected = true
	if _, err := current.Append(ctx, protocol.NewTurnStarted(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "same"); err != nil {
		t.Fatal(err)
	}
	if p.histories["same"] == nil || len(p.histories["same"].events) != 0 || calls != 1 || p.acquires != 0 {
		t.Fatal("memory instance affected current backend", calls)
	}
	if err := r.Delete(ctx, "same"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || p.acquires != 1 || p.releases != 1 || p.histories["same"] != nil {
		t.Fatal("unopened Delete did not acquire exactly one temporary lease")
	}
	selected = false
	if err := r.Delete(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := r.Open(ctx, "missing"); !errors.Is(err, session.ErrResumeUnsupported) {
		t.Fatal(err)
	}
}

func TestRuntimeDiscoveryAndAcquireErrorsDoNotBecomeMemory(t *testing.T) {
	cause := errors.New("discovery/acquisition failed")
	for _, kind := range []string{"discovery", "acquire", "nil-lease"} {
		t.Run(kind, func(t *testing.T) {
			p := newPersistenceStub()
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: func(ctx context.Context) (session.PersistenceLease, bool, error) {
				switch kind {
				case "discovery":
					return nil, false, cause
				case "nil-lease":
					return nil, true, nil
				default:
					p.acquireErr = cause
					return p.resolve(ctx)
				}
			}})
			ctx := context.Background()
			for _, op := range []func() error{
				func() error { _, err := r.Create(ctx, session.CreateRequest{ID: "failure"}); return err },
				func() error { _, err := r.Open(ctx, "failure"); return err },
				func() error { return r.Delete(ctx, "failure") },
			} {
				err := op()
				if err == nil || errors.Is(err, session.ErrResumeUnsupported) || (kind != "nil-lease" && !errors.Is(err, cause)) {
					t.Fatal("error became normal absence", err)
				}
			}
			if _, err := r.Get(ctx, "failure"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("failed acquisition created memory Session", err)
			}
			if p.acquires != 0 || len(p.histories) != 0 {
				t.Fatal("failed discovery transferred resources")
			}
		})
	}
}

func TestRuntimeLeaseReleaseFailurePreservesCleanupAndRetryCredential(t *testing.T) {
	for _, kind := range []string{"release", "rollback", "delete"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			p := newPersistenceStub()
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
			current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("lease release failed")
			p.releaseErr = cause
			t.Cleanup(func() { p.releaseErr = nil; _ = r.Close(ctx) })
			cleanup := func() error { return r.Release(ctx, current) }
			switch kind {
			case "rollback":
				cleanup = func() error { return r.RollbackCreate(ctx, current) }
			case "delete":
				cleanup = func() error { return r.Delete(ctx, current.ID()) }
			}
			err = cleanup()
			var retained *session.RuntimeCleanupError
			if !errors.Is(err, cause) || !errors.As(err, &retained) || retained.Runtime != r {
				t.Fatal("lost cleanup owner", err)
			}
			if !p.handles[0].closed || p.releases != 0 {
				t.Fatal("lease released before handle cleanup or failure discarded")
			}
			if _, err := current.Append(ctx, protocol.NewTurnStarted(1)); !errors.Is(err, contracts.ErrClosed) {
				t.Fatal("failed cleanup reopened admission", err)
			}
			if got, err := r.Get(ctx, "history"); err != nil || got != current {
				t.Fatal("lost exact cleanup credential", got, err)
			}
			before := append([]string(nil), p.trace...)
			p.releaseErr = nil
			if err := retained.Runtime.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, p.trace) || p.acquires != 1 || p.releases != 1 {
				t.Fatal("cleanup retry repeated destructive work or rediscovered backend", p.trace)
			}
			if (kind == "release") != (p.histories["history"] != nil) {
				t.Fatal("cleanup changed history intent")
			}
		})
	}
}

func TestRuntimeTemporaryLeaseFailuresRemainOwned(t *testing.T) {
	for _, kind := range []string{"create-conflict", "open-missing", "delete-missing", "delete-existing", "canceled-create"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			p := newPersistenceStub()
			p.seed(t, "history")
			cause := errors.New("temporary lease release failed")
			p.releaseErr = cause
			resolver := p.resolve
			if kind == "canceled-create" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				resolver = func(ctx context.Context) (session.PersistenceLease, bool, error) {
					lease, found, err := p.resolve(ctx)
					cancel()
					return lease, found, err
				}
			}
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolver})
			t.Cleanup(func() { p.releaseErr = nil; _ = r.Close(context.Background()) })
			var err, operationCause error
			switch kind {
			case "create-conflict":
				_, err = r.Create(ctx, session.CreateRequest{ID: "history"})
				operationCause = session.ErrAlreadyExists
			case "open-missing":
				_, err = r.Open(ctx, "missing")
				operationCause = session.ErrNotFound
			case "delete-missing":
				err = r.Delete(ctx, "missing")
				operationCause = session.ErrNotFound
			case "delete-existing":
				err = r.Delete(ctx, "history")
			case "canceled-create":
				_, err = r.Create(ctx, session.CreateRequest{ID: "new"})
				operationCause = context.Canceled
			}
			var retained *session.RuntimeCleanupError
			if !errors.Is(err, cause) || (operationCause != nil && !errors.Is(err, operationCause)) || !errors.As(err, &retained) || retained.Runtime != r {
				t.Fatal("lost temporary transaction errors/owner", err)
			}
			if p.acquires != 1 || p.releases != 0 {
				t.Fatal("lost temporary lease")
			}
			if _, err := r.Get(context.Background(), "history"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("temporary cleanup published a Session", err)
			}
			if err := retained.Runtime.Close(context.Background()); !errors.Is(err, cause) {
				t.Fatal("temporary cleanup not retried", err)
			}
			before := append([]string(nil), p.trace...)
			p.releaseErr = nil
			if err := r.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, p.trace) || p.acquires != 1 || p.releases != 1 {
				t.Fatal("temporary retry rediscovered/repeated Delete", p.trace)
			}
			if (kind != "delete-existing") != (p.histories["history"] != nil) {
				t.Fatal("temporary cleanup changed unrelated existing history")
			}
		})
	}
}

func TestRuntimeUnpublishedHandleCleanupRetainsFailedLease(t *testing.T) {
	for _, creating := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "create"}[creating], func(t *testing.T) {
			p := newPersistenceStub()
			p.seed(t, "history")
			cause := errors.New("unpublished lease release failed")
			p.releaseErr = cause
			options := session.RuntimeOptions{ResolvePersistence: p.resolve}
			reject := errors.New("header rejected")
			if !creating {
				options.HeaderCheck = func(protocol.SessionHeader) error { return reject }
			}
			r := session.NewRuntime(options)
			t.Cleanup(func() { p.releaseErr = nil; _ = r.Close(context.Background()) })
			id := protocol.SessionID("history")
			var err error
			if creating {
				id = "new"
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				p.afterCreate = cancel
				_, err = r.Create(ctx, session.CreateRequest{ID: id})
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				_, err = r.Open(context.Background(), id)
				if !errors.Is(err, reject) {
					t.Fatal(err)
				}
			}
			var retained *session.RuntimeCleanupError
			if !errors.Is(err, cause) || !errors.As(err, &retained) || retained.Runtime != r {
				t.Fatal("unpublished cleanup lost owner", err)
			}
			if !p.handles[0].closed || p.releases != 0 || p.handles[0].rolledBack != creating {
				t.Fatal("unpublished cleanup changed acquisition intent")
			}
			if _, err := r.Get(context.Background(), id); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("failed acquisition published", err)
			}
			if err := r.Delete(context.Background(), id); !errors.Is(err, session.ErrCleanupPending) {
				t.Fatal("pending acquisition lost its ID/cleanup intent", err)
			}
			before := append([]string(nil), p.trace...)
			p.releaseErr = nil
			if err := retained.Runtime.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, p.trace) || p.acquires != 1 || p.releases != 1 || p.histories["history"] == nil || p.histories["new"] != nil {
				t.Fatal("retry changed original history/cleanup or reacquired", p.trace)
			}
		})
	}
}
