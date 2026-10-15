package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type bindingProvider struct{ runtime *Runtime }

func (p *bindingProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.tools-provider", Provides: []gordis.ServiceSpec{RuntimeKey.Spec()}, New: func() gordis.Plugin { return &bindingProvider{runtime: p.runtime} }}
}
func (p *bindingProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(RuntimeKey, p.runtime)
}

type bindingProbe struct {
	ready    chan<- bindingProbeResult
	activate func(Binding, *gordis.Scope) (func(), error)
}
type bindingProbeResult struct {
	binding Binding
	scope   *gordis.Scope
	dispose func()
	closed  <-chan struct{}
}

func (p *bindingProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.tools-contributor", Requires: []gordis.ServiceSpec{RuntimeKey.Spec()}, New: func() gordis.Plugin { return &bindingProbe{ready: p.ready, activate: p.activate} }}
}
func (p *bindingProbe) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(RuntimeKey)
	if err != nil {
		return err
	}
	binding := runtime.Bind(scope)
	var dispose func()
	if p.activate != nil {
		dispose, err = p.activate(binding, scope)
		if err != nil {
			return err
		}
	}
	closed := make(chan struct{})
	if err := scope.Defer("close plugin resource", func(context.Context) error { close(closed); return nil }); err != nil {
		return err
	}
	p.ready <- bindingProbeResult{binding: binding, scope: scope, dispose: dispose, closed: closed}
	return nil
}

type bindingFixture struct {
	ctx     context.Context
	host    *gordis.Host
	runtime *Runtime
	ready   <-chan bindingProbeResult
}

