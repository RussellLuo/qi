package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/prompt"
)

const PluginID = "qi/prompt"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{prompt.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime := prompt.NewRuntime()
	if err := scope.Defer("close Prompt Runtime", func(context.Context) error {
		runtime.Close()
		return nil
	}); err != nil {
		return err
	}

	return scope.Provide(prompt.RuntimeKey, runtime)
}
