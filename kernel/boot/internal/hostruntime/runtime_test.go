package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
)

type lifecyclePlugin struct {
	activate func(context.Context, *gordis.Scope) error
}

func (p *lifecyclePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.lifecycle", New: func() gordis.Plugin { return p }}
}

func (p *lifecyclePlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	return p.activate(ctx, scope)
}

func TestStartupRollbackPreservesCauseAndCleanupError(t *testing.T) {
	cause := errors.New("service binding failed")
	cleanup := errors.New("disposal failed")
	var armed, disposed atomic.Int32
	plugin := &lifecyclePlugin{
		activate: func(_ context.Context, scope *gordis.Scope) error {
			return scope.Defer("failing disposer", func(context.Context) error {
				if armed.Load() != 1 {
					t.Error("cleanup began before launcher armed its grace")
				}

				disposed.Add(1)
				return cleanup
			})
		},
	}
	r, err := Start(context.Background(), Plan{
		Plugins:       []gordis.Plugin{plugin},
		BeforeCleanup: func() { armed.Add(1) },
		Initialize: func(ctx context.Context, r *Runtime) error {
			if err := r.Mount(ctx, []gordis.InstanceSpec{{ID: "one", Plugin: "test.lifecycle"}}); err != nil {
				return err
			}

			return cause
		},
	})
	var failed *CleanupError
	if r != nil || !errors.Is(err, cause) || !errors.Is(err, cleanup) || !errors.As(err, &failed) || disposed.Load() != 1 {
		t.Fatal(r, err, disposed.Load())
	}
}

func TestShutdownDrainsScopeAndCoalescesConcurrentCallers(t *testing.T) {
	var scope *gordis.Scope
	var disposed atomic.Int32
	plugin := &lifecyclePlugin{
		activate: func(_ context.Context, current *gordis.Scope) error {
			scope = current
			return current.Defer("record disposal", func(context.Context) error {
				disposed.Add(1)
				return nil
			})
		},
	}
	r, err := Start(context.Background(), Plan{
		Plugins: []gordis.Plugin{plugin},
		Initialize: func(ctx context.Context, r *Runtime) error {
			return r.Mount(ctx, []gordis.InstanceSpec{{ID: "one", Plugin: "test.lifecycle"}})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	release, err := scope.AcquireReady()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) || disposed.Load() != 0 {
		t.Fatal("live lease was released early", err, disposed.Load())
	}

	release()
	var callers sync.WaitGroup

	for i := 0; i < 20; i++ {
		callers.Go(func() {
			if err := r.Shutdown(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}

	callers.Wait()
	if disposed.Load() != 1 {
		t.Fatal("duplicate disposal", disposed.Load())
	}
}

func TestCancellationAfterInitializationAlsoRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var armed atomic.Int32
	r, err := Start(ctx, Plan{
		BeforeCleanup: func() { armed.Add(1) },
		Initialize: func(context.Context, *Runtime) error {
			cancel()
			return nil
		},
	})
	if r != nil || !errors.Is(err, context.Canceled) || armed.Load() != 1 {
		t.Fatal(r, err, armed.Load())
	}
}

func TestOwnershipReleaseFailureIsReportedOnce(t *testing.T) {
	cause := errors.New("ownership close failed")
	var calls atomic.Int32
	r, err := Start(context.Background(), Plan{
		Release: func() error {
			calls.Add(1)
			return cause
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := r.Shutdown(context.Background()); !errors.Is(err, cause) {
			t.Fatal(err)
		}
	}

	if calls.Load() != 1 {
		t.Fatal("duplicate ownership release", calls.Load())
	}
}
