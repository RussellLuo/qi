package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// Handle owns one Agent. Session is borrowed by Attach and owned by Create/Resume.
// Dispose stops admission, cancels and drains activity before releasing owned
// runtime resources. It never closes shared services or deletes history.
type Handle interface {
	Agent() Agent
	Session() session.Session
	Dispose(context.Context) error
}

type CreateOptions struct {
	Session session.CreateRequest

	// Legacy identity fields are accepted for compatibility. Factory identity always
	// comes from the exact Session; ID does not create a Registry alias.
	Agent AgentOptions
}

type AttachOptions struct {
	Session session.Session
	Agent   AgentOptions
	// ID lookup for legacy consumers queries only already opened Sessions.
	ID            ID
	SessionID     protocol.SessionID
	Config        Config
	ParentID      ID
	DefaultConfig func(context.Context) (Config, error)
	ComposeConfig ConfigComposer
}

// ResumeOptions opens existing history; it never borrows a Get result.
type ResumeOptions struct {
	SessionID protocol.SessionID
	Agent     AgentOptions
}

type Factory interface {
	Create(context.Context, CreateOptions) (Handle, error)
	Attach(context.Context, AttachOptions) (Handle, error)
	Resume(context.Context, ResumeOptions) (Handle, error)
	Close(context.Context) error
}

// FactoryOptions supplies an optional owner admission barrier. CheckAdmission
// runs before allocation and again before publication; rejection rolls back
// the transaction. It must be brief and must not call back into the Factory.
// The owner still calls Close to cancel and drain admitted transactions.
type FactoryOptions struct {
	Registry Registry
	Store    session.Store
	// AttachOnly permits a Store without owned resource capabilities. Create and
	// Resume reject before acquisition; Attach never releases the borrowed Session.
	AttachOnly     bool
	Driver         agentloop.Driver
	Compose        ConfigComposer
	Initialize     Initializer
	Observe        AgentObserver
	CheckAdmission func() error
}

var ErrReleaseUnsupported = errors.New("session Store does not support runtime release")
var ErrAttachOnly = errors.New("agent Factory only supports borrowed Attach")
var ErrResumeUnsupported = session.ErrResumeUnsupported
var ErrRollbackUnsupported = errors.New("session Store does not support exact creation rollback")

// resourceFactory owns creation transactions and published Handles, while
// borrowing the Registry, Store and Driver. Close provides a core shutdown seam;
// the Host bridge binds it to plugin owners separately. Creation contexts own
// only their transactions, never successfully published Agent lifetimes.
type resourceFactory struct {
	store              session.Store
	resources          session.ResourceStore
	opener             session.StoreOpener
	registry           *memoryRegistry
	driver             agentloop.Driver
	compose            ConfigComposer
	initialize         Initializer
	events             *agentEvents
	ownEvents          bool
	acquirePublication func() (func(), error)
	ctx                context.Context
	stop               context.CancelFunc
	mu                 sync.Mutex
	closed             bool
	handles            map[*resourceHandle]struct{}
	workers            sync.WaitGroup
	once               sync.Once
	done               chan struct{}
	err                error
	cleanupErr         error // protected by mu; includes already disposed Handles
	checkAdmission     func() error
}

