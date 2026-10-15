package headless_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	builtin "github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/builtin/cli/headless"
	"github.com/RussellLuo/qi/kernel/boot"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	launchapi "github.com/RussellLuo/qi/kernel/boot/launch"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"gopkg.in/yaml.v3"
)

// This Adapter deliberately implements only Stream, with no model catalog or
// ModelResolver. Requests travel through the real runtime and AgentLoop.
type adapter struct {
	mu       sync.Mutex
	requests []llm.Request
	started  chan struct{}
	drained  chan struct{}
	release  chan struct{}
}

func (a *adapter) Stream(ctx context.Context, request llm.Request) contracts.ChunkStream {
	a.mu.Lock()
	a.requests = append(a.requests, request.Clone())
	turn := len(a.requests)
	a.mu.Unlock()
	return func(yield func(protocol.StreamChunk, error) bool) {
		if a.started != nil {
			close(a.started)
			<-ctx.Done()
			<-a.release
			close(a.drained)
			yield(protocol.StreamChunk{}, ctx.Err())
			return
		}

		text := fmt.Sprintf("answer %d", turn)
		for _, chunk := range []protocol.StreamChunk{
			{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText},
			{Type: protocol.ChunkTextDelta, Index: 0, Text: text},
			{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: text}},
		} {
			if !yield(chunk, nil) {
				return
			}
		}

		yield(protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}, nil)
	}
}

func (a *adapter) seen() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

type modelPlugin struct {
	adapter *adapter
	closed  chan struct{}
}

func (p *modelPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.model",
		New:      func() gordis.Plugin { return &modelPlugin{p.adapter, p.closed} },
		Requires: []gordis.ServiceSpec{llm.RuntimeKey.Spec()},
	}
}

func (*modelPlugin) DescribeModels() (modelcatalog.Description, error) {
	return modelcatalog.Provider("test", "test", []string{"text"}, []modelcatalog.Entry{{ID: "test-text", Model: "text"}})
}

func (p *modelPlugin) Activate(_ context.Context, s *gordis.Scope) error {
	if p.closed != nil {
		if err := s.Defer("model closed", func(context.Context) error {
			close(p.closed)
			return nil
		}); err != nil {
			return err
		}
	}

	runtime, err := s.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	err = runtime.Bind(s).RegisterProvider(llm.AdapterRegistration{Route: "test", Provider: "test", Models: []string{"text"}, Adapter: p.adapter},
		[]modelcatalog.Spec{{ID: "test-text", Route: "test", Provider: "test", Model: "text"}},
	)
	return err
}

type recordingStore struct {
	*session.Runtime
	mu           sync.Mutex
	created      []session.Session
	createErr    error
	released     int
	releaseCheck func()
}

func (s *recordingStore) Create(ctx context.Context, r session.CreateRequest) (session.Session, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	current, err := s.Runtime.Create(ctx, r)
	s.mu.Lock()
	if err == nil {
		s.created = append(s.created, current)
	}

	s.mu.Unlock()
	return current, err
}

func (s *recordingStore) Release(ctx context.Context, current session.Session) error {
	if s.releaseCheck != nil {
		s.releaseCheck()
	}

	s.mu.Lock()
	s.released++
	s.mu.Unlock()
	return s.Runtime.Release(ctx, current)
}

type storePlugin struct {
	store  *recordingStore
	closed chan struct{}
}

func (p *storePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.store",
		New:      func() gordis.Plugin { return &storePlugin{p.store, p.closed} },
		Provides: []gordis.ServiceSpec{session.StoreKey.Spec()},
	}
}

func (p *storePlugin) Activate(_ context.Context, s *gordis.Scope) error {
	if p.closed != nil {
		if err := s.Defer("store closed", func(context.Context) error {
			close(p.closed)
			return nil
		}); err != nil {
			return err
		}
	}

	return s.Provide[session.Store](session.StoreKey, p.store)
}

