// Package greettool publishes a greeting tool for Profile composition.
package greettool

import (
	"context"
	"encoding/json"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const PluginID = "example/tool-plugin"

type Plugin struct{}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (*Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	// Scope owns the contribution: stopping the plugin withdraws greet.
	_, err = runtime.Bind(scope).Register(tools.Definition{
		Name:        "greet",
		Description: "Greet a user by name. Returns Hello, {name}!",
		Parameters: tools.JSONSchema(`{
			"type": "object",
			"properties": {"name": {"type": "string", "minLength": 1}},
			"required": ["name"],
			"additionalProperties": false
		}`),
		Output:      tools.JSONSchema(`{"type":"string"}`),
		FailureMode: tools.ToolFailureResult,
		Execute: tools.JSONTool(func(_ context.Context, execution tools.Execution) (json.RawMessage, error) {
			// The Tool Runtime validates arguments against Parameters first.
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(execution.Arguments, &args); err != nil {
				return nil, err
			}
			return json.Marshal("Hello, " + args.Name + "!")
		}),
	})
	return err
}

// Extension publishes the plugin type. The Profile selects its instance.
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{
		Plugin: new(Plugin), ProfileVisible: true,
		Descriptor: profile.PluginDescriptor{
			Title:         "Greeting tool",
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			UpdatePolicy:  profile.UpdateImmutable,
			Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		},
	}}}
}
