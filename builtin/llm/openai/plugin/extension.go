package plugin

import (
	"encoding/json"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"

	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

// Extension returns pure trusted metadata; it does not create runtime instances.
func Extension() applicationapi.Extension {
	hostCompatibility := profileapi.Compatibility{HostAPI: profileapi.HostAPIVersion}
	return applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{{Plugin: new(Plugin), ProfileVisible: true, Descriptor: profileapi.PluginDescriptor{
		Title: "OpenAI-compatible LLM", ConfigSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"route":{"type":"string"},"provider":{"type":"string"},"endpoint":{"type":"string"},
					"api_key_env":{"type":"string"},"headers":{"type":"object","additionalProperties":{"type":"string"}},
					"context_window":{"type":"integer","minimum":0},"defaults":{"type":"object"},"parameters":{"type":"object"},"catalog":` + modelcatalog.EntrySchema + `,
					"max_tokens_parameter":{"type":"string","enum":["max_completion_tokens","max_tokens"]},
					"reasoning_history":{"type":"string","enum":["omit","reasoning_content"]},
					"timeout_seconds":{"type":"integer","minimum":0}
				},
				"required":["endpoint"],"additionalProperties":false
			}`), UpdatePolicy: profileapi.UpdateReplaceGeneration, Compatibility: hostCompatibility,
	}}}}
}
