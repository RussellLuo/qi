package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// controlledStore models exclusive opened resources separately from committed
// history. Every write goes through Restore's real Session state machine and
// commit hook. Faults and barriers exercise transactions without file I/O.
type controlledStore struct {
	mu                             sync.Mutex // opened-instance ownership
	historyMu                      sync.Mutex
	header                         protocol.SessionHeader
	facts                          []protocol.SessionEvent
	opened                         session.Session
	last                           session.Session
	openErr, releaseErr, commitErr error
	releaseBeforeFailure           bool
	failCommit                     int
	commits, releases, rollbacks   int
	openBarrier                    func()
	releaseBarrier                 func()
}

func newControlledStore(t *testing.T) *controlledStore {
	t.Helper()
	header, err := session.CreateHeader("history", nil)
	if err != nil {
		t.Fatal(err)
	}
	facts := []protocol.SessionEvent{protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1)}
	for i := range facts {
		facts[i].Seq = protocol.Sequence(i)
	}
	return &controlledStore{header: header, facts: facts}
}

func (s *controlledStore) restore() (session.Session, error) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	return session.Restore(s.header, s.facts, nil, func(encoded []byte) error {
		s.historyMu.Lock()
		defer s.historyMu.Unlock()
		s.commits++
		if s.commits == s.failCommit {
			return s.commitErr
		}
		event, err := protocol.DecodeSessionEvent(encoded)
		if err != nil {
			return err
		}
		s.facts = append(s.facts, event.Clone())
		return nil
	})
}

func (s *controlledStore) Open(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openErr != nil {
		return nil, s.openErr
	}
	if id != s.header.ID {
		return nil, session.ErrNotFound
	}
	if s.opened != nil {
		return nil, session.ErrAlreadyExists
	}
	current, err := s.restore() // Validate before any repair or resource transfer.
	if err != nil {
		return nil, err
	}
	s.opened, s.last = current, current
	if s.openBarrier != nil {
		s.openBarrier()
	}
	return current, nil
}

func (s *controlledStore) Create(ctx context.Context, request session.CreateRequest) (session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened != nil {
		return nil, session.ErrAlreadyExists
	}
	header, err := session.CreateHeader(request.ID, request.Header)
	if err != nil {
		return nil, err
	}
	s.historyMu.Lock()
	s.header, s.facts = header, nil
	s.historyMu.Unlock()
	current, err := s.restore()
	if err == nil {
		s.opened, s.last = current, current
	}
	return current, err
}

func (s *controlledStore) Get(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened == nil || s.opened.ID() != id {
		return nil, session.ErrNotFound
	}
	return s.opened, nil
}

func (s *controlledStore) Delete(context.Context, protocol.SessionID) error {
	panic("Factory must never delete history by ID")
}

