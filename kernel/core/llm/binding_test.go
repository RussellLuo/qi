package llm_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type llmServicePlugin struct{ runtime *llm.Runtime }

func (p *llmServicePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.llm-service",
		Provides: []gordis.ServiceSpec{llm.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return &llmServicePlugin{runtime: p.runtime} },
	}
}

func (p *llmServicePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("close shared LLM runtime", func(context.Context) error {
		p.runtime.Close()
		return nil
	}); err != nil {
		return err
	}
	return scope.Provide(llm.RuntimeKey, p.runtime)
}

type llmScopeProbe struct {
	requires bool
	scopes   chan *gordis.Scope
	activate func(*gordis.Scope) error
}

func (p *llmScopeProbe) Spec() gordis.PluginSpec {
	spec := gordis.PluginSpec{
		ID: "test.llm-scope",
		New: func() gordis.Plugin {
			return &llmScopeProbe{requires: p.requires, scopes: p.scopes, activate: p.activate}
		},
	}
	if p.requires {
		spec.Requires = []gordis.ServiceSpec{llm.RuntimeKey.Spec()}
	}
	return spec
}

func (p *llmScopeProbe) Activate(_ context.Context, scope *gordis.Scope) error {
	if p.activate != nil {
		if err := p.activate(scope); err != nil {
			return err
		}
	}
	p.scopes <- scope
	return nil
}

type localLLM struct {
	answer string
	closed atomic.Bool
}

func (a *localLLM) Close() { a.closed.Store(true) }

func (a *localLLM) Stream(_ context.Context, _ llm.Request) contracts.ChunkStream {
	return func(yield func(protocol.StreamChunk, error) bool) {
		if a.closed.Load() {
			yield(protocol.StreamChunk{}, errors.New("transport is closed"))
			return
		}
		if !yield(protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: a.answer}, nil) {
			return
		}
		yield(protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}, nil)
	}
}

type llmHostFixture struct {
	ctx     context.Context
	host    *gordis.Host
	runtime *llm.Runtime
	scopes  chan *gordis.Scope
}

func newLLMHostFixture(t *testing.T, requires bool, activate ...func(*llm.Runtime, *gordis.Scope) error) *llmHostFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	f := &llmHostFixture{ctx: ctx, runtime: llm.NewRuntime(), scopes: make(chan *gordis.Scope, 8)}
	probe := &llmScopeProbe{requires: requires, scopes: f.scopes}
	if len(activate) != 0 {
		probe.activate = func(scope *gordis.Scope) error {
			runtime, err := scope.Get(llm.RuntimeKey)
			if err != nil {
				return err
			}
			return activate[0](runtime, scope)
		}
	}
	host, err := gordis.NewHost(&llmServicePlugin{runtime: f.runtime}, probe)
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	t.Cleanup(func() {
		if err := host.Shutdown(f.ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Env().MountReady(f.ctx, gordis.InstanceSpec{ID: "llm", Plugin: "test.llm-service"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *llmHostFixture) mount(t *testing.T, id string) (*gordis.Instance, *gordis.Scope) {
	t.Helper()
	instance, err := f.host.Env().MountReady(f.ctx, gordis.InstanceSpec{ID: id, Plugin: "test.llm-scope"})
	if err != nil {
		t.Fatal(err)
	}
	return instance, <-f.scopes
}

func llmRegistration(route, provider, answer string) llm.AdapterRegistration {
	return llm.AdapterRegistration{
		Route:    route,
		Provider: provider,
		Models:   []string{"text"},
		Adapter:  &localLLM{answer: answer},
	}
}

func assertLLMAnswer(t *testing.T, runtime *llm.Runtime, provider, answer string) {
	t.Helper()
	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: provider, Model: "text"})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	assertPreparedAnswer(t, call, answer)
}

func assertPreparedAnswer(t *testing.T, call *llm.PreparedCall, answer string) {
	t.Helper()
	result, err := llm.Accumulate(
		"answer",
		call.Stream(context.Background(), llm.Request{Config: call.Config()}),
		protocol.MessageSource{
			Kind:     protocol.SourceModel,
			Provider: call.Config().Provider,
			Model:    call.Config().Model,
		},
	)
	if err != nil || result.Message.Content[0].(protocol.TextBlock).Text != answer || result.Finish.Kind != protocol.FinishStop {
		t.Fatalf("answer/result/error = %q / %+v / %v", answer, result, err)
	}
}

func TestBindingAdapterDuplicateAndStaleDisposerKeepReplacement(t *testing.T) {
	f := newLLMHostFixture(t, true)
	first, a := f.mount(t, "first")
	_, b := f.mount(t, "second")
	dispose, err := f.runtime.Bind(a).RegisterAdapter(llmRegistration("text", "local", "first"))
	if err != nil {
		t.Fatal(err)
	}
	if rejected, err := f.runtime.Bind(b).RegisterAdapter(llmRegistration("text", "local", "duplicate")); !errors.Is(err, duplicateLLMRegistrationError(t)) || rejected != nil {
		t.Fatalf("duplicate disposer/error = %v / %v", rejected != nil, err)
	}
	assertLLMAnswer(t, f.runtime, "local", "first")
	dispose()
	if _, err := f.runtime.Bind(b).RegisterAdapter(llmRegistration("text", "local", "replacement")); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			dispose()
		}()
	}
	wait.Wait()
	if err := first.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertLLMAnswer(t, f.runtime, "local", "replacement")
	if _, err := f.runtime.Bind(a).RegisterAdapter(llmRegistration("late", "late", "late")); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("stopped owner admitted registration", err)
	}
	if err := f.runtime.Bind(a).RegisterProvider(llmRegistration("late", "late", "late"), nil); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("stopped owner admitted provider", err)
	}
}

