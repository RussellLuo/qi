package agent

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// Simple Driver fixtures allocate real Sessions explicitly. The production
// Agent always uses the same scheduler; a test Driver must claim its own input.
type testService struct {
	*fixtureController
	store *session.Runtime
}

func newTestService(driver agentloop.Driver) *testService {
	store := session.NewRuntime(session.RuntimeOptions{})
	return &testService{fixtureController: newFixtureController(driver, store), store: store}
}
func (s *testService) Create(ctx context.Context, request CreateRequest) (Agent, error) {
	if _, err := s.store.Get(ctx, request.SessionID); errors.Is(err, session.ErrNotFound) {
		if _, err := s.store.Create(ctx, session.CreateRequest{ID: request.SessionID}); err != nil {
			return nil, err
		}
	}
	return s.fixtureController.Create(ctx, request)
}

// fixtureController owns Handles for tests that borrow pre-created Sessions. It delegates
// every creation to Factory and every release to Handle, using an explicit Store.
// Registry is the only authority for Agent identity and visibility.
type fixtureController struct {
	mu                 sync.Mutex
	registry           Registry
	store              session.Store
	driver             agentloop.Driver
	compose            ConfigComposer
	initialize         Initializer
	observe            AgentObserver
	acquirePublication func() (func(), error)
	lifecycle          *lifecycleRelay
	factory            *resourceFactory
	factories          []*resourceFactory
	aliases            map[ID]ID
	closed             bool
	done               chan struct{}
}

func newFixtureController(driver agentloop.Driver, stores ...session.Store) *fixtureController {
	s := &fixtureController{registry: NewRegistry(), driver: driver, lifecycle: &lifecycleRelay{}, aliases: make(map[ID]ID), done: make(chan struct{})}
	if len(stores) == 1 {
		s.store = stores[0]
	}
	return s
}

func (s *fixtureController) newFactory(options FactoryOptions) (Factory, error) {
	// Test Stores without resource capabilities are explicitly borrowed.
	_, owned := options.Store.(session.ResourceStore)
	options.AttachOnly = !owned
	if s.acquirePublication != nil {
		return NewBoundFactory(options, s.acquirePublication)
	}
	return NewFactory(options)
}
func (s *fixtureController) SetConfigComposer(c ConfigComposer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compose = c
	for _, f := range s.factories {
		f.mu.Lock()
		f.compose = c
		f.mu.Unlock()
	}
}
func (s *fixtureController) SetLifecycle(l fixtureLifecycle) { s.lifecycle.set(l) }

func (s *fixtureController) Create(ctx context.Context, request CreateRequest) (Agent, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if s.factory == nil {
		f, err := s.newFactory(FactoryOptions{Registry: s.registry, Store: s.store, Driver: s.driver, Compose: s.compose, Initialize: fixtureInitializer{s.lifecycle}, Observe: fixtureObserver{s.lifecycle}})
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		s.factory = f.(*resourceFactory)
		s.factories = append(s.factories, s.factory)
	}
	f := s.factory
	s.mu.Unlock()
	current, err := s.store.Get(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	h, err := f.Attach(ctx, AttachOptions{Session: current, Agent: request})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if request.ID != "" {
		s.aliases[request.ID] = h.Agent().ID()
	}
	s.mu.Unlock()
	return h.Agent(), nil
}
func (s *fixtureController) Get(ctx context.Context, id ID) (Agent, error) {
	s.mu.Lock()
	closed := s.closed
	if canonical := s.aliases[id]; canonical != "" {
		id = canonical
	}
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	return s.registry.Get(ctx, id)
}
func (s *fixtureController) Snapshot() []Snapshot { return s.registry.Snapshot() }
func (s *fixtureController) Delete(ctx context.Context, id ID) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	factories := append([]*resourceFactory(nil), s.factories...)
	s.mu.Unlock()
	for _, f := range factories {
		f.mu.Lock()
		var owned *resourceHandle
		for h := range f.handles {
			if h.Agent() == a {
				owned = h
				break
			}
		}
		f.mu.Unlock()
		if owned != nil {
			return owned.Dispose(ctx)
		}
	}
	return errors.New("fixture has no owning Handle")
}
func (s *fixtureController) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return
	}
	s.closed = true
	factories := append([]*resourceFactory(nil), s.factories...)
	s.mu.Unlock()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, f := range factories {
		_ = f.Close(canceled)
	}
	for _, f := range factories {
		_ = f.Close(context.Background())
	}
	close(s.done)
}

