// Package tools validates Tool definitions and executes one-shot prepared calls
// through owner-bound middleware. Snapshots retain contributions until all
// admitted executions have finished.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrNotFound = registry.ErrNotFound

type contribution struct {
	definition *compiledDefinition
	middleware *Middleware
}

type compiledDefinition struct {
	definition Definition
	parameters *jsonschema.Schema
	output     *jsonschema.Schema
}

type Runtime struct {
	export        *Export
	contributions *registry.Registry[contribution]
}

func NewRuntime() *Runtime {
	return &Runtime{contributions: registry.New(cloneContribution)}
}

func (r *Runtime) Register(owner contracts.ContributionOwner, definition Definition) (func(), error) {
	return r.RegisterBatch(owner, []Definition{definition})
}

// RegisterBatch compiles and publishes a package's Tool definitions as one
// registry mutation. No member is visible when any schema or name conflicts.
func (r *Runtime) RegisterBatch(owner contracts.ContributionOwner, definitions []Definition) (dispose func(), err error) {
	if r == nil || r.contributions == nil {
		return nil, contracts.ErrClosed
	}
	var names []string
	if e := r.export; e != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		names = make([]string, len(definitions))
		for i, d := range definitions {
			names[i] = d.Name
		}
		var id contracts.OwnerIdentity
		if owner != nil {
			id = owner.Identity()
		}
		if e.err != nil {
			return nil, e.err
		}
		if e.phase != "collect" {
			return nil, e.retireLocked(e.phase, "register", id, names, fmt.Errorf("unsupported registration after seal"))
		}
		defer func() {
			if err != nil {
				err = e.retireLocked(e.phase, "register", id, names, err)
			}
		}()
		if owner != nil {
			owner = exportOwner{owner, e, names}
		}
	}

	if len(definitions) == 0 {
		return nil, errors.New("tool batch is empty")
	}

	registrations := make([]registry.Registration[contribution], 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))

	for _, definition := range definitions {
		if _, ok := seen[definition.Name]; ok {
			return nil, fmt.Errorf("duplicate tool name %q", definition.Name)
		}

		seen[definition.Name] = struct{}{}
		compiled, err := compileDefinition(definition)
		if err != nil {
			return nil, err
		}

		registrations = append(registrations, registry.Registration[contribution]{
			Key: "tool/" + definition.Name, Value: contribution{definition: compiled},
		})
	}

	dispose, err = r.contributions.RegisterBatch(owner, registrations)
	if err != nil || r.export == nil {
		return dispose, err
	}
	original := dispose
	var once sync.Once
	return func() {
		once.Do(func() {
			e := r.export
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.phase == "ready" {
				e.retireLocked(e.phase, "disposer", owner.Identity(), names, fmt.Errorf("unsupported contribution withdrawal"))
			}
			original()
		})
	}, nil
}

func (r *Runtime) RegisterMiddleware(owner contracts.ContributionOwner, middleware Middleware) (func(), error) {
	if err := r.rejectExport("middleware", owner); err != nil {
		return nil, err
	}
	if middleware.ID == "" || (middleware.Pre == nil && middleware.Execute == nil && middleware.Post == nil) {
		return nil, errors.New("tool middleware requires id and at least one stage")
	}

	cloned := middleware
	return r.contributions.Register(owner, "middleware/"+middleware.ID, middleware.Order, contribution{middleware: &cloned})
}

func (r *Runtime) Prepare() (*Snapshot, error) {
	if err := r.rejectExport("prepare", nil); err != nil {
		return nil, err
	}
	return r.PrepareSelected(nil)
}

// PrepareSelected freezes only the named Tool definitions while retaining the
// runtime-wide Tool middleware chain. A nil selection means all Tools; a
// non-nil empty selection intentionally exposes none.
func (r *Runtime) PrepareSelected(names []string) (*Snapshot, error) {
	if err := r.rejectExport("prepare-selected", nil); err != nil {
		return nil, err
	}
	if r == nil || r.contributions == nil {
		return nil, contracts.ErrClosed
	}

	selected := stringSet(names)
	prepared, err := r.contributions.PrepareWhere(func(item registry.Item[contribution]) bool {
		if strings.HasPrefix(item.Identity.Key, "middleware/") || selected == nil {
			return true
		}

		_, ok := selected[strings.TrimPrefix(item.Identity.Key, "tool/")]
		return ok
	})
	if err != nil {
		return nil, err
	}

	return &Snapshot{prepared: prepared}, nil
}

func stringSet(values []string) map[string]struct{} {
	if values == nil {
		return nil
	}

	result := make(map[string]struct{}, len(values))

	for _, value := range values {
		result[value] = struct{}{}
	}

	return result
}

