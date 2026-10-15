package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func factoryFixture(t *testing.T, store session.Store, drive driverFunc) (*resourceFactory, *fixtureController) {
	t.Helper()
	loop := agentloop.New(runprep.Coordinator{})
	var driver agentloop.Driver = loop
	if drive != nil {
		driver = &factoryDriver{Loop: loop, drive: drive}
	}

	service := newFixtureController(driver, store)
	factory, err := newFixtureFactory(store, service)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := factory.Close(context.Background()); err != nil {
			// Error tests assert the same retained error explicitly.
			t.Log("factory cleanup:", err)
		}

		service.Close()
	})

	return factory, service
}

type factoryDriver struct {
	*agentloop.Loop
	drive driverFunc
}

func (d *factoryDriver) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	return d.drive(ctx, run, emit)
}

func factoryCreate(t *testing.T, f Factory, id protocol.SessionID) Handle {
	t.Helper()
	h, err := f.Create(context.Background(), CreateOptions{Session: session.CreateRequest{ID: id}, Agent: CreateRequest{ID: ID("label-" + id)}})
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func TestFactoryIdentityAndOwnedRelease(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	f, service := factoryFixture(t, store, nil)
	h := factoryCreate(t, f, "owned")
	got, err := service.Get(ctx, "owned")
	if err != nil || got != h.Agent() || got.ID() != "owned" {
		t.Fatal(got, err)
	}

	current, err := store.Get(ctx, "owned")
	if err != nil || current != h.Session() {
		t.Fatal("Handle copied Session", current, err)
	}

	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "owned"}}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}

	if _, err := f.Attach(ctx, AttachOptions{SessionID: "owned"}); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}

	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}

	if len(service.Snapshot()) != 0 {
		t.Fatal("Agent registration leaked")
	}

	if _, err := store.Get(ctx, "owned"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("owned runtime leaked", err)
	}

	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("released Session still accepts attachment", err)
	}
	// Stale disposal must not remove a replacement with the same identity.
	replacement := factoryCreate(t, f, "owned")
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}

	if got, err := store.Get(ctx, "owned"); err != nil || got != replacement.Session() {
		t.Fatal(got, err)
	}
}

func TestFactoryGeneratedIdentityAndMismatchedIdentity(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	f, _ := factoryFixture(t, store, nil)
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "session"}, Agent: CreateRequest{SessionID: "other"}}); err == nil {
		t.Fatal("accepted conflicting Session identities")
	}

	if _, err := store.Get(ctx, "session"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}

	h, err := f.Create(ctx, CreateOptions{})
	if err != nil || h.Agent().ID() != ID(h.Session().ID()) || h.Session().ID() == "" {
		t.Fatal(h, err)
	}
}

func TestFactoryAttachBorrowsAndRemainsExclusiveAcrossServices(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, _ := store.Create(ctx, session.CreateRequest{ID: "borrowed"})
	_, _ = current.Append(ctx, protocol.NewTurnStarted(1))
	_, _ = current.Append(ctx, protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
	watermark := current.Watermark()
	f, _ := factoryFixture(t, store, nil)
	other, _ := factoryFixture(t, store, nil)
	h, err := f.Attach(ctx, AttachOptions{SessionID: "borrowed"})
	if err != nil || h.Session() != current {
		t.Fatal(h, err)
	}

	if _, err := other.Attach(ctx, AttachOptions{SessionID: "borrowed"}); !errors.Is(err, session.ErrAttached) {
		t.Fatal("cross-Service attachment was not exclusive", err)
	}

	if _, err := f.Attach(ctx, AttachOptions{SessionID: "missing"}); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}

	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}

	if got, err := store.Get(ctx, "borrowed"); err != nil || got != current || got.Watermark() != watermark {
		t.Fatal("borrowed history changed", got, err)
	}

	if _, err := other.Attach(ctx, AttachOptions{SessionID: "borrowed"}); err != nil {
		t.Fatal("attachment leaked", err)
	}
}