// NewFactory validates execution, identity and owned resource capabilities
// before starting observers or acquiring Sessions. AttachOnly skips ownership.
func NewFactory(options FactoryOptions) (Factory, error) {
	r, ok := options.Registry.(*memoryRegistry)
	if !ok || r == nil || options.Store == nil || options.Driver == nil {
		return nil, errors.New("agent factory requires the native Registry, Store and Driver")
	}
	var resources session.ResourceStore
	var opener session.StoreOpener
	if !options.AttachOnly {
		if _, ok := options.Store.(session.StoreReleaser); !ok {
			return nil, ErrReleaseUnsupported
		}
		var ok bool
		resources, ok = options.Store.(session.ResourceStore)
		if !ok {
			return nil, ErrRollbackUnsupported
		}
		opener, _ = options.Store.(session.StoreOpener)
	}
	ctx, stop := context.WithCancel(context.Background())
	events, native := options.Observe.(*agentEvents)
	if !native {
		mirror := options.Observe
		events = NewAgentEvents(mirror).(*agentEvents)
	}
	return &resourceFactory{
		store:          options.Store,
		resources:      resources,
		opener:         opener,
		registry:       r,
		driver:         options.Driver,
		compose:        options.Compose,
		initialize:     options.Initialize,
		events:         events,
		ownEvents:      !native,
		ctx:            ctx,
		stop:           stop,
		handles:        make(map[*resourceHandle]struct{}),
		done:           make(chan struct{}),
		checkAdmission: options.CheckAdmission,
	}, nil
}

// NewBoundFactory binds publication to fixed Ready generation leases. Ordinary
// consumers use NewFactory; framework adapters supply acquire. The publication
// gate spans Handle publication. acquire must not perform I/O, invoke business
// callbacks, or reenter the Factory while its lock is held.
func NewBoundFactory(options FactoryOptions, acquire func() (func(), error)) (Factory, error) {
	if acquire == nil {
		return nil, errors.New("bound Factory requires a publication gate")
	}
	f, err := NewFactory(options)
	if err != nil {
		return nil, err
	}
	f.(*resourceFactory).acquirePublication = acquire
	return f, nil
}

func (f *resourceFactory) publish(ctx context.Context, h *resourceHandle, ticket *lifecycleTicket) error {
	f.mu.Lock()
	f.registry.mu.Lock()
	a := h.agent
	a.mu.Lock()
	var release func()
	err := ctx.Err()
	if f.closed || f.registry.entries[h.token.id] != h.token || h.token.published {
		err = ErrClosed
	}
	if err == nil && f.checkAdmission != nil {
		err = f.checkAdmission()
	}
	if err == nil && f.acquirePublication != nil {
		release, err = f.acquirePublication()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		f.handles[h] = struct{}{}
		a.ready = true
		if a.initialWake {
			a.status = StatusRunning
			a.idle = make(chan struct{})
			a.activityCtx, a.activeStop = context.WithCancelCause(a.ctx)
			a.inbox <- submission{}
		}
		event := contracts.AgentCreated{Agent: a, Source: a.reservation.CreationSource(), Snapshot: contracts.AgentSnapshot{SessionID: a.sessionID, InstanceID: a.InstanceID(), Status: a.status}}
		ticket.created(event)
		for sub := range a.notifications {
			sub.Push(AgentNotification{InstanceID: event.Snapshot.InstanceID, Created: &event})
		}
		for _, pending := range a.initialNotifications {
			f.events.Inbox(pending.Inbox.Clone())
			for sub := range a.notifications {
				sub.Push(pending.Clone())
			}
		}
		a.initialNotifications = nil
		f.events.dropped.Add(a.initialDropped)
		for sub := range a.notifications {
			sub.Drop(a.initialDropped)
		}
		a.initialDropped = 0
		h.token.agent, h.token.published = a, true
	}
	a.mu.Unlock()
	f.registry.mu.Unlock()
	f.mu.Unlock()
	if release != nil {
		release()
	}
	return err
}

func (f *resourceFactory) begin(ctx context.Context) (context.Context, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return nil, nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if f.checkAdmission != nil {
		if err := f.checkAdmission(); err != nil {
			return nil, nil, err
		}
	}

	f.workers.Add(1)
	// Either the caller or factory shutdown may cancel initialization. Once
	// published, the Handle's lifetime no longer depends on this context.
	creation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(f.ctx, cancel)

	return creation, func() {
		stop()
		cancel()
		f.workers.Done()
	}, nil
}

