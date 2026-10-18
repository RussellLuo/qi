package process

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const providerID = "qi/tools/process/runtime"

type runtimeProvider struct{ capacity int }

func (plugin *runtimeProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       providerID,
		New:      func() gordis.Plugin { return &runtimeProvider{capacity: plugin.capacity} },
		Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec(), ProviderKey.Spec()},
	}
}

func (plugin *runtimeProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime := tools.NewRuntime()
	if err := scope.Defer("runtime", func(context.Context) error { runtime.Close(); return nil }); err != nil {
		runtime.Close()
		return err
	}
	if err := scope.Provide(tools.RuntimeKey, runtime); err != nil {
		return err
	}
	return scope.Provide(ProviderKey, Provider(&exporter{
		runtime: runtime,
		pending: make(chan struct{}, plugin.capacity),
	}))
}

// exporter is an ordinary Service over the same Runtime used by business Plugins.
// ProviderBinding validates its definitions after all Unit instances are Ready.
type exporter struct {
	runtime *tools.Runtime
	pending chan struct{}
}

func (plugin *exporter) ListTools(context.Context) ([]Tool, error) {
	snapshot, err := plugin.runtime.Prepare()
	if err != nil {
		return nil, err
	}
	defer snapshot.Close()
	return describeTools(snapshot.Descriptions())
}

func (plugin *exporter) CallTool(ctx context.Context, request CallRequest) (CallResponse, error) {
	select {
	case plugin.pending <- struct{}{}:
		defer func() { <-plugin.pending }()
	default:
		return CallResponse{Error: &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity"}}, nil
	}
	response, err := plugin.call(ctx, request)
	if err != nil {
		var failure *CallError
		if !errors.As(err, &failure) {
			failure = invalidResponse("invalid Tool call")
		}
		response = CallResponse{Error: failure}
	}
	return response, nil
}

func (plugin *exporter) call(ctx context.Context, request CallRequest) (CallResponse, error) {
	if ValidateCall(request) != nil {
		return CallResponse{}, invalidResponse("invalid Tool call")
	}
	snapshot, err := plugin.runtime.Prepare()
	if err != nil {
		return CallResponse{}, invalidResponse("Runtime unavailable")
	}
	defer snapshot.Close()
	descriptions := snapshot.Descriptions()
	if _, err = describeTools(descriptions); err != nil {
		return CallResponse{}, invalidResponse("unsupported Tool definitions")
	}
	call, err := snapshot.Prepare(protocol.ToolCall{ID: protocol.ToolCallID(request.ToolCallID), Name: request.Tool, Arguments: string(request.Arguments)})
	if err != nil {
		return CallResponse{}, invalidResponse("unknown Tool")
	}
	defer call.Close()
	// Keep the deadline visible to wireResult: Runtime's private execution
	// context is otherwise lost in ABORTED_BEFORE_DISPATCH results.
	for _, d := range descriptions {
		if d.Name == request.Tool && d.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d.Timeout)
			defer cancel()
			break
		}
	}
	result, err := call.Execute(ctx)
	return wireResult(ctx, result, err)
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
				{ID: "tool", Plugin: spec.ID},
			},
			ConfigTarget: "tool",
			Imports:      options.Imports,
			Exports:      []rpc.Export{{Instance: "runtime", Binding: ProviderBinding}},
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