func (s *controlledStore) Release(ctx context.Context, current session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.releaseBarrier != nil {
		s.releaseBarrier()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened != current {
		return session.ErrNotFound
	}
	if err := current.(*session.LiveSession).Release(func() error {
		if s.releaseBeforeFailure {
			return s.releaseErr
		}
		return nil
	}); err != nil {
		return err
	}
	s.opened = nil
	s.releases++
	return s.releaseErr // Fault after actual release: preserve both result and facts.
}

func (s *controlledStore) RollbackCreate(ctx context.Context, current session.Session) error {
	if err := s.Release(ctx, current); err != nil {
		return err
	}
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	s.facts = nil
	s.rollbacks++
	return nil
}

func (s *controlledStore) snapshot() []protocol.SessionEvent {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	result := make([]protocol.SessionEvent, len(s.facts))
	for i, event := range s.facts {
		result[i] = event.Clone()
	}
	return result
}

func (s *controlledStore) assertReleased(t *testing.T, registry Registry) {
	t.Helper()
	if _, err := s.Get(context.Background(), s.header.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("open resource remains", err)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("published Agent remains")
	}
	if s.last != nil {
		if _, err := s.last.Attach(); !errors.Is(err, contracts.ErrClosed) {
			t.Fatal("released instance admits attachment", err)
		}
	}
}

func noWorkDriver() agentloop.Driver {
	return rawDriver(func(context.Context, agentloop.Run, agentloop.EmitFunc) error { return nil })
}

func TestFactoryValidatesOwnedCapabilitiesBeforeAcquisition(t *testing.T) {
	store := session.NewRuntime(session.RuntimeOptions{})
	options := FactoryOptions{Registry: NewRegistry(), Store: borrowedOnlyStore{store}, Driver: noWorkDriver()}
	if _, err := NewFactory(options); !errors.Is(err, ErrReleaseUnsupported) {
		t.Fatal(err)
	}
	options.Store = releaseOnlyStore{Store: store, StoreReleaser: store}
	if _, err := NewFactory(options); !errors.Is(err, ErrRollbackUnsupported) {
		t.Fatal(err)
	}
	options.AttachOnly = true
	f := standaloneFactory(t, options)
	if _, err := f.Create(context.Background(), CreateOptions{}); !errors.Is(err, ErrAttachOnly) {
		t.Fatal(err)
	}
	if _, err := f.Resume(context.Background(), ResumeOptions{SessionID: "history"}); !errors.Is(err, ErrAttachOnly) {
		t.Fatal(err)
	}
	current, err := store.Create(context.Background(), session.CreateRequest{ID: "borrowed"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.Attach(context.Background(), AttachOptions{Session: current})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(context.Background(), current.ID()); err != nil || got != current {
		t.Fatal("borrowed resource released", err)
	}
	owned := standaloneFactory(t, FactoryOptions{Registry: NewRegistry(), Store: store, Driver: noWorkDriver()})
	if _, err := owned.Resume(context.Background(), ResumeOptions{SessionID: current.ID()}); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatal(err)
	}
	if got, err := store.Get(context.Background(), current.ID()); err != nil || got != current {
		t.Fatal("Resume used Get as acquisition", err)
	}
}

type releaseOnlyStore struct {
	session.Store
	session.StoreReleaser
}

func TestResumeRealLoopSettlementAndReopen(t *testing.T) {
	ctx := context.Background()
	store := newControlledStore(t)
	registry := NewRegistry()
	prompts, llmRuntime, toolRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
	t.Cleanup(func() { middleware.Close(); toolRuntime.Close(); llmRuntime.Close(); prompts.Close() })
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: []protocol.StreamChunk{
		{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText},
		{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: "resumed"}},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
	}}}}
	if _, err := llmRuntime.RegisterAdapter(newTestOwner("resume", 1), llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: adapter}); err != nil {
		t.Fatal(err)
	}
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: agentloop.New(runprep.Coordinator{Prompts: prompts, LLM: llmRuntime, Tools: toolRuntime, Middleware: middleware})})
	creation, cancel := context.WithCancel(ctx)
	h, err := f.Resume(creation, ResumeOptions{SessionID: store.header.ID, Agent: AgentOptions{Config: Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // A delivered Handle survives cancellation of its opening transaction.
	assertRepair(t, store.snapshot(), 5)
	if _, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal("Open borrowed a live resource", err)
	}
	if _, err := f.Attach(ctx, AttachOptions{Session: h.Session()}); !errors.Is(err, ErrExists) {
		t.Fatal("Attach overlapped Resume", err)
	}
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: store.header.ID}}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal("Create overlapped Resume", err)
	}
	_, stream, err := h.Agent().Submit(ctx, Input{Text: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for e, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		if e.Type == "ended" {
			completed = e.Reason.Kind == "completed"
		}
	}
	if !completed || len(adapter.Seen) != 1 || h.Session().LastTurn() != 2 {
		t.Fatal("resumed activity failed", completed, len(adapter.Seen), h.Session().LastTurn())
	}
	before := store.snapshot()
	if before[len(before)-1].Type != protocol.TurnEnded {
		t.Fatal("missing committed settlement")
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	store.assertReleased(t, registry)
	if !reflect.DeepEqual(before, store.snapshot()) {
		t.Fatal("Dispose changed committed history")
	}
	next, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID})
	if err != nil {
		t.Fatal(err)
	}
	if next.Session() == h.Session() || next.Agent().InstanceID() == h.Agent().InstanceID() {
		t.Fatal("reopen reused released identity")
	}
	if err := store.Release(ctx, h.Session()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale cleanup changed replacement", err)
	}
	if got, err := store.Get(ctx, store.header.ID); err != nil || got != next.Session() {
		t.Fatal(err)
	}
	if err := next.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	store.assertReleased(t, registry)
}

