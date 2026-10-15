package launcher_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	hostruntime "github.com/RussellLuo/qi/kernel/boot/internal/hostruntime"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/internal/launcher"
)

func TestCoordinatorConcurrencyAndCleanupFacts(t *testing.T) {
	var arms atomic.Int32
	c := launchcontract.NewCoordinator(func(code int) { arms.Add(1) })
	if err := c.Claim(); !errors.Is(err, launchcontract.ErrNotReady) {
		t.Fatal(err)
	}

	if err := c.Complete(0, nil); !errors.Is(err, launchcontract.ErrNotReady) || arms.Load() != 0 {
		t.Fatal(err)
	}

	var claims, completions atomic.Int32
	var wg sync.WaitGroup

	for range 30 {
		wg.Go(func() {
			if err := c.OpenReady(); err != nil {
				t.Error(err)
			}

			if c.Claim() == nil {
				claims.Add(1)
			}
		})
	}

	wg.Wait()
	cleanup := errors.New("task drain failed")

	for range 30 {
		wg.Go(func() {
			c.BeginShutdown(1)
			if c.Complete(0, cleanup) == nil {
				completions.Add(1)
			}
		})
	}

	wg.Wait()
	if claims.Load() != 1 || completions.Load() != 1 || arms.Load() != 1 {
		t.Fatal(claims.Load(), completions.Load(), arms.Load())
	}

	result := <-c.Done()
	if result.Code != 0 || !errors.Is(result.CleanupError, cleanup) {
		t.Fatal("lost task result or cleanup error", result)
	}

	select {
	case <-c.Done():
		t.Fatal("duplicate completion")
	default:
	}

	if err := c.Claim(); !errors.Is(err, launchcontract.ErrClaimed) {
		t.Fatal("reactivation got execution permit", err)
	}
}

func TestStopDoesNotOpenGateOrInventCompletion(t *testing.T) {
	c := launchcontract.NewCoordinator(nil)
	cause := errors.New("sibling failed")
	c.Stop(cause)
	c.Stop(errors.New("later"))
	if err := c.OpenReady(); !errors.Is(err, cause) || !errors.Is(err, launchcontract.ErrStopped) {
		t.Fatal(err)
	}

	if err := c.Claim(); !errors.Is(err, cause) {
		t.Fatal(err)
	}

	select {
	case <-c.Ready():
		t.Fatal("failed startup opened Ready")
	default:
	}

	select {
	case <-c.Done():
		t.Fatal("rollback fabricated task result")
	default:
	}
}

// A test carrier exercises the typed provider and Scope lifetime without
// implementing the Headless CLI plugin or another Host factory.
type carrier struct{ launch *launchcontract.Context }

func (p *carrier) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.launch",
		Provides: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec()},
		New:      func() gordis.Plugin { return p },
	}
}

func (p *carrier) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(launchcontract.LaunchContextKey, p.launch)
}

type taskPlugin struct {
	id               string
	starts, disposed *atomic.Int32
	fail             error
}

func (p *taskPlugin) Spec() gordis.PluginSpec {
	id := p.id
	if id == "" {
		id = "test.task"
	}

	return gordis.PluginSpec{
		ID:       id,
		Requires: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec()},
		New:      func() gordis.Plugin { return p },
	}
}

func (p *taskPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if p.fail != nil {
		return p.fail
	}

	launch, err := scope.Get(launchcontract.LaunchContextKey)
	if err != nil {
		return err
	}
	if err := scope.Defer("task generation disposed", func(context.Context) error {
		p.disposed.Add(1)
		return nil
	}); err != nil {
		return err
	}

	return scope.Go("one invocation", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-launch.Control.Ready():
		}

		if err := launch.Control.Claim(); err != nil {
			return nil
		}

		p.starts.Add(1)
		launch.Control.BeginShutdown(0)

		return launch.Control.Complete(0, nil)
	})
}

