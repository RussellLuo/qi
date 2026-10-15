package plugin

import (
	"encoding/json"

	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension returns pure metadata without activating file access or tools.
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{
		Plugin: new(Plugin), ProfileVisible: true,
		Descriptor: profile.PluginDescriptor{
			Title: "File tools",
			ConfigSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"read_limit":{"type":"integer","minimum":1,"default":2000},
					"read_max_line_length":{"type":"integer","minimum":1,"default":2000},
					"read_max_bytes":{"type":"integer","minimum":1,"default":51200}
				},
				"additionalProperties":false
			}`),
			UpdatePolicy:  profile.UpdateReplaceGeneration,
			Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		},
	}}}
}
