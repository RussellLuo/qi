package jsonl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func ownerLease(t *testing.T, o *Owner) session.PersistenceLease {
	t.Helper()
	lease, err := o.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	})
	return lease
}

func testResourceOwner(t *testing.T, dir string) *Owner {
	t.Helper()
	o, err := NewOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	})
	return o
}

func expectUnlocked(t *testing.T, dir string) *Backend {
	t.Helper()
	p, err := NewPersistence(dir)
	if err != nil {
		t.Fatal("directory lock was not released", err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func assertOwnerError(t *testing.T, err error, cause error, o *Owner) *OwnerCloseError {
	t.Helper()
	var cleanup *OwnerCloseError
	if !errors.Is(err, cause) || !errors.As(err, &cleanup) || cleanup.Owner != o {
		t.Fatalf("cleanup lost cause or owner: %v", err)
	}
	return cleanup
}

func TestOwnerConstructionAndNoLeaseClose(t *testing.T) {
	if o, err := NewOwner(""); o != nil || err == nil {
		t.Fatalf("empty directory: %v, %v", o, err)
	}
	dir := t.TempDir()
	o := testResourceOwner(t, dir)
	if other, err := NewOwner(dir); other != nil || !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second owner: %v, %v", other, err)
	}
	o.Stop()
	o.Stop()
	expectLocked(t, dir) // Stop still holds the plugin reference.
	if lease, err := o.Acquire(context.Background()); lease != nil || !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("acquire after stop: %v, %v", lease, err)
	}
	for range 2 {
		if err := o.Close(); err != nil {
			t.Fatal(err)
		}
	}
	expectUnlocked(t, dir)
}

func TestOwnerLeaseReleaseOrdersPreserveResources(t *testing.T) {
	for _, pluginFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("plugin-first=%v", pluginFirst), func(t *testing.T) {
			dir := t.TempDir()
			o := testResourceOwner(t, dir)
			first, second, third := ownerLease(t, o), ownerLease(t, o), ownerLease(t, o)
			h, err := first.Backend().Create(context.Background(), protocol.SessionHeader{Version: 4, ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Close() })
			if pluginFirst {
				// Closing the plugin neither waits for nor closes this active handle.
				for range 2 {
					if err := o.Close(); err != nil {
						t.Fatal(err)
					}
				}
				expectLocked(t, dir)
			}
			event, err := protocol.EncodeSessionEvent(protocol.NewTurnStarted(1))
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Commit(event); err != nil {
				t.Fatal("leased handle unusable after plugin close", err)
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			for _, lease := range []session.PersistenceLease{second, first} {
				for range 2 {
					if err := lease.Release(); err != nil {
						t.Fatal(err)
					}
				}
				expectLocked(t, dir)
			}
			if err := third.Release(); err != nil {
				t.Fatal(err)
			}
			if !pluginFirst {
				expectLocked(t, dir) // The plugin alone still owns the Backend.
				// A non-final lease Release must not stop future acquisition.
				lease := ownerLease(t, o)
				if err := lease.Release(); err != nil {
					t.Fatal(err)
				}
				if err := o.Close(); err != nil {
					t.Fatal(err)
				}
			}
			reopened := expectUnlocked(t, dir)
			restored, err := reopened.Open(context.Background(), "history")
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			header, events, err := restored.Read(context.Background())
			if err != nil || header.Version != 4 || len(events) != 1 || events[0].Type != protocol.TurnStarted {
				t.Fatalf("leased commit/history lost: %+v, %+v, %v", header, events, err)
			}
			if err := third.Release(); err != nil {
				t.Fatal(err)
			}
			if err := o.Close(); err != nil {
				t.Fatal(err)
			}
			expectLocked(t, dir) // Stale releases cannot unlock the new Backend.
		})
	}
}

