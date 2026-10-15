package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	ErrNotCreated     = errors.New("session was not created by this runtime acquisition")
	ErrCleanupPending = errors.New("session has a different cleanup operation pending")
)

type RuntimeOptions struct {
	// ResolvePersistence runs once per Create/Open or unopened Delete transaction.
	// Nil or normal absence selects memory. Errors never select memory. It runs
	// under the instance-table lock and must not reenter Runtime.
	ResolvePersistence PersistenceResolver
	Observer           contracts.ObservationDispatcher
	// HeaderCheck admits existing history before any physical repair or commit.
	// It runs under the instance-table lock and must not reenter Runtime.
	HeaderCheck func(protocol.SessionHeader) error
}

// Runtime owns the only running-instance table and connects the domain state
// machine to per-acquisition persistence handles and resource leases. The caller
// drains Factory/Handles before closing Runtime. Runtime releases its leases but
// never directly closes a shared backend.
type Runtime struct {
	mu       sync.RWMutex
	nextID   uint64
	sessions map[protocol.SessionID]*runtimeEntry
	pending  []*runtimeEntry // lease cleanup without a published Session or ID
	options  RuntimeOptions
	closed   bool
}

type cleanupKind uint8

const (
	cleanupNone cleanupKind = iota
	cleanupRelease
	cleanupDelete
	cleanupRollback
)

type runtimeEntry struct {
	session *LiveSession
	handle  PersistenceHandle
	backend Persistence
	lease   PersistenceLease
	created bool
	deleted bool
	cleanup cleanupKind
}

var (
	_ ResourceStore = (*Runtime)(nil)
	_ StoreOpener   = (*Runtime)(nil)
)

func NewRuntime(options RuntimeOptions) *Runtime {
	return &Runtime{sessions: make(map[protocol.SessionID]*runtimeEntry), options: options}
}

func (r *Runtime) Create(ctx context.Context, request CreateRequest) (result Session, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, contracts.ErrClosed
	}
	entry, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	entry.created = true
	var acquiredID protocol.SessionID
	defer func() {
		if result == nil {
			if entry.handle != nil {
				err = r.abort(acquiredID, entry, err)
			} else {
				err = r.releaseTemporary(entry, err)
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := request.ID
		if id == "" {
			r.nextID++
			id = protocol.SessionID(fmt.Sprintf("session-%d", r.nextID))
		}
		if _, exists := r.sessions[id]; exists {
			if request.ID == "" {
				continue
			}
			return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
		}
		header, err := CreateHeader(id, request.Header)
		if err != nil {
			return nil, err
		}
		current, err := Restore(header, nil, r.options.Observer, nil)
		if err != nil {
			return nil, err
		}
		entry.session = current
		if entry.backend != nil {
			entry.handle, err = entry.backend.Create(ctx, header.Clone())
			if errors.Is(err, ErrAlreadyExists) && request.ID == "" {
				continue
			}
			if err != nil {
				return nil, err
			}
			current.commit = entry.handle.Commit
			acquiredID = id
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current.runtime = r
		r.sessions[id] = entry
		return current, nil
	}
}

func (r *Runtime) Get(ctx context.Context, id protocol.SessionID) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, contracts.ErrClosed
	}
	entry := r.sessions[id]
	if entry == nil || entry.session == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return entry.session, nil
}

