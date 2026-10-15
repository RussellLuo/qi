package session_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// persistenceStub stores raw committed facts, not domain Sessions. Its handles
// model physical ownership independently of Runtime's running-instance table.
// Faults exercise resource semantics without a production memory provider/I/O.
type persistenceStub struct {
	mu                                                               sync.Mutex
	histories                                                        map[protocol.SessionID]*historyStub
	handles                                                          []*handleStub
	trace                                                            []string
	readErr, prepareErr, commitErr, closeErr, rollbackErr, deleteErr error
	failCommit, commits                                              int
	afterCreate                                                      func()
	closed                                                           bool
	acquireErr, releaseErr                                           error
	acquires, releases, releaseAttempts                              int
}

type leaseStub struct {
	backend  *persistenceStub
	released bool
}

func (p *persistenceStub) resolve(ctx context.Context) (session.PersistenceLease, bool, error) {
	lease, err := p.Acquire(ctx)
	return lease, err == nil, err
}

func (p *persistenceStub) Acquire(ctx context.Context) (session.PersistenceLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.acquireErr != nil {
		return nil, p.acquireErr
	}
	if p.closed {
		return nil, contracts.ErrClosed
	}
	p.acquires++
	return &leaseStub{backend: p}, nil
}

func (l *leaseStub) Backend() session.Persistence { return l.backend }

func (l *leaseStub) Release() error {
	p := l.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.released {
		return nil
	}
	p.releaseAttempts++
	if p.releaseErr != nil {
		return p.releaseErr
	}
	l.released = true
	p.releases++
	return nil
}

type historyStub struct {
	header protocol.SessionHeader
	events []protocol.SessionEvent
	active *handleStub
}
type handleStub struct {
	backend                               *persistenceStub
	history                               *historyStub
	created, prepared, closed, rolledBack bool
}

func newPersistenceStub() *persistenceStub {
	return &persistenceStub{histories: make(map[protocol.SessionID]*historyStub)}
}
func (p *persistenceStub) seed(t *testing.T, id protocol.SessionID, events ...protocol.SessionEvent) {
	t.Helper()
	header, err := session.CreateHeader(id, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].Seq = protocol.Sequence(i)
	}
	p.histories[id] = &historyStub{header: header, events: events}
}
func (p *persistenceStub) acquire(h *historyStub, created bool) *handleStub {
	handle := &handleStub{backend: p, history: h, created: created, prepared: created}
	h.active = handle
	p.handles = append(p.handles, handle)
	return handle
}
func (p *persistenceStub) Create(ctx context.Context, header protocol.SessionHeader) (session.PersistenceHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.closed {
		return nil, contracts.ErrClosed
	}
	if p.histories[header.ID] != nil {
		return nil, session.ErrAlreadyExists
	}
	h := &historyStub{header: header.Clone()}
	p.histories[header.ID] = h
	handle := p.acquire(h, true)
	p.trace = append(p.trace, "create")
	if p.afterCreate != nil {
		p.afterCreate()
	}
	return handle, nil
}
func (p *persistenceStub) Open(ctx context.Context, id protocol.SessionID) (session.PersistenceHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.closed {
		return nil, contracts.ErrClosed
	}
	h := p.histories[id]
	if h == nil {
		return nil, session.ErrNotFound
	}
	if h.active != nil {
		return nil, session.ErrAlreadyExists
	}
	p.trace = append(p.trace, "open")
	return p.acquire(h, false), nil
}
func (p *persistenceStub) Delete(_ context.Context, id protocol.SessionID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trace = append(p.trace, "delete")
	if p.deleteErr != nil {
		return p.deleteErr
	}
	h := p.histories[id]
	if h == nil {
		return session.ErrNotFound
	}
	if h.active != nil {
		return session.ErrAttached
	}
	delete(p.histories, id)
	return nil
}
func (p *persistenceStub) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trace = append(p.trace, "backend-close")
	for _, h := range p.histories {
		if h.active != nil {
			return session.ErrAttached
		}
	}
	p.closed = true
	return nil
}
func (h *handleStub) Read(_ context.Context) (protocol.SessionHeader, []protocol.SessionEvent, error) {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trace = append(p.trace, "read")
	events := make([]protocol.SessionEvent, len(h.history.events))
	for i, e := range h.history.events {
		events[i] = e.Clone()
	}
	return h.history.header.Clone(), events, p.readErr
}
func (h *handleStub) Prepare(_ context.Context) error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trace = append(p.trace, "prepare")
	if p.prepareErr != nil {
		return p.prepareErr
	}
	h.prepared = true
	return nil
}
func (h *handleStub) Commit(encoded []byte) error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trace = append(p.trace, "commit")
	p.commits++
	if p.commits == p.failCommit {
		return p.commitErr
	}
	if h.closed {
		return contracts.ErrClosed
	}
	if !h.prepared {
		return errors.New("commit before physical preparation")
	}
	event, err := protocol.DecodeSessionEvent(encoded)
	if err != nil {
		return err
	}
	h.history.events = append(h.history.events, event)
	return nil
}
func (h *handleStub) Close() error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.closed {
		return nil
	}
	p.trace = append(p.trace, "close")
	if p.closeErr != nil {
		return p.closeErr
	}
	h.closed = true
	if h.history.active != h {
		return errors.New("wrong physical acquisition")
	}
	h.history.active = nil
	return nil
}
func (h *handleStub) RollbackCreate() error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.rolledBack {
		return nil
	}
	p.trace = append(p.trace, "rollback")
	if !h.created {
		return session.ErrNotCreated
	}
	if p.rollbackErr != nil {
		return p.rollbackErr
	}
	id := h.history.header.ID
	if p.histories[id] != h.history || h.history.active != h {
		return errors.New("rollback affected replacement")
	}
	delete(p.histories, id)
	h.history.active = nil
	h.closed, h.rolledBack = true, true
	return nil
}

