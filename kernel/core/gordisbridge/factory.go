// Package gordisbridge adapts Host scopes and services to the core's ownership
// and observation contracts.
package gordisbridge

import (
	"context"
	"errors"
	"sync"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/agent"
)

type agentFactoryService struct {
	provider   *gordis.Scope
	options    agent.FactoryOptions
	mu         sync.Mutex
	closed     bool
	callers    map[*gordis.Scope]*scopeFactory
	once       sync.Once
	done       chan struct{}
	err        error
	cleanupErr error // protected by mu, includes retired caller generations
}

// NewAgentFactoryService is the provider-side lifecycle seam. The provider
// must publish it with agent.FactoryKey; callers must declare that dependency.
func NewAgentFactoryService(provider *gordis.Scope, options agent.FactoryOptions) (agent.FactoryProvider, error) {
	if provider == nil || provider.Scope == nil || options.Store == nil || options.Registry == nil || options.Driver == nil {
		return nil, errors.New("agent factory service requires a provider Scope and explicit dependencies")
	}
	f := &agentFactoryService{provider: provider, options: options, callers: make(map[*gordis.Scope]*scopeFactory), done: make(chan struct{})}

	if err := provider.OnStop("drain Agent factories", f.close); err != nil {
		return nil, err
	}

	return f, nil
}

// Bind assigns creation transactions and Handles to the exact caller generation.
// It may run in Activate; Create/Attach/Resume require both scopes to be Ready.
// Repeated binding within a generation returns the same Factory. Caller stop
// drains its owned resources; provider stop coordinates all bound factories.
func (s *agentFactoryService) Bind(caller *gordis.Scope) (agent.Factory, error) {
	if caller == nil || caller.Scope == nil {
		return nil, agent.ErrClosed
	}

	release, err := s.provider.AcquireReady()
	if err != nil {
		return nil, errors.Join(agent.ErrClosed, err)
	}

	defer release()

	releaseCaller, err := caller.Acquire()
	if err != nil {
		return nil, errors.Join(agent.ErrClosed, err)
	}

	defer releaseCaller()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, agent.ErrClosed
	}
	if f := s.callers[caller]; f != nil {
		return f, nil
	}

	f := &scopeFactory{caller: caller, provider: s.provider}
	options := s.options
	options.CheckAdmission = f.checkAdmission
	resources, err := agent.NewBoundFactory(options, f.admit)
	if err != nil {
		return nil, err
	}

	f.Factory = resources
	if err := caller.OnStop("drain owned Agent handles", func(ctx context.Context) error {
		err := resources.Close(ctx)
		s.retire(caller, f, err)
		return err
	}); err != nil {
		return nil, errors.Join(err, resources.Close(context.Background()))
	}

	s.callers[caller] = f

	return f, nil
}

func (s *agentFactoryService) retire(caller *gordis.Scope, f *scopeFactory, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.callers[caller] == f {
		delete(s.callers, caller)
		s.cleanupErr = errors.Join(s.cleanupErr, err)
	}
}

func (s *agentFactoryService) close(ctx context.Context) error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		factories := make([]*scopeFactory, 0, len(s.callers))

		for _, f := range s.callers {
			factories = append(factories, f)
		}

		s.mu.Unlock()
		// Close starts disposal even when its waiting context is canceled.
		// Stop every factory before waiting on any one slow Driver.
		waiting, cancel := context.WithCancel(context.Background())
		cancel()

		for _, f := range factories {
			_ = f.Close(waiting)
		}

		go func() {
			for _, f := range factories {
				s.retire(f.caller, f, f.Close(context.Background()))
			}

			s.mu.Lock()
			s.err = s.cleanupErr
			s.mu.Unlock()
			close(s.done)
		}()
	})

	select {
	case <-s.done:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type scopeFactory struct {
	agent.Factory
	caller   *gordis.Scope
	provider *gordis.Scope
}

func (f *scopeFactory) admit() (func(), error) {
	return acquireReadyPair(f.provider, f.caller)
}

type readyLeaser interface{ AcquireReady() (func(), error) }

// The provider acquisition is the successful publication point, conditional on
// caller acquisition also succeeding. Both releases span metadata publication.
func acquireReadyPair(providerScope, callerScope readyLeaser) (func(), error) {
	provider, err := providerScope.AcquireReady()
	if err != nil {
		return nil, errors.Join(agent.ErrClosed, err)
	}

	caller, err := callerScope.AcquireReady()
	if err != nil {
		provider()
		return nil, errors.Join(agent.ErrClosed, err)
	}

	return func() {
		caller()
		provider()
	}, nil
}

func (f *scopeFactory) checkAdmission() error {
	// Freeze precedes OnStop in Gordis. Do not expose a Handle from a
	// transaction that finished while either Scope was already frozen.
	release, err := f.admit()
	if err == nil {
		release()
	}

	return err
}

func (f *scopeFactory) Create(ctx context.Context, options agent.CreateOptions) (agent.Handle, error) {
	release, err := f.admit()
	if err != nil {
		return nil, err
	}

	defer release()

	return f.Factory.Create(ctx, options)
}

func (f *scopeFactory) Attach(ctx context.Context, options agent.AttachOptions) (agent.Handle, error) {
	release, err := f.admit()
	if err != nil {
		return nil, err
	}

	defer release()

	return f.Factory.Attach(ctx, options)
}

func (f *scopeFactory) Resume(ctx context.Context, options agent.ResumeOptions) (agent.Handle, error) {
	release, err := f.admit()
	if err != nil {
		return nil, err
	}
	defer release()
	return f.Factory.Resume(ctx, options)
}
