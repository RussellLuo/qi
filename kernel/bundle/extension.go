// Package bundle describes available baseline types and Bundles, without
// selecting instances or starting services.
package bundle

import (
	_ "embed"
	"encoding/json"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"

	"github.com/RussellLuo/gordis/events"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	llmplugin "github.com/RussellLuo/qi/kernel/core/llm/plugin"
	promptruntime "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
	sessionruntime "github.com/RussellLuo/qi/kernel/core/session/plugin"
	toolsruntime "github.com/RussellLuo/qi/kernel/core/tools/plugin"
)

//go:embed base.yaml
var base []byte

// Extension describes foundational plugin types. The caller's Profile still
// selects which instances to mount; this factory starts no runtime services.
func Extension() applicationapi.Extension {
	hostCompatibility := profileapi.Compatibility{HostAPI: profileapi.HostAPIVersion}
	extension := applicationapi.Extension{
		Registrations: []registrationapi.PluginRegistration{
			{
				Plugin:         new(events.Plugin),
				ProfileVisible: true,
				Descriptor: profileapi.PluginDescriptor{
					Title:         "Event bus",
					ConfigSchema:  json.RawMessage(`{"type":"object","properties":{"maxParallel":{"type":"integer","minimum":0}},"additionalProperties":false}`),
					UpdatePolicy:  profileapi.UpdateImmutable,
					Compatibility: hostCompatibility,
				},
			},
		},
	}
	for _, capability := range []applicationapi.Extension{
		llmplugin.Extension(), promptruntime.Extension(), toolsruntime.Extension(), agentplugin.Extension(),
		sessionruntime.Extension(),
	} {
		extension.Registrations = append(extension.Registrations, capability.Registrations...)
	}
	definition, err := profileapi.ParseBundleDefinition(base)
	if err != nil {
		panic(err)
	}
	extension.Bundles = []profileapi.BundleDefinition{definition}

	return extension
}