func (r *Runtime) Schemas() []Schema {
	if err := r.rejectExport("schemas", nil); err != nil {
		return nil
	}
	prepared, err := r.Prepare()
	if err != nil {
		return nil
	}

	defer prepared.Close()

	return prepared.Schemas()
}

func (r *Runtime) Close() {
	if err := r.rejectExport("runtime-close", nil); err != nil {
		return
	}
	if r != nil && r.contributions != nil {
		r.contributions.Close()
	}
}

type Snapshot struct {
	mu       sync.Mutex
	closed   bool
	prepared *registry.Snapshot[contribution]
	wait     sync.WaitGroup
}

func (s *Snapshot) Revision() uint64 {
	if s == nil || s.prepared == nil {
		return 0
	}

	return s.prepared.Revision()
}

func (s *Snapshot) Schemas() []Schema {
	definitions, _ := s.contents()
	schemas := make([]Schema, 0, len(definitions))

	for _, definition := range definitions {
		schemas = append(schemas, Schema{
			Name:         definition.definition.Name,
			Description:  definition.definition.Description,
			Parameters:   definition.definition.Parameters.Clone(),
			DeferLoading: definition.definition.DeferLoading,
		})
	}

	return schemas
}

// Descriptions returns the compiled definitions from this frozen generation.
// A closed (or nil) Snapshot no longer provides descriptions.
func (s *Snapshot) Descriptions() []DefinitionInfo {
	definitions, _ := s.contents()
	var descriptions []DefinitionInfo
	for _, compiled := range definitions {
		d := compiled.definition
		descriptions = append(descriptions, DefinitionInfo{
			Name: d.Name, Description: d.Description,
			Parameters: d.Parameters.Clone(), Output: d.Output.Clone(),
			Timeout: d.Timeout, FailureMode: d.FailureMode, DeferLoading: d.DeferLoading,
		})
	}
	return descriptions
}

func (s *Snapshot) Prepare(call protocol.ToolCall) (*PreparedCall, error) {
	return s.PrepareWithIdentity(call, ExecutionIdentity{})
}

// PrepareWithIdentity copies call and captures its execution attribution in the
// prepared call, never in the shared snapshot or context. Middleware may change
// arguments and wrap dispatch, but must preserve this identity.
func (s *Snapshot) PrepareWithIdentity(call protocol.ToolCall, identity ExecutionIdentity) (*PreparedCall, error) {
	return s.PrepareWithContext(call, ExecutionContext{Identity: identity})
}