func TestFactoryCreationFailuresRollbackOnlyNewResources(t *testing.T) {
	for _, phase := range []string{"parent", "composition", "initialization", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			store := session.NewRuntime(session.RuntimeOptions{})
			f, service := factoryFixture(t, store, nil)
			existing := factoryCreate(t, f, "existing")
			cause := errors.New("creation failed")
			request := CreateRequest{}

			switch phase {
			case "alias":
				request.ID = "label-existing"
				cause = ErrExists
			case "parent":
				request.ParentID = "missing"
				cause = ErrNotFound
			case "composition":
				request.ComposeConfig = func(context.Context, Creation) (Config, Cleanup, error) { return Config{}, nil, cause }
			case "initialization", "cancel":
				caller, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = caller
				service.SetLifecycle(&factoryLifecycle{
					created: func(context.Context) error {
						if phase == "cancel" {
							cancel()
							return nil
						}

						return cause
					},
				})
				if phase == "cancel" {
					cause = context.Canceled
				}
			}

			if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "failed"}, Agent: request}); !errors.Is(err, cause) {
				t.Fatal(err)
			}

			if _, err := store.Get(context.Background(), "failed"); !errors.Is(err, session.ErrNotFound) || len(service.Snapshot()) != 1 {
				t.Fatal("failed creation leaked", err, service.Snapshot())
			}

			if got, err := service.Get(context.Background(), "existing"); err != nil || got != existing.Agent() {
				t.Fatal("conflict removed existing Agent", err)
			}
		})
	}
}

type factoryLifecycle struct{ created func(context.Context) error }

func (l *factoryLifecycle) Created(ctx context.Context, _ contracts.AgentCreated) error {
	return l.created(ctx)
}

func (*factoryLifecycle) Status(contracts.AgentStatusObservation) {}

func (*factoryLifecycle) Disposed(context.Context, contracts.AgentDisposed) {}

func TestFactoryFailedAttachPreservesHistoryAndReleasesReservation(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, _ := store.Create(ctx, session.CreateRequest{ID: "history"})
	_, _ = current.Append(ctx, protocol.NewTurnStarted(1)) // existing recovery path
	f, service := factoryFixture(t, store, nil)
	cause := errors.New("initialization failed")
	service.SetLifecycle(&factoryLifecycle{created: func(context.Context) error { return cause }})
	if _, err := f.Attach(ctx, AttachOptions{SessionID: "history"}); !errors.Is(err, cause) {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "history")
	if err != nil || got != current || got.Watermark() < 0 || len(service.Snapshot()) != 0 {
		t.Fatal("failed Attach discarded history", got, err)
	}

	reservation, err := got.Attach()
	if err != nil {
		t.Fatal("failed Attach leaked reservation", err)
	}

	_ = reservation.Close()
}

