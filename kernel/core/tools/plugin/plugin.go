package plugin

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const PluginID = "qi/tools"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime := tools.NewRuntime()
	if err := scope.Defer("close Tool Runtime", func(context.Context) error {
		runtime.Close()
		return nil
	}); err != nil {
		return err
	}

	return scope.Provide(tools.RuntimeKey, runtime)
}
