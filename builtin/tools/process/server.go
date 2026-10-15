package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/RussellLuo/gordis"
	gordisprocess "github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// ServePlugin owns a minimal local Host for one process session. The ordinary
// Plugin Spec supplies all declarations; configuration is decoded and validated
// by Gordis before activation. At most one options value may be supplied.
func ServePlugin(ctx context.Context, in io.ReadCloser, out io.WriteCloser, prototype gordis.Plugin, options ...ServerOptions) error {
	if len(options) > 1 {
		return errors.New("toolprocess: at most one ServerOptions is allowed")
	}
	spec, err := inspectPlugin(prototype)
	if err != nil {
		return err
	}
	var o ServerOptions
	if len(options) == 1 {
		o = options[0]
	}
	identity, err := pluginIdentity(spec.ID, o.PackageID, o.Version)
	if err != nil {
		return err
	}
	runtime := tools.NewRuntime()
	defer runtime.Close() // Also covers NewHost failure and an unmounted provider.
	host, err := gordis.NewHost(&runtimeProvider{runtime: runtime}, prototype)
	if err != nil {
		return err
	}
	s := &pluginSession{host: host, runtime: runtime, spec: spec, identity: identity, stopped: make(chan struct{})}
	defer s.dispose()
	return gordisprocess.Serve(ctx, in, out, identity, defaultLimits(o.Limits), s.handler())
}

// The provider's constructor captures this session's Runtime, never a global.
type runtimeProvider struct{ runtime *tools.Runtime }

const providerID = "qi/tools/process/runtime"

func (p *runtimeProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: providerID, Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec()},
		New: func() gordis.Plugin { return &runtimeProvider{runtime: p.runtime} }}
}

func (p *runtimeProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("close process Tool Runtime", func(context.Context) error { p.runtime.Close(); return nil }); err != nil {
		return err
	}
	return scope.Provide(tools.RuntimeKey, p.runtime)
}

type pluginSession struct {
	host          *gordis.Host
	runtime       *tools.Runtime
	spec          gordis.PluginSpec
	identity      gordisprocess.Identity
	mu            sync.Mutex
	closing       bool
	work          sync.WaitGroup
	stopOnce      sync.Once
	stopped       chan struct{}
	stopErr       error
	monitorCancel context.CancelFunc
	monitorDone   chan struct{}
}

func (s *pluginSession) handler() gordisprocess.Handler {
	return gordisprocess.Handler{
		InitializeSession: func(ctx context.Context, caller gordisprocess.Caller, _ gordisprocess.Session, config json.RawMessage) error {
			if _, err := s.host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "runtime", Plugin: providerID}); err != nil {
				s.quiesce()
				return err
			}
			if _, err := s.host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "tool", Plugin: s.spec.ID, Config: append(json.RawMessage(nil), config...)}); err != nil {
				s.quiesce()
				return err
			}
			// Reject unsupported definitions before the session becomes ready.
			if _, err := s.listTools(); err != nil {
				s.quiesce()
				return err
			}
			monitorCtx, cancel := context.WithCancel(context.Background())
			s.monitorCancel, s.monitorDone = cancel, make(chan struct{})
			go s.monitor(monitorCtx, caller)
			return nil
		},
		Admit: func(method string) (func(), error) {
			if method != MethodDescribe && method != MethodListTools && method != MethodCall {
				return nil, &gordisprocess.RPCError{Code: "method", Message: "unknown ToolPackage method"}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.closing {
				return nil, gordisprocess.ErrClosed
			}
			s.work.Add(1)
			var once sync.Once
			return func() { once.Do(s.work.Done) }, nil
		},
		Call:    s.call,
		Quiesce: s.quiesce,
		Dispose: s.dispose,
	}
}

func (s *pluginSession) quiesce() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		if s.monitorCancel != nil {
			s.monitorCancel()
		}
		// Shutdown must start before waiting for workers: Scope stop callbacks
		// may be exactly what a cancelled, still-running worker needs to exit.
		go func() {
			if s.monitorDone != nil {
				<-s.monitorDone
			}
			s.stopErr = s.host.Shutdown(context.Background())
			close(s.stopped)
		}()
	})
}

func (s *pluginSession) dispose() gordisprocess.CleanupResult {
	s.quiesce()
	s.work.Wait()
	<-s.stopped
	if s.stopErr != nil {
		return gordisprocess.CleanupResult{Error: "tool plugin cleanup failed"}
	}
	for _, snapshot := range s.host.Snapshot() {
		if !snapshot.CleanupComplete || snapshot.CleanupError != "" || snapshot.Leases != 0 || len(snapshot.Residuals) != 0 ||
			(snapshot.Phase != gordis.PhaseStopped && snapshot.Generation != 0) {
			return gordisprocess.CleanupResult{Error: "tool plugin cleanup incomplete"}
		}
	}
	return gordisprocess.CleanupResult{Complete: true}
}

func (s *pluginSession) monitor(ctx context.Context, caller gordisprocess.Caller) {
	defer close(s.monitorDone)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, snapshot := range s.host.Snapshot() {
				if snapshot.Failure != "" && ctx.Err() == nil {
					_ = gordisprocess.ReportFailure(ctx, caller, errors.New("tool plugin failed"))
					return
				}
			}
		}
	}
}