func TestHandleConcurrentDisposeWaitsForActivityAndKeepsCallerContextSeparate(t *testing.T) {
	entered, canceled, drain := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var drainOnce sync.Once
	store := &factoryStore{Runtime: session.NewRuntime(session.RuntimeOptions{})}
	f, service := factoryFixture(t, store, func(ctx context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
		end, err := run.Attached.Reservation.BeginRun()
		if err != nil {
			return err
		}

		defer end()

		close(entered)
		<-ctx.Done()
		close(canceled)
		<-drain

		return ctx.Err()
	})
	t.Cleanup(func() { drainOnce.Do(func() { close(drain) }) })
	caller, cancel := context.WithCancel(context.Background())
	h, err := f.Create(caller, CreateOptions{Session: session.CreateRequest{ID: "active"}})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	if err := h.Agent().Followup(context.Background(), protocol.NewTextMessage("input", protocol.RoleUser, protocol.SourceUser, "hello")); err != nil {
		t.Fatal(err)
	}

	waitTestSignal(t, entered)

	select {
	case <-canceled:
		t.Fatal("creation context owns activity")
	default:
	}

	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()

	if err := h.Dispose(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}

	waitTestSignal(t, canceled)
	if store.releases.Load() != 0 || len(service.Snapshot()) != 0 {
		t.Fatal("disposed Agent is visible or resources released before activity drain")
	}

	attached := h.Agent().(*loopAgent)
	attached.mu.Lock()
	hasRequestState := attached.requestState != nil
	attached.mu.Unlock()
	if !hasRequestState {
		t.Fatal("released request state before activity drain")
	}

	if _, err := h.Session().Attach(); !errors.Is(err, session.ErrAttached) {
		t.Fatal(err)
	}

	if err := h.Agent().Followup(
		context.Background(),
		protocol.NewTextMessage("late", protocol.RoleUser, protocol.SourceUser, "late"),
	); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	for range 16 {
		wg.Go(func() {
			if err := h.Dispose(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}

	drainOnce.Do(func() { close(drain) })
	wg.Wait()
	if store.releases.Load() != 1 || store.deletes.Load() != 0 || len(service.Snapshot()) != 0 {
		t.Fatal("release did not converge")
	}

	attached.mu.Lock()
	hasRequestState = attached.requestState != nil
	attached.mu.Unlock()
	if hasRequestState {
		t.Fatal("Dispose retained attachment request state")
	}
}

type factoryStore struct {
	*session.Runtime
	releases, deletes     atomic.Int32
	releaseErr, deleteErr error
}

func TestFactoryOwnerBarrierRollsBackBeforePublication(t *testing.T) {
	for _, attach := range []bool{false, true} {
		t.Run(fmt.Sprintf("attach=%v", attach), func(t *testing.T) {
			ctx := context.Background()
			store := &factoryStore{Runtime: session.NewRuntime(session.RuntimeOptions{})}
			service := newFixtureController(agentloop.New(runprep.Coordinator{}), store)
			defer service.Close()

			var stopped atomic.Bool
			factory, err := newFixtureFactory(store, service, FactoryOptions{
				CheckAdmission: func() error {
					if stopped.Load() {
						return ErrClosed
					}

					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			defer factory.Close(ctx)

			var borrowed session.Session
			if attach {
				borrowed, err = store.Create(ctx, session.CreateRequest{ID: "guarded"})
				if err != nil {
					t.Fatal(err)
				}
			}

			request := CreateRequest{
				SessionID: "guarded",
				ComposeConfig: func(context.Context, Creation) (Config, Cleanup, error) {
					stopped.Store(true)
					return Config{}, nil, nil
				},
			}
			var h Handle
			if attach {
				h, err = factory.Attach(ctx, AttachOptions{SessionID: request.SessionID, Agent: request})
			} else {
				h, err = factory.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "guarded"}, Agent: request})
			}

			if h != nil || !errors.Is(err, ErrClosed) {
				t.Fatal("owner barrier failed:", h, err)
			}

			if len(service.Snapshot()) != 0 || store.releases.Load() != 0 {
				t.Fatal("failed transaction was published or released as a Handle")
			}

			if attach {
				if got, err := store.Get(ctx, "guarded"); err != nil || got != borrowed || store.deletes.Load() != 0 {
					t.Fatal("borrowed history lost", err)
				}

				reservation, err := borrowed.Attach()
				if err != nil {
					t.Fatal(err)
				}

				if err := reservation.Close(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.Get(ctx, "guarded"); !errors.Is(err, session.ErrNotFound) || store.deletes.Load() != 1 {
				t.Fatal("new history not rolled back", err)
			}

			if h, err := factory.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "late"}}); h != nil || !errors.Is(err, ErrClosed) {
				t.Fatal(h, err)
			}

			if _, err := store.Get(ctx, "late"); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("owner barrier allocated after stop", err)
			}
		})
	}
}

func TestHandleCanceledDisposeWaitStopsAdmissionImmediately(t *testing.T) {
	f, _ := factoryFixture(t, session.NewRuntime(session.RuntimeOptions{}), nil)
	h := factoryCreate(t, f, "canceled-wait")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.Dispose(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	message := protocol.NewTextMessage("late", protocol.RoleUser, protocol.SourceUser, "late")
	if err := h.Agent().Followup(context.Background(), message); !errors.Is(err, ErrClosed) {
		t.Fatal("Dispose returned before stopping admission", err)
	}

	if err := h.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (s *factoryStore) Release(ctx context.Context, current session.Session) error {
	s.releases.Add(1)
	return errors.Join(s.Runtime.Release(ctx, current), s.releaseErr)
}

func (s *factoryStore) RollbackCreate(ctx context.Context, current session.Session) error {
	s.deletes.Add(1)
	return errors.Join(s.Runtime.RollbackCreate(ctx, current), s.deleteErr)
}

func (s *factoryStore) Delete(ctx context.Context, id protocol.SessionID) error {
	s.deletes.Add(1)
	return errors.Join(s.Runtime.Delete(ctx, id), s.deleteErr)
}

func TestFactoryReleaseAndDeleteErrorsRemainVisible(t *testing.T) {
	ctx := context.Background()
	releaseErr, deleteErr, createErr := errors.New("release failure"), errors.New("delete failure"), errors.New("creation failure")
	store := &factoryStore{
		Runtime:    session.NewRuntime(session.RuntimeOptions{}),
		releaseErr: releaseErr,
		deleteErr:  deleteErr,
	}
	f, _ := factoryFixture(t, store, nil)
	h := factoryCreate(t, f, "errors")

	for range 2 {
		if err := h.Dispose(ctx); !errors.Is(err, releaseErr) {
			t.Fatal(err)
		}
	}

	if store.releases.Load() != 1 || store.deletes.Load() != 0 {
		t.Fatal("Dispose deleted history or repeated release")
	}

	if _, err := f.Create(ctx, CreateOptions{
		Session: session.CreateRequest{ID: "rollback"},
		Agent:   CreateRequest{DefaultConfig: func(context.Context) (Config, error) { return Config{}, createErr }},
	}); !errors.Is(err, createErr) || !errors.Is(err, deleteErr) {
		t.Fatal(err)
	}

	if err := f.Close(ctx); !errors.Is(err, releaseErr) {
		t.Fatal("Close hid cleanup error", err)
	}
}

// This controlled Store keeps committed history separately from open runtime
// instances, without requiring file I/O or the standard JSONL implementation.
type historyStore struct {
	*factoryStore
	history session.Session
}

func (s *historyStore) Create(ctx context.Context, request session.CreateRequest) (session.Session, error) {
	current, err := s.Runtime.Create(ctx, request)
	if err == nil {
		s.history = current
	}

	return current, err
}

func (s *historyStore) Delete(ctx context.Context, id protocol.SessionID) error {
	err := s.factoryStore.Delete(ctx, id)
	if err == nil {
		s.history = nil
	}

	return err
}

func TestHandleReleasesRuntimeWithoutDeletingCommittedHistory(t *testing.T) {
	ctx := context.Background()
	store := &historyStore{factoryStore: &factoryStore{Runtime: session.NewRuntime(session.RuntimeOptions{})}}
	f, _ := factoryFixture(t, store, nil)
	h := factoryCreate(t, f, "retained")

	for _, event := range []protocol.SessionEvent{
		protocol.NewTurnStarted(1),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	} {
		if _, err := h.Session().Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}

	if store.history == nil || store.history.Watermark() != 1 || store.deletes.Load() != 0 || store.releases.Load() != 1 {
		t.Fatal("history deleted during Dispose")
	}

	count := 0

	for _, err := range store.history.Events(ctx, -1) {
		if err != nil {
			t.Fatal(err)
		}

		count++
	}

	if count != 2 {
		t.Fatal("committed facts lost", count)
	}
}

type borrowedOnlyStore struct{ session.Store }

func TestFactoryCreateRequiresReleaseButAttachDoesNot(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, _ := store.Create(ctx, session.CreateRequest{ID: "borrowed"})
	f, _ := factoryFixture(t, borrowedOnlyStore{store}, nil)
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "unsupported"}}); !errors.Is(err, ErrAttachOnly) {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, "unsupported"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("allocated before capability check", err)
	}

	h, err := f.Attach(ctx, AttachOptions{SessionID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
}

type reservationErrorStore struct {
	session.Store
	cause  error
	closes atomic.Int32
}

func (s *reservationErrorStore) Get(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	current, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	return &reservationErrorSession{Session: current, store: s}, nil
}

type reservationErrorSession struct {
	session.Session
	store *reservationErrorStore
}

func (s *reservationErrorSession) Attach() (contracts.SessionReservation, error) {
	reservation, err := s.Session.Attach()
	if err != nil {
		return nil, err
	}

	return &reservationError{SessionReservation: reservation, store: s.store}, nil
}

type reservationError struct {
	contracts.SessionReservation
	store *reservationErrorStore
}

func (r *reservationError) Close() error {
	r.store.closes.Add(1)
	return errors.Join(r.SessionReservation.Close(), r.store.cause)
}

func TestFactoryReservationCleanupErrorsAreVisible(t *testing.T) {
	for _, phase := range []string{"dispose", "composition", "initialization"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			base := session.NewRuntime(session.RuntimeOptions{})
			current, err := base.Create(ctx, session.CreateRequest{ID: "close-error"})
			if err != nil {
				t.Fatal(err)
			}

			cause := errors.New("reservation close failure")
			store := &reservationErrorStore{Store: base, cause: cause}
			f, service := factoryFixture(t, store, nil)
			options := AttachOptions{SessionID: current.ID()}
			initErr := errors.New("initialization failure")
			if phase == "composition" {
				options.ComposeConfig = func(context.Context, Creation) (Config, Cleanup, error) { return Config{}, nil, initErr }
			} else if phase == "initialization" {
				service.SetLifecycle(&factoryLifecycle{created: func(context.Context) error { return initErr }})
			}

			h, err := f.Attach(ctx, options)
			if phase == "dispose" {
				if err != nil {
					t.Fatal(err)
				}

				for range 2 {
					if err := h.Dispose(ctx); !errors.Is(err, cause) {
						t.Fatal(err)
					}
				}
			} else if !errors.Is(err, initErr) || !errors.Is(err, cause) {
				t.Fatal(err)
			}

			if store.closes.Load() != 1 || len(service.Snapshot()) != 0 {
				t.Fatal("reservation cleanup repeated or registration leaked")
			}
		})
	}
}

