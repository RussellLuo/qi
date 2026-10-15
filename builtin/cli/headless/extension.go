package headless

import (
	"encoding/json"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension returns pure trusted metadata; it does not create runtime instances.
func Extension() applicationapi.Extension {
	schema := json.RawMessage(`{"type":"object","properties":{"model":{"type":"string","minLength":1,"pattern":"^[A-Za-z0-9][A-Za-z0-9._-]*$"}},"additionalProperties":false}`)
	hostCompatibility := profileapi.Compatibility{HostAPI: profileapi.HostAPIVersion}
	return applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{{Plugin: new(Plugin), ProfileVisible: true, Invocation: Parser{}, Completes: true, Descriptor: profileapi.PluginDescriptor{
		Title: "Headless invocation", ConfigSchema: schema, RequiresModels: true, UpdatePolicy: profileapi.UpdateImmutable, Compatibility: hostCompatibility,
	}}}}
}