// PrepareWithContext captures Session cwd per call, never as mutable Snapshot
// state. Middleware must preserve this context through actual dispatch.
func (s *Snapshot) PrepareWithContext(call protocol.ToolCall, executionContext ExecutionContext) (*PreparedCall, error) {
	if err := call.Validate(); err != nil {
		return nil, err
	}
	if executionContext.CWD != "" && !filepath.IsAbs(executionContext.CWD) {
		return nil, errors.New("tool execution cwd must be absolute")
	}

	definitions, middleware := s.contents()
	if definitions == nil && s.isClosed() {
		return nil, contracts.ErrClosed
	}

	for _, definition := range definitions {
		if definition.definition.Name == call.Name {
			return &PreparedCall{
				snapshot:   s,
				definition: definition,
				middleware: middleware,
				execution: Execution{
					Identity:  executionContext.Identity,
					CWD:       executionContext.CWD,
					Call:      call.Clone(),
					Arguments: json.RawMessage(call.Arguments),
				},
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrNotFound, call.Name)
}

func (s *Snapshot) contents() ([]*compiledDefinition, []Middleware) {
	if s == nil || s.prepared == nil || s.isClosed() {
		return nil, nil
	}

	items := s.prepared.Items()
	definitions := make([]*compiledDefinition, 0)
	middleware := make([]Middleware, 0)

	for _, item := range items {
		if item.Value.definition != nil {
			definitions = append(definitions, item.Value.definition)
		}

		if item.Value.middleware != nil {
			middleware = append(middleware, *item.Value.middleware)
		}
	}

	return definitions, middleware
}

func (s *Snapshot) beginExecution() bool {
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

func (s *Snapshot) isClosed() bool {
	if s == nil {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

func (s *Snapshot) Close() {
	if s == nil {
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
	if s.prepared != nil {
		s.prepared.Close()
	}
}

// PreparedCall captures a Tool and its middleware from a snapshot. Execute may
// be called only once, including when validation or dispatch later fails.
type PreparedCall struct {
	mu         sync.Mutex
	used       bool
	closed     bool
	snapshot   *Snapshot
	definition *compiledDefinition
	middleware []Middleware
	execution  Execution
}

func (p *PreparedCall) Definition() Schema {
	if p == nil || p.definition == nil {
		return Schema{}
	}

	return Schema{
		Name:         p.definition.definition.Name,
		Description:  p.definition.definition.Description,
		Parameters:   p.definition.definition.Parameters.Clone(),
		DeferLoading: p.definition.definition.DeferLoading,
	}
}

func (p *PreparedCall) Execute(ctx context.Context) (ExecutionResult, error) {
	if p == nil || p.definition == nil || p.snapshot == nil {
		return ExecutionResult{}, contracts.ErrClosed
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ExecutionResult{}, contracts.ErrClosed
	}

	if p.used {
		p.mu.Unlock()
		return ExecutionResult{}, contracts.ErrAlreadyUsed
	}

	p.used = true
	p.mu.Unlock()
	if !p.snapshot.beginExecution() {
		return ExecutionResult{}, contracts.ErrClosed
	}

	defer p.snapshot.wait.Done()

	if err := ctx.Err(); err != nil {
		result := AbortedBeforeDispatchResult()
		if p.definition.definition.FailureMode == ToolFailureAbort {
			return result, &ExecutionError{Result: result.Clone(), Cause: err}
		}

		return result, nil
	}

	if err := validateJSON(p.definition.parameters, p.execution.Arguments); err != nil {
		return p.failure("invalid_arguments", fmt.Errorf("validate arguments: %w", err))
	}

	type outcome struct {
		content    ExecutionResult
		err        error
		panic      any
		dispatched bool
	}
	completed := make(chan outcome, 1)
	executionContext := ctx
	cancel := func() {}
	if timeout := p.definition.definition.Timeout; timeout > 0 {
		executionContext, cancel = context.WithTimeout(ctx, timeout)
	}

	defer cancel()

	go func() {
		result := outcome{}
		defer func() {
			result.panic = recover()
			completed <- result
		}()

		execution := p.execution.Clone()
		var err error

		for _, middleware := range p.middleware {
			if middleware.Pre != nil {
				execution, err = middleware.Pre(executionContext, execution.Clone())
				if err != nil {
					result.err = fmt.Errorf("pre-execute %q: %w", middleware.ID, err)
					return
				}

				if execution.Identity != p.execution.Identity || execution.CWD != p.execution.CWD ||
					execution.Call.ID != p.execution.Call.ID ||
					execution.Call.Name != p.execution.Call.Name {
					result.err = fmt.Errorf("pre-execute %q changed tool identity", middleware.ID)
					return
				}
			}
		}

		// Pre hooks may adjust arguments; execute hooks may wrap dispatch. Both
		// stages must preserve execution attribution and the call identity used
		// for result settlement.
		plan := ExecutePlan{
			Execution: execution.Clone(),
			Invoke: func(ctx context.Context, input Execution) (ExecutionResult, error) {
				// An Execute wrapper can supply a different value to Invoke even
				// when its returned plan passed validation. Keep attribution bound
				// at the actual Tool boundary as well.
				if input.Identity != p.execution.Identity || input.CWD != p.execution.CWD {
					return ExecutionResult{}, errors.New("tool invocation changed execution identity")
				}

				return p.definition.definition.Execute(ctx, input)
			},
		}

		for _, middleware := range p.middleware {
			if middleware.Execute != nil {
				plan, err = middleware.Execute(executionContext, plan)
				if err != nil {
					result.err = fmt.Errorf("execute middleware %q: %w", middleware.ID, err)
					return
				}

				// Middleware may change arguments or invocation behavior, but the
				// admitted call identity must stay attached to the prepared lease.
				if plan.Invoke == nil ||
					plan.Execution.Identity != p.execution.Identity || plan.Execution.CWD != p.execution.CWD ||
					plan.Execution.Call.ID != execution.Call.ID ||
					plan.Execution.Call.Name != execution.Call.Name {
					result.err = fmt.Errorf("execute middleware %q returned invalid plan", middleware.ID)
					return
				}
			}
		}

		if err := validateJSON(p.definition.parameters, plan.Execution.Arguments); err != nil {
			result.content = FailureResult("invalid_arguments", fmt.Errorf("validate arguments: %w", err))
			return
		}

		if executionContext.Err() != nil {
			result.content = AbortedBeforeDispatchResult()
			return
		}

		result.dispatched = true
		result.content, result.err = plan.Invoke(executionContext, plan.Execution.Clone())
		if result.err != nil {
			return
		}

		// Post hooks settle an already dispatched result even if cancellation
		// arrived while the Tool was running.
		for _, middleware := range p.middleware {
			if middleware.Post != nil {
				result.content, result.err = middleware.Post(context.WithoutCancel(executionContext), execution.Clone(), result.content.Clone())
				if result.err != nil {
					result.err = fmt.Errorf("post-execute %q: %w", middleware.ID, result.err)
					return
				}
			}
		}
	}()

	// An abort is a stop request, not proof that side effects have stopped.
	// Keep the caller and generation lease until the actual worker exits;
	// preserve its result (including contexts) even when it finishes after abort.
	result := <-completed

	if result.panic != nil {
		return p.failure("panic", fmt.Errorf("tool panicked: %v", result.panic))
	}
	if result.err != nil {
		if !result.dispatched && executionContext.Err() != nil {
			aborted := AbortedBeforeDispatchResult()
			if p.definition.definition.FailureMode == ToolFailureAbort {
				return aborted, &ExecutionError{Result: aborted.Clone(), Cause: result.err}
			}

			return aborted, nil
		}

		return p.failure(classifyFailure(result.err), result.err)
	}

	if err := result.content.Validate(); err != nil {
		return p.failure("invalid_result", err)
	}
	if !result.content.IsError {
		if err := validateJSON(p.definition.output, result.content.Value); err != nil {
			return p.failure("invalid_result", fmt.Errorf("validate result: %w", err))
		}
	}

	if result.content.IsError && p.definition.definition.FailureMode == ToolFailureAbort {
		return result.content.Clone(), &ExecutionError{Result: result.content.Clone(), Cause: errors.New(result.content.Error.Message)}
	}

	return result.content.Clone(), nil
}

func (p *PreparedCall) failure(code string, cause error) (ExecutionResult, error) {
	result := FailureResult(code, cause)
	if p.definition.definition.FailureMode == ToolFailureAbort {
		return result, &ExecutionError{Result: result.Clone(), Cause: cause}
	}

	return result, nil
}

func (p *PreparedCall) Close() {
	if p == nil {
		return
	}

	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

type ExecutionError struct {
	Result ExecutionResult
	Cause  error
}

func (e *ExecutionError) Error() string { return e.Cause.Error() }

func (e *ExecutionError) Unwrap() error { return e.Cause }

func compileDefinition(definition Definition) (*compiledDefinition, error) {
	if definition.Name == "" || definition.Execute == nil {
		return nil, errors.New("tool requires name and execute function")
	}
	if strings.Contains(definition.Name, "/") {
		return nil, errors.New("tool name cannot contain slash")
	}
	if definition.FailureMode == "" {
		definition.FailureMode = ToolFailureResult
	}

	if definition.FailureMode != ToolFailureResult && definition.FailureMode != ToolFailureAbort {
		return nil, fmt.Errorf("unknown tool failure mode %q", definition.FailureMode)
	}

	parameters, normalizedParameters, err := compileSchema(definition.Parameters, "parameters")
	if err != nil {
		return nil, fmt.Errorf("tool %q parameters: %w", definition.Name, err)
	}

	output, normalizedOutput, err := compileSchema(definition.Output, "output")
	if err != nil {
		return nil, fmt.Errorf("tool %q output: %w", definition.Name, err)
	}

	definition.Parameters = normalizedParameters
	definition.Output = normalizedOutput

	return &compiledDefinition{
		definition: cloneDefinition(definition),
		parameters: parameters,
		output:     output,
	}, nil
}

func compileSchema(raw JSONSchema, suffix string) (*jsonschema.Schema, JSONSchema, error) {
	if len(raw) == 0 {
		raw = JSONSchema(`{}`)
	}

	if !json.Valid(raw) {
		return nil, nil, errors.New("schema is not valid JSON")
	}

	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, err
	}

	compiler := jsonschema.NewCompiler()
	location := "urn:qi:tool-schema:" + suffix
	if err := compiler.AddResource(location, document); err != nil {
		return nil, nil, err
	}

	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, nil, err
	}

	return compiled, raw.Clone(), nil
}

func validateJSON(schema *jsonschema.Schema, raw json.RawMessage) error {
	if !json.Valid(raw) {
		return errors.New("value is not JSON")
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}

	return schema.Validate(value)
}

func classifyFailure(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	return "execution"
}

func cloneDefinition(definition Definition) Definition {
	definition.Parameters = definition.Parameters.Clone()
	definition.Output = definition.Output.Clone()
	return definition
}

func cloneContribution(value contribution) contribution {
	if value.definition != nil {
		definition := *value.definition
		definition.definition = cloneDefinition(definition.definition)
		value.definition = &definition
	}

	if value.middleware != nil {
		middleware := *value.middleware
		value.middleware = &middleware
	}

	return value
}
