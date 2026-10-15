// Package runtime owns the shared Host bootstrap and disposal lifecycle.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/gordis"
)

// Plan supplies product initialization after the shared factory creates a Host.
// Initialize may execute a Reconciler Apply or a dependency-ordered mount plan.
type Plan struct {
	Plugins       []gordis.Plugin
	Initialize    func(context.Context, *Runtime) error
	BeforeCleanup func()

	// Hooks are owned by the composition using this sole Host.
	Quiesce func()
	Stop    func(context.Context) error
	Release func() error
}

type Runtime struct {
	Host          *gordis.Host
	beforeCleanup func()
	quiesce       func()
	stop          func(context.Context) error
	release       func() error
	once          sync.Once
	done          chan struct{}
	closeErr      error
}

// CleanupError preserves failed startup disposal for process exit handling.
type CleanupError struct{ Err error }

func (e *CleanupError) Error() string { return e.Err.Error() }

func (e *CleanupError) Unwrap() error { return e.Err }

func Start(ctx context.Context, plan Plan) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	host, err := gordis.NewHost(plan.Plugins...)
	if err != nil {
		return nil, err
	}

	r := &Runtime{
		Host:          host,
		beforeCleanup: plan.BeforeCleanup,
		quiesce:       plan.Quiesce,
		stop:          plan.Stop,
		release:       plan.Release,
		done:          make(chan struct{}),
	}
	if plan.Initialize != nil {
		if err := plan.Initialize(ctx, r); err != nil {
			return nil, r.Rollback(err)
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, r.Rollback(err)
	}

	return r, nil
}

// Mount initializes a typed, dependency-ordered plan; Gordis owns Scope rollback.
func (r *Runtime) Mount(ctx context.Context, specs []gordis.InstanceSpec) error {
	for _, spec := range specs {
		if _, err := r.Host.Env().MountReady(ctx, spec); err != nil {
			return fmt.Errorf("mount %s: %w", spec.ID, err)
		}
	}

	return nil
}

// Rollback also covers product-specific checks performed after initialization.
func (r *Runtime) Rollback(cause error) error {
	if r.beforeCleanup != nil {
		r.beforeCleanup()
	}

	if err := r.Shutdown(context.Background()); err != nil {
		return errors.Join(cause, &CleanupError{Err: err})
	}

	return cause
}

// Shutdown stops management admission/watchers, then drains the sole Host.
// Disposal continues after a caller's deadline; later calls can await its result.
// Process launchers own the shutdown grace and may force exit if cleanup hangs.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.once.Do(func() {
		if r.quiesce != nil {
			r.quiesce()
		}

		go func() {
			var stopErr error
			if r.stop != nil {
				stopErr = r.stop(context.Background())
			}

			r.closeErr = errors.Join(stopErr, r.Host.Shutdown(context.Background()))
			// An uncertain drain retains artifact ownership until process exit.
			if r.closeErr == nil && r.release != nil {
				r.closeErr = r.release()
			}

			close(r.done)
		}()
	})

	select {
	case <-r.done:
		return r.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
