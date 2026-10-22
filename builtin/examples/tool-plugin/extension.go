// Package greettool publishes the shared greeting Plugin for local Profile composition.
package greettool

import (
	"encoding/json"

	greet "github.com/RussellLuo/qi/builtin/examples/tool-plugin/plugin"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension publishes the plugin type. The Profile selects its instance.
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{
		Plugin: new(greet.Plugin), ProfileVisible: true,
		Descriptor: profile.PluginDescriptor{
			Title:         "Greeting tool",
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			UpdatePolicy:  profile.UpdateImmutable,
			Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		},
	}}}
}
