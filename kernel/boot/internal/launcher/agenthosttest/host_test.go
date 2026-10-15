package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	loopplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	llmapi "github.com/RussellLuo/qi/kernel/core/llm"
	llm "github.com/RussellLuo/qi/kernel/core/llm/plugin"
	promptruntime "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	sessionmemory "github.com/RussellLuo/qi/kernel/core/session/plugin"
	toolsruntime "github.com/RussellLuo/qi/kernel/core/tools/plugin"
)

type caller struct {
	scope       *gordis.Scope
	provider    agent.FactoryProvider
	factory     agent.Factory
	store       session.Store
	service     agent.Registry
	beforeReady error
}

type callerPlugin struct {
	ready chan *caller
	stop  func(context.Context) error
}

func (p *callerPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "test.caller",
		Requires: []gordis.ServiceSpec{
			events.BusKey.Spec(),
			agent.FactoryKey.Spec(), session.StoreKey.Spec(), agent.RegistryKey.Spec(),
			agent.InitializerKey.Spec(), agent.ObserverKey.Spec(),
		},
		New: func() gordis.Plugin { return &callerPlugin{ready: p.ready, stop: p.stop} },
	}
}

func (p *callerPlugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	provider, err := scope.Get(agent.FactoryKey)
	if err != nil {
		return err
	}
	f, err := provider.Bind(scope)
	if err != nil {
		return err
	}

	again, err := provider.Bind(scope)
	if err != nil {
		return err
	}
	if f != again {
		return errors.New("binding within a generation changed factory")
	}

	store, err := scope.Get(session.StoreKey)
	if err != nil {
		return err
	}

	service, err := scope.Get(agent.RegistryKey)
	if err != nil {
		return err
	}

	_, beforeReady := f.Create(ctx, agent.CreateOptions{})
	if p.stop != nil {
		if err := scope.OnStop("test freeze barrier", p.stop); err != nil {
			return err
		}
	}

	p.ready <- &caller{
		scope:       scope,
		provider:    provider,
		factory:     f,
		store:       store,
		service:     service,
		beforeReady: beforeReady,
	}

	return nil
}

// This is a real Scope-owned user model plugin, with no standard module,
// runtime facade, Profile, invocation, credentials or network dependency.
type modelPlugin struct{ adapter *adapter }

func (p *modelPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.model",
		Requires: []gordis.ServiceSpec{llmapi.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return &modelPlugin{adapter: p.adapter} },
	}
}

func (p *modelPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("close user transport", func(context.Context) error {
		if p.adapter.active.Load() != 0 {
			return errors.New("transport closed during model use")
		}

		p.adapter.closed.Store(true)
		return nil
	}); err != nil {
		return err
	}

	runtime, err := scope.Get(llmapi.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).RegisterAdapter(llmapi.AdapterRegistration{
		Route:    "local",
		Provider: "local",
		Models:   []string{"text"},
		Adapter:  p.adapter,
	})

	return err
}

