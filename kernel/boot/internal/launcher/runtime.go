// Package launcher executes a preflighted graph and coordinates its one-shot
// consumer. Task semantics stay in the registered plugin's invocation metadata.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RussellLuo/gordis"
	hostruntime "github.com/RussellLuo/qi/kernel/boot/internal/hostruntime"
	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	appregistration "github.com/RussellLuo/qi/kernel/boot/internal/registration"
)

const carrierID = CarrierID

type carrier struct{ launch *Context }

func (p *carrier) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       LaunchContextKey.Name(),
		Provides: []gordis.ServiceSpec{LaunchContextKey.Spec()},
		New:      func() gordis.Plugin { return &carrier{p.launch} },
	}
}

func (p *carrier) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(LaunchContextKey, p.launch)
}

// Hooks bind trusted assembly callbacks without adding product branches.
type Hooks struct {
	Mounted func(context.Context, *hostruntime.Runtime) error
	Ready   func() error
}

type Run struct {
	*hostruntime.Runtime
	control *Coordinator
}

// Start mounts the whole plan and opens Ready only after all assembly checks.
func Start(
	ctx context.Context,
	begin func(int),
	candidate *appregistration.Candidate,
	prepared Prepared,
	input Input,
	policy Policy,
	hooks Hooks,
) (*Run, error) {
	for _, instance := range candidate.Profile.Instances {
		if instance.ID == carrierID {
			return nil, fmt.Errorf("instance id %q is reserved for launch context", carrierID)
		}
	}

	plugins, err := Bind(prepared, candidate.Plugins)
	if err != nil {
		return nil, err
	}

	control := NewCoordinator(begin)
	launch := &Context{
		Parent:     ctx,
		Invocation: prepared,
		Stdout:     input.Stdout,
		Stderr:     input.Stderr,
		Control:    control,
	}
	if directory, ok := prepared.(WorkingDirectory); ok {
		launch.CWD = directory.WorkingDirectory()
	}

	provider := &carrier{launch}
	mountCarrier := func(ctx context.Context, r *hostruntime.Runtime) error {
		return r.Mount(ctx, []gordis.InstanceSpec{{ID: carrierID, Plugin: LaunchContextKey.Name()}})
	}
	beforeCleanup := func() {
		control.Stop(errors.New("launch stopped before Ready"))
		control.BeginShutdown(1)
	}
	mountCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var runtime *hostruntime.Runtime
	if policy.Start != nil {
		runtime, err = policy.Start(mountCtx, StartRequest{
			Candidate:     candidate,
			Plugins:       plugins,
			Carrier:       provider,
			BeforeMount:   mountCarrier,
			BeforeCleanup: beforeCleanup,
		})
	} else {
		plugins = append(plugins, provider)
		runtime, err = hostruntime.Start(mountCtx, hostruntime.Plan{
			Plugins:       plugins,
			BeforeCleanup: beforeCleanup,
			Initialize: func(ctx context.Context, r *hostruntime.Runtime) error {
				if err := mountCarrier(ctx, r); err != nil {
					return err
				}

				return r.Mount(ctx, candidate.Mounts)
			},
		})
	}

	if err != nil {
		return nil, err
	}
	// Mounted services can be probed before Ready admits the invocation. Any
	// failed probe or assembly hook uses the same rollback path.
	if hooks.Mounted != nil {
		if err = hooks.Mounted(mountCtx, runtime); err != nil {
			return nil, runtime.Rollback(err)
		}
	}

	if check, ok := prepared.(ReadyCheck); ok {
		err = check.CheckReady(mountCtx)
	}

	if err == nil {
		err = mountCtx.Err()
	}

	if err != nil {
		return nil, runtime.Rollback(err)
	}
	if hooks.Ready != nil {
		if err = hooks.Ready(); err != nil {
			return nil, runtime.Rollback(err)
		}
	}

	if err = control.OpenReady(); err != nil {
		return nil, runtime.Rollback(err)
	}

	cancel() // only the initial Mount/probe deadline; not the task parent.
	return &Run{Runtime: runtime, control: control}, nil
}

// Execute is the single wait/drain/Host cleanup path for every runtime graph.
func Execute(
	ctx context.Context,
	begin func(int),
	candidate *appregistration.Candidate,
	prepared Prepared,
	input Input,
	policy Policy,
	callbacks ...Hooks,
) (code int, cleanupErr error, startErr error) {
	var hooks Hooks
	if len(callbacks) != 0 {
		hooks = callbacks[0]
	}

	run, err := Start(ctx, begin, candidate, prepared, input, policy, hooks)
	if err != nil {
		return 1, nil, err
	}

	runtime, control := run.Runtime, run.control
	code = 1
	if candidate.CompletionOwnerID != "" {
		select {
		case completion := <-control.Done():
			code, cleanupErr = completion.Code, completion.CleanupError
		case <-ctx.Done():
			// Arm first, stop further claims, and drain the invocation while Agent,
			// Store and projection services are still alive.
			control.BeginShutdown(1)
			control.Stop(ctx.Err())
			if drain, ok := prepared.(InvocationDrain); ok {
				cleanupErr = drain.Drain(context.Background())
			}
			select {
			case completion := <-control.Done():
				code, cleanupErr = completion.Code, errors.Join(cleanupErr, completion.CleanupError)
			default:
			}
		}
	} else {
		<-ctx.Done()
		code = 0
	}

	control.BeginShutdown(code)
	control.Stop(nil)
	cleanupErr = errors.Join(cleanupErr, runtime.Shutdown(context.Background()))

	return code, cleanupErr, nil
}

// StartRequest lets the composition owner decorate one shared Host bootstrap.
type StartRequest struct {
	Candidate     *appregistration.Candidate
	Plugins       []gordis.Plugin
	Carrier       gordis.Plugin
	BeforeMount   func(context.Context, *hostruntime.Runtime) error
	BeforeCleanup func()
}

// Policy supplies optional pure planning and bootstrap strategies. Zero Policy
// uses the explicit registrations and the basic Host lifecycle.
type Policy struct {
	Plan  func([]appregistration.PluginRegistration, appprofile.ResolvedProfile, appprofile.BundleCatalog) (*appregistration.Candidate, error)
	Start func(context.Context, StartRequest) (*hostruntime.Runtime, error)
}
