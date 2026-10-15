// Boot-local process fixture. Only task and lifecycle faults are test code.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

func mark(value string) {
	f, err := os.OpenFile("effects", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(f, value); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
}

type plugin struct {
	Mode string `json:"mode"`
}

func (*plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "fixture.task", New: func() gordis.Plugin { return new(plugin) }, Requires: []gordis.ServiceSpec{launch.LaunchContextKey.Spec()}}
}
func (p *plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("fault disposer", func(context.Context) error {
		mark("disposing")
		if p.Mode == "cleanup-hang" || p.Mode == "startup-hang" {
			select {}
		}
		if p.Mode == "cleanup-error" || p.Mode == "startup-error" {
			return errors.New("fixture cleanup failure")
		}
		mark("disposed")
		return nil
	}); err != nil {
		return err
	}
	if p.Mode == "startup-error" || p.Mode == "startup-hang" {
		return errors.New("fixture startup failure")
	}
	call, err := scope.Get(launch.LaunchContextKey)
	if err != nil {
		return err
	}
	prepared := call.Invocation.(*prepared)
	return prepared.Start(scope, call, "process task", func(context.Context) error { return nil }, func(ctx context.Context, begin func(int)) launch.Completion {
		mark("started")
		if p.Mode == "wait" || p.Mode == "drain" {
			<-ctx.Done()
			begin(1)
			mark("cancelled")
			if p.Mode == "drain" {
				time.Sleep(600 * time.Millisecond)
			}
			mark("drained")
			return launch.Completion{Code: 1}
		}
		_, err := fmt.Fprintln(call.Stdout, "done")
		code := 0
		if err != nil {
			code = 1
			fmt.Fprintln(call.Stderr, err)
		}
		begin(code)
		return launch.Completion{Code: code}
	})
}

type parser struct{}

func (parser) Help() string { return "fixture help\n" }
func (parser) Parse(args []string, _ bool) (launch.Invocation, error) {
	return invocation{help: len(args) == 1 && args[0] == "--help", hangInput: len(args) == 1 && args[0] == "input-hang"}, nil
}

type invocation struct{ help, hangInput bool }

func (i invocation) HelpRequested() bool { return i.help }
func (i invocation) Prepare(_ context.Context, input launch.Input) (launch.Prepared, error) {
	if i.hangInput {
		// Deliberately uncooperative borrowed input proves the process supervisor
		// also bounds the pre-Host window. No plugin has been mounted yet.
		mark("preparing")
		if _, err := io.ReadAll(input.Stdin); err != nil {
			return nil, err
		}
	}
	return new(prepared), nil
}

type prepared struct{ launch.Task }

func (*prepared) Bind(plugins []gordis.Plugin) ([]gordis.Plugin, error) { return plugins, nil }

func main() {
	app, err := application.Compose(application.Extension{Registrations: []registration.PluginRegistration{{Plugin: new(plugin), ProfileVisible: true, Invocation: parser{}, Completes: true}}})
	if err != nil {
		panic(err)
	}
	os.Exit(boot.Run(os.Args[1:], launch.Input{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, app))
}
