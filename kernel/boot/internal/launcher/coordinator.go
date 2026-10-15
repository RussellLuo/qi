package launcher

import (
	"errors"
	"sync"
)

var (
	ErrNotReady  = errors.New("launch plan is not ready")
	ErrClaimed   = errors.New("launch invocation was already claimed")
	ErrCompleted = errors.New("launch invocation was already completed")
	ErrStopped   = errors.New("launch plan has stopped")
)

// Coordinator owns one Ready gate and one invocation for the whole launch.
// It has no timer/signal loop: begin delegates to the existing RunProcess.
// The launcher opens Ready only after its entire plan/probes are successful,
// calls Stop before rollback/shutdown, and closes the Host after Done.
type Coordinator struct {
	mu                                  sync.Mutex
	ready                               chan struct{}
	done                                chan Completion
	opened, claimed, completed, stopped bool
	cause                               error
	beginOnce                           sync.Once
	begin                               func(int)
}

func NewCoordinator(begin func(int)) *Coordinator {
	return &Coordinator{
		ready: make(chan struct{}),
		done:  make(chan Completion, 1),
		begin: begin,
	}
}

func (c *Coordinator) Ready() <-chan struct{} { return c.ready }

func (c *Coordinator) Done() <-chan Completion { return c.done }

func (c *Coordinator) OpenReady() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return errors.Join(ErrStopped, c.cause)
	}
	if !c.opened {
		c.opened = true
		close(c.ready)
	}

	return nil
}

// Stop never opens Ready or fabricates a completion. Waiting Scope tasks must
// also select on their cancellation context; the owner cancels before drain.
func (c *Coordinator) Stop(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.stopped {
		c.stopped = true
		c.cause = cause
	}
}

func (c *Coordinator) Claim() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return errors.Join(ErrStopped, c.cause)
	}
	if !c.opened {
		return ErrNotReady
	}
	if c.claimed {
		return ErrClaimed
	}

	c.claimed = true

	return nil
}

func (c *Coordinator) BeginShutdown(code int) {
	c.beginOnce.Do(func() {
		if c.begin != nil {
			c.begin(code)
		}
	})
}

func (c *Coordinator) Complete(code int, cleanupErr error) error {
	c.mu.Lock()
	if !c.claimed {
		c.mu.Unlock()
		return ErrNotReady
	}

	if c.completed {
		c.mu.Unlock()
		return ErrCompleted
	}

	c.completed = true
	c.mu.Unlock()
	// sync.Once also serializes concurrent BeginShutdown/Complete calls. Done
	// cannot be observed before the cleanup deadline has actually been armed.
	c.BeginShutdown(code)
	c.done <- Completion{Code: code, CleanupError: cleanupErr}

	return nil
}