// YAML input remains local to these consumer Profile tests.
type profileSource struct {
	Bundles   []string                  `yaml:"bundles,omitempty"`
	Instances []profileapi.InstanceSpec `yaml:"instances,omitempty"`
	Overrides []profileapi.Override     `yaml:"overrides,omitempty"`
}

func application(
	t *testing.T,
	a *adapter,
	s *recordingStore,
	modelClosed, storeClosed chan struct{},
) (applicationapi.Application, profileSource) {
	t.Helper()
	app, err := applicationapi.Compose(builtin.Extension(), applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{
		{Plugin: &modelPlugin{a, modelClosed}, ProfileVisible: true},
		{Plugin: &storePlugin{s, storeClosed}, ProfileVisible: true},
	}})
	if err != nil {
		t.Fatal(err)
	}

	replacement := "test.store"
	source := profileSource{
		Bundles: []string{"base"},
		Overrides: []profileapi.Override{
			{ID: "session", Plugin: &replacement},
		},
		Instances: []profileapi.InstanceSpec{
			{ID: "model", Plugin: "test.model"},
			{
				ID:     "conversation",
				Plugin: headless.PluginID,
				Config: map[string]any{"model": "test-text"},
			},
		},
	}
	return app, source
}

func newStore() *recordingStore {
	return &recordingStore{Runtime: session.NewRuntime(session.RuntimeOptions{})}
}

func TestCreateFailureCompletesAndClosesActivatedResources(t *testing.T) {
	a, s := &adapter{}, newStore()
	s.createErr = errors.New("create failed")
	modelClosed, storeClosed := make(chan struct{}), make(chan struct{})
	app, source := application(t, a, s, modelClosed, storeClosed)
	code, stderr := work(t, deadline(t), app, source, []string{"hello"}, io.Discard)
	if code != 1 || !strings.Contains(stderr, "create failed") || len(a.seen()) != 0 || s.released != 0 {
		t.Fatal(code, stderr)
	}
	for _, closed := range []chan struct{}{modelClosed, storeClosed} {
		select {
		case <-closed:
		default:
			t.Fatal("failed Create left shared resources active")
		}
	}
}

func work(t *testing.T, ctx context.Context, app applicationapi.Application, source profileSource, args []string, out io.Writer) (int, string) {
	t.Helper()
	code, stderr := runProfile(t, ctx, app, source, args, launchapi.Input{Stdin: panicReader{}, Stdout: out})
	return code, stderr
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("unexpected stdin") }

func deadline(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestWordsSubmitOneTaskAndCommittedFacts(t *testing.T) {
	a, s := &adapter{}, newStore()
	app, source := application(t, a, s, nil, nil)
	var out bytes.Buffer
	code, stderr := work(t, deadline(t), app, source, []string{"first", "second"}, &out)
	if code != 0 || stderr != "" || out.String() != "answer 1\n" {
		t.Fatal(code, stderr, out.String())
	}

	seen := a.seen()
	if len(seen) != 1 || len(seen[0].Messages) != 1 || seen[0].Messages[0].Role != protocol.RoleUser || seen[0].Messages[0].Content[0].(protocol.TextBlock).Text != "first second" {
		t.Fatalf("requests: %+v", seen)
	}

	if len(s.created) != 1 || s.released != 1 {
		t.Fatal(len(s.created), s.released)
	}

	ends, answers := 0, 0

	for event, err := range s.created[0].Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}

		if event.Type == protocol.TurnEnded && protocol.EventPayload[protocol.TurnEndData](event).Reason.Kind == "completed" {
			ends++
		}

		if event.Type == protocol.AssistantMessage {
			answers++
		}
	}

	if ends != 1 || answers != 1 {
		t.Fatal(ends, answers)
	}

}