func assertRepair(t *testing.T, facts []protocol.SessionEvent, length int) {
	t.Helper()
	if len(facts) != length {
		t.Fatal("unexpected repair prefix", len(facts), length)
	}
	want := []protocol.SessionEventKind{protocol.TurnStarted, protocol.StepStarted, protocol.StepEnded, protocol.TurnEnded, protocol.SessionEndSeed}
	for i, event := range facts {
		if event.Seq != protocol.Sequence(i) || event.Type != want[i] {
			t.Fatal("repair fact mismatch", event)
		}
	}
}

func TestResumeAcquisitionValidationAndPartialRepairFailures(t *testing.T) {
	for _, phase := range []string{"open", "prefix", "repair"} {
		t.Run(phase, func(t *testing.T) {
			store := newControlledStore(t)
			original := store.snapshot()
			cause := errors.New(phase)
			switch phase {
			case "open":
				store.openErr = cause
			case "prefix":
				store.facts[1].Seq = 9
				cause = session.ErrInvalidSequence
				original = store.snapshot()
			case "repair":
				store.failCommit, store.commitErr = 2, cause // StepEnded commits; TurnEnded fails.
			}
			registry := NewRegistry()
			f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: noWorkDriver()})
			if _, err := f.Resume(context.Background(), ResumeOptions{SessionID: store.header.ID}); !errors.Is(err, cause) {
				t.Fatal(err)
			}
			store.assertReleased(t, registry)
			if phase == "repair" {
				assertRepair(t, store.snapshot(), 3)
				if store.releases != 1 || store.rollbacks != 0 {
					t.Fatal("repair failure deleted history")
				}
				store.failCommit = 0
				h, err := f.Resume(context.Background(), ResumeOptions{SessionID: store.header.ID})
				if err != nil {
					t.Fatal(err)
				}
				assertRepair(t, store.snapshot(), 5)
				if err := h.Dispose(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if !reflect.DeepEqual(original, store.snapshot()) || store.releases != 0 {
				t.Fatal("failed acquisition changed history or transferred ownership")
			}
		})
	}
}

