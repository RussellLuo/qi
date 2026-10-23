package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// HostOptions controls trusted process settings; business configuration comes from the Profile.
type HostOptions struct {
	Imports                   []rpc.Binding
	Args, Env                 []string
	Limits                    Limits
	StartupTimeout, ExitGrace time.Duration
}

func Extension() application.Extension {
	return HostExtension("qi/tools/process", HostOptions{})
}

func HostExtension(id string, options HostOptions) application.Extension {
	options = options.defaults()
	if _, err := rpc.DescribeBoundary(toolBoundary(options.Imports)); err != nil {
		panic(err)
	}
	required := []profile.CapabilityRef{{ID: tools.RuntimeKey.Name()}}
	for _, binding := range options.Imports {
		required = append(required, profile.CapabilityRef{ID: binding.Spec().Name()})
	}
	return application.Extension{
		Registrations: []registration.PluginRegistration{{
			Plugin:         &loaderPlugin{id: id, options: options},
			ProfileVisible: true,
			ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
				if business == nil {
					business = map[string]any{}
				}
				raw, err := json.Marshal(business)
				if err != nil {
					return nil, err
				}
				return map[string]any{"command": command, "config": json.RawMessage(raw)}, nil
			},
			Descriptor: profile.PluginDescriptor{
				ID:            id,
				Title:         "Process tools",
				Requires:      required,
				UpdatePolicy:  profile.UpdateReplaceGeneration,
				Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
				ConfigSchema: json.RawMessage(`{
					"type":"object",
					"properties":{"command":{"type":"string","minLength":1},"identity":{"type":"object","properties":{"id":{"type":"string","minLength":1},"version":{"type":"string","minLength":1}},"required":["id","version"],"additionalProperties":false},"config":{"type":"object"}},
					"required":["command"],
					"additionalProperties":false
				}`),
			},
		}},
	}
}

func toolBoundary(imports []rpc.Binding) rpc.Boundary {
	return rpc.Boundary{Imports: imports, Exports: []rpc.Binding{ProviderBinding}}
}

type loaderPlugin struct {
	id       string
	options  HostOptions
	command  string
	identity rpc.Identity
	config   json.RawMessage
	session  *rpc.Session
}

func (plugin *loaderPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: plugin.id,
		New: func() gordis.Plugin {
			return &loaderPlugin{id: plugin.id, options: plugin.options.defaults()}
		},
		Requires: append([]gordis.ServiceSpec{tools.RuntimeKey.Spec()}, rpc.RequiredServices(plugin.options.Imports)...),
	}
}

func (plugin *loaderPlugin) UnmarshalJSON(raw []byte) error {
	var configuration struct {
		Command  string `json:"command"`
		Identity *struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"identity"`
		Config json.RawMessage `json:"config"`
	}
	if err := decodeStrict(raw, &configuration); err != nil {
		return err
	}
	if strings.TrimSpace(configuration.Command) == "" {
		return errors.New("toolprocess: command is required")
	}
	config := bytes.TrimSpace(configuration.Config)
	if len(config) == 0 {
		config = []byte(`{}`)
	}
	if config[0] != '{' || !json.Valid(config) {
		return errors.New("toolprocess: config must be a JSON object")
	}
	plugin.identity = rpc.Identity{}
	if configuration.Identity != nil {
		plugin.identity = rpc.Identity{ID: configuration.Identity.ID, Version: configuration.Identity.Version, Protocol: rpc.Protocol}
		if err := validateIdentity(plugin.identity); err != nil {
			return err
		}
	}
	plugin.command = configuration.Command
	plugin.config = append(json.RawMessage(nil), config...)
	return nil
}

func executablePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), path), nil
}

func (plugin *loaderPlugin) artifact() (rpc.Command, error) {
	path, err := executablePath(plugin.command)
	if err != nil {
		return rpc.Command{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return rpc.Command{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return rpc.Command{}, errors.New("toolprocess: command is not executable")
	}
	return rpc.Command{Path: path}, nil
}
func (plugin *loaderPlugin) Validate() error { _, err := plugin.artifact(); return err }

func (plugin *loaderPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	// Recheck the artifact at activation, even when preparation already validated it.
	command, err := plugin.artifact()
	if err != nil {
		return err
	}
	command.Args = plugin.options.Args
	command.Env = plugin.options.Env
	plugin.session, err = rpc.Attach(ctx, scope, rpc.AttachOptions{
		Command:       command,
		Identity:      plugin.identity,
		Config:        plugin.config,
		ChildBoundary: toolBoundary(plugin.options.Imports),
		Accept: func(ctx context.Context, session *rpc.Session) error {
			return registerRemoteTools(ctx, scope, runtime, session, plugin.options)
		},
		Limits: rpc.Limits{
			MaxMessage:     plugin.options.Limits.MaxMessage,
			StartupTimeout: plugin.options.StartupTimeout,
			StopTimeout:    plugin.options.ExitGrace,
		},
	})
	return err
}

// Process exposes diagnostics; cleanup fields are read after Unmount/Shutdown.
func (plugin *loaderPlugin) Process() *rpc.Session { return plugin.session }
