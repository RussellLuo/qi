// Package greet provides the shared greeting Plugin for local and process deployment.
package greet

import (
	"context"
	"encoding/json"

	"github.com/RussellLuo/gordis"
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
