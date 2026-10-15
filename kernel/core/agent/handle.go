package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type resourceHandle struct {
	factory  *resourceFactory
	token    *entry
	ticket   *lifecycleTicket
	agent    *loopAgent
	session  session.Session
	resource *sessionResource
	once     sync.Once
	done     chan struct{}
	err      error
}

func (h *resourceHandle) Agent() Agent { return h.agent }

func (h *resourceHandle) Session() session.Session { return h.session }

func (h *resourceHandle) startDispose() {
	h.once.Do(func() {
		// Close admission before a canceled waiting context can return. The
		// potentially blocking inbox cleanup and drain run independently.
		f := h.factory
		f.mu.Lock()
		f.registry.mu.Lock()
		h.agent.mu.Lock()
		h.agent.disposed = true
		if f.registry.entries[h.token.id] == h.token {
			h.token.published = false
		}
		h.agent.mu.Unlock()
		f.registry.mu.Unlock()
		f.mu.Unlock()
		go func() {
			h.agent.shutdown()
			<-h.agent.done
			h.err = h.agent.cleanupErr
			if h.resource != nil {
				h.err = errors.Join(h.err, callCleanup(context.Background(), h.resource.release))
			}

			h.factory.mu.Lock()
			h.factory.registry.remove(h.token)
			h.factory.cleanupErr = errors.Join(h.factory.cleanupErr, h.err)

			event := contracts.AgentDisposed{Agent: h.agent, Snapshot: h.agent.snapshot(), Source: h.agent.reservation.CreationSource(), Err: h.err}
			h.agent.finishObservations(event)
			h.ticket.disposed(event)
			close(h.done)
			delete(h.factory.handles, h)
			h.factory.mu.Unlock()
		}()
	})
}

func (h *resourceHandle) Dispose(ctx context.Context) error {
	h.startDispose()
	return waitCleanup(ctx, h.done, &h.err)
}

func waitCleanup(ctx context.Context, done <-chan struct{}, result *error) error {
	select {
	case <-done:
		return *result
	default:
	}

	select {
	case <-done:
		return *result
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cleanup failures must not prevent detaching or rolling back later resources.
func callCleanup(ctx context.Context, cleanup Cleanup) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if cause, ok := recovered.(error); ok {
				err = fmt.Errorf("agent resource cleanup panicked: %w", cause)
			} else {
				err = fmt.Errorf("agent resource cleanup panicked: %v", recovered)
			}
		}
	}()
	return cleanup(ctx)
}