func TestCreateAttachResumeFailureOwnershipMatrix(t *testing.T) {
	for _, operation := range []string{"create", "attach", "resume"} {
		for _, phase := range []string{"configuration", "initialize", "panic", "publication"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				ctx := context.Background()
				store := newControlledStore(t)
				registry := NewRegistry()
				cause, cleanupErr := errors.New(phase), errors.New("cleanup")
				options := FactoryOptions{Registry: registry, Store: store, Driver: noWorkDriver()}
				if phase == "configuration" {
					options.Compose = func(context.Context, Creation) (Config, Cleanup, error) {
						return Config{}, func(context.Context) error { return cleanupErr }, cause
					}
				}
				if phase == "initialize" || phase == "panic" {
					options.Initialize = testInitializer(func(_ context.Context, _ Initialization, cleanup CleanupRegistrar) error {
						if err := cleanup.Defer("fixture", func(context.Context) error { return cleanupErr }); err != nil {
							t.Fatal(err)
						}
						if phase == "panic" {
							panic(cause)
						}
						return cause
					})
				}
				f, err := NewBoundFactory(options, func() (func(), error) { return nil, cause })
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close(context.Background()) })
				var current session.Session
				if operation == "attach" {
					current, err = store.Open(ctx, store.header.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				var h Handle
				switch operation {
				case "create":
					h, err = f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: store.header.ID}})
				case "attach":
					h, err = f.Attach(ctx, AttachOptions{Session: current})
				case "resume":
					h, err = f.Resume(ctx, ResumeOptions{SessionID: store.header.ID})
				}
				if h != nil || !errors.Is(err, cause) {
					t.Fatal(h, err)
				}
				if phase != "publication" && !errors.Is(err, cleanupErr) {
					t.Fatal("cleanup error lost", err)
				}
				if len(registry.Snapshot()) != 0 {
					t.Fatal("failed transaction published")
				}
				switch operation {
				case "create":
					store.assertReleased(t, registry)
					if len(store.snapshot()) != 0 || store.rollbacks != 1 {
						t.Fatal("new creation not rolled back")
					}
				case "resume":
					store.assertReleased(t, registry)
					assertRepair(t, store.snapshot(), 5)
					if store.releases != 1 || store.rollbacks != 0 {
						t.Fatal("Resume rollback deleted history")
					}
				case "attach":
					assertRepair(t, store.snapshot(), 5)
					if got, err := store.Get(ctx, store.header.ID); err != nil || got != current || store.releases != 0 {
						t.Fatal("borrowed resource lost", err)
					}
					lease, err := current.Attach()
					if err != nil {
						t.Fatal("borrowed attachment leaked", err)
					}
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
					if err := store.Release(ctx, current); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestResumeReleaseErrorJoinsOriginalAndDisposeResult(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispose", true: "rollback"}[fail], func(t *testing.T) {
			store, registry := newControlledStore(t), NewRegistry()
			cause, releaseErr := errors.New("initialize"), errors.New("runtime release")
			store.releaseErr = releaseErr
			opts := FactoryOptions{Registry: registry, Store: store, Driver: noWorkDriver()}
			if fail {
				opts.Initialize = testInitializer(func(context.Context, Initialization, CleanupRegistrar) error { return cause })
			}
			f := standaloneFactory(t, opts)
			h, err := f.Resume(context.Background(), ResumeOptions{SessionID: store.header.ID})
			if fail {
				if !errors.Is(err, cause) || !errors.Is(err, releaseErr) {
					t.Fatal("rollback errors lost", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := h.Dispose(context.Background()); !errors.Is(err, releaseErr) {
						t.Fatal(err)
					}
				}
				if err := f.Close(context.Background()); !errors.Is(err, releaseErr) {
					t.Fatal("Factory lost Dispose error", err)
				}
			}
			store.assertReleased(t, registry)
			assertRepair(t, store.snapshot(), 5)
			if store.releases != 1 || store.rollbacks != 0 {
				t.Fatal("repeated release or history rollback")
			}
		})
	}
}

func TestResumeCancellationAndCloseDrainOpenedResource(t *testing.T) {
	for _, point := range []string{"open", "initialize"} {
		for _, stop := range []string{"cancel", "close"} {
			t.Run(point+"/"+stop, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store, registry := newControlledStore(t), NewRegistry()
				entered, unblock := make(chan struct{}), make(chan struct{})
				barrier := func() { close(entered); <-unblock }
				opts := FactoryOptions{Registry: registry, Store: store, Driver: noWorkDriver()}
				if point == "open" {
					store.openBarrier = barrier
				} else {
					opts.Initialize = testInitializer(func(context.Context, Initialization, CleanupRegistrar) error { barrier(); return nil })
				}
				f := standaloneFactory(t, opts)
				result := make(chan error, 1)
				go func() {
					h, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID})
					if h != nil {
						result <- errors.New("stopped transaction delivered Handle")
					} else {
						result <- err
					}
				}()
				<-entered
				if len(registry.Snapshot()) != 0 {
					t.Fatal("uninitialized object visible")
				}
				if stop == "cancel" {
					cancel()
				} else {
					waiting, done := context.WithCancel(context.Background())
					done()
					if err := f.Close(waiting); !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				}
				close(unblock)
				if err := <-result; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
					t.Fatal(err)
				}
				if err := f.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				store.assertReleased(t, registry)
				assertRepair(t, store.snapshot(), map[string]int{"open": 2, "initialize": 5}[point])
				if store.releases != 1 || store.rollbacks != 0 {
					t.Fatal("opened resource not released exactly once")
				}
			})
		}
	}
}

