package session

import (
	"context"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// PersistenceProvider atomically acquires an independent resource reference.
// Acquisition racing with provider shutdown either transfers a complete lease
// or returns an error without transferring resources. Failed partial acquisition
// remains provider-owned. The lease protects backend resources, not plugin Scope
// readiness or generation identity, and creates no framework dependency.
type PersistenceProvider interface {
	Acquire(context.Context) (PersistenceLease, error)
}

// PersistenceLease protects one backend acquisition transaction and any history
// handles it obtains. Backend is fixed for this lease. Close or roll back every
// acquired handle before Release; do not use the backend after successful release.
// Shared Backend.Close belongs to the provider, never to the lease holder.
// Release is concurrent-safe, idempotent and retryable. Failure retains cleanup
// responsibility and must expose an owner or an explicit retry path; it must not
// abandon the physical resources. Cleanup cannot be canceled by a context.
type PersistenceLease interface {
	Backend() Persistence
	Release() error
}

// PersistenceResolver discovers and acquires a backend once per transaction.
// Normal absence returns nil, false, nil. Success returns a nonnil lease, true,
// nil. Discovery/acquisition errors are not absence: return nil, false, error
// without transferring a lease. It must not silently fall back to memory.
type PersistenceResolver func(context.Context) (PersistenceLease, bool, error)

// Persistence owns histories and shared physical resources, never domain
// Sessions. Create/Open acquire exclusive writing ownership for one history.
// Success transfers exactly one handle to Runtime within its resource lease.
// On error no handle is
// transferred: the backend must clean up partial acquisition or retain it for
// its own retryable Close. Create returns ErrAlreadyExists for existing history;
// Open/Delete return ErrNotFound for missing history. Delete refuses acquired
// histories. Close releases shared resources only after all handles close;
// failures remain retryable. Runtime does not close the shared backend.
// Backend/handle methods must not call back into Runtime or domain Sessions.
type Persistence interface {
	Create(context.Context, protocol.SessionHeader) (PersistenceHandle, error)
	Open(context.Context, protocol.SessionID) (PersistenceHandle, error)
	Delete(context.Context, protocol.SessionID) error
	Close() error
}

// PersistenceHandle identifies one acquisition, including after Close. Read
// returns the header and committed prefix without modifying physical history.
// Prepare repairs an interrupted physical tail only after Runtime accepts the
// header and domain history; it never appends logical recovery facts.
// Commit receives one encoded event without framing and must durably commit
// it or leave the committed prefix unchanged. Runtime serializes these calls.
// Close is retryable and idempotent and preserves history. RollbackCreate is
// retryable and idempotent and closes/removes only this handle's new history,
// never a replacement. It is invalid on an Open handle. Cleanup has no context
// so cancellation cannot abandon acquired resources.
type PersistenceHandle interface {
	Read(context.Context) (protocol.SessionHeader, []protocol.SessionEvent, error)
	Prepare(context.Context) error
	Commit([]byte) error
	Close() error
	RollbackCreate() error
}