func (f *resourceFactory) Create(ctx context.Context, options CreateOptions) (Handle, error) {
	creation, finish, err := f.begin(ctx)
	if err != nil {
		return nil, err
	}

	defer finish()

	if f.resources == nil {
		return nil, ErrAttachOnly
	}
	if options.Agent.SessionID != "" && options.Agent.SessionID != options.Session.ID {
		return nil, errors.New("agent SessionID must match the requested Session ID")
	}

	current, err := f.store.Create(creation, options.Session)
	if err != nil {
		return nil, err
	}

	request := options.Agent
	request.SessionID = current.ID()

	return f.create(creation, current, request, &sessionResource{
		release:  func(ctx context.Context) error { return f.resources.Release(ctx, current) },
		rollback: func(ctx context.Context) error { return f.resources.RollbackCreate(ctx, current) },
	})
}

// Attach uses an already opened Store Session. It does not load disk history,
// and initialization failure never deletes or releases the borrowed instance.
func (f *resourceFactory) Attach(ctx context.Context, options AttachOptions) (Handle, error) {
	creation, finish, err := f.begin(ctx)
	if err != nil {
		return nil, err
	}

	defer finish()

	current := options.Session
	request := options.Agent
	if current == nil {
		// Legacy ID-based Attach borrows an already opened Session without disk I/O.
		current, err = f.store.Get(creation, options.SessionID)
		if options.Agent.SessionID == "" && options.Agent.ComposeConfig == nil && options.Agent.DefaultConfig == nil && options.Agent.Config.Loop.LLM.Provider == "" {
			request = CreateRequest{Config: options.Config, ParentID: options.ParentID, DefaultConfig: options.DefaultConfig, ComposeConfig: options.ComposeConfig}
		}
		if err != nil {
			return nil, err
		}
	}
	return f.create(creation, current, request, nil)
}

// sessionResource captures the exact successful acquisition. Create rolls back
// untransferred history; Resume releases runtime resources on both paths. Attach
// supplies no credential and cannot take ownership of a caller's Session.
type sessionResource struct {
	release  Cleanup
	rollback Cleanup
}

func (f *resourceFactory) Resume(ctx context.Context, options ResumeOptions) (Handle, error) {
	creation, finish, err := f.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if f.resources == nil {
		return nil, ErrAttachOnly
	}
	if f.opener == nil {
		return nil, ErrResumeUnsupported
	}
	if options.SessionID == "" || (options.Agent.SessionID != "" && options.Agent.SessionID != options.SessionID) {
		return nil, errors.New("Resume requires a matching Session ID")
	}
	current, err := f.opener.Open(creation, options.SessionID)
	if err != nil {
		return nil, err
	}
	release := func(ctx context.Context) error { return f.resources.Release(ctx, current) }
	request := options.Agent
	request.SessionID = options.SessionID
	return f.create(creation, current, request, &sessionResource{release: release, rollback: release})
}

