package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const providerID = "qi/tools/process/runtime"
const toolInstanceID = "tool"

// ConfigForTool maps program configuration to the business tool instance.
// value must encode as a JSON object; gordis-rpc validates it during initialization.
func ConfigForTool(value any) map[string]any {
	return map[string]any{toolInstanceID: value}
}

type runtimeProvider struct{ capacity int }

func (plugin *runtimeProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       providerID,
		New:      func() gordis.Plugin { return &runtimeProvider{capacity: plugin.capacity} },
		Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec(), ProviderKey.Spec()},
	}
}

func (plugin *runtimeProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	export := tools.NewExport()
	if err := scope.Defer("runtime", func(context.Context) error { export.Close(); return nil }); err != nil {
		export.Close()
		return err
	}
	if err := scope.AfterReady("tool export retirement", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return nil
		case <-export.Done():
			return export.Err()
		}
	}); err != nil {
		return err
	}
	if err := scope.Provide(tools.RuntimeKey, export.Runtime()); err != nil {
		return err
	}
	return scope.Provide(ProviderKey, Provider(&exporter{export: export, pending: make(chan struct{}, plugin.capacity)}))
}

// ListTools seals only after Unit activation (ProviderBinding.Validate). Each
// business program exposes its actual catalog inside its own RPC Session.
type exporter struct {
	export  *tools.Export
	pending chan struct{}
}

func (p *exporter) ListTools(ctx context.Context) ([]Tool, error) {
	if err := p.export.Seal(); err != nil {
		return nil, err
	}
	ds, err := p.export.Describe(ctx)
	if err != nil {
		return nil, err
	}
	return describeTools(ds)
}
func (p *exporter) CallTool(ctx context.Context, request CallRequest) (response CallResponse, err error) {
	select {
	case p.pending <- struct{}{}:
		defer func() { <-p.pending }()
	default:
		return CallResponse{Error: &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity"}}, nil
	}
	// Recover at the raw worker boundary after Export has released its owner lease.
	defer func() {
		if recover() != nil {
			response = CallResponse{Error: &CallError{Code: CodePanic, Message: "Tool execution panicked"}}
			err = nil
		}
	}()
	if ValidateCall(request) != nil {
		return CallResponse{Error: invalidResponse("invalid Tool call")}, nil
	}
	if p.export.Err() != nil {
		return CallResponse{Error: &CallError{Code: CodeRetired, Message: "Tool export retired"}}, nil
	}
	result, executeErr := p.export.Execute(ctx, decodeExecution(request))
	wire, codecErr := encodeResult(result)
	if codecErr != nil {
		return CallResponse{Error: invalidResponse("unencodable Tool result")}, nil
	}
	response.Result = &wire
	if executeErr != nil {
		code := CodeToolError
		if errors.Is(executeErr, context.Canceled) {
			code = CodeCancelled
		}
		if errors.Is(executeErr, context.DeadlineExceeded) {
			code = CodeTimeout
		}
		if len(executeErr.Error()) > MaxErrorMessage {
			return CallResponse{Error: invalidResponse("oversized Tool error")}, nil
		}
		response.Error = &CallError{Code: code, Message: executeErr.Error()}
	}
	raw, encodeErr := json.Marshal(response)
	if encodeErr != nil || len(raw) > MaxResultBytes {
		return CallResponse{Error: invalidResponse("oversized Tool result")}, nil
	}
	return response, nil
}

func unitOptions(prototype gordis.Plugin, supplied ...ServerOptions) (rpc.ServeUnitOptions, error) {
	if len(supplied) > 1 {
		return rpc.ServeUnitOptions{}, errors.New("toolprocess: at most one ServerOptions")
	}
	spec, err := inspectPlugin(prototype)
	if err != nil {
		return rpc.ServeUnitOptions{}, err
	}
	var options ServerOptions
	if len(supplied) == 1 {
		options = supplied[0]
	}
	if err = checkRequirements(spec, options.Imports); err != nil {
		return rpc.ServeUnitOptions{}, err
	}
	identity, err := pluginIdentity(spec.ID, options.ID, options.Version)
	if err != nil {
		return rpc.ServeUnitOptions{}, err
	}
	limits := options.Limits.defaults()
	return rpc.ServeUnitOptions{
		Identity: identity,
		Limits:   rpc.Limits{MaxMessage: limits.MaxMessage},
		Unit: rpc.Unit{
			Plugins: []gordis.Plugin{&runtimeProvider{capacity: limits.MaxPending}, prototype},
			Instances: []gordis.InstanceSpec{
				{ID: "runtime", Plugin: providerID},
				{ID: toolInstanceID, Plugin: spec.ID},
			},
			Config:  options.Config,
			Imports: options.Imports,
			Exports: []rpc.Export{{Instance: "runtime", Binding: ProviderBinding}},
		},
	}, nil
}

func ServePlugin(ctx context.Context, plugin gordis.Plugin, options ...ServerOptions) error {
	unit, err := unitOptions(plugin, options...)
	if err != nil {
		return err
	}
	return rpc.ServeUnit(ctx, unit)
}

// Main serves a tool Plugin.
func Main(plugin gordis.Plugin, options ...ServerOptions) int {
	if err := ServePlugin(context.Background(), plugin, options...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