func (s *pluginSession) listTools() ([]Tool, error) {
	snapshot, err := s.runtime.Prepare()
	if err != nil {
		return nil, err
	}
	defer snapshot.Close()
	return describeTools(snapshot.Descriptions())
}

func describeTools(descriptions []tools.DefinitionInfo) ([]Tool, error) {
	var definitions []Tool
	for _, d := range descriptions {
		if d.DeferLoading || d.Timeout < 0 || d.Timeout > MaxToolTimeout || d.Timeout%time.Millisecond != 0 {
			return nil, errors.New("toolprocess: unsupported deferred Tool or timeout")
		}
		definitions = append(definitions, Tool{
			Name: d.Name, Description: d.Description, Parameters: json.RawMessage(d.Parameters), Output: json.RawMessage(d.Output),
			TimeoutMillis: d.Timeout.Milliseconds(), FailureMode: FailureMode(d.FailureMode),
		})
	}
	return definitions, ValidateTools(definitions)
}

func (s *pluginSession) call(ctx context.Context, _ gordisprocess.Caller, method string, raw json.RawMessage) (any, error) {
	switch method {
	case MethodDescribe:
		return Metadata{ID: s.identity.Package, Version: s.identity.Version, Capabilities: []string{CapabilityTools}}, nil
	case MethodListTools:
		definitions, err := s.listTools()
		if err != nil {
			return nil, invalidResponse("unsupported Tool definitions")
		}
		return definitions, nil
	case MethodCall:
		var request CallRequest
		if decodeStrict(raw, &request) != nil || ValidateCall(request) != nil {
			return nil, invalidResponse("invalid Tool call")
		}
		snapshot, err := s.runtime.Prepare()
		if err != nil {
			return nil, invalidResponse("Tool Runtime unavailable")
		}
		defer snapshot.Close()
		descriptions := snapshot.Descriptions()
		if _, err := describeTools(descriptions); err != nil {
			return nil, invalidResponse("unsupported Tool definitions")
		}
		call, err := snapshot.Prepare(protocol.ToolCall{ID: protocol.ToolCallID(request.Invocation.ToolCallID), Name: request.Tool, Arguments: string(request.Arguments)})
		if err != nil {
			return nil, invalidResponse("invalid Tool call")
		}
		defer call.Close()
		// Use the same deadline outside Runtime so a pre-dispatch abort can
		// still distinguish a timeout from explicit cancellation on the wire.
		for _, d := range descriptions {
			if d.Name == request.Tool && d.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, d.Timeout)
				defer cancel()
				break
			}
		}
		result, executeErr := call.Execute(ctx) // Holds the Snapshot until the actual worker returns.
		return wireResult(ctx, result, executeErr)
	default:
		return nil, &gordisprocess.RPCError{Code: "method", Message: "unknown ToolPackage method"}
	}
}

func invalidResponse(message string) error {
	return &gordisprocess.RPCError{Code: CodeHostValidation, Message: message}
}

func wireResult(ctx context.Context, result tools.ExecutionResult, executeErr error) (any, error) {
	if result.Meta != nil || len(result.AdditionalContexts) != 0 || result.ConcludesTurn || len(result.Content) != 1 {
		return nil, invalidResponse("unsupported Tool result")
	}
	text, ok := result.Content[0].(protocol.TextBlock)
	if !ok {
		return nil, invalidResponse("unsupported Tool content")
	}
	if result.IsError {
		if result.Error == nil || text.Text != "Error: "+result.Error.Message {
			return nil, invalidResponse("unsupported Tool failure")
		}
		code := ""
		if result.Error.Info != nil {
			code = result.Error.Info.Code
		}
		switch code {
		case "ABORTED_BEFORE_DISPATCH":
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &gordisprocess.RPCError{Code: CodeTimeout, Message: "Tool call timed out"}
			}
			return nil, &gordisprocess.RPCError{Code: CodeCancelled, Message: "Tool call cancelled"}
		case "cancelled":
			return nil, &gordisprocess.RPCError{Code: CodeCancelled, Message: "Tool call cancelled"}
		case "timeout":
			return nil, &gordisprocess.RPCError{Code: CodeTimeout, Message: "Tool call timed out"}
		case "invalid_result", "invalid_arguments":
			return nil, invalidResponse("invalid Tool value")
		case "panic":
			return nil, &gordisprocess.RPCError{Code: CodeTransport, Message: "Tool execution panicked"}
		}
		// Native errors can contain arguments or credentials. Preserve the
		// ToolPackage/1 business-error category with a bounded safe message.
		return CallResponse{Error: NewToolError("tool execution failed")}, nil
	}
	if executeErr != nil || text.Text != string(result.Value) || validateJSONDocument(result.Value, MaxResultBytes, "result") != nil {
		return nil, invalidResponse("invalid Tool result")
	}
	return CallResponse{Result: append(json.RawMessage(nil), result.Value...)}, nil
}

func decodeStrict(raw json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("toolprocess: trailing JSON data")
	}
	return nil
}
