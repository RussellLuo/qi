package agent

import (
	"context"
	"errors"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// InitialAgent is valid only during Initialize. It cannot wait on or dispose
// the unpublished worker. Wake requests are held until successful publication.
type InitialAgent interface {
	ID() ID
	SessionID() protocol.SessionID
	Config() Config
	Configure(context.Context, Config) error
	Send(context.Context, protocol.Message, contracts.InboxTarget, bool) error
	Pending(context.Context) (contracts.InboxState, error)
}

type Initialization struct {
	Agent  InitialAgent
	Parent *ParentView
	Source string
}
type Initializer interface {
	Initialize(context.Context, Initialization, CleanupRegistrar) error
}
type CleanupRegistrar interface{ Defer(string, Cleanup) error }
type InitializationHandler struct {
	ID         string
	Order      int
	Initialize func(context.Context, Initialization, CleanupRegistrar) error
}
type InitializerRegistry interface {
	Initializer
	Register(contracts.ContributionOwner, InitializationHandler) (func(), error)
	Close()
}

type initializers struct {
	entries *registry.Registry[InitializationHandler]
}

func NewInitializers() InitializerRegistry {
	return &initializers{registry.New[InitializationHandler](nil)}
}
func (r *initializers) Register(owner contracts.ContributionOwner, h InitializationHandler) (func(), error) {
	if h.ID == "" || h.Initialize == nil {
		return nil, errors.New("initializer requires id and handler")
	}
	return r.entries.Register(owner, h.ID, h.Order, h)
}
func (r *initializers) Close() { r.entries.Close() }
func (r *initializers) Initialize(ctx context.Context, init Initialization, cleanup CleanupRegistrar) error {
	snapshot, err := r.entries.Prepare()
	if err != nil {
		return err
	}
	defer snapshot.Close()
	for _, item := range snapshot.Items() {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Give each handler its own expiring port and Parent clone.
		child := &initialAgent{target: init.Agent, valid: true}
		current := init
		current.Agent = child
		if init.Parent != nil {
			p := *init.Parent
			p.Config = cloneConfig(p.Config)
			current.Parent = &p
		}
		err := func() error {
			defer child.expire()
			registrar := &scopedRegistrar{target: cleanup, valid: true}
			defer registrar.expire()
			return item.Value.Initialize(ctx, current, registrar)
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

type cleanupStack struct {
	mu     sync.Mutex
	sealed bool
	items  []Cleanup
}

func (s *cleanupStack) Defer(name string, cleanup Cleanup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return ErrClosed
	}
	if name == "" || cleanup == nil {
		return errors.New("cleanup requires name and function")
	}
	s.items = append(s.items, cleanup)
	return nil
}
func (s *cleanupStack) seal() { s.mu.Lock(); s.sealed = true; s.mu.Unlock() }
func (s *cleanupStack) run(ctx context.Context) error {
	s.mu.Lock()
	s.sealed = true
	items := s.items
	s.items = nil
	s.mu.Unlock()
	var err error
	for i := len(items) - 1; i >= 0; i-- {
		err = errors.Join(err, callCleanup(ctx, items[i]))
	}
	return err
}

type initialAgent struct {
	mu     sync.Mutex
	agent  *loopAgent
	target InitialAgent
	valid  bool
}

func (p *initialAgent) expire() { p.mu.Lock(); p.valid = false; p.mu.Unlock() }
func (p *initialAgent) ID() ID {
	if p.target != nil {
		return p.target.ID()
	}
	return p.agent.ID()
}
func (p *initialAgent) SessionID() protocol.SessionID {
	if p.target != nil {
		return p.target.SessionID()
	}
	return p.agent.SessionID()
}
func (p *initialAgent) Config() Config {
	if p.target != nil {
		return p.target.Config()
	}
	return p.agent.Config()
}
func (p *initialAgent) Configure(ctx context.Context, config Config) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.valid {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.target != nil {
		return p.target.Configure(ctx, config)
	}
	if err := validateConfig(config); err != nil {
		return err
	}
	p.agent.mu.Lock()
	defer p.agent.mu.Unlock()
	p.agent.config = cloneConfig(config)
	return nil
}
func (p *initialAgent) Send(ctx context.Context, m protocol.Message, target contracts.InboxTarget, wake bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.valid {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.target != nil {
		return p.target.Send(ctx, m, target, wake)
	}
	p.agent.transitions.Lock()
	defer p.agent.transitions.Unlock()
	if err := p.agent.send(ctx, m, target, false); err != nil {
		return err
	}
	p.agent.initialWake = p.agent.initialWake || wake
	return nil
}
func (p *initialAgent) Pending(ctx context.Context) (contracts.InboxState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.valid {
		return nil, ErrClosed
	}
	if p.target != nil {
		return p.target.Pending(ctx)
	}
	return p.agent.Pending(ctx)
}

func validateConfig(config Config) error {
	if config.Loop.LLM.Provider == "" || config.Loop.LLM.Model == "" {
		return errors.New("agent configuration requires an LLM provider and model")
	}
	return nil
}

type scopedRegistrar struct {
	mu     sync.Mutex
	target CleanupRegistrar
	valid  bool
}

func (s *scopedRegistrar) expire() { s.mu.Lock(); s.valid = false; s.mu.Unlock() }
func (s *scopedRegistrar) Defer(name string, cleanup Cleanup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid {
		return ErrClosed
	}
	return s.target.Defer(name, cleanup)
}
