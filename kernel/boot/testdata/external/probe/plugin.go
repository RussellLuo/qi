// Package probe is an external finite consumer of public launch contracts.
package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

type Plugin struct{ call *prepared }

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "fixture/cli", Requires: []gordis.ServiceSpec{launch.LaunchContextKey.Spec()}, New: func() gordis.Plugin { return &Plugin{call: p.call} }}
}
func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	call, err := scope.Get(launch.LaunchContextKey)
	if err != nil {
		return err
	}
	if call.Invocation != p.call || p.call == nil {
		return errors.New("missing prepared invocation")
	}
	return p.call.Start(scope, call, "external task", func(context.Context) error { return nil }, func(_ context.Context, begin func(int)) launch.Completion {
		_, err := fmt.Fprintln(call.Stdout, strings.Join(p.call.args, " "))
		begin(0)
		code := 0
		if err != nil {
			code = 1
		}
		return launch.Completion{Code: code, CleanupError: err}
	})
}

type parser struct{}

func (parser) Help() string { return "fixture consumer" }
func (parser) Parse(args []string, _ bool) (launch.Invocation, error) {
	return invocation{append([]string(nil), args...)}, nil
}

type invocation struct{ args []string }

func (i invocation) HelpRequested() bool { return len(i.args) == 1 && i.args[0] == "--help" }
func (i invocation) Prepare(context.Context, launch.Input) (launch.Prepared, error) {
	return &prepared{args: i.args}, nil
}

type prepared struct {
	launch.Task
	args []string
}

func (p *prepared) Bind(plugins []gordis.Plugin) ([]gordis.Plugin, error) {
	for i, v := range plugins {
		if _, ok := v.(*Plugin); ok {
			plugins[i] = &Plugin{call: p}
		}
	}
	return plugins, nil
}
func Extension() application.Extension {
	return application.Extension{Registrations: []registration.PluginRegistration{{Plugin: new(Plugin), ProfileVisible: true, Invocation: parser{}, Completes: true, Descriptor: profile.PluginDescriptor{Title: "External CLI", UpdatePolicy: profile.UpdateImmutable, ConfigSchema: []byte(`{"type":"object","additionalProperties":false}`)}}}, Bundles: []profile.BundleDefinition{{ID: "fixture", Instances: []profile.InstanceSpec{{ID: "cli", Plugin: "fixture/cli"}}}}}
}
