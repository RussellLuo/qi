package process

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
)

type fixtureOptions struct {
	Command                   string
	ID, Version               string
	Args, Env                 []string
	Limits                    Limits
	StartupTimeout, ExitGrace time.Duration
}

// fixtureLoader captures each real loader instance and wraps fixture business
// configuration in the deployment envelope expected by the generic loader.
type fixtureLoader struct {
	*loaderPlugin
	spec   gordis.PluginSpec
	client *rpc.Session
}

func (loader *fixtureLoader) Spec() gordis.PluginSpec { return loader.spec }
func (loader *fixtureLoader) UnmarshalJSON(business []byte) error {
	raw, err := json.Marshal(struct {
		Command string          `json:"command"`
		Config  json.RawMessage `json:"config"`
	}{loader.command, business})
	if err != nil {
		return err
	}
	expected := loader.identity
	err = loader.loaderPlugin.UnmarshalJSON(raw)
	loader.identity = expected
	return err
}
func (loader *fixtureLoader) Activate(ctx context.Context, scope *gordis.Scope) error {
	err := loader.loaderPlugin.Activate(ctx, scope)
	loader.client = loader.session
	return err
}

func newFixtureLoader(t *testing.T, options fixtureOptions) (gordis.Plugin, error) {
	t.Helper()
	identity, err := pluginIdentity(fixture.ID, options.ID, options.Version)
	if err != nil {
		return nil, err
	}
	prototype := HostExtension(fixture.ID, HostOptions{
		Args: options.Args, Env: options.Env, Limits: options.Limits,
		StartupTimeout: options.StartupTimeout, ExitGrace: options.ExitGrace,
	}).Registrations[0].Plugin.(*loaderPlugin)
	spec := prototype.Spec()
	factory := spec.New
	spec.New = func() gordis.Plugin {
		plugin := factory().(*loaderPlugin)
		plugin.command = options.Command
		plugin.identity = rpc.Identity{ID: identity.ID, Version: identity.Version, Protocol: rpc.Protocol}
		return &fixtureLoader{loaderPlugin: plugin, spec: spec}
	}
	return &fixtureLoader{loaderPlugin: prototype, spec: spec}, nil
}
