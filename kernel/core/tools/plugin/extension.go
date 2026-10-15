package plugin

import (
	"encoding/json"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension describes the available type without activating runtime services.
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{
		Plugin: new(Plugin), ProfileVisible: true,
		Descriptor: profile.PluginDescriptor{
			Title:         "Tool runtime",
			ConfigSchema:  json.RawMessage(`{"type":"object","additionalProperties":false}`),
			UpdatePolicy:  profile.UpdateImmutable,
			Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		},
	}}}
}
