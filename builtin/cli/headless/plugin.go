// Package headless implements one invocation inside the launcher's sole Host.
package headless

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/session"
)

const PluginID = "qi/cli/headless"

// Plugin selects a product model; argv and streams are run-local services.
type Plugin struct {
	Model string `json:"model"`
	// DisableEventHints is a trusted fixture seam for lossy/dropped wakeups.
	// Production uses notifications plus polling; this is never Profile config.
	DisableEventHints bool `json:"-"`
}

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (p *Plugin) Validate() error {
	if p.Model != "" && !modelID.MatchString(p.Model) {
		return errors.New("model must be a product model ID")
	}
	return nil
}

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: PluginID, New: func() gordis.Plugin { return &Plugin{DisableEventHints: p.DisableEventHints} }, Requires: []gordis.ServiceSpec{
		launchcontract.LaunchContextKey.Spec(), events.BusKey.Spec(), session.StoreKey.Spec(), agent.FactoryKey.Spec(), llm.RuntimeKey.Spec(),
	}}
}
func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	launch, err := scope.Get(launchcontract.LaunchContextKey)
	if err != nil {
		return err
	}
	if launch == nil || launch.Parent == nil || launch.Control == nil || launch.Stdout == nil || launch.Stderr == nil {
		return errors.New("headless requires a prepared launch context")
	}
	call, ok := launch.Invocation.(*prepared)
	if !ok {
		return errors.New("headless requires its prepared invocation")
	}
	sessions, err := scope.Get(session.StoreKey)
	if err != nil {
		return err
	}
	provider, err := scope.Get(agent.FactoryKey)
	if err != nil {
		return err
	}
	factory, err := provider.Bind(scope)
	if err != nil {
		return err
	}
	llmRuntime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	binding := events.Bind(scope)
	runner := &runner{Sessions: sessions, Factory: factory, outcomes: call.outcomes, Stdout: launch.Stdout, Stderr: launch.Stderr, CWD: call.cwd, BeginShutdown: launch.Control.BeginShutdown,
		Observe: func(handler func(context.Context, contracts.AssistantStreamObservation) error) (func(), error) {
			return binding.On(gordisbridge.AssistantStreamTopic, handler, events.Global())
		},
		ObserveEvents: func(handler func(context.Context, gordisbridge.SessionObservation) error) (func(), error) {
			return binding.On(gordisbridge.SessionAppendedTopic, handler, events.Global())
		},
	}
	if p.DisableEventHints {
		runner.ObserveEvents = nil
	}
	return call.Start(scope, launch, "headless invocation", func(ctx context.Context) error {
		model, err := llmRuntime.ResolveModel(p.Model)
		if err != nil {
			return err
		}
		runner.Config.Loop.LLM, err = model.LLMConfig()
		if err != nil {
			return err
		}
		resolved, err := llmRuntime.PrepareCall(ctx, runner.Config.Loop.LLM)
		if err != nil {
			return fmt.Errorf("resolve model: %w", err)
		}
		resolved.Close()
		return nil
	}, func(ctx context.Context, begin func(int)) launchcontract.Completion {
		result, runErr := runner.Run(ctx, call.request)
		code := result.ExitCode()
		if runErr != nil {
			code = 1
			if call.request.JSON {
				runErr = errors.Join(runErr, writeJSONError(launch.Stdout, runErr))
			}
			fmt.Fprintf(launch.Stderr, "qi: %s\n", runErr)
		}
		begin(code)
		return launchcontract.Completion{Code: code, CleanupError: result.CleanupError}
	})
}
