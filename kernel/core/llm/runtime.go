// Package llm binds explicit provider/model selections to leased
// adapters, resolves call defaults, and exposes validated model streams.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var ErrRouteNotFound = errors.New("LLM provider/model binding not found")

type Runtime struct {
	routes   *registry.Registry[AdapterRegistration]
	models   *registry.Registry[modelcatalog.Spec]
	defaults *registry.Registry[modelcatalog.Defaults]
}

func NewRuntime() *Runtime {
	return &Runtime{
		routes:   registry.New(cloneRegistration),
		models:   registry.New(func(m modelcatalog.Spec) modelcatalog.Spec { return m.Clone() }),
		defaults: registry.New[modelcatalog.Defaults](nil),
	}
}

// ValidateDefaults is shared by provider preflight and adapter registration.
func ValidateDefaults(raw json.RawMessage, provider string) error {
	defaults, err := normalizeObject(raw)
	if err != nil {
		return err
	}

	_, err = callDefaults(defaults, provider, "registration")
	return err
}

func (r *Runtime) RegisterAdapter(owner contracts.ContributionOwner, registration AdapterRegistration) (func(), error) {
	if registration.Route == "" || registration.Provider == "" || registration.Adapter == nil {
		return nil, errors.New("LLM adapter requires route, provider, and adapter")
	}

	defaults, err := normalizeObject(registration.Defaults)
	if err != nil {
		return nil, fmt.Errorf("LLM route %q defaults: %w", registration.Route, err)
	}
	if _, err := callDefaults(defaults, registration.Provider, "registration"); err != nil {
		return nil, err
	}

	registration.Defaults = defaults
	if err := registration.RetryPolicy.Validate(); err != nil {
		return nil, err
	}

	return r.routes.Register(owner, registration.Route, 0, registration)
}

func (r *Runtime) Prepare() (*Snapshot, error) {
	if r == nil || r.routes == nil {
		return nil, contracts.ErrClosed
	}

	prepared, err := r.routes.Prepare()
	if err != nil {
		return nil, err
	}

	return &Snapshot{prepared: prepared}, nil
}

func (r *Runtime) PrepareCall(ctx context.Context, config protocol.CallConfig) (*PreparedCall, error) {
	snapshot, err := r.Prepare()
	if err != nil {
		return nil, err
	}

	call, err := snapshot.prepareCall(ctx, config, snapshot.Close)
	if err != nil {
		snapshot.Close()
		return nil, err
	}

	return call, nil
}

func (r *Runtime) Close() {
	if r != nil && r.routes != nil {
		r.routes.Close()
		r.models.Close()
		r.defaults.Close()
	}
}

type Snapshot struct {
	mu       sync.Mutex
	closed   bool
	wait     sync.WaitGroup
	prepared *registry.Snapshot[AdapterRegistration]
}

func (s *Snapshot) Revision() uint64 {
	if s == nil || s.prepared == nil {
		return 0
	}

	return s.prepared.Revision()
}

func (s *Snapshot) PrepareCall(ctx context.Context, config protocol.CallConfig) (*PreparedCall, error) {
	return s.prepareCall(ctx, config, nil)
}