func TestBindingProviderCatalogFailureRollsBackOnlyCurrentAdapter(t *testing.T) {
	f := newLLMHostFixture(t, true)
	_, existing := f.mount(t, "existing")
	failed, candidate := f.mount(t, "candidate")
	want := modelcatalog.Spec{ID: "shared", Label: "Existing", Route: "existing", Provider: "existing", Model: "text"}
	if err := f.runtime.Bind(existing).RegisterProvider(llmRegistration("existing", "existing", "existing"), []modelcatalog.Spec{want}); err != nil {
		t.Fatal(err)
	}
	for _, models := range [][]modelcatalog.Spec{
		{{ID: "new-only"}, {ID: "shared"}},   // Existing ID conflicts; no partial batch.
		{{ID: "repeated"}, {ID: "repeated"}}, // Duplicate in the new batch.
	} {
		if err := f.runtime.Bind(candidate).RegisterProvider(llmRegistration("candidate", "candidate", "failed"), models); !errors.Is(err, duplicateLLMRegistrationError(t)) {
			t.Fatalf("catalog conflict = %v", err)
		}
		if call, err := f.runtime.PrepareCall(
			context.Background(),
			protocol.CallConfig{Provider: "candidate", Model: "text"},
		); !errors.Is(err, llm.ErrRouteNotFound) {
			if call != nil {
				call.Close()
			}
			t.Fatal("failed catalog left an adapter", err)
		}
		catalog, _, err := f.runtime.ModelCatalog()
		if err != nil || !reflect.DeepEqual(catalog, []modelcatalog.Spec{want}) {
			t.Fatalf("failed batch changed existing catalog: %+v / %v", catalog, err)
		}
	}
	// Stale rollback callbacks must not remove a later same-route registration.
	if _, err := f.runtime.Bind(existing).RegisterAdapter(llmRegistration("candidate", "candidate", "replacement")); err != nil {
		t.Fatal(err)
	}
	if err := failed.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertLLMAnswer(t, f.runtime, "candidate", "replacement")
	assertLLMAnswer(t, f.runtime, "existing", "existing")
	// Adapter conflict must not publish the supplied catalog either.
	_, duplicate := f.mount(t, "duplicate")
	if err := f.runtime.Bind(duplicate).RegisterProvider(llmRegistration("existing", "other", "duplicate"),
		[]modelcatalog.Spec{{ID: "orphan"}},
	); !errors.Is(err, duplicateLLMRegistrationError(t)) {
		t.Fatal(err)
	}
	if catalog, _, err := f.runtime.ModelCatalog(); err != nil || !reflect.DeepEqual(catalog, []modelcatalog.Spec{want}) {
		t.Fatalf("adapter conflict published catalog: %+v / %v", catalog, err)
	}
}

