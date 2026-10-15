package agentmiddleware

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
)

type bindingProvider struct{ runtime *Runtime }

func (p *bindingProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.agentmiddleware-provider", Provides: []gordis.ServiceSpec{RuntimeKey.Spec()}, New: func() gordis.Plugin { return &bindingProvider{runtime: p.runtime} }}
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
	return gordis.PluginSpec{ID: "test.agentmiddleware-contributor", Requires: []gordis.ServiceSpec{RuntimeKey.Spec()}, New: func() gordis.Plugin { return &bindingProbe{ready: p.ready, activate: p.activate} }}
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
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "tools", Plugin: "test.agentmiddleware-provider"}); err != nil {
		t.Fatal(err)
	}
	return &bindingFixture{ctx: ctx, host: host, runtime: runtime, ready: ready}
}
func (f *bindingFixture) mount(t *testing.T, id string) (*gordis.Instance, bindingProbeResult) {
	t.Helper()
	instance, err := f.host.Env().MountReady(f.ctx, gordis.InstanceSpec{ID: id, Plugin: "test.agentmiddleware-contributor"})
	if err != nil {
		t.Fatal(err)
	}
	return instance, <-f.ready
}
func bindingContribution(id, value string) Middleware {
	return Middleware{ID: id, Request: func(_ context.Context, r Request) (Request, error) { r.AgentID += value; return r, nil }}
}
func bindingSnapshotText(t *testing.T, p *Snapshot) string {
	t.Helper()
	result, err := p.Request(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	return result.AgentID
}
func TestBindingScopesWithdrawAndDrainExactGeneration(t *testing.T) {
	f := newBindingFixture(t, func(b Binding, s *gordis.Scope) (func(), error) {
		return b.Register(bindingContribution(s.ID(), fmt.Sprint(s.Generation())))
	})
	a, first := f.mount(t, "a")
	_, other := f.mount(t, "b")
	snapshot, err := f.runtime.PrepareSelected([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.Close)
	done := make(chan error, 1)
	go func() { done <- a.Unmount(f.ctx) }()
	select {
	case <-first.scope.Context().Done():
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case <-first.closed:
		t.Fatal("resource closed before drain")
	default:
	}
	select {
	case err := <-done:
		t.Fatalf("stop overtook snapshot: %v", err)
	default:
	}
	if _, err := first.binding.Register(bindingContribution("late", "late")); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("frozen Scope admitted contribution", err)
	}
	if got := bindingSnapshotText(t, snapshot); got != fmt.Sprint(first.scope.Generation()) {
		t.Fatal("frozen contribution lost", got)
	}
	current, err := f.runtime.PrepareSelected([]string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := bindingSnapshotText(t, current); got != fmt.Sprint(other.scope.Generation()) {
		t.Fatal("stopping A changed B", got)
	}
	current.Close()
	select {
	case <-other.closed:
		t.Fatal("stopping A closed B")
	default:
	}
	snapshot.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case <-first.closed:
	default:
		t.Fatal("resource not closed after drain")
	}
	_, replacement := f.mount(t, "a")
	if first.scope.Generation() == replacement.scope.Generation() {
		t.Fatal("generation reused")
	}
	first.dispose()
	newest, err := f.runtime.PrepareSelected([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	defer newest.Close()
	if got := bindingSnapshotText(t, newest); got != fmt.Sprint(replacement.scope.Generation()) {
		t.Fatal("old disposer removed replacement", got)
	}
}

func TestBindingUnavailableAndClosed(t *testing.T) {
	f := newBindingFixture(t, nil)
	instance, live := f.mount(t, "caller")
	// Capturing Binding alone never delays stop.
	if err := instance.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	var nilRuntime *Runtime
	closed := NewRuntime()
	closed.Close()
	cases := []struct {
		name string
		b    Binding
		want error
	}{
		{"stopped", live.binding, gordis.ErrClosed},
		{"zero binding", Binding{}, contracts.ErrClosed},
		{"nil Runtime", nilRuntime.Bind(live.scope), contracts.ErrClosed},
		{"zero Runtime", new(Runtime).Bind(live.scope), contracts.ErrClosed},
		{"closed Runtime", closed.Bind(live.scope), contracts.ErrClosed},
		{"nil Scope", f.runtime.Bind(nil), contracts.ErrOwnerUnavailable},
		{"invalid Scope", f.runtime.Bind(&gordis.Scope{}), contracts.ErrOwnerUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispose, err := tc.b.Register(bindingContribution("rejected", "value"))
			if dispose != nil {
				dispose()
			}
			if dispose != nil || !errors.Is(err, tc.want) {
				t.Fatalf("dispose/error = %t / %v, want %v", dispose != nil, err, tc.want)
			}
		})
	}
	p, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := bindingSnapshotText(t, p); got != "" {
		t.Fatal("failed registration remained", got)
	}
}

func TestBindingConcurrentScopesAndExactDisposer(t *testing.T) {
	f := newBindingFixture(t, nil)
	a, first := f.mount(t, "a")
	_, second := f.mount(t, "b")
	bindings := []Binding{first.binding, second.binding}
	done := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func(i int) {
			_, err := bindings[i%2].Register(bindingContribution(fmt.Sprintf("item-%02d", i), "x"))
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
	dispose, err := second.binding.Register(bindingContribution("replacement", "old"))
	if err != nil {
		t.Fatal(err)
	}
	dispose()
	if _, err := second.binding.Register(bindingContribution("replacement", "new")); err != nil {
		t.Fatal(err)
	}
	dispose()
	p, err := f.runtime.PrepareSelected([]string{"replacement"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := bindingSnapshotText(t, p); got != "new" {
		t.Fatal("old disposer erased replacement", got)
	}
	p.Close()
	// All A entries have withdrawn while B entries still render/run.
	all, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer all.Close()
	if got := strings.Count(bindingSnapshotText(t, all), "x"); got != 8 {
		t.Fatalf("remaining B contributions = %d, want 8", got)
	}
}