func (s *Snapshot) prepareCall(ctx context.Context, config protocol.CallConfig, release func()) (*PreparedCall, error) {
	if s == nil || s.prepared == nil {
		return nil, contracts.ErrClosed
	}
	if !s.beginUse() {
		return nil, contracts.ErrClosed
	}

	defer s.wait.Done()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	config = config.Clone()
	if err := config.Validate(); err != nil {
		return nil, err
	}

	var candidates []AdapterRegistration
	exact := false

	// Exact model routes take precedence over provider-wide routes. More than
	// one surviving route is an error rather than an order-dependent choice.
	for _, item := range s.prepared.Items() {
		registration := item.Value
		if registration.Provider != config.Provider {
			continue
		}

		matches := false

		for _, name := range registration.Models {
			if name == config.Model {
				matches = true
			}
		}

		if len(registration.Models) > 0 && !matches {
			continue
		}

		if matches && !exact {
			candidates = nil
			exact = true
		}

		if exact && !matches {
			continue
		}

		candidates = append(candidates, registration)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: %s/%s", ErrRouteNotFound, config.Provider, config.Model)
	}
	if len(candidates) != 1 {
		return nil, fmt.Errorf("ambiguous LLM binding for %s/%s", config.Provider, config.Model)
	}

	registration := candidates[0]
	// The fallback describes the selected target, not its existence or capabilities.
	model := ModelInfo{Ref: ModelRef{Provider: config.Provider, Model: config.Model}}
	if resolver, ok := registration.Adapter.(ModelResolver); ok {
		var err error
		model, err = resolver.ResolveModel(ctx, model.Ref)
		if err != nil {
			return nil, fmt.Errorf("resolve model %s/%s: %w", config.Provider, config.Model, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if model.Ref.Provider != config.Provider ||
		model.Ref.Model != config.Model ||
		model.ContextWindow < 0 ||
		(model.SystemPromptUpdate != "" && model.SystemPromptUpdate != "in-history") {
		return nil, errors.New("LLM adapter returned inconsistent model information")
	}

	model = model.Clone()
	resolved := config.Clone()
	defaults, err := callDefaults(registration.Defaults, config.Provider, config.Model)
	if err != nil {
		return nil, err
	}
	// Exact-model defaults override provider registration defaults.
	if model.Defaults.ReasoningEffort != nil {
		defaults.ReasoningEffort = model.Defaults.ReasoningEffort
	}

	if model.Defaults.MaxTokens != nil {
		defaults.MaxTokens = model.Defaults.MaxTokens
	}

	// Record which values came from the adapter so later requests can resolve
	// those defaults again while preserving explicit caller configuration.
	marks := protocol.AdapterDefaults{}
	if resolved.ReasoningEffort == nil && defaults.ReasoningEffort != nil {
		resolved.ReasoningEffort = defaults.ReasoningEffort
		marks.ReasoningEffort = true
	}

	if resolved.MaxTokens == nil && defaults.MaxTokens != nil {
		resolved.MaxTokens = defaults.MaxTokens
		marks.MaxTokens = true
	}

	if resolved.Temperature == nil {
		resolved.Temperature = defaults.Temperature
	}

	if resolved.Stop == nil {
		resolved.Stop = defaults.Stop
	}

	if err := resolved.Validate(); err != nil {
		return nil, err
	}

	return &PreparedCall{
		config:      resolved.Clone(),
		defaults:    marks,
		model:       model.Clone(),
		retryPolicy: registration.RetryPolicy.Clone(),
		adapter:     registration.Adapter,
		parent:      s,
		release:     release,
	}, nil
}

func (s *Snapshot) Close() {
	if s == nil || s.prepared == nil {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	s.mu.Unlock()
	s.wait.Wait()
	s.prepared.Close()
}

func (s *Snapshot) beginUse() bool {
	if s == nil {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}

	s.wait.Add(1)

	return true
}

type PreparedCall struct {
	retryPolicy *ResolvedRetryPolicy
	mu          sync.Mutex
	used        bool
	closed      bool
	defaults    protocol.AdapterDefaults
	config      protocol.CallConfig
	model       ModelInfo
	adapter     Adapter
	parent      *Snapshot
	release     func()
	releaseOnce sync.Once
}

func (p *PreparedCall) Config() protocol.CallConfig {
	if p == nil {
		return protocol.CallConfig{}
	}

	return p.config.Clone()
}

func (p *PreparedCall) Model() ModelInfo {
	if p == nil {
		return ModelInfo{}
	}

	return p.model.Clone()
}

func (p *PreparedCall) Stream(ctx context.Context, request Request) iter.Seq2[protocol.StreamChunk, error] {
	if p == nil || p.adapter == nil {
		return errorSequence(contracts.ErrClosed)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errorSequence(contracts.ErrClosed)
	}

	if p.used {
		p.mu.Unlock()
		return errorSequence(contracts.ErrAlreadyUsed)
	}

	p.used = true
	p.mu.Unlock()
	if !reflect.DeepEqual(request.Config, p.config) {
		p.Close()
		return errorSequence(errors.New("LLM request config does not match prepared config"))
	}

	request = request.Clone()

	return func(yield func(protocol.StreamChunk, error) bool) {
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed || !p.parent.beginUse() {
			yield(protocol.StreamChunk{}, contracts.ErrClosed)
			return
		}

		defer p.Close()
		defer p.parent.wait.Done()

		terminal := func(err error) {
			reason := protocol.FinishReason{
				Kind:    protocol.FinishError,
				Failure: &protocol.LlmFailure{Message: err.Error(), Code: "UNKNOWN"},
			}
			var failure interface{ LlmFailure() protocol.LlmFailure }
			if errors.As(err, &failure) {
				detail := failure.LlmFailure()
				reason.Failure = &detail
			}

			if reason.Failure.Code == "ABORTED" ||
				ctx.Err() != nil ||
				errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				reason.Kind = protocol.FinishAborted
				if reason.Failure.Code != "ABORTED" {
					reason.Failure = &protocol.LlmFailure{Message: err.Error(), Code: "ABORTED"}
				}
			}

			yield(protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: reason}, nil)
		}
		if err := ctx.Err(); err != nil {
			terminal(err)
			return
		}

		upstream := p.adapter.Stream(ctx, request)
		if upstream == nil {
			terminal(errors.New("LLM adapter returned a nil stream"))
			return
		}

		for chunk, err := range upstream {
			if err != nil {
				terminal(err)
				return
			}

			if err := ctx.Err(); err != nil {
				terminal(err)
				return
			}

			snapshot, err := chunk.Snapshot()
			if err != nil {
				terminal(fmt.Errorf("invalid LLM stream chunk: %w", err))
				return
			}

			if !yield(snapshot, nil) {
				return
			}
			if chunk.Type == protocol.ChunkFinish {
				return
			}
		}

		if err := ctx.Err(); err != nil {
			terminal(err)
			return
		}
		// EOF is preserved as received; the assembler accessor defaults to stop.
	}
}

func (p *PreparedCall) Close() {
	if p == nil {
		return
	}

	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.releaseOnce.Do(func() {
		if p.release != nil {
			p.release()
		}
	})
}

func cloneRegistration(value AdapterRegistration) AdapterRegistration {
	value.RetryPolicy = value.RetryPolicy.Clone()
	value.Models = append([]string(nil), value.Models...)
	value.Defaults = append(json.RawMessage(nil), value.Defaults...)
	return value
}

func (p *PreparedCall) RetryPolicy() *ResolvedRetryPolicy {
	if p == nil {
		return nil
	}

	return p.retryPolicy.Clone()
}

func normalizeObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("expected one valid JSON object")
	}

	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("expected JSON object")
	}

	return json.Marshal(value)
}