// Open validates before physical preparation. Attach owns logical recovery;
// Open never borrows a live instance and never commits recovery facts.
func (r *Runtime) Open(ctx context.Context, id protocol.SessionID) (result Session, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, contracts.ErrClosed
	}
	entry, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			if entry.handle != nil {
				err = r.abort(id, entry, err)
			} else {
				err = r.releaseTemporary(entry, err)
			}
		}
	}()
	if entry.backend == nil {
		return nil, ErrResumeUnsupported
	}
	if _, exists := r.sessions[id]; exists {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, id)
	}
	handle, err := entry.backend.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	entry.handle = handle
	header, events, err := handle.Read(ctx)
	if err == nil && header.ID != id {
		err = fmt.Errorf("%w: history ID does not match %s", protocol.ErrInvalidProtocol, id)
	}
	if err == nil && r.options.HeaderCheck != nil {
		err = r.options.HeaderCheck(header.Clone())
	}
	if err == nil {
		entry.session, err = Restore(header, events, r.options.Observer, handle.Commit)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = handle.Prepare(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	// Every reopened acquisition enters through Attach, including balanced or
	// empty history. Attach commits the existing recovery marker policy once.
	entry.session.needsRecovery = true
	entry.session.runtime = r
	r.sessions[id] = entry
	return entry.session, nil
}

func (r *Runtime) acquire(ctx context.Context) (*runtimeEntry, error) {
	entry := &runtimeEntry{}
	if r.options.ResolvePersistence == nil {
		return entry, nil
	}
	lease, found, err := r.options.ResolvePersistence(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return entry, nil
	}
	if lease == nil {
		return nil, errors.New("Session persistence resolver returned a nil lease")
	}
	entry.lease, entry.backend = lease, lease.Backend()
	if entry.backend == nil {
		return nil, r.releaseTemporary(entry, errors.New("Session persistence lease returned a nil backend"))
	}
	return entry, nil
}

// releaseTemporary keeps failed lease cleanup even when no history was acquired.
// It never retries a Delete or adopts cleanup of a conflicting existing ID.
func (r *Runtime) releaseTemporary(entry *runtimeEntry, cause error) error {
	entry.session = nil
	entry.cleanup = cleanupRelease
	if err := r.clean("", entry); err != nil {
		r.pending = append(r.pending, entry)
		return errors.Join(cause, &RuntimeCleanupError{Runtime: r, Err: err})
	}
	return cause
}

// RuntimeCleanupError retains the Runtime and its explicit Close retry path,
// including failures with no published Session. Underlying errors are preserved.
type RuntimeCleanupError struct {
	Runtime *Runtime
	Err     error
}

func (e *RuntimeCleanupError) Error() string {
	return fmt.Sprintf("Session Runtime cleanup: %v", e.Err)
}
func (e *RuntimeCleanupError) Unwrap() error { return e.Err }

// abort retains failed cleanup under the same ID for Close to retry. No
// unpublished Session is returned or made available through Get.
func (r *Runtime) abort(id protocol.SessionID, entry *runtimeEntry, cause error) error {
	entry.session = nil
	entry.cleanup = cleanupRelease
	if entry.created {
		entry.cleanup = cleanupRollback
	}
	if err := r.clean(id, entry); err != nil {
		r.sessions[id] = entry
		return errors.Join(cause, &RuntimeCleanupError{Runtime: r, Err: err})
	}
	return cause
}

func (r *Runtime) Release(ctx context.Context, current Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current == nil {
		return ErrNotFound
	}
	entry := r.sessions[current.ID()]
	if entry == nil {
		if instance, ok := current.(*LiveSession); ok && instance.runtime == r {
			instance.mu.Lock()
			released := instance.runtimeReleased
			instance.mu.Unlock()
			if released {
				return nil
			}
		}
	}
	if entry == nil || entry.session != current {
		return fmt.Errorf("%w: runtime instance %s", ErrNotFound, current.ID())
	}
	return r.dispose(current.ID(), entry, cleanupRelease)
}

func (r *Runtime) RollbackCreate(ctx context.Context, current Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current == nil {
		return ErrNotFound
	}
	entry := r.sessions[current.ID()]
	if entry == nil || entry.session != current {
		return fmt.Errorf("%w: creation instance %s", ErrNotFound, current.ID())
	}
	if !entry.created {
		return ErrNotCreated
	}
	return r.dispose(current.ID(), entry, cleanupRollback)
}

func (r *Runtime) Delete(ctx context.Context, id protocol.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return contracts.ErrClosed
	}
	if entry := r.sessions[id]; entry != nil {
		if entry.session == nil {
			return ErrCleanupPending
		}
		return r.dispose(id, entry, cleanupDelete)
	}
	entry, err := r.acquire(ctx)
	if err != nil {
		return err
	}
	if entry.backend == nil {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	err = entry.backend.Delete(ctx, id)
	return r.releaseTemporary(entry, err)
}

func (r *Runtime) dispose(id protocol.SessionID, entry *runtimeEntry, kind cleanupKind) error {
	if entry.cleanup != cleanupNone && entry.cleanup != kind {
		return ErrCleanupPending
	}
	cleanup := func() error {
		entry.cleanup = kind
		if err := r.clean(id, entry); err != nil {
			return &RuntimeCleanupError{Runtime: r, Err: err}
		}
		entry.session.runtimeReleased = true // Session mutex held by Release.
		return nil
	}
	if err := entry.session.Release(cleanup); err != nil {
		return err
	}
	delete(r.sessions, id)
	return nil
}

func (r *Runtime) clean(id protocol.SessionID, entry *runtimeEntry) error {
	if entry.handle != nil {
		var err error
		if entry.cleanup == cleanupRollback {
			err = entry.handle.RollbackCreate()
		} else {
			err = entry.handle.Close()
		}
		if err != nil {
			return err
		}
		entry.handle = nil
	}
	if entry.cleanup == cleanupDelete && entry.backend != nil && !entry.deleted {
		if err := entry.backend.Delete(context.Background(), id); err != nil {
			return err
		}
		entry.deleted = true
	}
	if entry.lease != nil {
		if err := entry.lease.Release(); err != nil {
			return err
		}
		entry.lease = nil
	}
	entry.backend = nil
	return nil
}

// Close rejects new admission, releases detached instances and retries earlier
// cleanup failures. Active owners must drain first; their resources are kept.
// Failed cleanup keeps the exact handle/lease or temporary resource for retry.
func (r *Runtime) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var result error
	for id, entry := range r.sessions {
		if entry.session == nil {
			if err := r.clean(id, entry); err != nil {
				result = errors.Join(result, err)
			} else {
				delete(r.sessions, id)
			}
			continue
		}
		kind := entry.cleanup
		if kind == cleanupNone {
			kind = cleanupRelease
		}
		result = errors.Join(result, r.dispose(id, entry, kind))
	}
	pending := r.pending
	r.pending = nil
	for _, entry := range pending {
		if err := r.clean("", entry); err != nil {
			r.pending = append(r.pending, entry)
			result = errors.Join(result, err)
		}
	}
	if result != nil {
		return &RuntimeCleanupError{Runtime: r, Err: result}
	}
	return result
}