type adapter struct {
	active   atomic.Int64
	closed   atomic.Bool
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (*adapter) ResolveModel(_ context.Context, ref llmapi.ModelRef) (llmapi.ModelInfo, error) {
	return llmapi.ModelInfo{Ref: ref, Capabilities: []string{"text"}}, nil
}

func (a *adapter) Stream(ctx context.Context, request llmapi.Request) contracts.ChunkStream {
	return func(yield func(protocol.StreamChunk, error) bool) {
		a.active.Add(1)
		defer a.active.Add(-1)

		if a.started != nil {
			a.once.Do(func() { close(a.started) })
			<-ctx.Done()
			close(a.canceled)
			<-a.release // deliberately retain the snapshot until actual exit
			yield(protocol.StreamChunk{}, ctx.Err())
			return
		}

		turns := 0

		for _, m := range request.Messages {
			if m.Role == protocol.RoleUser {
				turns++
			}
		}

		answer := fmt.Sprintf("answer %d", turns)

		for _, chunk := range []protocol.StreamChunk{
			{
				Type:      protocol.ChunkBlockStart,
				Index:     0,
				BlockType: protocol.BlockText,
			},
			{
				Type:  protocol.ChunkTextDelta,
				Index: 0,
				Text:  answer,
			},
			{
				Type:  protocol.ChunkBlockEnd,
				Index: 0,
				Block: protocol.TextBlock{Text: answer},
			},
			{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
		} {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

type fixture struct {
	host     *gordis.Host
	provider *gordis.Instance
	callers  chan *caller
	model    *adapter
}

func setup(t *testing.T, model *adapter, stop func(context.Context) error, store session.Store) *fixture {
	t.Helper()
	if model == nil {
		model = &adapter{}
	}

	f := &fixture{callers: make(chan *caller, 8), model: model}
	var memory gordis.Plugin = &sessionmemory.Plugin{}
	if store != nil {
		memory = &storePlugin{store: store}
	}

	host, err := gordis.NewHost(&events.Plugin{}, memory, &llm.Plugin{}, &promptruntime.Plugin{},
		&toolsruntime.Plugin{}, &loopplugin.Plugin{}, &modelPlugin{adapter: model}, &callerPlugin{ready: f.callers, stop: stop})
	if err != nil {
		t.Fatal(err)
	}

	f.host = host
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := host.Shutdown(ctx); err != nil {
			t.Log("fixture shutdown:", err)
		}
	})

	for _, spec := range []gordis.InstanceSpec{
		{ID: "events", Plugin: events.PluginID},
		{ID: "sessions", Plugin: memory.Spec().ID},
		{ID: "llm", Plugin: llm.PluginID},
		{ID: "prompts", Plugin: promptruntime.PluginID},
		{ID: "tools", Plugin: toolsruntime.PluginID},
		{ID: "agents", Plugin: loopplugin.PluginID},
	} {
		instance := mount(t, host, spec)
		if spec.ID == "agents" {
			f.provider = instance
		}
	}

	return f
}

func mount(t *testing.T, host *gordis.Host, spec gordis.InstanceSpec) *gordis.Instance {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	i, err := host.Env().MountReady(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}

	return i
}

func (f *fixture) caller(t *testing.T, id string) (*gordis.Instance, *caller) {
	t.Helper()
	i := mount(t, f.host, gordis.InstanceSpec{ID: id, Plugin: "test.caller"})
	c := <-f.callers
	if !errors.Is(c.beforeReady, gordis.ErrNotReady) {
		t.Fatal("creation before caller Ready:", c.beforeReady)
	}

	return i, c
}

func TestFactoryProviderBindRejectsInvalidAndStoppedCallers(t *testing.T) {
	f := setup(t, nil, nil, nil)
	owner, c := f.caller(t, "caller")
	for _, scope := range []*gordis.Scope{nil, {}} {
		if factory, err := c.provider.Bind(scope); factory != nil || !errors.Is(err, agent.ErrClosed) {
			t.Fatal("invalid Scope:", factory, err)
		}
	}
	if err := owner.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	if factory, err := c.provider.Bind(c.scope); factory != nil || !errors.Is(err, agent.ErrClosed) {
		t.Fatal("stopped caller:", factory, err)
	}
	_, replacement := f.caller(t, "caller")
	if replacement.factory == c.factory || replacement.provider != c.provider {
		t.Fatal("caller generation changed shared provider or retained old factory")
	}
	assertNoLeases(t, f.host)
}

func TestAgentKeysSelectFactoryProviderViewAndGeneration(t *testing.T) {
	f := setup(t, nil, nil, nil)
	_, public := f.caller(t, "public-caller")
	private := f.host.Env().
		Isolate(agent.FactoryKey, "private-factory").
		Isolate(agent.RegistryKey, "private-registry").
		Isolate(agent.InitializerKey, "private-initializers").
		Isolate(agent.ObserverKey, "private-observers").
		Isolate(agentmiddleware.RuntimeKey, "private-middleware")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, err := private.MountReady(ctx, gordis.InstanceSpec{ID: "private-agents", Plugin: loopplugin.PluginID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := private.MountReady(ctx, gordis.InstanceSpec{ID: "private-caller", Plugin: "test.caller"}); err != nil {
		t.Fatal(err)
	}
	c := <-f.callers
	if !errors.Is(c.beforeReady, gordis.ErrNotReady) {
		t.Fatal("private caller admitted before Ready:", c.beforeReady)
	}
	if c.provider == public.provider || c.service == public.service || c.store != public.store {
		t.Fatal("Factory/Registry View isolation changed shared Session Store")
	}
	initializers, err := c.scope.Get(agent.InitializerKey)
	if err != nil {
		t.Fatal(err)
	}
	publicInitializers, err := public.scope.Get(agent.InitializerKey)
	if err != nil || initializers == publicInitializers {
		t.Fatal("Initializer Key crossed View:", err)
	}
	observers, err := c.scope.Get(agent.ObserverKey)
	if err != nil {
		t.Fatal(err)
	}
	publicObservers, err := public.scope.Get(agent.ObserverKey)
	if err != nil || observers == publicObservers {
		t.Fatal("Observer Key crossed View:", err)
	}
	owned := create(t, c, "private-session")
	publicHandle := create(t, public, "public-session")
	if len(c.service.Snapshot()) != 1 || len(public.service.Snapshot()) != 1 {
		t.Fatal("Agent publication crossed registries")
	}
	changes := f.host.Changes()
	changes.Unmount(provider)
	operation, err := changes.Apply(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, c)
	if !owned.Agent().Disposed() || publicHandle.Agent().Disposed() {
		t.Fatal("private provider drain crossed View")
	}
	if factory, err := c.provider.Bind(public.scope); factory != nil || !errors.Is(err, agent.ErrClosed) {
		t.Fatal("stopped provider rebound:", factory, err)
	}
	if _, err := private.MountReady(ctx, gordis.InstanceSpec{ID: "private-agents", Plugin: loopplugin.PluginID}); err != nil {
		t.Fatal(err)
	}
	if _, err := private.MountReady(ctx, gordis.InstanceSpec{ID: "replacement-caller", Plugin: "test.caller"}); err != nil {
		t.Fatal(err)
	}
	replacement := <-f.callers
	if replacement.provider == c.provider || replacement.factory == c.factory {
		t.Fatal("Factory binding retained old provider generation")
	}
	current := create(t, replacement, "private-session")
	if err := owned.Dispose(ctx); err != nil || current.Agent().Disposed() {
		t.Fatal("old provider disposal affected replacement:", err)
	}
	assertNoLeases(t, f.host)
}

func config() agent.Config {
	return agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "local", Model: "text"}}}
}

func create(t *testing.T, c *caller, id protocol.SessionID) agent.Handle {
	t.Helper()
	h, err := c.factory.Create(context.Background(), agent.CreateOptions{
		Session: session.CreateRequest{ID: id},
		Agent:   agent.CreateRequest{Config: config()},
	})
	if err != nil {
		t.Fatal(err)
	}

	return h
}

func send(t *testing.T, h agent.Handle, id string) {
	t.Helper()
	if err := h.Agent().Followup(
		context.Background(),
		protocol.NewTextMessage(protocol.MessageID(id), protocol.RoleUser, protocol.SourceUser, id),
	); err != nil {
		t.Fatal(err)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lifecycle signal")
	}
}

func assertClosed(t *testing.T, c *caller) {
	t.Helper()
	if h, err := c.factory.Create(context.Background(), agent.CreateOptions{}); h != nil || !errors.Is(err, agent.ErrClosed) {
		t.Fatal("stopped Create:", h, err)
	}

	if h, err := c.factory.Attach(context.Background(), agent.AttachOptions{SessionID: "borrowed"}); h != nil || !errors.Is(err, agent.ErrClosed) {
		t.Fatal("stopped Attach:", h, err)
	}
}

func assertNoLeases(t *testing.T, host *gordis.Host) {
	t.Helper()
	// Handle cleanup does not join passive event publication. An active provider
	// may still hold a short Ready lease while that queue finishes delivery.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		drained := true
		for _, s := range host.Snapshot() {
			if s.Leases != 0 {
				drained = false
				break
			}
		}
		if drained {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("scopes retain leases after observation drain", host.Snapshot())
		}
	}
}

func assertUnavailable(t *testing.T, store session.Store, id protocol.SessionID, expected error) {
	t.Helper()
	if _, err := store.Get(context.Background(), id); !errors.Is(err, expected) {
		t.Fatal("unexpected Session availability:", id, err)
	}
}

func TestMinimalHostConversationAndOwnerIsolation(t *testing.T) {
	f := setup(t, nil, nil, nil)
	mount(t, f.host, gordis.InstanceSpec{ID: "model", Plugin: "test.model"})
	one, c1 := f.caller(t, "one")
	_, c2 := f.caller(t, "two")
	h := create(t, c1, "owned")

	for turn := 1; turn <= 2; turn++ {
		watermark := h.Session().Watermark()
		send(t, h, fmt.Sprintf("question-%d", turn))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := h.Agent().WhenIdle(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}

		messages, err := h.Session().Messages(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		last := messages[len(messages)-1]
		if last.Role != protocol.RoleAssistant || last.Content[0].(protocol.TextBlock).Text != fmt.Sprintf("answer %d", turn) {
			t.Fatal("committed response:", last)
		}

		ended := false

		for e, err := range h.Session().Events(context.Background(), watermark) {
			if err != nil {
				t.Fatal(err)
			}

			if e.Type == protocol.TurnEnded {
				var end protocol.TurnEndData
				if err := json.Unmarshal(e.Data, &end); err != nil {
					t.Fatal(err)
				}

				ended = end.Reason.Kind == "completed"
			}
		}

		if !ended {
			t.Fatal("missing committed successful end")
		}
	}

	borrowed, err := c1.store.Create(context.Background(), session.CreateRequest{ID: "borrowed"})
	if err != nil {
		t.Fatal(err)
	}

	attached, err := c1.factory.Attach(context.Background(), agent.AttachOptions{SessionID: borrowed.ID(), Config: config()})
	if err != nil {
		t.Fatal(err)
	}

	send(t, attached, "borrowed-question")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = attached.Agent().WhenIdle(ctx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}

	history, err := borrowed.Messages(context.Background())
	if err != nil || len(history) == 0 {
		t.Fatal("borrowed activity did not commit history", err)
	}

	watermark := borrowed.Watermark()
	other := create(t, c2, "other")
	if err := one.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertClosed(t, c1)
	assertUnavailable(t, c1.store, "owned", session.ErrNotFound)
	if !h.Agent().Disposed() || !attached.Agent().Disposed() || other.Agent().Disposed() {
		t.Fatal("caller ownership crossed")
	}

	if current, err := c1.store.Get(context.Background(), borrowed.ID()); err != nil || current != borrowed {
		t.Fatal("borrowed Session released", err)
	}

	retained, err := borrowed.Messages(context.Background())
	if err != nil || !reflect.DeepEqual(retained, history) || borrowed.Watermark() != watermark {
		t.Fatal("caller stop changed borrowed history", err)
	}

	reservation, err := borrowed.Attach()
	if err != nil {
		t.Fatal("borrowed attachment leaked:", err)
	}

	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	_, replacement := f.caller(t, "one")
	if replacement.factory == c1.factory {
		t.Fatal("factory crossed generations")
	}

	create(t, replacement, "owned")
	if err := h.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := replacement.store.Get(context.Background(), "owned"); err != nil {
		t.Fatal("old disposal affected new owner", err)
	}

	if err := f.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertUnavailable(t, c2.store, "other", contracts.ErrClosed)
	if len(c2.service.Snapshot()) != 0 || !f.model.closed.Load() {
		t.Fatal("Host did not drain shared resources")
	}

	assertNoLeases(t, f.host)
}

func TestActiveStopDrainsBeforeRelease(t *testing.T) {
	for _, kind := range []string{"caller", "provider", "host"} {
		t.Run(kind, func(t *testing.T) {
			a := &adapter{
				started:  make(chan struct{}),
				canceled: make(chan struct{}),
				release:  make(chan struct{}),
			}
			f := setup(t, a, nil, nil)
			owner, c := f.caller(t, "caller")
			// Mount the model after the caller to exercise Host quiesce ordering
			// with contribution withdrawal preceding caller OnStop.
			mount(t, f.host, gordis.InstanceSpec{ID: "model", Plugin: "test.model"})
			h := create(t, c, "active")
			send(t, h, "question")
			waitSignal(t, a.started)
			stop := f.host.Shutdown
			if kind != "host" {
				target := owner
				if kind == "provider" {
					target = f.provider
				}

				changes := f.host.Changes()
				changes.Unmount(target)
				var operation *gordis.Operation
				stop = func(ctx context.Context) error {
					if operation == nil {
						var err error
						operation, err = changes.Apply(ctx)
						if err != nil {
							return err
						}
					}

					return operation.Wait(ctx)
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()

			if err := stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("stop did not bound wait:", err)
			}

			waitSignal(t, a.canceled)
			assertClosed(t, c)
			if a.closed.Load() || a.active.Load() != 1 {
				t.Fatal("model transport released early")
			}

			if _, err := c.store.Get(context.Background(), "active"); err != nil {
				t.Fatal("Session released before activity exit:", err)
			}

			leased := false

			for _, s := range f.host.Snapshot() {
				if s.QualifiedID == "model" {
					leased = s.Leases > 0
				}
			}

			if !leased {
				t.Fatal("active contribution lost its lease")
			}

			close(a.release)
			if err := stop(context.Background()); err != nil {
				t.Fatal(err)
			}

			if err := h.Dispose(context.Background()); err != nil {
				t.Fatal(err)
			}

			expected := session.ErrNotFound
			if kind == "host" {
				expected = contracts.ErrClosed
			}
			assertUnavailable(t, c.store, "active", expected)
			if len(c.service.Snapshot()) != 0 || a.active.Load() != 0 {
				t.Fatal("stop leaked Agent or model activity")
			}

			assertNoLeases(t, f.host)
			if kind != "host" {
				if a.closed.Load() {
					t.Fatal("caller/provider closed borrowed model transport")
				}
				// The shared Store remains available to its owner.
				if _, err := c.store.Create(context.Background(), session.CreateRequest{ID: "external"}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCreationStopRaceRollsBack(t *testing.T) {
	for _, kind := range []string{"caller", "provider", "host"} {
		for _, attach := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/attach=%v", kind, attach), func(t *testing.T) {
				f := setup(t, nil, nil, nil)
				owner, c := f.caller(t, "caller")
				var borrowed session.Session
				if attach {
					var err error
					borrowed, err = c.store.Create(context.Background(), session.CreateRequest{ID: "race"})
					if err != nil {
						t.Fatal(err)
					}
				}

				entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				request := agent.CreateRequest{
					SessionID: "race",
					ComposeConfig: func(ctx context.Context, _ agent.Creation) (agent.Config, agent.Cleanup, error) {
						close(entered)
						<-ctx.Done()
						close(canceled)
						<-release
						return agent.Config{}, nil, ctx.Err()
					},
				}
				result := make(chan error, 1)
				go func() {
					var h agent.Handle
					var err error
					if attach {
						h, err = c.factory.Attach(context.Background(), agent.AttachOptions{SessionID: request.SessionID, Agent: request})
					} else {
						h, err = c.factory.Create(context.Background(), agent.CreateOptions{Session: session.CreateRequest{ID: "race"}, Agent: request})
					}

					if h != nil {
						result <- errors.New("published Handle after owner stopped")
						return
					}

					result <- err
				}()
				waitSignal(t, entered)
				stop := owner.Unmount
				if kind == "provider" {
					stop = f.provider.Unmount
				}

				if kind == "host" {
					stop = f.host.Shutdown
				}

				done := make(chan error, 1)
				go func() {
					done <- stop(context.Background())
				}()
				waitSignal(t, canceled)
				assertClosed(t, c)

				select {
				case err := <-done:
					t.Fatal("stop overtook creation rollback", err)
				default:
				}

				close(release)
				if err := <-result; !errors.Is(err, context.Canceled) && !errors.Is(err, agent.ErrClosed) {
					t.Fatal("creation race:", err)
				}

				if err := <-done; err != nil {
					t.Fatal(err)
				}

				if len(c.service.Snapshot()) != 0 {
					t.Fatal("creation registration leaked")
				}

				if attach && kind == "host" {
					assertUnavailable(t, c.store, "race", contracts.ErrClosed)
					if _, err := borrowed.Attach(); !errors.Is(err, contracts.ErrClosed) {
						t.Fatal("Host did not close borrowed Runtime instance", err)
					}
				} else if attach {
					if current, err := c.store.Get(context.Background(), "race"); err != nil || current != borrowed {
						t.Fatal("borrowed history lost", err)
					}

					reservation, err := borrowed.Attach()
					if err != nil {
						t.Fatal("rollback attachment leaked:", err)
					}

					if err := reservation.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					expected := session.ErrNotFound
					if kind == "host" {
						expected = contracts.ErrClosed
					}
					assertUnavailable(t, c.store, "race", expected)
				}

				assertNoLeases(t, f.host)
			})
		}
	}
}

func TestFrozenCallerCannotPublishBeforeOnStop(t *testing.T) {
	frozen, allowStop := make(chan struct{}), make(chan struct{})
	f := setup(t, nil, func(context.Context) error {
		close(frozen)
		<-allowStop
		return nil
	}, nil)
	owner, c := f.caller(t, "caller")
	entered, initialize := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		h, err := c.factory.Create(context.Background(), agent.CreateOptions{
			Session: session.CreateRequest{ID: "frozen"},
			Agent: agent.CreateRequest{
				ComposeConfig: func(context.Context, agent.Creation) (agent.Config, agent.Cleanup, error) {
					close(entered)
					<-initialize
					return config(), nil, nil
				},
			},
		})
		if h != nil {
			result <- errors.New("Handle published into frozen Scope")
			return
		}

		result <- err
	}()
	waitSignal(t, entered)
	done := make(chan error, 1)
	go func() {
		done <- owner.Unmount(context.Background())
	}()
	waitSignal(t, frozen)
	assertClosed(t, c)
	close(initialize)
	if err := <-result; !errors.Is(err, agent.ErrClosed) {
		t.Fatal(err)
	}

	assertUnavailable(t, c.store, "frozen", session.ErrNotFound)
	close(allowStop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	assertNoLeases(t, f.host)
}

type storePlugin struct{ store session.Store }

func (p *storePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.store",
		Provides: []gordis.ServiceSpec{session.StoreKey.Spec()},
		New:      func() gordis.Plugin { return &storePlugin{store: p.store} },
	}
}

func (p *storePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide(session.StoreKey, p.store)
}

type failingStore struct {
	*session.Runtime
	releases atomic.Int64
	err      error
}

func (s *failingStore) Release(ctx context.Context, current session.Session) error {
	s.releases.Add(1)
	return errors.Join(s.Runtime.Release(ctx, current), s.err)
}

func TestHostRetainsHandleCleanupErrors(t *testing.T) {
	want := errors.New("controlled Session release failure")
	store := &failingStore{Runtime: session.NewRuntime(session.RuntimeOptions{}), err: want}
	f := setup(t, nil, nil, store)
	owner, c := f.caller(t, "caller")
	h := create(t, c, "error")
	if err := h.Dispose(context.Background()); !errors.Is(err, want) {
		t.Fatal(err)
	}

	if err := owner.Unmount(context.Background()); !errors.Is(err, want) {
		t.Fatal("caller hid cleanup error:", err)
	}

	if err := f.host.Shutdown(context.Background()); !errors.Is(err, want) {
		t.Fatal("provider hid prior Handle cleanup error:", err)
	}

	if store.releases.Load() != 1 {
		t.Fatal("duplicate cleanup:", store.releases.Load())
	}

	assertNoLeases(t, f.host)
}

func TestPublicInitializationAndPassiveResourceSnapshots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := setup(t, nil, nil, nil)
	mount(t, f.host, gordis.InstanceSpec{ID: "model", Plugin: "test.model"})
	instance, c := f.caller(t, "new-observer")
	initializers, err := c.scope.Get(agent.InitializerKey)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := c.scope.Get(agent.ObserverKey)
	if err != nil {
		t.Fatal(err)
	}
	var retained agent.InitialAgent
	var cleanups atomic.Int32
	_, err = initializers.Register(gordisbridge.NewScopeOwner(c.scope), agent.InitializationHandler{ID: "public-initialize", Initialize: func(ctx context.Context, i agent.Initialization, r agent.CleanupRegistrar) error {
		retained = i.Agent
		if _, err := c.service.Get(ctx, i.Agent.ID()); !errors.Is(err, agent.ErrNotFound) {
			return errors.New("initializing Registry entry was visible")
		}
		if err := r.Defer("local-resource", func(context.Context) error { cleanups.Add(1); return nil }); err != nil {
			return err
		}
		if err := i.Agent.Configure(ctx, config()); err != nil {
			return err
		}
		return i.Agent.Send(ctx, protocol.NewTextMessage("seed", protocol.RoleUser, protocol.SourceUser, "prepared input"), contracts.NextTurn, true)
	}})
	if err != nil {
		t.Fatal(err)
	}
	handles := make(chan agent.Handle, 1)
	reentered := make(chan error, 1)
	disposed := make(chan contracts.AgentDisposed, 1)
	_, err = observed.Register(gordisbridge.NewScopeOwner(c.scope), agent.ObserverHandlers{ID: "public-observer", Created: func(e contracts.AgentCreated) {
		if e.Source != "startup" || e.Snapshot.InstanceID == "" || e.Snapshot.SessionID != "public-init" {
			t.Error("Created snapshot", e)
		}
		h := <-handles
		// Both waits are safe from a passive callback.
		reentered <- errors.Join(e.Agent.WhenIdle(ctx), h.Dispose(ctx))
	}, Disposed: func(e contracts.AgentDisposed) {
		if _, err := c.store.Get(ctx, e.Snapshot.SessionID); !errors.Is(err, session.ErrNotFound) {
			t.Error("Disposed before owned resource release", err)
		}
		disposed <- e
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.factory.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "public-init"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := retained.Send(ctx, protocol.NewTextMessage("late", protocol.RoleUser, protocol.SourceUser, "late"), contracts.NextTurn, true); !errors.Is(err, agent.ErrClosed) {
		t.Fatal("retained initialization port", err)
	}
	handles <- h
	select {
	case err := <-reentered:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("passive callback deadlock")
	}
	select {
	case e := <-disposed:
		if !e.Snapshot.Disposed || e.Snapshot.Status != agent.StatusIdle || e.Err != nil || cleanups.Load() != 1 {
			t.Fatal(e, cleanups.Load())
		}
	case <-ctx.Done():
		t.Fatal("Disposed missing")
	}
	if err := instance.Unmount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoLeases(t, f.host)
}