func newBindingFixture(t *testing.T, activate func(Binding, *gordis.Scope) (func(), error)) *bindingFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	runtime := NewRuntime()
	t.Cleanup(runtime.Close)
	ready := make(chan bindingProbeResult, 1)
	host, err := gordis.NewHost(&bindingProvider{runtime: runtime}, &bindingProbe{ready: ready, activate: activate})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "tools", Plugin: "test.tools-provider"}); err != nil {
		t.Fatal(err)
	}
	return &bindingFixture{ctx: ctx, host: host, runtime: runtime, ready: ready}
}
func (f *bindingFixture) mount(t *testing.T, id string) (*gordis.Instance, bindingProbeResult) {
	t.Helper()
	instance, err := f.host.Env().MountReady(f.ctx, gordis.InstanceSpec{ID: id, Plugin: "test.tools-contributor"})
	if err != nil {
		t.Fatal(err)
	}
	return instance, <-f.ready
}
func bindingDefinition(name, version string) Definition {
	d := echoDefinition(version)
	d.Name = name
	return d
}
func assertBindingNames(t *testing.T, runtime *Runtime, want ...string) {
	t.Helper()
	var got []string
	for _, schema := range runtime.Schemas() {
		got = append(got, schema.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}
func executeBindingTool(t *testing.T, snapshot *Snapshot, name string) ExecutionResult {
	t.Helper()
	call, err := snapshot.Prepare(protocol.ToolCall{ID: "bound-call", Name: name, Arguments: `{"text":"hello"}`})
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

func TestBindingScopesWithdrawAndDrainExactGeneration(t *testing.T) {
	var middlewareCalls atomic.Int32
	f := newBindingFixture(t, func(binding Binding, scope *gordis.Scope) (func(), error) {
		if scope.ID() == "a" {
			if _, err := binding.RegisterMiddleware(Middleware{ID: "a", Pre: func(_ context.Context, execution Execution) (Execution, error) {
				middlewareCalls.Add(1)
				return execution, nil
			}}); err != nil {
				return nil, err
			}
			return binding.Register(bindingDefinition("echo", fmt.Sprint(scope.Generation())))
		}
		return binding.RegisterBatch([]Definition{bindingDefinition("other", "b"), bindingDefinition("extra", "b")})
	})
	a, first := f.mount(t, "a")
	_, other := f.mount(t, "b")
	assertBindingNames(t, f.runtime, "echo", "other", "extra")
	snapshot, err := f.runtime.PrepareSelected([]string{"echo"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.Close)
	done := make(chan error, 1)
	go func() { done <- a.Unmount(f.ctx) }()
	select {
	case <-first.scope.Context().Done():
	case <-f.ctx.Done():
		t.Fatal("freeze not reached:", f.ctx.Err())
	}
	select {
	case <-first.closed:
		t.Fatal("plugin resource closed before snapshot drained")
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("stop overtook snapshot: %v", err)
	default:
	}
	assertBindingNames(t, f.runtime, "other", "extra")
	if got := executeBindingTool(t, snapshot, "echo"); string(got.Value) != fmt.Sprintf(`{"value":"%d:hello"}`, first.scope.Generation()) {
		t.Fatalf("frozen execution = %s", got.Value)
	}
	if middlewareCalls.Load() != 1 {
		t.Fatal("frozen middleware was lost")
	}
	current, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(current.Close)
	if got := executeBindingTool(t, current, "other"); string(got.Value) != `{"value":"b:hello"}` {
		t.Fatalf("other Scope result = %s", got.Value)
	}
	current.Close()
	if middlewareCalls.Load() != 1 {
		t.Fatal("stopped Scope middleware leaked into another Scope")
	}
	select {
	case <-other.closed:
		t.Fatal("stopping A closed B")
	default:
	}
	assertBindingRegistrationErrors(t, first.binding, gordis.ErrClosed)
	snapshot.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal("stop did not drain:", f.ctx.Err())
	}
	select {
	case <-first.closed:
	default:
		t.Fatal("plugin resource did not close after drain")
	}
	_, replacement := f.mount(t, "a")
	if replacement.scope.Generation() == first.scope.Generation() {
		t.Fatal("remount reused generation")
	}
	first.dispose()
	assertBindingNames(t, f.runtime, "echo", "other", "extra")
	newest, err := f.runtime.PrepareSelected([]string{"echo"})
	if err != nil {
		t.Fatal(err)
	}
	defer newest.Close()
	if got := executeBindingTool(t, newest, "echo"); string(got.Value) != fmt.Sprintf(`{"value":"%d:hello"}`, replacement.scope.Generation()) {
		t.Fatalf("old disposer removed replacement: %s", got.Value)
	}
}

func assertBindingRegistrationErrors(t *testing.T, binding Binding, want error) {
	t.Helper()
	operations := []struct {
		name     string
		register func() (func(), error)
	}{
		{"Register", func() (func(), error) { return binding.Register(bindingDefinition("rejected", "v")) }},
		{"RegisterBatch", func() (func(), error) {
			return binding.RegisterBatch([]Definition{bindingDefinition("rejected-batch", "v")})
		}},
		{"RegisterMiddleware", func() (func(), error) {
			return binding.RegisterMiddleware(Middleware{ID: "rejected-middleware", Pre: func(_ context.Context, e Execution) (Execution, error) { return e, nil }})
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			dispose, err := op.register()
			if dispose != nil {
				dispose()
			}
			if dispose != nil || !errors.Is(err, want) {
				t.Fatalf("registration returned disposer: %t, error: %v; want %v", dispose != nil, err, want)
			}
		})
	}
}

func TestBindingUnavailableAndClosed(t *testing.T) {
	f := newBindingFixture(t, nil)
	instance, live := f.mount(t, "caller")
	// Bind alone has no Ready lease and cannot delay stop.
	if err := instance.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertBindingRegistrationErrors(t, live.binding, gordis.ErrClosed)
	_, active := f.mount(t, "active")
	var nilRuntime *Runtime
	closed := NewRuntime()
	closed.Close()
	for _, tc := range []struct {
		name    string
		binding Binding
		want    error
	}{
		{"zero binding", Binding{}, contracts.ErrClosed},
		{"nil runtime", nilRuntime.Bind(active.scope), contracts.ErrClosed},
		{"zero runtime", new(Runtime).Bind(active.scope), contracts.ErrClosed},
		{"closed runtime", closed.Bind(active.scope), contracts.ErrClosed},
		{"nil Scope", f.runtime.Bind(nil), contracts.ErrOwnerUnavailable},
		{"invalid Scope", f.runtime.Bind(&gordis.Scope{}), contracts.ErrOwnerUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) { assertBindingRegistrationErrors(t, tc.binding, tc.want) })
	}
	assertBindingNames(t, f.runtime)
}

func TestBindingBatchFailureAndStaleDisposer(t *testing.T) {
	f := newBindingFixture(t, nil)
	_, scope := f.mount(t, "batch")
	binding := scope.binding
	invalid := bindingDefinition("broken", "v")
	invalid.Parameters = JSONSchema(`{"type":`)
	if _, err := binding.RegisterBatch([]Definition{bindingDefinition("partial", "v"), invalid}); err == nil {
		t.Fatal("invalid batch registered")
	}
	assertBindingNames(t, f.runtime)
	dispose, err := binding.RegisterBatch([]Definition{bindingDefinition("one", "v1"), bindingDefinition("two", "v1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.RegisterBatch([]Definition{bindingDefinition("partial", "v"), bindingDefinition("two", "conflict")}); err == nil {
		t.Fatal("conflicting batch registered")
	}
	assertBindingNames(t, f.runtime, "one", "two")
	dispose()
	if _, err := binding.RegisterBatch([]Definition{bindingDefinition("one", "v2"), bindingDefinition("two", "v2")}); err != nil {
		t.Fatal(err)
	}
	dispose()
	assertBindingNames(t, f.runtime, "one", "two")
}

func TestBindingConcurrentRegistrationsKeepScopeOwners(t *testing.T) {
	f := newBindingFixture(t, nil)
	a, first := f.mount(t, "a")
	_, second := f.mount(t, "b")
	bindings := []Binding{first.binding, second.binding}
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			_, err := bindings[i%2].Register(bindingDefinition(fmt.Sprintf("tool-%02d", i), "v"))
			done <- err
		}(i)
	}
	for i := 0; i < 16; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	if err := a.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 1; i < 16; i += 2 {
		want = append(want, fmt.Sprintf("tool-%02d", i))
	}
	assertBindingNames(t, f.runtime, want...)
}
