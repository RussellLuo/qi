package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RussellLuo/gordis"
	gordisprocess "github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// NewAdapter reads declarations only. It neither constructs the business Plugin
// nor accesses the executable; each New creates an independent config instance.
func NewAdapter(prototype gordis.Plugin, options Options) (gordis.Plugin, error) {
	spec, err := inspectPlugin(prototype)
	if err != nil {
		return nil, err
	}
	if spec.ID == providerID {
		return nil, errors.New("toolprocess: plugin ID conflicts with internal Runtime provider")
	}
	if strings.TrimSpace(options.Command) == "" {
		return nil, errors.New("toolprocess: Command is required")
	}
	identity, err := pluginIdentity(spec.ID, options.PackageID, options.Version)
	if err != nil {
		return nil, err
	}
	o := options.defaults()
	spec.New = func() gordis.Plugin {
		return &adapterPlugin{spec: spec, options: o.defaults(), identity: identity}
	}
	return spec.New(), nil
}

type adapterPlugin struct {
	spec     gordis.PluginSpec
	options  Options
	identity gordisprocess.Identity
	config   json.RawMessage
	client   *gordisprocess.Client
}

func (p *adapterPlugin) Spec() gordis.PluginSpec {
	spec := p.spec
	spec.Requires = append([]gordis.ServiceSpec(nil), spec.Requires...)
	spec.Provides = append([]gordis.ServiceSpec(nil), spec.Provides...)
	return spec
}

// The host decodes business JSON, never deployment Options. Business field and
// Validate checks belong to the ordinary Plugin's preparation in the child.
func (p *adapterPlugin) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return errors.New("toolprocess: configuration must be a JSON object")
	}
	p.config = append(json.RawMessage(nil), raw...)
	return nil
}

func executablePath(command string) (string, error) {
	if filepath.IsAbs(command) {
		return command, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), command), nil
}

func (p *adapterPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	// Own acquisition before starting: activation rollback must also close a
	// child that failed its handshake or whose definitions could not publish.
	if err := scope.Defer("close Tool Plugin process", func(closeCtx context.Context) error {
		if p.client == nil {
			return nil
		}
		if err := p.client.Close(closeCtx); err != nil {
			return errors.New("toolprocess: process cleanup failed")
		}
		return nil
	}); err != nil {
		return err
	}
	path, err := executablePath(p.options.Command)
	if err != nil {
		return err
	}
	config := append(json.RawMessage(nil), p.config...)
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	p.client, err = gordisprocess.Start(ctx, gordisprocess.Options{
		Path: path, Args: p.options.Args, Env: p.options.Env,
		Identity: p.identity, Config: config, Instance: scope.ID(), Generation: scope.Generation(),
		Limits: p.options.Limits, StartupTimeout: p.options.StartupTimeout, ExitGrace: p.options.ExitGrace,
	})
	if err != nil {
		return errors.New("toolprocess: process startup failed")
	}
	wire, err := NewClient(p.client)
	if err != nil {
		return err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, p.options.StartupTimeout)
	defer cancel()
	metadata, err := wire.Describe(discoveryCtx)
	if err != nil {
		return errors.New("toolprocess: Describe failed")
	}
	if metadata.ID != p.identity.Package || metadata.Version != p.identity.Version ||
		len(metadata.Capabilities) != 1 || metadata.Capabilities[0] != CapabilityTools {
		return errors.New("toolprocess: incompatible metadata")
	}
	remoteTools, err := wire.ListTools(discoveryCtx)
	if err != nil {
		return errors.New("toolprocess: ListTools failed")
	}
	var sequence atomic.Uint64
	definitions := make([]tools.Definition, 0, len(remoteTools))
	for _, remote := range remoteTools {
		executeJSON := tools.JSONTool(func(callCtx context.Context, execution tools.Execution) (json.RawMessage, error) {
			base := fmt.Sprintf("%s/%d/%d", scope.ID(), scope.Generation(), sequence.Add(1))
			return wire.Call(callCtx, CallRequest{
				Tool: remote.Name, Arguments: execution.Arguments,
				Invocation: Invocation{RequestID: base + "/request", ToolCallID: execution.Call.ID.String(), InvocationID: base + "/invocation"},
			})
		})
		definitions = append(definitions, tools.Definition{
			Name: remote.Name, Description: remote.Description,
			Parameters: tools.JSONSchema(remote.Parameters), Output: tools.JSONSchema(remote.Output),
			Timeout: time.Duration(remote.TimeoutMillis) * time.Millisecond, FailureMode: tools.FailureMode(remote.FailureMode),
			Execute: func(callCtx context.Context, execution tools.Execution) (tools.ExecutionResult, error) {
				result, err := executeJSON(callCtx, execution)
				var callErr *CallError
				if errors.As(err, &callErr) && callErr.Code != CodeToolError && callErr.Code != CodeCancelled && callErr.Code != CodeTimeout {
					// Keep protocol/transport failures distinct from ordinary business
					// errors. Runtime still applies the definition's FailureMode.
					return tools.FailureResult(callErr.Code, callErr), nil
				}
				return result, err
			},
		})
	}
	if _, err := runtime.Bind(scope).RegisterBatch(definitions); err != nil {
		return fmt.Errorf("toolprocess: register Tools: %w", err)
	}
	return scope.AfterReady("watch Tool Plugin process", func(watchCtx context.Context) error {
		if err := p.client.Watch(watchCtx); err != nil && watchCtx.Err() == nil {
			return errors.New("toolprocess: process failed")
		}
		return watchCtx.Err()
	})
}