// newFixtureFactory assembles a test Factory using immutable production options.
func newFixtureFactory(store session.Store, service *fixtureController, options ...FactoryOptions) (*resourceFactory, error) {
	if service == nil || len(options) > 1 {
		return nil, errors.New("test factory requires its explicit fixture owner")
	}
	opts := FactoryOptions{Registry: service.registry, Store: store, Driver: service.driver, Compose: service.compose, Initialize: fixtureInitializer{service.lifecycle}, Observe: fixtureObserver{service.lifecycle}}
	if len(options) == 1 {
		opts.CheckAdmission = options[0].CheckAdmission
	}
	f, err := service.newFactory(opts)
	if err != nil {
		return nil, err
	}
	resources := f.(*resourceFactory)
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		_ = resources.Close(context.Background())
		return nil, ErrClosed
	}
	service.factories = append(service.factories, resources)
	return resources, nil
}

// lifecycleRelay binds test callbacks to the canonical initialization and observation ports.
// Canonical Factory dependencies are immutable and use separate initialization
// and passive observation ports.
type lifecycleRelay struct {
	mu     sync.RWMutex
	target fixtureLifecycle
}

func (l *lifecycleRelay) set(target fixtureLifecycle) {
	l.mu.Lock()
	l.target = target
	l.mu.Unlock()
}
func (l *lifecycleRelay) get() fixtureLifecycle {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.target
}
func (l *lifecycleRelay) Created(ctx context.Context, e contracts.AgentCreated) error {
	if target := l.get(); target != nil {
		return target.Created(ctx, e)
	}
	return nil
}
func (l *lifecycleRelay) Status(e contracts.AgentStatusObservation) {
	if target := l.get(); target != nil {
		target.Status(e)
	}
}
func (l *lifecycleRelay) Disposed(ctx context.Context, e contracts.AgentDisposed) {
	if target := l.get(); target != nil {
		target.Disposed(ctx, e)
	}
}
func (l *lifecycleRelay) AgentError(e contracts.AgentErrorObservation) {
	if target, ok := l.get().(contracts.AgentErrorObserver); ok {
		target.AgentError(e)
	}
}
func (l *lifecycleRelay) Inbox(e contracts.AgentInboxObservation) {
	if target, ok := l.get().(contracts.AgentInboxObserver); ok {
		target.Inbox(e)
	}
}

type fixtureLifecycle interface {
	Created(context.Context, contracts.AgentCreated) error
	Status(contracts.AgentStatusObservation)
	Disposed(context.Context, contracts.AgentDisposed)
}
type fixtureInitializer struct{ target fixtureLifecycle }

func (i fixtureInitializer) Initialize(ctx context.Context, in Initialization, _ CleanupRegistrar) error {
	a := in.Agent.(*initialAgent).agent
	return i.target.Created(ctx, contracts.AgentCreated{Agent: a, Source: in.Source, Snapshot: a.snapshot()})
}

// fixtureEvents combines test initialization callbacks with the canonical
// passive AgentEvents dispatcher. It owns no scheduling or notification queue.
type fixtureHandlers struct {
	ID       string
	Order    int
	Created  func(context.Context, contracts.AgentCreated) error
	Status   func(contracts.AgentStatusObservation)
	Disposed func(context.Context, contracts.AgentDisposed)
	Inbox    func(contracts.AgentInboxObservation)
	Error    func(contracts.AgentErrorObservation)
}
type fixtureEvents struct {
	init   *registry.Registry[fixtureHandlers]
	events AgentEvents
	mirror fixtureLifecycle
}

func newFixtureEvents(mirrors ...fixtureLifecycle) *fixtureEvents {
	r := &fixtureEvents{init: registry.New[fixtureHandlers](nil)}
	if len(mirrors) > 0 {
		r.mirror = mirrors[0]
		r.events = NewAgentEvents(fixtureObserver{r.mirror})
	} else {
		r.events = NewAgentEvents()
	}
	return r
}
func (r *fixtureEvents) Register(o contracts.ContributionOwner, h fixtureHandlers) (func(), error) {
	if h.ID == "" {
		return nil, errors.New("fixture handler requires ID")
	}
	var undoInit, undoObserver func()
	if h.Created != nil {
		var err error
		undoInit, err = r.init.Register(o, h.ID, h.Order, h)
		if err != nil {
			return nil, err
		}
	}
	if h.Status != nil || h.Disposed != nil || h.Inbox != nil || h.Error != nil {
		disposed := func(e contracts.AgentDisposed) {
			if h.Disposed != nil {
				h.Disposed(context.Background(), e)
			}
		}
		var err error
		undoObserver, err = r.events.Register(o, ObserverHandlers{ID: h.ID, Order: h.Order, Status: h.Status, Disposed: disposed, Inbox: h.Inbox, Error: h.Error})
		if err != nil {
			if undoInit != nil {
				undoInit()
			}
			return nil, err
		}
	}
	return func() {
		if undoInit != nil {
			undoInit()
		}
		if undoObserver != nil {
			undoObserver()
		}
	}, nil
}
func (r *fixtureEvents) Created(ctx context.Context, e contracts.AgentCreated) error {
	snapshot, err := r.init.Prepare()
	if err != nil {
		return err
	}
	defer snapshot.Close()
	for _, item := range snapshot.Items() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := item.Value.Created(ctx, e); err != nil {
			return err
		}
	}
	if r.mirror != nil {
		return r.mirror.Created(ctx, e)
	}
	return nil
}
func (r *fixtureEvents) Status(e contracts.AgentStatusObservation)             { r.events.Status(e) }
func (r *fixtureEvents) Disposed(_ context.Context, e contracts.AgentDisposed) { r.events.Disposed(e) }
func (r *fixtureEvents) Inbox(e contracts.AgentInboxObservation)               { r.events.Inbox(e) }
func (r *fixtureEvents) AgentError(e contracts.AgentErrorObservation)          { r.events.AgentError(e) }
func (r *fixtureEvents) Close()                                                { r.init.Close(); _ = r.events.Close(context.Background()) }
func (r *fixtureEvents) Dropped() uint64                                       { return r.events.Dropped() }

