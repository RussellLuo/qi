package launch

import (
	"context"
	"errors"
	"sync"
)

// TaskScope is the finite Go capability of a Scope, not a Host operation.
type TaskScope interface {
	Go(string, func(context.Context) error) error
}

// FiniteFunc owns task work, output and cleanup policy. It must arm the process
// deadline with begin before cleanup, including failure paths. The helper also
// arms it before reporting completion. It must return only after task drain;
// shared services remain owned by the launcher/Scope.
type FiniteFunc func(ctx context.Context, begin func(int)) Completion

// Task coordinates one prepared finite invocation. Consumers retain parsing,
// binding, service/config checks and task/output/exit policy. A Task must not be
// copied after use. Start preserves the initial activation's check and drain
// probe; later generations never execute the invocation again.
type Task struct {
	mu    sync.Mutex
	check func(context.Context) error
	done  chan struct{}
}

func (t *Task) CheckReady(ctx context.Context) error {
	t.mu.Lock()
	check := t.check
	t.mu.Unlock()
	if check == nil {
		return errors.New("finite invocation services were not activated")
	}

	return check(ctx)
}

func (t *Task) Drain(ctx context.Context) error {
	t.mu.Lock()
	done := t.done
	t.mu.Unlock()
	if done == nil {
		return nil
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Task) Start(scope TaskScope, launch *Context, name string, check func(context.Context) error, run FiniteFunc) error {
	if scope == nil || launch == nil || launch.Parent == nil || launch.Control == nil || check == nil || run == nil {
		return errors.New("finite invocation requires scope, prepared launch context, check and callback")
	}

	t.mu.Lock()
	if t.done != nil {
		t.mu.Unlock()
		return nil
	}

	done := make(chan struct{})
	t.check, t.done = check, done
	t.mu.Unlock()
	err := scope.Go(name, func(scopeCtx context.Context) error {
		defer close(done)
		// Mount's deadline never owns the task; process and Scope cancellation do.
		ctx, cancel := context.WithCancel(launch.Parent)
		stop := context.AfterFunc(scopeCtx, cancel)
		defer stop()
		defer cancel()

		select {
		case <-ctx.Done():
			return nil
		case <-launch.Control.Ready():
		}

		if ctx.Err() != nil {
			return nil
		}
		if err := launch.Control.Claim(); err != nil {
			return nil
		}

		result := run(ctx, launch.Control.BeginShutdown)
		launch.Control.BeginShutdown(result.Code)

		return launch.Control.Complete(result.Code, result.CleanupError)
	})
	if err != nil {
		close(done)
	}

	return err
}
