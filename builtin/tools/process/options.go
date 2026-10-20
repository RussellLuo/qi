package process

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type Limits struct{ MaxMessage, MaxPending int }
type ServerOptions struct {
	ID, Version string
	Limits      Limits
	Imports     []rpc.Binding
	Config      rpc.ConfigSpec
}

func (options HostOptions) defaults() HostOptions {
	options.Imports = append([]rpc.Binding(nil), options.Imports...)
	options.Args = append([]string(nil), options.Args...)
	options.Env = append([]string{}, options.Env...)
	options.Limits = options.Limits.defaults()
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = 3 * time.Second
	}
	if options.ExitGrace <= 0 {
		options.ExitGrace = 3 * time.Second
	}
	return options
}

func (limits Limits) defaults() Limits {
	if limits.MaxMessage <= 0 {
		limits.MaxMessage = 256 * 1024
	}
	if limits.MaxPending <= 0 {
		limits.MaxPending = 16
	}
	return limits
}

func pluginIdentity(id, identityID, version string) (rpc.Identity, error) {
	if identityID == "" {
		identityID = id
	}
	if version == "" {
		version = "dev"
	}
	identity := rpc.Identity{ID: identityID, Version: version, Protocol: rpc.Protocol}
	return identity, validateIdentity(identity)
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Tool program identities preserve readable Plugin IDs, including Unicode.
func validateIdentity(identity rpc.Identity) error {
	if identity.ID == "" || len(identity.ID) > 512 || !utf8.ValidString(identity.ID) {
		return errors.New("toolprocess: invalid program identity")
	}
	for _, r := range identity.ID {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("toolprocess: invalid program identity")
		}
	}
	if !versionPattern.MatchString(identity.Version) {
		return errors.New("toolprocess: invalid program version")
	}
	return nil
}

func inspectPlugin(plugin gordis.Plugin) (spec gordis.PluginSpec, err error) {
	defer func() {
		if recover() != nil {
			spec = gordis.PluginSpec{}
			err = errors.New("toolprocess: invalid Plugin Spec")
		}
	}()
	if plugin == nil || (reflect.ValueOf(plugin).Kind() == reflect.Pointer && reflect.ValueOf(plugin).IsNil()) {
		return spec, errors.New("toolprocess: nil plugin")
	}
	spec = plugin.Spec()
	if spec.ID == "" || spec.ID == providerID || spec.New == nil {
		return spec, errors.New("toolprocess: plugin requires ID and New")
	}
	if _, ok := plugin.(interface {
		Activate(context.Context, *gordis.Scope) error
	}); !ok {
		return spec, fmt.Errorf("toolprocess: plugin %q requires Activate", spec.ID)
	}
	requiresRuntime := false
	for _, requirement := range spec.Requires {
		if requirement == tools.RuntimeKey.Spec() {
			requiresRuntime = true
		}
	}
	if !requiresRuntime || len(spec.Provides) != 0 {
		return spec, fmt.Errorf("toolprocess: plugin %q must require Runtime and provide no services", spec.ID)
	}
	spec.Requires = append([]gordis.ServiceSpec(nil), spec.Requires...)
	return spec, nil
}

func checkRequirements(spec gordis.PluginSpec, imports []rpc.Binding) error {
	allowed := map[gordis.ServiceSpec]bool{tools.RuntimeKey.Spec(): true}
	for _, binding := range imports {
		allowed[binding.Spec()] = true
	}
	for _, requirement := range spec.Requires {
		if !allowed[requirement] {
			return fmt.Errorf("toolprocess: plugin %q requires undeclared service %q", spec.ID, requirement.Name())
		}
	}
	return nil
}
