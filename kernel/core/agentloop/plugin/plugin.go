package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const PluginID = "qi/agent-loop"

type Plugin struct {
	ConfigRequires []gordis.ServiceSpec                              `json:"-"`
	ConfigureAgent func(*gordis.Scope) (agent.ConfigComposer, error) `json:"-"`

	// WrapDriver is a local composition seam; nil preserves the default Loop.
	WrapDriver func(agentloop.Driver) agentloop.Driver `json:"-"`
}

func (p *Plugin) Spec() gordis.PluginSpec {
	spec := gordis.PluginSpec{
		ID: PluginID,
		Requires: []gordis.ServiceSpec{
			events.BusKey.Spec(),
			session.StoreKey.Spec(),
			llm.RuntimeKey.Spec(),
			prompt.RuntimeKey.Spec(),
			tools.RuntimeKey.Spec(),
		},
		Provides: []gordis.ServiceSpec{
			agent.RegistryKey.Spec(),
			agent.InitializerKey.Spec(),
			agent.ObserverKey.Spec(),
			agentmiddleware.RuntimeKey.Spec(),
			agent.FactoryKey.Spec(),
		},
		New: func() gordis.Plugin {
			return &Plugin{
				ConfigRequires: append([]gordis.ServiceSpec(nil), p.ConfigRequires...),
				ConfigureAgent: p.ConfigureAgent,
				WrapDriver:     p.WrapDriver,
			}
		},
	}
	spec.Requires = append(spec.Requires, p.ConfigRequires...)

	return spec
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	sessions, err := scope.Get(session.StoreKey)
	if err != nil {
		return err
	}

	llmRuntime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}

	prompts, err := scope.Get(prompt.RuntimeKey)
	if err != nil {
		return err
	}

	toolRuntime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}

	// Resolve the composition before allocating lifecycle workers, so a failed
	// adapter binding cannot leave unowned core resources behind.
	var compose agent.ConfigComposer
	if p.ConfigureAgent != nil {
		compose, err = p.ConfigureAgent(scope)
		if err != nil {
			return err
		}
	}

	dispatcher, err := gordisbridge.NewEventDispatcher(scope, 0)
	if err != nil {
		return err
	}

	middleware := agentmiddleware.NewRuntime()
	loop := agentloop.New(runprep.Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: middleware,
	}, dispatcher)
	initializers := agent.NewInitializers()
	observed := agent.NewAgentEvents(dispatcher)
	var driver agentloop.Driver = loop
	if p.WrapDriver != nil {
		driver = p.WrapDriver(driver)
	}

	registry := agent.NewRegistry()
	if err := scope.OnStop("close Agent observation", func(context.Context) error {
		initializers.Close()
		return observed.Close(context.Background())
	}); err != nil {
		return err
	}

	// Registered after observation cleanup so factories drain first (OnStop is
	// reverse ordered), while shared registries and contribution owners live.
	_, ownedStore := sessions.(session.ResourceStore)
	factories, err := gordisbridge.NewAgentFactoryService(scope, agent.FactoryOptions{AttachOnly: !ownedStore, Registry: registry, Store: sessions, Driver: driver, Compose: compose, Initialize: initializers, Observe: observed})
	if err != nil {
		return err
	}
	if err := scope.Defer("close Agent middleware", func(context.Context) error {
		middleware.Close()
		return nil
	}); err != nil {
		return err
	}
	if err := scope.Provide(agentmiddleware.RuntimeKey, middleware); err != nil {
		return err
	}
	if err := scope.Provide(agent.FactoryKey, factories); err != nil {
		return err
	}

	if err := scope.Provide(agent.RegistryKey, registry); err != nil {
		return err
	}
	if err := scope.Provide(agent.InitializerKey, initializers); err != nil {
		return err
	}
	if err := scope.Provide(agent.ObserverKey, observed); err != nil {
		return err
	}
	return nil
}
