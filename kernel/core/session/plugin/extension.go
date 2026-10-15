package plugin

import (
	"encoding/json"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension returns pure trusted metadata; it does not create runtime instances.
func Extension() applicationapi.Extension {
	schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	hostCompatibility := profileapi.Compatibility{HostAPI: profileapi.HostAPIVersion}

	return applicationapi.Extension{
		Registrations: []registrationapi.PluginRegistration{
			{
				Plugin:         new(Plugin),
				ProfileVisible: true,
				Descriptor: profileapi.PluginDescriptor{
					Title:         "Session Runtime",
					ConfigSchema:  schema,
					UpdatePolicy:  profileapi.UpdateImmutable,
					Compatibility: hostCompatibility,
				},
			},
		},
	}
}