func callDefaults(raw json.RawMessage, provider, model string) (protocol.CallConfig, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return protocol.CallConfig{}, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}

	for key := range fields {
		if key != "reasoningEffort" && key != "temperature" && key != "maxTokens" && key != "stop" {
			return protocol.CallConfig{}, fmt.Errorf("unsupported core default %q; keep private options in the adapter", key)
		}
	}

	fields["provider"], _ = json.Marshal(provider)
	fields["model"], _ = json.Marshal(model)
	b, err := json.Marshal(fields)
	if err != nil {
		return protocol.CallConfig{}, err
	}

	return protocol.DecodeCallConfig(b)
}

func (p *PreparedCall) AdapterDefaults() *protocol.AdapterDefaults {
	if p == nil || (!p.defaults.ReasoningEffort && !p.defaults.MaxTokens) {
		return nil
	}

	v := p.defaults
	return &v
}

func (p *PreparedCall) Context() protocol.RequestContext {
	if p == nil {
		return protocol.RequestContext{}
	}

	result := protocol.RequestContext{Provider: p.config.Provider, Model: p.config.Model}
	if p.model.ContextWindow > 0 {
		v := int64(p.model.ContextWindow)
		result.ContextWindow = &v
	}

	if p.model.SystemPromptUpdate != "" {
		v := p.model.SystemPromptUpdate
		result.SystemPromptUpdate = &v
	}

	return result
}

func errorSequence(err error) iter.Seq2[protocol.StreamChunk, error] {
	return func(yield func(protocol.StreamChunk, error) bool) { yield(protocol.StreamChunk{}, err) }
}