func TestCancellationDrainsBeforeSharedDependenciesClose(t *testing.T) {
	a := &adapter{started: make(chan struct{}), drained: make(chan struct{}), release: make(chan struct{})}
	s := newStore()
	modelClosed, storeClosed := make(chan struct{}), make(chan struct{})
	s.releaseCheck = func() {
		select {
		case <-a.drained:
		default:
			t.Error("handle released before model drain")
		}

		select {
		case <-storeClosed:
			t.Error("store already closed")
		default:
		}

		select {
		case <-modelClosed:
			t.Error("model already closed")
		default:
		}
	}
	app, source := application(t, a, s, modelClosed, storeClosed)
	ctx, cancel := context.WithCancel(deadline(t))
	defer cancel()
	done := make(chan struct{})
	var code int
	go func() {
		code, _ = work(t, ctx, app, source, []string{"one"}, io.Discard)
		close(done)
	}()

	select {
	case <-a.started:
	case <-ctx.Done():
		t.Fatal("model never started")
	}

	cancel()

	select {
	case <-done:
		t.Fatal("completed before drain")
	case <-modelClosed:
		t.Fatal("model closed before drain")
	case <-storeClosed:
		t.Fatal("store closed before drain")
	case <-time.After(30 * time.Millisecond):
	}

	close(a.release)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("drain stuck")
	}

	if code == 0 || s.released != 1 {
		t.Fatal(code, s.released)
	}

	select {
	case <-modelClosed:
	default:
		t.Fatal("model not closed")
	}

	select {
	case <-storeClosed:
	default:
		t.Fatal("store not closed")
	}
}

// Drive the original consumer with public Gordis and launch contracts. The
// kernel tests separately verify that boot owns the whole-plan Ready gate.
func TestReadyGateAndReactivationDoNotRepeatInvocation(t *testing.T) {
	a, s := &adapter{}, newStore()
	app, _ := application(t, a, s, nil, nil)
	call, err := (headless.Parser{}).Parse([]string{"one"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deadline(t)
	prepared, err := call.Prepare(ctx, launchapi.Input{Stdin: panicReader{}})
	if err != nil {
		t.Fatal(err)
	}
	plugins := []gordis.Plugin{}
	for _, r := range app.Registrations {
		plugins = append(plugins, r.Plugin)
	}
	plugins, err = launchapi.Bind(prepared, plugins)
	if err != nil {
		t.Fatal(err)
	}
	control := &gate{ready: make(chan struct{}), done: make(chan launchapi.Completion, 1)}
	carrier := &launchCarrier{&launchapi.Context{Parent: ctx, Invocation: prepared, Stdout: io.Discard, Stderr: io.Discard, Control: control}}
	host, err := gordis.NewHost(append(plugins, carrier)...)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Shutdown(context.Background())
	mount := func(id, plugin string, config map[string]any) *gordis.Instance {
		t.Helper()
		if config == nil {
			config = map[string]any{}
		}
		raw, _ := json.Marshal(config)
		instance, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: id, Plugin: plugin, Config: raw})
		if err != nil {
			t.Fatal(err)
		}
		return instance
	}
	mount(launchapi.CarrierID, launchapi.CarrierID, nil)
	for _, spec := range builtin.Extension().Bundles[0].Instances {
		if spec.ID == "session" {
			spec.Plugin = "test.store"
		}
		mount(spec.ID, spec.Plugin, spec.Config)
	}
	mount("model", "test.model", nil)
	instance := mount("conversation", headless.PluginID, map[string]any{"model": "test-text"})
	time.Sleep(20 * time.Millisecond)
	if len(a.seen()) != 0 || len(s.created) != 0 {
		t.Fatal("ran before whole graph Ready")
	}
	if err := prepared.(launchapi.ReadyCheck).CheckReady(ctx); err != nil {
		t.Fatal(err)
	}
	close(control.ready)
	select {
	case result := <-control.done:
		if result.Code != 0 || result.CleanupError != nil {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := prepared.(launchapi.InvocationDrain).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := instance.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	instance = mount("reactivated", headless.PluginID, map[string]any{"model": "test-text"})
	if err := instance.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := prepared.(launchapi.InvocationDrain).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.seen()) != 1 || len(s.created) != 1 || s.released != 1 {
		t.Fatal("invocation repeated")
	}
}

type gate struct {
	ready   chan struct{}
	done    chan launchapi.Completion
	claimed atomic.Bool
}

func (g *gate) Ready() <-chan struct{} { return g.ready }
func (g *gate) Claim() error {
	if !g.claimed.CompareAndSwap(false, true) {
		return errors.New("already claimed")
	}
	return nil
}
func (*gate) BeginShutdown(int) {}
func (g *gate) Complete(code int, err error) error {
	g.done <- launchapi.Completion{Code: code, CleanupError: err}
	return nil
}

type launchCarrier struct{ call *launchapi.Context }

func (p *launchCarrier) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: launchapi.CarrierID, Provides: []gordis.ServiceSpec{launchapi.LaunchContextKey.Spec()}, New: func() gordis.Plugin { return p }}
}
func (p *launchCarrier) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(launchapi.LaunchContextKey, p.call)
}