func TestRuntimeMemoryConformance(t *testing.T) {
	checkSessionStore(t, func(t *testing.T) (session.Store, func()) {
		r := session.NewRuntime(session.RuntimeOptions{})
		return r, func() {
			if err := r.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
}

func TestRuntimePersistenceReleaseOpenAndDelete(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
	current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []protocol.SessionEvent{protocol.NewTurnStarted(1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})} {
		if _, err := current.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	facts := conformanceEvents(t, current)
	if _, err := r.Open(ctx, current.ID()); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}
	if len(p.handles) != 1 {
		t.Fatal("duplicate Open touched backend")
	}
	for range 2 {
		if err := r.Release(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Get(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.histories[current.ID()].events, facts) {
		t.Fatal("release deleted history")
	}
	if _, err := r.Create(ctx, session.CreateRequest{ID: current.ID()}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}
	reopened, err := r.Open(ctx, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	if reopened == current || !reflect.DeepEqual(conformanceEvents(t, reopened), facts) {
		t.Fatal("reopen did not restore distinct instance")
	}
	if err := r.RollbackCreate(ctx, reopened); !errors.Is(err, session.ErrNotCreated) {
		t.Fatal(err)
	}
	if err := r.Release(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale release", err)
	}
	foreign, _ := session.Restore(reopened.Header(), facts, nil, nil)
	if err := r.Release(ctx, foreign); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("foreign release", err)
	}
	if err := r.Delete(ctx, reopened.ID()); err != nil {
		t.Fatal(err)
	}
	if p.histories[reopened.ID()] != nil {
		t.Fatal("delete kept history")
	}
	if err := r.RollbackCreate(ctx, reopened); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	next, err := r.Create(ctx, session.CreateRequest{ID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RollbackCreate(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale rollback", err)
	}
	if err := r.Release(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, next.ID()); err != nil {
		t.Fatal("delete unopened history", err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if p.closed {
		t.Fatal("Runtime closed shared backend")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOpenAdmissionBeforePhysicalRepair(t *testing.T) {
	cause := errors.New("injected")
	for _, kind := range []string{"header-check", "header-invalid", "wrong-id", "invalid-sequence", "invalid-transition", "read", "prepare"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			p := newPersistenceStub()
			p.seed(t, "history", protocol.NewTurnStarted(1))
			options := session.RuntimeOptions{ResolvePersistence: p.resolve}
			switch kind {
			case "header-check":
				options.HeaderCheck = func(protocol.SessionHeader) error { p.trace = append(p.trace, "check"); return cause }
			case "header-invalid":
				p.histories["history"].header.Version = -1
			case "wrong-id":
				p.histories["history"].header.ID = "other"
			case "invalid-sequence":
				p.histories["history"].events[0].Seq = 5
			case "invalid-transition":
				p.histories["history"].events[0] = protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})
			case "read":
				p.readErr = cause
			case "prepare":
				p.prepareErr = cause
			}
			before := p.histories["history"].events[0].Clone()
			r := session.NewRuntime(options)
			if _, err := r.Open(ctx, "history"); err == nil {
				t.Fatal("accepted rejected history")
			}
			if _, err := r.Get(ctx, "history"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal(err)
			}
			h := p.handles[0]
			if !h.closed || h.rolledBack || h.history.active != nil {
				t.Fatal("failed Open ownership leak")
			}
			if !reflect.DeepEqual(h.history.events, []protocol.SessionEvent{before}) {
				t.Fatal("rejected history changed")
			}
			if kind != "prepare" && h.prepared {
				t.Fatal("repaired before validation")
			}
			for _, action := range p.trace {
				if action == "commit" || action == "rollback" || action == "delete" {
					t.Fatal("destructive rejection", p.trace)
				}
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeRecoveryCommitsOnlyThroughAttach(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	p.seed(t, "history", protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1))
	options := session.RuntimeOptions{ResolvePersistence: p.resolve, HeaderCheck: func(protocol.SessionHeader) error { p.trace = append(p.trace, "check"); return nil }}
	r := session.NewRuntime(options)
	current, err := r.Open(ctx, "history")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.trace, []string{"open", "read", "check", "prepare"}) || current.Watermark() != 1 {
		t.Fatal(p.trace)
	}
	cause := errors.New("repair commit")
	p.failCommit, p.commitErr = 2, cause
	if _, err := current.Attach(); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	// A legal committed repair remains; failed repair must not appear as a fact.
	if current.Watermark() != 2 || len(p.histories["history"].events) != 3 {
		t.Fatal("failed repair leaked", current.Watermark())
	}
	if err := r.Release(ctx, current); err != nil {
		t.Fatal(err)
	}
	p.failCommit = 0
	current, err = r.Open(ctx, "history")
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	facts := conformanceEvents(t, current)
	if facts[len(facts)-1].Type != protocol.SessionEndSeed {
		t.Fatal("no logical recovery marker", facts)
	}
	if err := r.Release(ctx, current); !errors.Is(err, session.ErrAttached) {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCommitFailureDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	cause := errors.New("commit")
	p.failCommit, p.commitErr = 1, cause
	o := new(observer)
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve, Observer: o})
	current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
	if err != nil {
		t.Fatal(err)
	}
	o.current = current
	sub, err := current.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Append(ctx, protocol.NewTurnStarted(1)); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	messages, err := current.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Watermark() != -1 || len(o.events) != 0 || len(messages) != 0 || len(p.histories["history"].events) != 0 {
		t.Fatal("uncommitted fact visible")
	}
	if err := r.Release(ctx, current); err != nil {
		t.Fatal(err)
	}
	for event, err := range sub.Events() {
		t.Fatal("failed commit notified subscriber", event, err)
	}
	sub.Close()
}

func TestRuntimeCleanupFailuresRemainOwnedAndRetryable(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("cleanup")
	for _, kind := range []string{"release", "rollback", "delete"} {
		t.Run(kind, func(t *testing.T) {
			p := newPersistenceStub()
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
			current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			var cleanup func() error
			switch kind {
			case "release":
				p.closeErr = cause
				cleanup = func() error { return r.Release(ctx, current) }
			case "rollback":
				p.rollbackErr = cause
				cleanup = func() error { return r.RollbackCreate(ctx, current) }
			case "delete":
				p.deleteErr = cause
				cleanup = func() error { return r.Delete(ctx, current.ID()) }
			}
			if err := cleanup(); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			if p.releases != 0 {
				t.Fatal("lease released before failed handle/history cleanup")
			}
			if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
				t.Fatal("admission reopened", err)
			}
			if _, err := r.Open(ctx, current.ID()); !errors.Is(err, session.ErrAlreadyExists) {
				t.Fatal(err)
			}
			if got, err := r.Get(ctx, current.ID()); err != nil || got != current {
				t.Fatal("lost retry credential", err)
			}
			if kind != "release" {
				if err := r.Release(ctx, current); !errors.Is(err, session.ErrCleanupPending) {
					t.Fatal("changed cleanup intent", err)
				}
			}
			releasesBefore := p.releases
			p.closeErr, p.rollbackErr, p.deleteErr = nil, nil, nil
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
			if p.releases != releasesBefore+1 {
				t.Fatal("successful cleanup did not release its lease")
			}
			if kind == "release" && p.histories[current.ID()] == nil {
				t.Fatal("Release deleted history")
			}
			if kind != "release" && p.histories[current.ID()] != nil {
				t.Fatal("cleanup kept new/deleted history")
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if p.closed {
				t.Fatal("backend closed by runtime")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeUnpublishedCleanupRetainedUntilClose(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("cleanup")
	for _, kind := range []string{"create", "open"} {
		t.Run(kind, func(t *testing.T) {
			p := newPersistenceStub()
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
			var err error
			if kind == "create" {
				canceled, cancel := context.WithCancel(ctx)
				p.afterCreate = cancel
				p.rollbackErr = cause
				_, err = r.Create(canceled, session.CreateRequest{ID: "history"})
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				p.seed(t, "history")
				p.readErr = errors.New("read")
				p.closeErr = cause
				_, err = r.Open(ctx, "history")
				if !errors.Is(err, p.readErr) {
					t.Fatal(err)
				}
			}
			if !errors.Is(err, cause) {
				t.Fatal("cleanup error lost", err)
			}
			if _, err := r.Get(ctx, "history"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("published failed acquisition", err)
			}
			if _, err := r.Open(ctx, "history"); !errors.Is(err, session.ErrAlreadyExists) {
				t.Fatal("acquired pending resource", err)
			}
			if err := r.Close(ctx); !errors.Is(err, cause) {
				t.Fatal("did not retry failure", err)
			}
			if err := p.Close(); !errors.Is(err, session.ErrAttached) {
				t.Fatal("shared ownership released early", err)
			}
			p.closeErr, p.rollbackErr = nil, nil
			for range 2 {
				if err := r.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "open" && p.histories["history"] == nil {
				t.Fatal("Open failure deleted history")
			}
			if kind == "create" && p.histories["history"] != nil {
				t.Fatal("failed Create history kept")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeGeneratedIDsSkipUnopenedHistoryAndConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	p.seed(t, "session-1")
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
	current, err := r.Create(ctx, session.CreateRequest{})
	if err != nil || current.ID() != "session-2" {
		t.Fatal(current, err)
	}
	if _, err := r.Get(ctx, "session-1"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("Get loaded disk", err)
	}
	var wg sync.WaitGroup
	winners := make(chan session.Session, 20)
	for range 20 {
		wg.Go(func() {
			current, err := r.Open(ctx, "session-1")
			if err == nil {
				winners <- current
			} else if !errors.Is(err, session.ErrAlreadyExists) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(winners) != 1 || len(p.handles) != 2 {
		t.Fatal("multiple owned opens", len(winners), len(p.handles))
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCloseRequiresDrainAndPreservesSharedBackend(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
	attached, _ := r.Create(ctx, session.CreateRequest{ID: "attached"})
	running, _ := r.Create(ctx, session.CreateRequest{ID: "running"})
	reservation, _ := attached.Attach()
	end, _ := running.BeginRun()
	if err := r.Close(ctx); !errors.Is(err, session.ErrAttached) || !errors.Is(err, session.ErrRunActive) {
		t.Fatal(err)
	}
	for _, h := range p.handles {
		if h.closed {
			t.Fatal("closed active resource")
		}
	}
	if _, err := r.Create(ctx, session.CreateRequest{}); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := r.Open(ctx, "missing"); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "attached"); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	end()
	p.closeErr = errors.New("close")
	if err := r.Close(ctx); !errors.Is(err, p.closeErr) {
		t.Fatal(err)
	}
	if p.closed {
		t.Fatal("backend prematurely closed")
	}
	p.closeErr = nil
	if err := r.Release(ctx, attached); err != nil {
		t.Fatal("Release after Close cannot retry", err)
	}
	for range 2 {
		if err := r.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.histories) != 2 {
		t.Fatal("shutdown deleted histories")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

type runtimeDriver struct{}

func (runtimeDriver) Drive(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }

func TestRuntimeFactoryResourceTransactions(t *testing.T) {
	ctx := context.Background()
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "persistent"}[persistent], func(t *testing.T) {
			p := newPersistenceStub()
			options := session.RuntimeOptions{}
			if persistent {
				options.ResolvePersistence = p.resolve
			}
			r := session.NewRuntime(options)
			newFactory := func(initialize agent.Initializer) agent.Factory {
				f, err := agent.NewFactory(agent.FactoryOptions{Registry: agent.NewRegistry(), Store: r, Driver: runtimeDriver{}, Initialize: initialize})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := f.Close(ctx); err != nil {
						t.Error(err)
					}
				})
				return f
			}
			f := newFactory(nil)
			h, err := f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Dispose(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Get(ctx, "history"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal(err)
			}
			if persistent {
				h, err = f.Resume(ctx, agent.ResumeOptions{SessionID: "history"})
				if err != nil {
					t.Fatal(err)
				}
				if err := h.Dispose(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.Resume(ctx, agent.ResumeOptions{SessionID: "history"}); !errors.Is(err, session.ErrResumeUnsupported) {
					t.Fatal(err)
				}
				h, err = f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}})
				if err != nil {
					t.Fatal(err)
				}
				if err := h.Dispose(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type runtimeInitializer func(context.Context, agent.Initialization, agent.CleanupRegistrar) error

func (f runtimeInitializer) Initialize(ctx context.Context, init agent.Initialization, cleanup agent.CleanupRegistrar) error {
	return f(ctx, init, cleanup)
}

func TestRuntimeFactoryInitializationFailureAndBorrowedOwnership(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("initialize")
	for _, operation := range []string{"create", "resume", "attach"} {
		t.Run(operation, func(t *testing.T) {
			p := newPersistenceStub()
			if operation == "resume" {
				p.seed(t, "history", protocol.NewTurnStarted(1))
			}
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
			var borrowed session.Session
			if operation == "attach" {
				var err error
				borrowed, err = r.Create(ctx, session.CreateRequest{ID: "history"})
				if err != nil {
					t.Fatal(err)
				}
			}
			f, err := agent.NewFactory(agent.FactoryOptions{Registry: agent.NewRegistry(), Store: r, Driver: runtimeDriver{}, Initialize: runtimeInitializer(func(_ context.Context, _ agent.Initialization, cleanup agent.CleanupRegistrar) error {
				if err := cleanup.Defer("initializer", func(context.Context) error {
					p.trace = append(p.trace, "init-cleanup")
					if p.histories["history"].active == nil {
						t.Error("storage closed before initializer cleanup")
					}
					return nil
				}); err != nil {
					return err
				}
				return cause
			})})
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "create":
				_, err = f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}})
			case "resume":
				_, err = f.Resume(ctx, agent.ResumeOptions{SessionID: "history"})
			case "attach":
				_, err = f.Attach(ctx, agent.AttachOptions{Session: borrowed})
			}
			if !errors.Is(err, cause) {
				t.Fatal(err)
			}
			if operation == "create" {
				if p.histories["history"] != nil || !p.handles[0].rolledBack {
					t.Fatal("creation rollback lost")
				}
			} else if operation == "resume" {
				if p.histories["history"] == nil || !p.handles[0].closed || p.handles[0].rolledBack {
					t.Fatal("resume destroyed history/leaked handle")
				}
				if len(p.histories["history"].events) <= 1 {
					t.Fatal("successful logical repair was undone")
				}
			} else {
				if got, err := r.Get(ctx, "history"); err != nil || got != borrowed {
					t.Fatal("borrowed instance withdrawn", err)
				}
				if p.handles[0].closed || p.handles[0].rolledBack {
					t.Fatal("Attach acquired resource release right")
				}
				owner, err := borrowed.Attach()
				if err != nil {
					t.Fatal("failed Attach kept reservation", err)
				}
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
				// Successful borrowed Handle disposal must preserve the same ownership too.
				success, err := agent.NewFactory(agent.FactoryOptions{Registry: agent.NewRegistry(), Store: r, Driver: runtimeDriver{}})
				if err != nil {
					t.Fatal(err)
				}
				h, err := success.Attach(ctx, agent.AttachOptions{Session: borrowed})
				if err != nil {
					t.Fatal(err)
				}
				if err := h.Dispose(ctx); err != nil {
					t.Fatal(err)
				}
				if p.handles[0].closed {
					t.Fatal("borrowed Dispose closed storage")
				}
				if err := success.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeCanceledOperationsAndMissingHistory(t *testing.T) {
	ctx := context.Background()
	p := newPersistenceStub()
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
	current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for _, op := range []func() error{
		func() error { _, err := r.Create(canceled, session.CreateRequest{}); return err },
		func() error { _, err := r.Open(canceled, "history"); return err },
		func() error { _, err := r.Get(canceled, "history"); return err },
		func() error { return r.Delete(canceled, "history") },
		func() error { return r.Release(canceled, current) },
		func() error { return r.RollbackCreate(canceled, current) },
		func() error { return r.Close(canceled) },
	} {
		if err := op(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if len(p.trace) != 1 {
		t.Fatal("canceled operation touched backend", p.trace)
	}
	if _, err := r.Open(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Release(ctx, nil); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.RollbackCreate(ctx, nil); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeOpenRequiresAttachRecoveryForEmptyAndBalancedHistory(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"empty", "balanced", "marker"} {
		t.Run(kind, func(t *testing.T) {
			p := newPersistenceStub()
			var facts []protocol.SessionEvent
			switch kind {
			case "balanced":
				facts = []protocol.SessionEvent{protocol.NewTurnStarted(1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})}
			case "marker":
				facts = []protocol.SessionEvent{protocol.NewSessionEvent(protocol.SessionEndSeed, protocol.EndSeedData{})}
			}
			p.seed(t, "history", facts...)
			r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: p.resolve})
			current, err := r.Open(ctx, "history")
			if err != nil {
				t.Fatal(err)
			}
			if current.Watermark() != protocol.Sequence(len(facts)-1) {
				t.Fatal("Open wrote recovery marker")
			}
			if _, err := current.BeginRun(); !errors.Is(err, session.ErrUnclosedLog) {
				t.Fatal("Open skipped Attach", err)
			}
			owner, err := current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			want := len(facts)
			if kind != "marker" {
				want++
			}
			if current.Watermark() != protocol.Sequence(want-1) {
				t.Fatal("recovery marker count", current.Watermark())
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.Release(ctx, current); err != nil {
				t.Fatal(err)
			}
			current, err = r.Open(ctx, "history")
			if err != nil {
				t.Fatal(err)
			}
			owner, err = current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			if current.Watermark() != protocol.Sequence(want-1) {
				t.Fatal("duplicate marker on repeat reopen")
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