func TestOwnerCanceledAcquireDoesNotRetainReference(t *testing.T) {
	dir := t.TempDir()
	o := testResourceOwner(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if lease, err := o.Acquire(ctx); lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire: %v, %v", lease, err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	expectUnlocked(t, dir)
}

// Close failures are injected before touching the real directory lock.
type faultLockCloser struct {
	io.Closer
	err error
}

func (f *faultLockCloser) Close() error {
	if f.err != nil {
		return f.err
	}
	return f.Closer.Close()
}

func TestOwnerFinalBackendCloseFailureRetainsRetry(t *testing.T) {
	for _, withLease := range []bool{false, true} {
		for _, retryOwner := range []bool{false, true} {
			t.Run(fmt.Sprintf("lease=%v/owner-retry=%v", withLease, retryOwner), func(t *testing.T) {
				dir := t.TempDir()
				o := testResourceOwner(t, dir)
				cause := errors.New("injected directory lock close failure")
				fault := &faultLockCloser{Closer: o.backend.writerLock, err: cause}
				o.backend.writerLock = fault
				// Restore before cleanup if an assertion fails.
				t.Cleanup(func() { fault.err = nil })
				release := o.Close
				if withLease {
					lease := ownerLease(t, o)
					if err := o.Close(); err != nil {
						t.Fatal(err)
					}
					release = lease.Release
				}
				cleanup := assertOwnerError(t, release(), cause, o)
				expectLocked(t, dir)
				if lease, err := o.Acquire(context.Background()); lease != nil || !errors.Is(err, contracts.ErrClosed) {
					t.Fatalf("acquire after failed close: %v, %v", lease, err)
				}
				// Repeated failures preserve the same resource owner.
				assertOwnerError(t, cleanup.Owner.Close(), cause, o)
				expectLocked(t, dir)
				fault.err = nil
				retry := release
				if retryOwner {
					retry = cleanup.Owner.Close
				}
				if err := retry(); err != nil {
					t.Fatal(err)
				}
				if err := release(); err != nil {
					t.Fatal("release after owner retry", err)
				}
				expectUnlocked(t, dir)
			})
		}
	}
}

func TestOwnerHandleCleanupFailureKeepsLeaseAndLock(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			dir := t.TempDir()
			o := testResourceOwner(t, dir)
			lease := ownerLease(t, o)
			handle, err := lease.Backend().Create(context.Background(), protocol.SessionHeader{Version: 4, ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			h := handle.(*fileHandle)
			cause := errors.New("injected history close failure")
			fault := &faultFile{logFile: h.file, closeErr: cause}
			h.file = fault
			t.Cleanup(func() { fault.closeErr = nil; _ = h.Close() })
			if err := o.Close(); err != nil {
				t.Fatal(err)
			}
			cleanupHandle := h.Close
			if rollback {
				cleanupHandle = h.RollbackCreate
			}
			if err := cleanupHandle(); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			// Even a premature Release cannot close an outstanding physical handle.
			cleanup := assertOwnerError(t, lease.Release(), session.ErrAttached, o)
			expectLocked(t, dir)
			assertOwnerError(t, cleanup.Owner.Close(), session.ErrAttached, o)
			fault.closeErr = nil
			for range 2 {
				if err := cleanupHandle(); err != nil {
					t.Fatal(err)
				}
			}
			if err := lease.Release(); err != nil {
				t.Fatal(err)
			}
			reopened := expectUnlocked(t, dir)
			_, statErr := os.Stat(reopened.path("history"))
			if (rollback && !errors.Is(statErr, os.ErrNotExist)) || (!rollback && statErr != nil) {
				t.Fatalf("history cleanup semantics: %v", statErr)
			}
		})
	}
}

func TestOwnerAcquireRacesWithStopAndClose(t *testing.T) {
	for _, closePlugin := range []bool{false, true} {
		t.Run(fmt.Sprintf("close-plugin=%v", closePlugin), func(t *testing.T) {
			for round := range 8 {
				dir := t.TempDir()
				o := testResourceOwner(t, dir)
				seed := ownerLease(t, o)
				start := make(chan struct{})
				leases := make(chan session.PersistenceLease, 16)
				errs := make(chan error, 17)
				var workers sync.WaitGroup
				for range 16 {
					workers.Go(func() {
						<-start
						lease, err := o.Acquire(context.Background())
						if err == nil {
							leases <- lease
						} else if lease != nil || !errors.Is(err, contracts.ErrClosed) {
							errs <- fmt.Errorf("acquisition during stop: %v, %v", lease, err)
						}
					})
				}
				workers.Go(func() {
					<-start
					if closePlugin {
						if err := o.Close(); err != nil {
							errs <- err
						}
					} else {
						o.Stop()
					}
				})
				close(start)
				workers.Wait()
				close(leases)
				close(errs)
				for err := range errs {
					t.Error(err)
				}
				if lease, err := o.Acquire(context.Background()); lease != nil || !errors.Is(err, contracts.ErrClosed) {
					t.Fatalf("acquire after stop: %v, %v", lease, err)
				}
				if err := o.Close(); err != nil {
					t.Fatal(err)
				}
				expectLocked(t, dir)
				// Every accepted lease can still acquire and close real histories.
				for lease := range leases {
					workers.Go(func() {
						h, err := lease.Backend().Create(context.Background(), protocol.SessionHeader{Version: 4, ID: protocol.SessionID(fmt.Sprintf("%p", lease))})
						if err != nil {
							t.Error(err)
						} else if err := h.Close(); err != nil {
							t.Error(err)
						}
						if err := lease.Release(); err != nil {
							t.Error(err)
						}
					})
				}
				workers.Wait()
				expectLocked(t, dir) // Seed keeps the last reference alive.
				for range 8 {
					workers.Go(func() {
						if err := seed.Release(); err != nil {
							t.Error(err)
						}
					})
				}
				workers.Wait()
				p := expectUnlocked(t, dir)
				if err := p.Close(); err != nil {
					t.Fatalf("round %d: %v", round, err)
				}
			}
		})
	}
}
