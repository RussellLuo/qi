package launcher_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	hostruntime "github.com/RussellLuo/qi/kernel/boot/internal/hostruntime"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/internal/launcher"
	"github.com/RussellLuo/qi/kernel/boot/internal/profile"
	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
	builtin "github.com/RussellLuo/qi/kernel/bundle"
)

type consumer struct{ calls, disposed *atomic.Int32 }

func (p *consumer) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.consumer",
		Requires: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec()},
		New:      func() gordis.Plugin { return &consumer{p.calls, p.disposed} },
	}
}

func (p *consumer) Activate(_ context.Context, s *gordis.Scope) error {
	launch, err := s.Get(launchcontract.LaunchContextKey)
	if err != nil {
		return err
	}

	s.Defer("dispose", func(context.Context) error {
		p.disposed.Add(1)
		return nil
	})

	return s.Go("invocation", func(ctx context.Context) error {
		select {
		case <-launch.Control.Ready():
		case <-ctx.Done():
			return nil
		}

		if err := launch.Control.Claim(); err != nil {
			return err
		}

		p.calls.Add(1)

		return launch.Control.Complete(0, nil)
	})
}

func TestExplicitBootstrapUsesOneHostAndPreservesReadyRollback(t *testing.T) {
	for _, custom := range []bool{false, true} {
		for _, failReady := range []bool{false, true} {
			t.Run(strings.Join(
				[]string{
					map[bool]string{false: "base", true: "policy"}[custom],
					map[bool]string{false: "success", true: "rollback"}[failReady],
				},
				"/",
			), func(t *testing.T) {
				var calls, disposed atomic.Int32
				registrations := []registration.PluginRegistration{{
					Plugin:         &consumer{&calls, &disposed},
					ProfileVisible: true,
					Completes:      true,
				}}
				candidate, err := registration.Plan(registrations, profile.Profile{Instances: []profile.InstanceSpec{{ID: "consumer", Plugin: "test.consumer"}}})
				if err != nil {
					t.Fatal(err)
				}

				var policy launchcontract.Policy
				var supplied *hostruntime.Runtime
				starts := 0
				if custom {
					policy.Start = func(ctx context.Context, request launchcontract.StartRequest) (*hostruntime.Runtime, error) {
						starts++
						runtime, err := hostruntime.Start(ctx, hostruntime.Plan{
							Plugins:       append(request.Plugins, request.Carrier),
							BeforeCleanup: request.BeforeCleanup,
							Initialize: func(ctx context.Context, r *hostruntime.Runtime) error {
								supplied = r
								if err := request.BeforeMount(ctx, r); err != nil {
									return err
								}

								return r.Mount(ctx, request.Candidate.Mounts)
							},
						})

						return runtime, err
					}
				}

				readyErr := errors.New("assembly Ready check failed")
				hooks := launchcontract.Hooks{
					Mounted: func(_ context.Context, r *hostruntime.Runtime) error {
						if custom && r != supplied {
							t.Fatal("launcher replaced supplied runtime")
						}

						if calls.Load() != 0 {
							t.Fatal("consumer executed before entire graph Ready")
						}

						return nil
					},
					Ready: func() error {
						if failReady {
							return readyErr
						}

						return nil
					},
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()

				code, cleanup, err := launchcontract.Execute(ctx, func(int) {}, candidate, nil, launchcontract.Input{}, policy, hooks)
				if failReady {
					if code != 1 || !errors.Is(err, readyErr) || calls.Load() != 0 {
						t.Fatal(code, err, calls.Load())
					}
				} else if code != 0 || err != nil || calls.Load() != 1 {
					t.Fatal(code, err, calls.Load())
				}

				if cleanup != nil || disposed.Load() != 1 || custom && starts != 1 {
					t.Fatal(cleanup, disposed.Load(), starts)
				}
			})
		}
	}
}

func TestWorkHasNoImplicitRegistrations(t *testing.T) {
	var diagnostics bytes.Buffer
	code, err := launchcontract.Work(context.Background(), func(int) {}, launchcontract.Options{
		Load: func() (profile.Profile, error) {
			return profile.Profile{Instances: []profile.InstanceSpec{{ID: "events", Plugin: "gordis/events"}}}, nil
		},
		Input: launchcontract.Input{Stdout: &bytes.Buffer{}, Stderr: &diagnostics},
	})
	if code != 1 || err != nil || !strings.Contains(diagnostics.String(), "trusted catalog") {
		t.Fatal(code, err, diagnostics.String())
	}
}

func TestBuiltinBaseRequiresExplicitSelection(t *testing.T) {
	app, err := applicationapi.Compose(builtin.Extension())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		bundles []string
		count   int
	}{{nil, 0}, {[]string{"base"}, 6}} {
		resolved, err := profile.Compose(profile.ProfileSource{Bundles: test.bundles}, app.BundleCatalog)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := registration.Plan(app.Registrations, resolved.Profile)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidate.Mounts) != test.count || candidate.ConsumerID != "" || candidate.CompletionOwnerID != "" {
			t.Fatal("implicit selection", candidate)
		}
	}
}