func (f *resourceFactory) create(ctx context.Context, current session.Session, request CreateRequest, resource *sessionResource) (handle Handle, err error) {
	id := ID(current.ID())
	f.mu.Lock()
	factoryCompose := f.compose
	f.mu.Unlock()
	var a *loopAgent
	var token *entry
	var releaseConfig Cleanup
	resources := &cleanupStack{}
	var ticket *lifecycleTicket
	var reservation contracts.SessionReservation
	// Every acquired resource is recorded immediately. Rollback preserves the original error.
	defer func() {
		if recovered := recover(); recovered != nil {
			if cause, ok := recovered.(error); ok {
				err = fmt.Errorf("agent creation panicked: %w", cause)
			} else {
				err = fmt.Errorf("agent creation panicked: %v", recovered)
			}
			handle = nil
		}
		if err == nil {
			return
		}
		if token != nil {
			f.registry.withdraw(token)
		}
		if a != nil {
			a.cleanup()
			err = errors.Join(err, a.cleanupErr)
		} else {
			err = errors.Join(err, resources.run(context.Background()))
			if reservation != nil {
				err = errors.Join(err, callCleanup(context.Background(), func(context.Context) error { return reservation.Close() }))
			}
		}
		if resource != nil {
			err = errors.Join(err, callCleanup(context.Background(), resource.rollback))
		}
		if token != nil {
			f.registry.remove(token)
		}
		if ticket != nil {
			ticket.cancel()
		}
	}()
	if request.SessionID != "" && request.SessionID != current.ID() {
		return nil, errors.New("agent SessionID does not match the attached Session")
	}
	var parent *ParentView
	token, parent, err = f.registry.reserve(id, request.ParentID)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = f.ctx.Err(); err != nil {
		return nil, err
	}
	reservation, err = current.Attach()
	if err != nil {
		return nil, err
	}
	config := cloneConfig(request.Config)
	compose := request.ComposeConfig
	if compose == nil {
		compose = factoryCompose
	}
	if compose != nil {
		config, releaseConfig, err = compose(ctx, Creation{ID: id, Parent: parent, Config: config, DefaultConfig: request.DefaultConfig})
	} else if request.DefaultConfig != nil {
		config, err = request.DefaultConfig(ctx)
	}
	if releaseConfig != nil {
		_ = resources.Defer("configuration", releaseConfig)
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	idle := make(chan struct{})
	close(idle)
	agentCtx, stop := context.WithCancelCause(context.Background())
	a = &loopAgent{
		driver:           f.driver,
		observer:         f.events,
		session:          current,
		id:               id,
		sessionID:        current.ID(),
		parentID:         request.ParentID,
		config:           cloneConfig(config),
		cleanupResources: resources.run,
		ctx:              agentCtx,
		stop:             stop,
		done:             make(chan struct{}),
		inbox:            make(chan submission, 1),
		idle:             idle,
		inputInbox:       session.NewInbox(current),
		pendingOutputs:   make(map[protocol.MessageID]*submission),
		status:           StatusIdle,
		reservation:      reservation,
		requestState:     &agentloop.RequestState{},
		invocationPrefix: fmt.Sprintf("%s-%x", id, nonce),
	}
	ticket, err = f.events.reserve()
	if err != nil {
		return nil, err
	}
	port := &initialAgent{agent: a, valid: true}
	err = func() error {
		defer port.expire()
		defer resources.seal()
		if f.initialize != nil {
			if err := f.initialize.Initialize(ctx, Initialization{Agent: port, Parent: parent, Source: reservation.CreationSource()}, resources); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	h := &resourceHandle{factory: f, agent: a, session: current, resource: resource, token: token, ticket: ticket, done: make(chan struct{})}
	// Publication holds Factory -> Registry -> Agent locks through the final
	// generation acquisition and the entire ownership/visibility commit.
	err = f.publish(ctx, h, ticket)
	if err != nil {
		return nil, err
	}
	if a.initialWake {
		a.notifyStatus(StatusRunning)
	}
	go a.work()
	return h, nil
}

// Close rejects new creation, cancels in-flight initialization and disposes all
// Handles. A timeout only ends this wait; cleanup continues and later calls
// observe the same result. It does not close the borrowed Store or Driver.
func (f *resourceFactory) Close(ctx context.Context) error {
	f.once.Do(func() {
		f.mu.Lock()
		f.closed = true
		f.stop()
		handles := make([]*resourceHandle, 0, len(f.handles))

		for h := range f.handles {
			handles = append(handles, h)
		}

		f.mu.Unlock()

		for _, h := range handles {
			h.startDispose()
		}

		go func() {
			f.workers.Wait()

			for _, h := range handles {
				_ = h.Dispose(context.Background())
			}

			f.mu.Lock()
			f.err = f.cleanupErr
			f.mu.Unlock()
			if f.ownEvents {
				canceled, cancel := context.WithCancel(context.Background())
				cancel()
				_ = f.events.Close(canceled)
			}
			close(f.done)
		}()
	})
	return waitCleanup(ctx, f.done, &f.err)
}
