// Package greettool demonstrates an ordinary Tool Plugin running in a child.
package greettool

import (
	"context"
	"encoding/json"

	"github.com/RussellLuo/gordis"
	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const PluginID = "example/tool-process"

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
	registered, err := toolprocess.Registration(
		new(Plugin),
		profile.PluginDescriptor{
			Title:         "Process greeting tool",
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			UpdatePolicy:  profile.UpdateImmutable,
			Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		},
		toolprocess.Options{Command: "greet"},
	)
	if err != nil {
		// Invalid static declaration; activation returns runtime errors.
		panic(err)
	}

	return application.Extension{Registrations: []registration.PluginRegistration{registered}}
}
