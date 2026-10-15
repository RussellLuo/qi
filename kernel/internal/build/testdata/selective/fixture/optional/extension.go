package optional

import (
	"context"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "fixture/optional", New: func() gordis.Plugin { return new(Plugin) }}
}
func (*Plugin) Activate(context.Context, *gordis.Scope) error { return nil }
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{Plugin: new(Plugin), ProfileVisible: true}}}
}
