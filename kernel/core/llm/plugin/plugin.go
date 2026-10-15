package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/llm"
)

const PluginID = "qi/llm"

type Plugin struct {
	Ready func(*llm.Runtime) `json:"-"`
}

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{llm.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return &Plugin{Ready: p.Ready} },
	}
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime := llm.NewRuntime()
	if err := scope.Defer("close LLM Runtime", func(context.Context) error {
		runtime.Close()
		return nil
	}); err != nil {
		return err
	}
	if err := scope.Provide(llm.RuntimeKey, runtime); err != nil {
		return err
	}
	if p.Ready != nil {
		p.Ready(runtime)
	}

	return nil
}