func TestFactoryCloseCancelsCreationAndDrainsPublishedHandles(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	f, service := factoryFixture(t, store, nil)
	h := factoryCreate(t, f, "published")
	entered := make(chan struct{})
	service.SetLifecycle(&factoryLifecycle{
		created: func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	result := make(chan error, 1)
	go func() {
		_, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: "pending"}})
		result <- err
	}()
	waitTestSignal(t, entered)
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if !h.Agent().Disposed() || len(service.Snapshot()) != 0 {
		t.Fatal("Close leaked Agents")
	}

	for _, id := range []protocol.SessionID{"pending", "published"} {
		if _, err := store.Get(ctx, id); !errors.Is(err, session.ErrNotFound) {
			t.Fatal("Close leaked Session", id, err)
		}
	}

	if _, err := f.Create(ctx, CreateOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}

	if _, err := f.Attach(ctx, AttachOptions{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	// Factory shutdown leaves shared services usable by their real owner.
	service.SetLifecycle(nil)
	current, err := store.Create(ctx, session.CreateRequest{ID: "shared"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Create(ctx, CreateRequest{SessionID: current.ID()}); err != nil {
		t.Fatal(err)
	}
}

func (l *factoryLifecycle) Initialize(ctx context.Context, _ Initialization, _ CleanupRegistrar) error {
	return l.Created(ctx, contracts.AgentCreated{})
}

func TestCreationCompositionRunsAfterAdmissionAndRollsBackReservation(t *testing.T) {
	store := session.NewRuntime(session.RuntimeOptions{})
	current, err := store.Create(context.Background(), session.CreateRequest{ID: "composed"})
	if err != nil {
		t.Fatal(err)
	}

	service := newFixtureController(agentloop.New(runprep.Coordinator{}), store)
	defer service.Close()

	var calls, releases atomic.Int32
	cause := errors.New("composition failed")
	service.SetConfigComposer(func(_ context.Context, creation Creation) (Config, Cleanup, error) {
		calls.Add(1)
		if creation.ID != "composed" || creation.Parent != nil {
			t.Fatal(creation)
		}

		return Config{}, func(context.Context) error { releases.Add(1); return nil }, cause
	})
	if _, err := service.Create(context.Background(), CreateRequest{SessionID: "missing"}); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}

	if _, err := service.Create(context.Background(), CreateRequest{SessionID: current.ID(), ParentID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	if calls.Load() != 0 {
		t.Fatal("composition ran before Session/parent admission")
	}

	if _, err := service.Create(context.Background(), CreateRequest{SessionID: current.ID()}); !errors.Is(err, cause) {
		t.Fatal(err)
	}

	if calls.Load() != 1 || releases.Load() != 1 || len(service.Snapshot()) != 0 {
		t.Fatal(calls.Load(), releases.Load(), service.Snapshot())
	}

	reservation, err := current.Attach()
	if err != nil {
		t.Fatal("failed composition leaked Session reservation", err)
	}

	_ = reservation.Close()
}

func TestCreationCancellationTakesCompositionReleaseBeforePublication(t *testing.T) {
	service := newTestService(driverFunc(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil }))
	defer service.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var releases atomic.Int32
	service.SetConfigComposer(func(context.Context, Creation) (Config, Cleanup, error) {
		cancel()
		return Config{}, func(context.Context) error { releases.Add(1); return nil }, nil
	})
	if _, err := service.Create(ctx, CreateRequest{SessionID: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	service.Close()
	if releases.Load() != 1 || len(service.Snapshot()) != 0 {
		t.Fatal("cancelled creation leaked composition", releases.Load(), service.Snapshot())
	}
}

func TestCompositionReleaseWaitsForAdmittedActivityDrain(t *testing.T) {
	entered, drain := make(chan struct{}), make(chan struct{})
	service := newTestService(driverFunc(func(ctx context.Context, _ agentloop.Run, _ agentloop.EmitFunc) error {
		close(entered)
		<-ctx.Done()
		<-drain
		return ctx.Err()
	}))
	defer service.Close()

	var releases atomic.Int32
	service.SetConfigComposer(func(context.Context, Creation) (Config, Cleanup, error) {
		return Config{}, func(context.Context) error { releases.Add(1); return nil }, nil
	})
	current, err := service.Create(context.Background(), CreateRequest{SessionID: "active"})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := current.Submit(context.Background(), Input{Text: "work"}); err != nil {
		t.Fatal(err)
	}

	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := current.CancelActivity(CancelRequested); err != nil {
		t.Fatal(err)
	}

	if err := current.WaitIdle(ctx); !errors.Is(err, context.Canceled) || releases.Load() != 0 {
		t.Fatal("composition released before drain", err, releases.Load())
	}

	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	if releases.Load() != 0 {
		t.Fatal("composition released during activity")
	}

	close(drain)
	<-closed
	service.Close()
	if releases.Load() != 1 {
		t.Fatal("composition must release once after drain", releases.Load())
	}
}