// Any explicit default contributor is accepted; the consumer has no concrete
// dependency on the optional qi/agent-defaults plugin or its instance count.
func TestProductModelSelectionAndReadyRollback(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		success     bool
	}{
		{name: "explicit", model: "test-text", success: true},
		{name: "unknown", model: "not-declared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, s := &adapter{}, newStore()
			modelClosed, storeClosed := make(chan struct{}), make(chan struct{})
			app, source := application(t, a, s, modelClosed, storeClosed)
			source.Instances[1].Config = map[string]any{}
			if tc.model != "" {
				source.Instances[1].Config["model"] = tc.model
			}
			code, stderr := work(t, deadline(t), app, source, []string{"hello"}, io.Discard)
			if (code == 0) != tc.success {
				t.Fatal(code, stderr)
			}
			if tc.success {
				if len(a.seen()) != 1 || len(s.created) != 1 || s.released != 1 {
					t.Fatal("task not completed", a.seen(), s.created, s.released)
				}
			} else if len(a.seen()) != 0 || len(s.created) != 0 || stderr == "" {
				t.Fatal("Ready rejection executed task", stderr)
			}
			for _, closed := range []chan struct{}{modelClosed, storeClosed} {
				select {
				case <-closed:
				default:
					t.Fatal("activated resource did not roll back or close")
				}
			}
		})
	}
}

// Local boot fixture for consumer startup, cancellation and resource cleanup.
func profileFile(t testing.TB, source any) string {
	t.Helper()
	raw, err := yaml.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runProfile supplies cancellation through the consumer's public launch context.
func runProfile(t testing.TB, ctx context.Context, app applicationapi.Application, source any, args []string, input launchapi.Input) (int, string) {
	t.Helper()
	app.Registrations = append([]registrationapi.PluginRegistration(nil), app.Registrations...)
	for i, r := range app.Registrations {
		if r.Invocation != nil {
			app.Registrations[i].Plugin = &contextPlugin{r.Plugin, ctx}
		}
	}
	var diagnostic bytes.Buffer
	if input.Stdout == nil {
		input.Stdout = io.Discard
	}
	input.Stderr = &diagnostic
	code := boot.Run(append([]string{"--profile", profileFile(t, source), "--"}, args...), input, app)
	return code, diagnostic.String()
}

type contextPlugin struct {
	gordis.Plugin
	ctx context.Context
}

func (p *contextPlugin) UnmarshalJSON(raw []byte) error { return json.Unmarshal(raw, p.Plugin) }
func (p *contextPlugin) Validate() error {
	if v, ok := p.Plugin.(gordis.Validator); ok {
		return v.Validate()
	}
	return nil
}
func (p *contextPlugin) Spec() gordis.PluginSpec {
	spec := p.Plugin.Spec()
	original := spec.New
	spec.New = func() gordis.Plugin { return &contextPlugin{original(), p.ctx} }
	return spec
}
func (p *contextPlugin) Activate(ctx context.Context, s *gordis.Scope) error {
	call, err := s.Get(launchapi.LaunchContextKey)
	if err != nil {
		return err
	}
	call.Parent = p.ctx
	return p.Plugin.(interface {
		Activate(context.Context, *gordis.Scope) error
	}).Activate(ctx, s)
}