// fixtureObserver forwards test callbacks. The veto callback runs through
// Initialize; passive disposal receives a released-resource snapshot.
type fixtureObserver struct{ target fixtureLifecycle }

func (l fixtureObserver) Created(contracts.AgentCreated)            {}
func (l fixtureObserver) Status(e contracts.AgentStatusObservation) { l.target.Status(e) }
func (l fixtureObserver) Disposed(e contracts.AgentDisposed) {
	l.target.Disposed(context.Background(), e)
}
func (l fixtureObserver) Inbox(e contracts.AgentInboxObservation) {
	if h, ok := l.target.(contracts.AgentInboxObserver); ok {
		h.Inbox(e)
	}
}
func (l fixtureObserver) AgentError(e contracts.AgentErrorObservation) {
	if h, ok := l.target.(contracts.AgentErrorObserver); ok {
		h.AgentError(e)
	}
}

// testOwner is a deterministic ContributionOwner for runtime and conformance
// tests. Stop rejects new acquisitions, waits for admitted work, and then runs
// cleanup callbacks in reverse registration order.
type testOwner struct {
	mu         sync.Mutex
	identity   contracts.OwnerIdentity
	stopping   bool
	stopped    bool
	active     int
	cleanups   []func()
	activeZero chan struct{}
}

func newTestOwner(id string, generation uint64) *testOwner {
	return &testOwner{identity: contracts.OwnerIdentity{ID: id, Generation: generation}}
}

func (o *testOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *testOwner) Acquire() (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return nil, contracts.ErrOwnerUnavailable
	}

	o.active++
	var once sync.Once

	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.active--
			if o.active == 0 && o.activeZero != nil {
				close(o.activeZero)
				o.activeZero = nil
			}

			o.mu.Unlock()
		})
	}, nil
}

func (o *testOwner) DeferCleanup(_ string, cleanup func()) error {
	if cleanup == nil {
		return errors.New("cleanup is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return contracts.ErrOwnerUnavailable
	}

	o.cleanups = append(o.cleanups, cleanup)

	return nil
}

func (o *testOwner) Stop(ctx context.Context) error {
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	if !o.stopping {
		o.stopping = true
	}

	wait := o.activeZero
	if o.active > 0 && wait == nil {
		wait = make(chan struct{})
		o.activeZero = wait
	}

	o.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	cleanups := o.cleanups
	o.cleanups = nil
	o.stopped = true
	o.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	return nil
}

type scriptedScript struct {
	Chunks        []protocol.StreamChunk
	Err           error
	WaitForCancel bool
}

type scriptedAdapter struct {
	mu      sync.Mutex
	Version string
	Scripts []scriptedScript
	Seen    []llm.Request
}

func (a *scriptedAdapter) ResolveModel(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	if ref.Provider == "" || ref.Model == "" {
		return llm.ModelInfo{}, errors.New("provider and model are required")
	}

	return llm.ModelInfo{
		Ref:           ref,
		ContextWindow: 4096,
		Capabilities:  []string{"tools", a.Version},
	}, nil
}

func (a *scriptedAdapter) Stream(ctx context.Context, request llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	a.mu.Lock()
	a.Seen = append(a.Seen, request.Clone())
	if len(a.Scripts) == 0 {
		a.mu.Unlock()
		return func(yield func(protocol.StreamChunk, error) bool) {
			yield(protocol.StreamChunk{}, errors.New("script exhausted"))
		}
	}

	script := a.Scripts[0]
	a.Scripts = a.Scripts[1:]
	a.mu.Unlock()
	return func(yield func(protocol.StreamChunk, error) bool) {
		if script.WaitForCancel {
			<-ctx.Done()
			yield(protocol.StreamChunk{}, ctx.Err())
			return
		}

		for _, chunk := range script.Chunks {
			if !yield(chunk, nil) {
				return
			}
		}

		if script.Err != nil {
			yield(protocol.StreamChunk{}, script.Err)
		}
	}
}
