package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	objectWithText  = JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)
	objectWithValue = JSONSchema(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
)

func TestToolSnapshotKeepsSchemaAndDispatchFromSameGeneration(t *testing.T) {
	runtime := NewRuntime()
	first := newTestOwner("echo", 1)
	disposeFirst, err := runtime.Register(first, echoDefinition("v1"))
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	disposeFirst()
	second := newTestOwner("echo", 2)
	if _, err := runtime.Register(second, echoDefinition("v2")); err != nil {
		t.Fatal(err)
	}

	oldResult := execute(t, prepared, `{"text":"hello"}`)
	current, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer current.Close()

	newResult := execute(t, current, `{"text":"hello"}`)
	if string(oldResult.Value) != `{"value":"v1:hello"}` || string(newResult.Value) != `{"value":"v2:hello"}` {
		t.Fatalf("old/new results = %s / %s", oldResult.Content, newResult.Content)
	}

	if got := prepared.Schemas()[0].Description; got != "v1" {
		t.Fatalf("prepared schema description = %q", got)
	}

	stopped := make(chan error, 1)
	go func() {
		stopped <- first.Stop(context.Background())
	}()

	select {
	case err := <-stopped:
		t.Fatalf("owner stopped before snapshot release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	prepared.Close()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestRegisterBatchRejectsWholeGroup(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	owned := newTestOwner("package", 1)
	_, err := runtime.RegisterBatch(owned, []Definition{
		echoDefinition("first"),
		{
			Name:       "broken",
			Parameters: JSONSchema(`{"type":`),
			Execute: JSONTool(func(context.Context, Execution) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			}),
		},
	})
	if err == nil {
		t.Fatal("invalid batch registered")
	}

	if schemas := runtime.Schemas(); len(schemas) != 0 {
		t.Fatalf("partial batch remained: %+v", schemas)
	}
}

func TestToolFailuresBecomeStructuredResults(t *testing.T) {
	tests := []struct {
		name       string
		definition Definition
		arguments  string
		cancel     bool
		wantCode   string
	}{
		{
			name:       "arguments",
			definition: echoDefinition("ok"),
			arguments:  `{}`,
			wantCode:   "invalid_arguments",
		},
		{
			name: "result",
			definition: Definition{
				Name:       "echo",
				Parameters: objectWithText,
				Output:     objectWithValue,
				Execute: JSONTool(func(context.Context, Execution) (json.RawMessage, error) {
					return json.RawMessage(`{"wrong":true}`), nil
				}),
			},
			arguments: `{"text":"hello"}`,
			wantCode:  "invalid_result",
		},
		{
			name: "panic",
			definition: Definition{
				Name:       "echo",
				Parameters: objectWithText,
				Output:     objectWithValue,
				Execute:    JSONTool(func(context.Context, Execution) (json.RawMessage, error) { panic("boom") }),
			},
			arguments: `{"text":"hello"}`,
			wantCode:  "panic",
		},
		{
			name: "timeout",
			definition: Definition{
				Name:       "echo",
				Parameters: objectWithText,
				Output:     objectWithValue,
				Timeout:    time.Millisecond,
				Execute: JSONTool(func(ctx context.Context, _ Execution) (json.RawMessage, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}),
			},
			arguments: `{"text":"hello"}`,
			wantCode:  "timeout",
		},
		{
			name: "cancel",
			definition: Definition{
				Name:       "echo",
				Parameters: objectWithText,
				Output:     objectWithValue,
				Execute: JSONTool(func(ctx context.Context, _ Execution) (json.RawMessage, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}),
			},
			arguments: `{"text":"hello"}`,
			cancel:    true,
			wantCode:  "ABORTED_BEFORE_DISPATCH",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime()
			if _, err := runtime.Register(newTestOwner("tool", 1), test.definition); err != nil {
				t.Fatal(err)
			}

			prepared, err := runtime.Prepare()
			if err != nil {
				t.Fatal(err)
			}

			defer prepared.Close()

			call, err := prepared.Prepare(protocol.ToolCall{
				ID:        "call-1",
				Name:      "echo",
				Arguments: string(test.arguments),
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}

			result, err := call.Execute(ctx)
			if err != nil {
				t.Fatalf("result-mode Execute error = %v", err)
			}

			if !result.IsError {
				t.Fatalf("result = %+v", result)
			}

			if result.Error == nil || result.Error.Info == nil || result.Error.Info.Code != test.wantCode {
				t.Fatalf("failure = %+v, want %q", result.Error, test.wantCode)
			}

			if _, err := call.Execute(context.Background()); !errors.Is(err, contracts.ErrAlreadyUsed) {
				t.Fatalf("second Execute error = %v", err)
			}
		})
	}
}

func TestToolMiddlewareIsOrderedAndFrozenWithDefinition(t *testing.T) {
	runtime := NewRuntime()
	owned := newTestOwner("tool", 1)
	if _, err := runtime.Register(owned, echoDefinition("body")); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	record := func(value string) {
		mu.Lock()
		order = append(order, value)
		mu.Unlock()
	}

	for _, middleware := range []Middleware{
		{
			ID:    "second",
			Order: 20,
			Pre: func(_ context.Context, execution Execution) (Execution, error) {
				record("pre-second")
				return execution, nil
			},
			Post: func(_ context.Context, _ Execution, result ExecutionResult) (ExecutionResult, error) {
				record("post-second")
				return result, nil
			},
		},
		{
			ID:    "first",
			Order: 10,
			Pre: func(_ context.Context, execution Execution) (Execution, error) {
				record("pre-first")
				return execution, nil
			},
			Post: func(_ context.Context, _ Execution, result ExecutionResult) (ExecutionResult, error) {
				record("post-first")
				return result, nil
			},
		},
	} {
		if _, err := runtime.RegisterMiddleware(owned, middleware); err != nil {
			t.Fatal(err)
		}
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	if _, err := runtime.RegisterMiddleware(newTestOwner("late", 1), Middleware{
		ID:    "late",
		Order: 0,
		Pre: func(_ context.Context, execution Execution) (Execution, error) {
			record("late")
			return execution, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	_ = execute(t, prepared, `{"text":"hello"}`)
	if want := []string{"pre-first", "pre-second", "post-first", "post-second"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
}

func TestToolAbortPolicyReturnsStructuredResultAndError(t *testing.T) {
	runtime := NewRuntime()
	definition := echoDefinition("abort")
	definition.FailureMode = ToolFailureAbort
	if _, err := runtime.Register(newTestOwner("tool", 1), definition); err != nil {
		t.Fatal(err)
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	call, err := prepared.Prepare(protocol.ToolCall{
		ID:        "call-1",
		Name:      "echo",
		Arguments: string(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := call.Execute(context.Background())
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || !result.IsError || !executionErr.Result.IsError {
		t.Fatalf("result/error = %+v / %v", result, err)
	}
}

func echoDefinition(version string) Definition {
	return Definition{
		Name:        "echo",
		Description: version,
		Parameters:  objectWithText,
		Output:      objectWithValue,
		Execute: JSONTool(func(_ context.Context, execution Execution) (json.RawMessage, error) {
			var input struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(execution.Arguments, &input); err != nil {
				return nil, err
			}

			return json.Marshal(map[string]string{"value": version + ":" + input.Text})
		}),
	}
}

func execute(t *testing.T, prepared *Snapshot, arguments string) ExecutionResult {
	t.Helper()
	call, err := prepared.Prepare(protocol.ToolCall{
		ID:        "call-1",
		Name:      "echo",
		Arguments: string(arguments),
	})
	if err != nil {
		t.Fatal(err)
	}

	defer call.Close()

	result, err := call.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func TestRawArgumentsValidateOnlyAtExecutionBoundary(t *testing.T) {
	for _, arguments := range []string{"{broken", "", "null", ` { "text": "hi" } `} {
		t.Run(arguments, func(t *testing.T) {
			runtime := NewRuntime()
			defer runtime.Close()

			calls := 0
			definition := echoDefinition("v1")
			definition.Execute = JSONTool(func(_ context.Context, e Execution) (json.RawMessage, error) {
				calls++
				if e.Call.Arguments != arguments || string(e.Arguments) != arguments {
					t.Errorf("raw arguments changed: %+v", e)
				}

				return json.RawMessage(`{"value":"hi"}`), nil
			})
			if _, err := runtime.Register(newTestOwner("raw", 1), definition); err != nil {
				t.Fatal(err)
			}

			snapshot, err := runtime.Prepare()
			if err != nil {
				t.Fatal(err)
			}

			defer snapshot.Close()

			call, err := snapshot.Prepare(protocol.ToolCall{
				ID:        "c",
				Name:      "echo",
				Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("fact rejected before execution: %v", err)
			}

			result, err := call.Execute(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			if arguments == ` { "text": "hi" } ` {
				if calls != 1 || result.IsError {
					t.Fatalf("valid raw arguments: %+v", result)
				}
			} else {
				if calls != 0 || !result.IsError || result.Error.Info.Code != "invalid_arguments" {
					t.Fatalf("invalid raw arguments executed or lost: %+v", result)
				}
			}
		})
	}
}

func TestCanonicalToolResultAndScheduling(t *testing.T) {
	for _, kind := range []string{"success", "failure", "contexts", "concludes"} {
		t.Run(kind, func(t *testing.T) {
			runtime := NewRuntime()
			defer runtime.Close()

			want := ExecutionResult{
				Value:   json.RawMessage(`{"private":true}`),
				Content: []protocol.ContentBlock{protocol.TextBlock{Text: "visible"}},
				Meta:    json.RawMessage(`{"native":7}`),
			}
			if kind == "failure" {
				want = ExecutionResult{
					IsError: true,
					Error: &Failure{
						Message: "failed",
						Info: &protocol.ToolFailureInfo{
							Name:   "BusinessError",
							Code:   "BUSINESS",
							Reason: stringPointer("quota"),
						},
					},
					Content: []protocol.ContentBlock{protocol.TextBlock{Text: "explain"}},
					Meta:    json.RawMessage(`{"native":7}`),
				}
			}

			if kind == "contexts" {
				want.AdditionalContexts = []protocol.Message{protocol.NewTextMessage("extra", protocol.RoleUser, protocol.SourceUser, "next")}
			}

			if kind == "concludes" {
				want.ConcludesTurn = true
			}

			if _, err := runtime.Register(newTestOwner(kind, 1), Definition{
				Name: "echo",
				Execute: func(context.Context, Execution) (ExecutionResult, error) {
					return want, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			snapshot, err := runtime.Prepare()
			if err != nil {
				t.Fatal(err)
			}

			defer snapshot.Close()

			call, err := snapshot.Prepare(protocol.ToolCall{
				ID:        "c",
				Name:      "echo",
				Arguments: `{}`,
			})
			if err != nil {
				t.Fatal(err)
			}

			result, err := call.Execute(context.Background())
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Fatalf("canonical result changed: %+v / %v", result, err)
			}

			result.Meta[0] = '['
			result.Content[0] = protocol.TextBlock{Text: "changed"}
			if string(want.Meta) != `{"native":7}` || want.Content[0].(protocol.TextBlock).Text == "changed" {
				t.Fatal("runtime returned an aliased result")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

// testOwner is a deterministic ContributionOwner for runtime and conformance
// tests. Stop rejects new acquisitions, waits for admitted work, and then runs
// cleanup callbacks in reverse registration order.
type testOwner struct {
	mu         sync.Mutex
	identity   contracts.OwnerIdentity
	stopping   bool
	stopped    bool
	active     int
	cleanups   []func()
	activeZero chan struct{}
}

func newTestOwner(id string, generation uint64) *testOwner {
	return &testOwner{identity: contracts.OwnerIdentity{ID: id, Generation: generation}}
}

func (o *testOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *testOwner) Acquire() (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return nil, contracts.ErrOwnerUnavailable
	}

	o.active++
	var once sync.Once

	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.active--
			if o.active == 0 && o.activeZero != nil {
				close(o.activeZero)
				o.activeZero = nil
			}

			o.mu.Unlock()
		})
	}, nil
}

func (o *testOwner) DeferCleanup(_ string, cleanup func()) error {
	if cleanup == nil {
		return errors.New("cleanup is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return contracts.ErrOwnerUnavailable
	}

	o.cleanups = append(o.cleanups, cleanup)

	return nil
}

func (o *testOwner) Stop(ctx context.Context) error {
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	if !o.stopping {
		o.stopping = true
	}

	wait := o.activeZero
	if o.active > 0 && wait == nil {
		wait = make(chan struct{})
		o.activeZero = wait
	}

	o.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	cleanups := o.cleanups
	o.cleanups = nil
	o.stopped = true
	o.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	return nil
}

func TestToolMiddlewareCancellationAndDrain(t *testing.T) {
	for _, stage := range []string{"pre", "execute", "post"} {
		t.Run(stage, func(t *testing.T) {
			runtime := NewRuntime()
			defer runtime.Close()

			owned := newTestOwner("drain", 1)
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()

			invoked := false
			if _, err := runtime.Register(owned, Definition{
				Name: "tool",
				Execute: func(context.Context, Execution) (ExecutionResult, error) {
					invoked = true
					return ExecutionResult{
						Value:   json.RawMessage(`{}`),
						Content: []protocol.ContentBlock{protocol.TextBlock{Text: "actual result"}},
					}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			middleware := Middleware{ID: "barrier"}

			switch stage {
			case "pre":
				middleware.Pre = func(ctx context.Context, in Execution) (Execution, error) {
					close(entered)
					<-release
					return in, ctx.Err()
				}
			case "execute":
				middleware.Execute = func(_ context.Context, in ExecutePlan) (ExecutePlan, error) {
					close(entered)
					<-release
					return in, nil
				}
			case "post":
				middleware.Post = func(ctx context.Context, _ Execution, in ExecutionResult) (ExecutionResult, error) {
					close(entered)
					<-release
					if ctx.Err() != nil {
						t.Error("result finalization inherited activity cancellation")
					}

					return in, nil
				}
			}

			if _, err := runtime.RegisterMiddleware(owned, middleware); err != nil {
				t.Fatal(err)
			}

			snapshot, err := runtime.Prepare()
			if err != nil {
				t.Fatal(err)
			}

			call, err := snapshot.Prepare(protocol.ToolCall{
				ID:        "call",
				Name:      "tool",
				Arguments: `{}`,
			})
			if err != nil {
				t.Fatal(err)
			}

			defer call.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			result := make(chan ExecutionResult, 1)
			go func() {
				r, err := call.Execute(ctx)
				if err != nil {
					t.Error(err)
				}

				result <- r
			}()

			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("barrier not reached")
			}

			cancel()
			closed := make(chan struct{})
			go func() {
				snapshot.Close()
				close(closed)
			}()
			stopped := make(chan error, 1)
			go func() {
				stopped <- owned.Stop(context.Background())
			}()

			select {
			case <-closed:
				t.Fatal("snapshot closed during execution")
			case <-result:
				t.Fatal("returned before drain")
			case <-stopped:
				t.Fatal("owner cleaned before drain")
			case <-time.After(20 * time.Millisecond):
			}

			close(release)
			var r ExecutionResult

			select {
			case r = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("execution blocked")
			}

			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owner drain blocked")
			}

			if stage == "post" {
				if !invoked || r.IsError || r.Content[0].(protocol.TextBlock).Text != "actual result" {
					t.Fatal(r, invoked)
				}
			} else if invoked || !r.IsError || r.Error.Info.Name != "AbortError" || r.Error.Info.Code != "ABORTED_BEFORE_DISPATCH" {
				t.Fatal(r, invoked)
			}
		})
	}
}

func TestToolExecutionIdentityStagesAndCompatibility(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	owned := newTestOwner("identity", 1)
	want := ExecutionIdentity{AgentID: "agent", SessionID: "session", Turn: 2, Step: 3}
	wantContext := ExecutionContext{Identity: want, CWD: t.TempDir()}
	var seen []ExecutionContext
	var stages []string
	record := func(stage string, execution Execution) {
		stages = append(stages, stage)
		seen = append(seen, ExecutionContext{Identity: execution.Identity, CWD: execution.CWD})
	}
	definition := echoDefinition("identity")
	invoke := definition.Execute
	definition.Execute = func(ctx context.Context, execution Execution) (ExecutionResult, error) {
		record("tool", execution)
		if string(execution.Arguments) != `{"text":"changed"}` {
			t.Errorf("dispatch arguments = %s", execution.Arguments)
		}
		return invoke(ctx, execution)
	}
	if _, err := runtime.Register(owned, definition); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterMiddleware(owned, Middleware{
		ID: "identity",
		Pre: func(_ context.Context, execution Execution) (Execution, error) {
			record("pre", execution)
			execution.Arguments = json.RawMessage(`{"text":"changed"}`)
			return execution, nil
		},
		Execute: func(_ context.Context, plan ExecutePlan) (ExecutePlan, error) {
			record("execute", plan.Execution)
			inner := plan.Invoke
			plan.Invoke = func(ctx context.Context, execution Execution) (ExecutionResult, error) {
				record("wrapper", execution)
				return inner(ctx, execution)
			}
			return plan, nil
		},
		Post: func(_ context.Context, execution Execution, result ExecutionResult) (ExecutionResult, error) {
			record("post", execution)
			return result, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	// A reused snapshot never holds mutable identity shared by its calls.
	for _, attributed := range []bool{true, false} {
		call := protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{"text":"original"}`}
		var prepared *PreparedCall
		executionContext := wantContext
		if attributed {
			prepared, err = snapshot.PrepareWithContext(call, executionContext)
		} else {
			prepared, err = snapshot.Prepare(call)
		}
		if err != nil {
			t.Fatal(err)
		}
		executionContext.Identity.AgentID = "caller mutation"
		executionContext.CWD = "caller mutation"
		call.Name = "caller mutation"
		seen, stages = nil, nil
		result, err := prepared.Execute(context.Background())
		if err != nil || result.IsError {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		if fmt.Sprint(stages) != "[pre execute wrapper tool post]" {
			t.Fatalf("stages = %v", stages)
		}
		expected := wantContext
		if !attributed {
			expected = ExecutionContext{}
		}
		for _, got := range seen {
			if got != expected {
				t.Errorf("identity = %+v, want %+v", got, expected)
			}
		}
		if _, err := prepared.Execute(context.Background()); !errors.Is(err, contracts.ErrAlreadyUsed) {
			t.Errorf("second execution = %v", err)
		}
		prepared.Close()
	}
}

func TestToolExecutionIdentityRejectsMiddlewareMutation(t *testing.T) {
	mutations := map[string]func(*Execution){
		"agent":   func(i *Execution) { i.Identity.AgentID = "other" },
		"session": func(i *Execution) { i.Identity.SessionID = "other" },
		"turn":    func(i *Execution) { i.Identity.Turn++ },
		"step":    func(i *Execution) { i.Identity.Step++ },
		"cwd":     func(i *Execution) { i.CWD = t.TempDir() },
	}
	for _, stage := range []string{"pre", "execute", "wrapper"} {
		for field, mutate := range mutations {
			t.Run(stage+"/"+field, func(t *testing.T) {
				runtime := NewRuntime()
				defer runtime.Close()
				owned := newTestOwner("mutation", 1)
				invoked := false
				definition := echoDefinition("mutation")
				definition.Execute = JSONTool(func(context.Context, Execution) (json.RawMessage, error) {
					invoked = true
					return json.RawMessage(`{"value":"unexpected"}`), nil
				})
				if _, err := runtime.Register(owned, definition); err != nil {
					t.Fatal(err)
				}
				middleware := Middleware{ID: "mutation"}
				if stage == "pre" {
					middleware.Pre = func(_ context.Context, execution Execution) (Execution, error) {
						mutate(&execution)
						return execution, nil
					}
				} else if stage == "execute" {
					middleware.Execute = func(_ context.Context, plan ExecutePlan) (ExecutePlan, error) {
						mutate(&plan.Execution)
						return plan, nil
					}
				} else {
					middleware.Execute = func(_ context.Context, plan ExecutePlan) (ExecutePlan, error) {
						invoke := plan.Invoke
						plan.Invoke = func(ctx context.Context, execution Execution) (ExecutionResult, error) {
							mutate(&execution)
							return invoke(ctx, execution)
						}
						return plan, nil
					}
				}
				if _, err := runtime.RegisterMiddleware(owned, middleware); err != nil {
					t.Fatal(err)
				}
				snapshot, err := runtime.Prepare()
				if err != nil {
					t.Fatal(err)
				}
				defer snapshot.Close()
				call, err := snapshot.PrepareWithContext(
					protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{"text":"hello"}`},
					ExecutionContext{Identity: ExecutionIdentity{AgentID: "agent", SessionID: "session", Turn: 1, Step: 1}, CWD: t.TempDir()},
				)
				if err != nil {
					t.Fatal(err)
				}
				defer call.Close()
				result, err := call.Execute(context.Background())
				message := "mutation"
				if stage == "wrapper" {
					message = "identity"
				}
				if err != nil || !result.IsError || invoked || !strings.Contains(result.Error.Message, message) {
					t.Fatalf("identity mutation dispatched: result=%+v, err=%v, invoked=%v", result, err, invoked)
				}
			})
		}
	}
}

func TestToolExecutionIdentityConcurrentSnapshotContexts(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	owned := newTestOwner("shared-snapshot", 1)
	cwds := map[protocol.SessionID]string{"cancelled": t.TempDir(), "sibling": t.TempDir()}
	type markerKey struct{}
	check := func(ctx context.Context, execution Execution) {
		if execution.CWD != cwds[execution.Identity.SessionID] {
			t.Errorf("cwd crossed calls: %+v", execution)
		}
		if ctx.Value(markerKey{}) != execution.Identity.SessionID {
			t.Errorf("context crossed calls: identity=%+v, marker=%v", execution.Identity, ctx.Value(markerKey{}))
		}
	}
	entered := make(chan protocol.SessionID, 2)
	release := make(chan struct{})
	if _, err := runtime.Register(owned, Definition{
		Name: "tool",
		Execute: JSONTool(func(ctx context.Context, execution Execution) (json.RawMessage, error) {
			check(ctx, execution)
			entered <- execution.Identity.SessionID
			if execution.Identity.SessionID == "cancelled" {
				<-ctx.Done()
			} else {
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return json.Marshal(execution.Identity.SessionID)
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RegisterMiddleware(owned, Middleware{
		ID: "contexts",
		Pre: func(ctx context.Context, execution Execution) (Execution, error) {
			check(ctx, execution)
			return execution, nil
		},
		Execute: func(ctx context.Context, plan ExecutePlan) (ExecutePlan, error) {
			check(ctx, plan.Execution)
			return plan, nil
		},
		Post: func(ctx context.Context, execution Execution, result ExecutionResult) (ExecutionResult, error) {
			check(ctx, execution)
			if ctx.Err() != nil {
				t.Error("post context is cancelled")
			}
			return result, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	ctx, cancelAll := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAll()
	cancelledCtx, cancelOne := context.WithCancel(ctx)
	defer cancelOne()
	type outcome struct {
		id     protocol.SessionID
		result ExecutionResult
		err    error
	}
	done := make(chan outcome, 2)
	for _, id := range []protocol.SessionID{"cancelled", "sibling"} {
		call, err := snapshot.PrepareWithContext(
			protocol.ToolCall{ID: "same-call", Name: "tool", Arguments: `{}`},
			ExecutionContext{Identity: ExecutionIdentity{AgentID: string(id), SessionID: id, Turn: 1, Step: 1}, CWD: cwds[id]},
		)
		if err != nil {
			t.Fatal(err)
		}
		callCtx := ctx
		if id == "cancelled" {
			callCtx = cancelledCtx
		}
		callCtx = context.WithValue(callCtx, markerKey{}, id)
		go func() {
			defer call.Close()
			result, err := call.Execute(callCtx)
			done <- outcome{id, result, err}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("concurrent calls did not enter", ctx.Err())
		}
	}
	cancelOne()
	close(release)
	for range 2 {
		select {
		case got := <-done:
			want, _ := json.Marshal(got.id)
			if got.err != nil || got.result.IsError || string(got.result.Value) != string(want) {
				t.Errorf("outcome = %+v", got)
			}
		case <-ctx.Done():
			t.Fatal("calls did not drain", ctx.Err())
		}
	}
}