func TestWholePlanFailureRollsBackWaitingTaskAndSuccessRunsOnce(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "failure", false: "success"}[fail], func(t *testing.T) {
			var starts, disposed atomic.Int32
			c := launchcontract.NewCoordinator(nil)
			launch := &launchcontract.Context{Parent: context.Background(), Control: c}
			provider := &carrier{launch: launch}
			task := &taskPlugin{starts: &starts, disposed: &disposed}
			failure := errors.New("sibling activation failed")
			sibling := &taskPlugin{id: "test.failure", fail: failure}
			var taskInstance *gordis.Instance
			r, err := hostruntime.Start(context.Background(), hostruntime.Plan{
				Plugins:       []gordis.Plugin{provider, task, sibling},
				BeforeCleanup: func() { c.Stop(failure) },
				Initialize: func(ctx context.Context, r *hostruntime.Runtime) error {
					if err := r.Mount(ctx, []gordis.InstanceSpec{{ID: "launch", Plugin: "test.launch"}}); err != nil {
						return err
					}

					var err error
					taskInstance, err = r.Host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "task", Plugin: "test.task"})
					if err != nil {
						return err
					}
					if fail {
						return r.Mount(ctx, []gordis.InstanceSpec{{ID: "failure", Plugin: "test.failure"}})
					}

					return nil
				},
			})
			if fail {
				if r != nil || !errors.Is(err, failure) || starts.Load() != 0 || disposed.Load() != 1 {
					t.Fatal(r, err, starts.Load(), disposed.Load())
				}

				if err := c.OpenReady(); !errors.Is(err, failure) {
					t.Fatal(err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			defer r.Shutdown(context.Background())

			if starts.Load() != 0 {
				t.Fatal("ran before whole plan Ready")
			}

			if err := c.OpenReady(); err != nil {
				t.Fatal(err)
			}

			select {
			case <-c.Done():
			case <-time.After(time.Second):
				t.Fatal("invocation did not complete")
			}

			if disposed.Load() != 0 {
				t.Fatal("completion shut down Host")
			}
			// Replacing a generation still sees the global execution permit.
			if err := taskInstance.Unmount(context.Background()); err != nil {
				t.Fatal(err)
			}

			if err := r.Mount(context.Background(), []gordis.InstanceSpec{{ID: "task", Plugin: "test.task"}}); err != nil {
				t.Fatal(err)
			}

			c.Stop(nil)
			if err := r.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}

			if starts.Load() != 1 || disposed.Load() != 2 {
				t.Fatal("reactivation reran task or failed drain", starts.Load(), disposed.Load())
			}
		})
	}
}

type invocation struct {
	help  bool
	reads *atomic.Int32
}

func (i invocation) HelpRequested() bool { return i.help }

func (i invocation) Prepare(ctx context.Context, input launchcontract.Input) (launchcontract.Prepared, error) {
	i.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	_, err := io.ReadAll(input.Stdin)
	return prepared{}, err
}

type prepared struct{}

func (prepared) Bind(p []gordis.Plugin) ([]gordis.Plugin, error) {
	return append([]gordis.Plugin(nil), p...), nil
}

type parser struct{ reads *atomic.Int32 }

func (parser) Help() string { return "pure help" }

func (p parser) Parse(args []string, _ bool) (launchcontract.Invocation, error) {
	if len(args) != 1 {
		return nil, errors.New("invalid args")
	}

	return invocation{help: args[0] == "--help", reads: p.reads}, nil
}

func TestInvocationParseHelpAndInputAreSeparate(t *testing.T) {
	var reads atomic.Int32
	parse := parser{reads: &reads}
	call, help, err := launchcontract.Parse(parse, []string{"--help"}, false)
	if err != nil || call != nil || help != "pure help" || reads.Load() != 0 {
		t.Fatal(call, help, err, reads.Load())
	}

	if _, _, err := launchcontract.Parse(parse, nil, false); err == nil || reads.Load() != 0 {
		t.Fatal("invalid argv prepared input")
	}

	if _, _, err := launchcontract.Parse(nil, []string{"extra"}, false); err == nil {
		t.Fatal("no-consumer graph accepted argv")
	}

	if call, _, err := launchcontract.Parse(nil, nil, false); err != nil || call != nil {
		t.Fatal(call, err)
	}

	call, _, err = launchcontract.Parse(parse, []string{"task"}, false)
	if err != nil || reads.Load() != 0 {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := call.Prepare(ctx, launchcontract.Input{Stdin: strings.NewReader("task")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