func TestBindingProviderStopDrainsSnapshotBeforePluginClosesTransport(t *testing.T) {
	f := newLLMHostFixture(t, true)
	instance, scope := f.mount(t, "provider")
	a := &localLLM{answer: "leased answer"}
	if err := scope.Defer("close owned transport", func(context.Context) error {
		a.Close()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	registration := llmRegistration("text", "local", "unused")
	registration.Adapter = a
	if err := f.runtime.Bind(scope).RegisterProvider(registration, []modelcatalog.Spec{{ID: "text", Route: "text", Provider: "local", Model: "text"}}); err != nil {
		t.Fatal(err)
	}
	call, err := f.runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "text"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(call.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- instance.Unmount(ctx) }()
	select {
	case <-scope.Context().Done():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		t.Fatalf("Scope stop overtook its prepared snapshot: %v", err)
	default:
	}
	if a.closed.Load() {
		t.Fatal("transport closed before leased snapshot drained")
	}
	if _, err := f.runtime.PrepareCall(ctx, protocol.CallConfig{Provider: "local", Model: "text"}); err == nil {
		t.Fatal("stopping Scope admitted a fresh snapshot")
	}
	assertPreparedAnswer(t, call, "leased answer")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !a.closed.Load() {
		t.Fatal("plugin's own transport cleanup did not run")
	}
	if catalog, _, err := f.runtime.ModelCatalog(); err != nil || len(catalog) != 0 {
		t.Fatalf("catalog survived stop: %+v / %v", catalog, err)
	}
	if _, err := f.runtime.PrepareCall(ctx, protocol.CallConfig{Provider: "local", Model: "text"}); !errors.Is(err, llm.ErrRouteNotFound) {
		t.Fatal("adapter survived stop", err)
	}
}

func TestBindingAdapterDisposerPreservesLeasedGenerationAndDoesNotCloseAdapter(t *testing.T) {
	f := newLLMHostFixture(t, true)
	_, scope := f.mount(t, "provider")
	a := &localLLM{answer: "old"}
	registration := llmRegistration("text", "local", "unused")
	registration.Adapter = a
	dispose, err := f.runtime.Bind(scope).RegisterAdapter(registration)
	if err != nil {
		t.Fatal(err)
	}
	call, err := f.runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "text"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(call.Close)
	dispose()
	if a.closed.Load() {
		t.Fatal("helper closed a plugin-owned adapter")
	}
	if _, err := f.runtime.Bind(scope).RegisterAdapter(llmRegistration("text", "local", "new")); err != nil {
		t.Fatal(err)
	}
	assertPreparedAnswer(t, call, "old")
	assertLLMAnswer(t, f.runtime, "local", "new")
}

// Obtain the shared registration error through the public Runtime contract.
// Binding tests use the public Runtime contract.
func duplicateLLMRegistrationError(t *testing.T) error {
	t.Helper()
	f := newLLMHostFixture(t, true)
	_, scope := f.mount(t, "duplicate-error")
	owned := f.runtime.Bind(scope)
	registration := llmRegistration("duplicate-error", "local", "answer")
	if _, err := owned.RegisterAdapter(registration); err != nil {
		t.Fatal(err)
	}
	_, err := owned.RegisterAdapter(registration)
	if err == nil {
		t.Fatal("Runtime admitted duplicate registration")
	}
	return errors.Unwrap(err)
}

func assertBindingErrors(t *testing.T, b llm.Binding, want error) {
	t.Helper()
	registration := llmRegistration("rejected", "rejected", "rejected")
	dispose, err := b.RegisterAdapter(registration)
	if dispose != nil {
		dispose()
	}
	if dispose != nil || !errors.Is(err, want) {
		t.Fatalf("adapter dispose/error = %t / %v, want %v", dispose != nil, err, want)
	}
	for name, register := range map[string]func() error{
		"models":   func() error { return b.RegisterModels([]modelcatalog.Spec{{ID: "rejected"}}) },
		"defaults": func() error { return b.RegisterDefaults(modelcatalog.Defaults{Model: "rejected"}) },
		"provider": func() error { return b.RegisterProvider(registration, []modelcatalog.Spec{{ID: "rejected"}}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := register(); !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestBindingUnavailableClosedAndDoesNotLease(t *testing.T) {
	f := newLLMHostFixture(t, true)
	instance, scope := f.mount(t, "caller")
	captured := f.runtime.Bind(scope)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := instance.Unmount(ctx); err != nil {
		t.Fatal("Bind held a lease", err)
	}
	assertBindingErrors(t, captured, gordis.ErrClosed)
	var nilRuntime *llm.Runtime
	closed := llm.NewRuntime()
	closed.Close()
	for name, b := range map[string]llm.Binding{
		"zero Binding": {}, "nil Runtime": nilRuntime.Bind(scope), "zero Runtime": new(llm.Runtime).Bind(scope), "closed Runtime": closed.Bind(scope),
	} {
		t.Run(name, func(t *testing.T) { assertBindingErrors(t, b, contracts.ErrClosed) })
	}
	for name, scope := range map[string]*gordis.Scope{"nil Scope": nil, "invalid Scope": {}} {
		t.Run(name, func(t *testing.T) { assertBindingErrors(t, f.runtime.Bind(scope), contracts.ErrOwnerUnavailable) })
	}
	// The low-level catalog API treats an empty model list as no contribution.
	if err := f.runtime.Bind(scope).RegisterModels(nil); err != nil {
		t.Fatal(err)
	}
	if catalog, selected, err := f.runtime.ModelCatalog(); err != nil || len(catalog) != 0 || selected != "" {
		t.Fatal(catalog, selected, err)
	}
}

func TestBindingActivateScopesCatalogDefaultsAndGeneration(t *testing.T) {
	var oldDispose func()
	f := newLLMHostFixture(t, true, func(runtime *llm.Runtime, scope *gordis.Scope) error {
		b := runtime.Bind(scope)
		registration := llmRegistration(scope.ID(), scope.ID(), fmt.Sprint(scope.Generation()))
		if scope.ID() == "a" {
			dispose, err := b.RegisterAdapter(registration)
			if err != nil {
				return err
			}
			if oldDispose == nil {
				oldDispose = dispose
			}
			if err := b.RegisterModels([]modelcatalog.Spec{{ID: "a", Route: "a", Provider: "a", Model: "text"}}); err != nil {
				return err
			}
			return b.RegisterDefaults(modelcatalog.Defaults{Model: "a"})
		}
		return b.RegisterProvider(registration, []modelcatalog.Spec{{ID: "b", Route: "b", Provider: "b", Model: "text"}})
	})
	a, first := f.mount(t, "a")
	_, second := f.mount(t, "b")
	if catalog, selected, err := f.runtime.ModelCatalog(); err != nil || len(catalog) != 2 || selected != "a" {
		t.Fatal(catalog, selected, err)
	}
	call, err := f.runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "a", Model: "text"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(call.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Unmount(ctx) }()
	select {
	case <-first.Context().Done():
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		t.Fatal("stop overtook call", err)
	default:
	}
	assertBindingErrors(t, f.runtime.Bind(first), gordis.ErrClosed)
	assertPreparedAnswer(t, call, fmt.Sprint(first.Generation()))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if catalog, selected, err := f.runtime.ModelCatalog(); err != nil || len(catalog) != 1 || catalog[0].ID != "b" || selected != "" {
		t.Fatal(catalog, selected, err)
	}
	assertLLMAnswer(t, f.runtime, "b", fmt.Sprint(second.Generation()))
	_, replacement := f.mount(t, "a")
	if replacement.Generation() == first.Generation() {
		t.Fatal("generation reused")
	}
	oldDispose()
	assertLLMAnswer(t, f.runtime, "a", fmt.Sprint(replacement.Generation()))
}

func TestBindingConcurrentRegistrationsKeepScopes(t *testing.T) {
	f := newLLMHostFixture(t, true)
	a, first := f.mount(t, "a")
	_, second := f.mount(t, "b")
	bindings := []llm.Binding{f.runtime.Bind(first), f.runtime.Bind(second)}
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			name := fmt.Sprintf("route-%02d", i)
			done <- bindings[i%2].RegisterProvider(llmRegistration(name, name, name), []modelcatalog.Spec{{ID: name, Route: name, Provider: name, Model: "text"}})
		}(i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 16; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := a.Unmount(ctx); err != nil {
		t.Fatal(err)
	}
	if catalog, _, err := f.runtime.ModelCatalog(); err != nil || len(catalog) != 8 {
		t.Fatal(catalog, err)
	}
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("route-%02d", i)
		if i%2 == 1 {
			assertLLMAnswer(t, f.runtime, name, name)
		} else {
			if call, err := f.runtime.PrepareCall(ctx, protocol.CallConfig{Provider: name, Model: "text"}); !errors.Is(err, llm.ErrRouteNotFound) {
				if call != nil {
					call.Close()
				}
				t.Fatal("A survived stop", err)
			}
		}
	}
}

// Service selection remains scope.Get's responsibility. Binding neither declares
// dependencies nor supplies a missing provider.
func TestBindingServiceDependencyIsExplicit(t *testing.T) {
	for _, scope := range []*gordis.Scope{nil, {}} {
		if _, err := scope.Get(llm.RuntimeKey); !errors.Is(err, gordis.ErrClosed) {
			t.Fatal(err)
		}
	}
	f := newLLMHostFixture(t, false)
	_, scope := f.mount(t, "undeclared")
	if _, err := scope.Get(llm.RuntimeKey); err == nil {
		t.Fatal("undeclared dependency admitted service access")
	}
	probe := &llmScopeProbe{requires: true, scopes: make(chan *gordis.Scope, 1)}
	host, err := gordis.NewHost(probe)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "missing", Plugin: "test.llm-scope"}); err == nil || len(probe.scopes) != 0 {
		t.Fatal("missing LLM service activated or was implicitly supplied", err)
	}
}