func TestResumeFailedResourceReleaseRequiresOwnerRecovery(t *testing.T) {
	ctx := context.Background()
	store, registry := newControlledStore(t), NewRegistry()
	cause := errors.New("resource remains held")
	store.releaseErr, store.releaseBeforeFailure = cause, true
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store, Driver: noWorkDriver()})
	h, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Dispose(ctx); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("failed cleanup kept Agent visible")
	}
	if got, err := store.Get(ctx, store.header.ID); err != nil || got != h.Session() {
		t.Fatal("failed physical cleanup hid held resource", err)
	}
	if _, err := h.Session().Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("failed release reopened admission", err)
	}
	if _, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal("Resume overlapped unreleased resource", err)
	}
	assertRepair(t, store.snapshot(), 5)
	// The Store owner remedies the physical fault. Handle keeps its original,
	// idempotent Dispose result; it cannot act on the later replacement.
	store.releaseErr, store.releaseBeforeFailure = nil, false
	if err := store.Release(ctx, h.Session()); err != nil {
		t.Fatal(err)
	}
	next, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Dispose(ctx); !errors.Is(err, cause) {
		t.Fatal("Dispose result changed", err)
	}
	if got, err := store.Get(ctx, store.header.ID); err != nil || got != next.Session() {
		t.Fatal("old Dispose removed replacement", err)
	}
	if err := next.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	store.assertReleased(t, registry)
}

func TestResumeReservationAndDisposeWaitForSettlementAndRelease(t *testing.T) {
	ctx := context.Background()
	store, registry := newControlledStore(t), NewRegistry()
	initializing, initialized := make(chan struct{}), make(chan struct{})
	driving, settled := make(chan struct{}), make(chan struct{})
	releasing, released := make(chan struct{}), make(chan struct{})
	store.releaseBarrier = func() { close(releasing); <-released }
	f := standaloneFactory(t, FactoryOptions{Registry: registry, Store: store,
		Initialize: testInitializer(func(context.Context, Initialization, CleanupRegistrar) error {
			close(initializing)
			<-initialized
			return nil
		}),
		Driver: rawDriver(func(_ context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
			current := run.Attached.Session
			end, err := current.BeginRunOwned(run.Attached.Reservation)
			if err != nil {
				return err
			}
			defer end()
			if _, err := current.Append(ctx, protocol.NewTurnStarted(2)); err != nil {
				return err
			}
			close(driving)
			<-settled // Cancellation must wait for the actual execution/settlement.
			_, err = current.Append(ctx, protocol.NewTurnEnded(2, protocol.TurnEndReason{Kind: "aborted", Reason: &protocol.AgentCancelCause{Kind: "user"}}))
			return err
		}),
	})
	type result struct {
		h   Handle
		err error
	}
	resultCh := make(chan result, 1)
	go func() { h, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID}); resultCh <- result{h, err} }()
	<-initializing
	current, err := store.Get(ctx, store.header.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Snapshot()) != 0 {
		t.Fatal("initializing Resume visible")
	}
	if _, err := f.Attach(ctx, AttachOptions{Session: current}); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	if _, err := f.Resume(ctx, ResumeOptions{SessionID: store.header.ID}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}
	if _, err := f.Create(ctx, CreateOptions{Session: session.CreateRequest{ID: store.header.ID}}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}
	close(initialized)
	got := <-resultCh
	if got.err != nil {
		t.Fatal(got.err)
	}
	h := got.h
	if err := h.Agent().Followup(ctx, inboxUser("settle")); err != nil {
		t.Fatal(err)
	}
	<-driving
	waiting, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Dispose(waiting); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-releasing:
		t.Fatal("released before Driver settled")
	default:
	}
	if err := current.(*session.LiveSession).Release(func() error { return nil }); !errors.Is(err, session.ErrAttached) {
		t.Fatal("active resource admitted release", err)
	}
	close(settled)
	<-releasing
	if len(registry.Snapshot()) != 0 {
		t.Fatal("disposing Agent remained visible")
	}
	if _, err := f.Attach(ctx, AttachOptions{Session: current}); !errors.Is(err, ErrExists) {
		t.Fatal("identity reused before resource release", err)
	}
	committedSettlement := false
	for _, fact := range store.snapshot() {
		if fact.Type == protocol.TurnEnded && protocol.EventPayload[protocol.TurnEndData](fact).Turn == 2 {
			committedSettlement = true
		}
	}
	if !committedSettlement {
		close(released)
		t.Fatal("resource release preceded committed settlement")
	}
	close(released)
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	store.assertReleased(t, registry)
}
