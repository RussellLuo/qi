package jsonl

import (
	"context"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// Owner holds the plugin's resource reference and independent transaction leases.
// It never waits for another Scope or calls Runtime. Construct it with NewOwner;
// all reference changes and physical close attempts share one mutex.
type Owner struct {
	mu         sync.Mutex
	backend    *Backend
	accepting  bool
	pluginHeld bool
	leases     int
	closed     bool
	// A failed final Release keeps the lease and an explicit owner retry path.
	pending *persistenceLease
}

var _ session.PersistenceProvider = (*Owner)(nil)

// NewOwner creates a Backend and holds its initial plugin reference. It has the
// same directory validation and exclusive-lock behavior as NewPersistence.
func NewOwner(directory string) (*Owner, error) {
	backend, err := NewPersistence(directory)
	if err != nil {
		return nil, err
	}
	return &Owner{backend: backend, accepting: true, pluginHeld: true}, nil
}

// Acquire returns a lease only while this owner accepts new transactions. Stop
// and Close serialize with acquisition; an accepted lease survives both.
func (o *Owner) Acquire(ctx context.Context) (session.PersistenceLease, error) {
	if o == nil {
		return nil, contracts.ErrClosed
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !o.accepting {
		return nil, contracts.ErrClosed
	}
	o.leases++
	return &persistenceLease{owner: o}, nil
}

// Stop rejects new acquisitions without releasing the plugin's reference or
// invalidating existing leases. It is idempotent and suitable for OnStop.
func (o *Owner) Stop() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

// Close stops acquisition and releases the plugin's reference. With active
// leases it returns immediately, transferring final-close responsibility to the
// last Release; success does not then mean the Backend is physically closed.
// Without leases it closes the Backend. Failure retains the plugin reference.
// Close also retries a failed final lease Release, without releasing any other
// active lease. A successful retry makes that lease's Release idempotently succeed.
func (o *Owner) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
	if o.closed {
		return nil
	}
	if o.pluginHeld && o.leases > 0 {
		o.pluginHeld = false
		return nil
	}
	if o.pluginHeld || o.pending != nil {
		return o.closeBackendLocked()
	}
	return nil
}

// OwnerCloseError identifies retained physical cleanup responsibility. Preserve
// this error (or Owner) and retry Owner.Close after repairing the cause. Backend
// errors remain discoverable through errors.Is/As; Host cleanup need not rerun.
type OwnerCloseError struct {
	Owner *Owner
	Err   error
}

func (e *OwnerCloseError) Error() string {
	return fmt.Sprintf("close JSONL resource owner for %q: %v", e.Owner.backend.dir, e.Err)
}

func (e *OwnerCloseError) Unwrap() error { return e.Err }

func (o *Owner) closeBackendLocked() error {
	if err := o.backend.Close(); err != nil {
		return &OwnerCloseError{Owner: o, Err: err}
	}
	if o.pending != nil {
		o.pending.released = true
		o.pending = nil
		o.leases--
	}
	o.pluginHeld, o.closed = false, true
	return nil
}

type persistenceLease struct {
	owner    *Owner
	released bool // guarded by owner.mu
}

var _ session.PersistenceLease = (*persistenceLease)(nil)

func (l *persistenceLease) Backend() session.Persistence { return l.owner.backend }

func (l *persistenceLease) Release() error {
	o := l.owner
	o.mu.Lock()
	defer o.mu.Unlock()
	if l.released {
		return nil
	}
	if o.pluginHeld || o.leases > 1 {
		l.released = true
		o.leases--
		return nil
	}
	o.pending = l
	return o.closeBackendLocked()
}
