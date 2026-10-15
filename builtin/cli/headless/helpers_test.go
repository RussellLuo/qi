package headless

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/builtin/session/jsonl"
	jsonlplugin "github.com/RussellLuo/qi/builtin/session/jsonl/plugin"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/llm"
	llmplugin "github.com/RussellLuo/qi/kernel/core/llm/plugin"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	promptplugin "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	sessionplugin "github.com/RussellLuo/qi/kernel/core/session/plugin"
	toolsplugin "github.com/RussellLuo/qi/kernel/core/tools/plugin"
)

func resolveJSONLOwner(owner *jsonl.Owner) session.PersistenceResolver {
	return func(ctx context.Context) (session.PersistenceLease, bool, error) {
		lease, err := owner.Acquire(ctx)
		return lease, err == nil, err
	}
}

func newFixtureFactory(driver agentloop.Driver, store session.Store) agent.Factory {
	_, owned := store.(session.ResourceStore)
	factory, err := agent.NewFactory(agent.FactoryOptions{
		Registry: agent.NewRegistry(), Store: store, Driver: driver, AttachOnly: !owned,
	})
	if err != nil {
		panic(err)
	}
	return factory
}

// contributionOwner is a deterministic ContributionOwner for runtime and conformance
// tests. Stop rejects new acquisitions, waits for admitted work, and then runs
// cleanup callbacks in reverse registration order.
type contributionOwner struct {
	mu         sync.Mutex
	identity   contracts.OwnerIdentity
	stopping   bool
	stopped    bool
	active     int
	cleanups   []func()
	activeZero chan struct{}
}

func newContributionOwner(id string, generation uint64) *contributionOwner {
	return &contributionOwner{identity: contracts.OwnerIdentity{ID: id, Generation: generation}}
}

func (o *contributionOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *contributionOwner) Acquire() (func(), error) {
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

func (o *contributionOwner) DeferCleanup(_ string, cleanup func()) error {
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

func (o *contributionOwner) Stop(ctx context.Context) error {
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

type echoAdapter struct{ prefix string }

func (a echoAdapter) Stream(_ context.Context, request llm.Request) contracts.ChunkStream {
	return func(yield func(protocol.StreamChunk, error) bool) {
		text := ""
		for _, m := range request.Messages {
			if m.Role == protocol.RoleUser {
				for _, b := range m.Content {
					if b, ok := b.(protocol.TextBlock); ok {
						text = b.Text
					}
				}
			}
		}
		text = a.prefix + text
		for _, c := range []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText}, {Type: protocol.ChunkTextDelta, Index: 0, Text: text}, {Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: text}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}} {
			if !yield(c, nil) {
				return
			}
		}
	}
}

type fixtureProvider struct{}

func (*fixtureProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.echo-provider", New: func() gordis.Plugin { return &fixtureProvider{} }, Requires: []gordis.ServiceSpec{llm.RuntimeKey.Spec()}}
}

func (*fixtureProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	return runtime.Bind(scope).RegisterProvider(llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: echoAdapter{}}, []modelcatalog.Spec{{ID: "fake", Route: "chat", Provider: "fake", Model: "echo"}})
}

func fixtureRegistrations() []registrationapi.PluginRegistration {
	plugins := []gordis.Plugin{&events.Plugin{}, &agentplugin.Plugin{}, &llmplugin.Plugin{}, &promptplugin.Plugin{}, &sessionplugin.Plugin{}, &toolsplugin.Plugin{}, &jsonlplugin.Plugin{}, &fixtureProvider{}}
	var result []registrationapi.PluginRegistration
	for _, p := range plugins {
		result = append(result, registrationapi.PluginRegistration{Plugin: p})
	}
	return result
}

func fixtureProfile() (profileapi.Profile, error) {
	return profileapi.Profile{Instances: []profileapi.InstanceSpec{{ID: "events", Plugin: events.PluginID}, {ID: "session", Plugin: sessionplugin.PluginID}, {ID: "llm-runtime", Plugin: llmplugin.PluginID}, {ID: "prompt-runtime", Plugin: promptplugin.PluginID}, {ID: "tools", Plugin: toolsplugin.PluginID}, {ID: "agent", Plugin: agentplugin.PluginID}, {ID: "llm-fake", Plugin: "test.echo-provider"}}}, nil
}

type scriptedScript struct {
	Chunks        []protocol.StreamChunk
	Err           error
	WaitForCancel bool
}

type scriptedAdapter struct {
	mu      sync.Mutex
	Version string
	Scripts []scriptedScript
	Seen    []llm.Request
}

func (a *scriptedAdapter) ResolveModel(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	if ref.Provider == "" || ref.Model == "" {
		return llm.ModelInfo{}, errors.New("provider and model are required")
	}

	return llm.ModelInfo{
		Ref:           ref,
		ContextWindow: 4096,
		Capabilities:  []string{"tools", a.Version},
	}, nil
}

func (a *scriptedAdapter) Stream(ctx context.Context, request llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	a.mu.Lock()
	a.Seen = append(a.Seen, request.Clone())
	if len(a.Scripts) == 0 {
		a.mu.Unlock()
		return func(yield func(protocol.StreamChunk, error) bool) {
			yield(protocol.StreamChunk{}, errors.New("script exhausted"))
		}
	}

	script := a.Scripts[0]
	a.Scripts = a.Scripts[1:]
	a.mu.Unlock()
	return func(yield func(protocol.StreamChunk, error) bool) {
		if script.WaitForCancel {
			<-ctx.Done()
			yield(protocol.StreamChunk{}, ctx.Err())
			return
		}

		for _, chunk := range script.Chunks {
			if !yield(chunk, nil) {
				return
			}
		}

		if script.Err != nil {
			yield(protocol.StreamChunk{}, script.Err)
		}
	}
}

const probeID = "services"

func registrations(wrap func(agentloop.Driver) agentloop.Driver) []registrationapi.PluginRegistration {
	registrations := fixtureRegistrations()
	for i := range registrations {
		if registrations[i].Plugin.Spec().ID == agentplugin.PluginID {
			registrations[i].Plugin = &agentplugin.Plugin{WrapDriver: wrap}
		}
	}
	return registrations
}

type Runtime struct {
	Host     *gordis.Host
	Sessions session.Store
	Agents   agent.Registry
	Factory  agent.Factory
	Config   agent.Config
	binding  events.Binding
	llm      *llm.Runtime
}

func (r *Runtime) Close(ctx context.Context) error { return r.Host.Shutdown(ctx) }

func (r *Runtime) Observe(handler func(context.Context, contracts.AssistantStreamObservation) error) (func(), error) {
	return r.binding.On(gordisbridge.AssistantStreamTopic, handler, events.Global())
}

func (r *Runtime) ObserveEvents(handler func(context.Context, gordisbridge.SessionObservation) error) (func(), error) {
	return r.binding.On(gordisbridge.SessionAppendedTopic, handler, events.Global())
}

type probe struct{ ready chan<- *Runtime }

func (p *probe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: probeID, New: func() gordis.Plugin { return &probe{ready: p.ready} }, Requires: []gordis.ServiceSpec{events.BusKey.Spec(), session.StoreKey.Spec(), agent.RegistryKey.Spec(), agent.FactoryKey.Spec(), llm.RuntimeKey.Spec()}}
}

func (p *probe) Activate(_ context.Context, scope *gordis.Scope) error {
	sessions, err := scope.Get(session.StoreKey)
	if err != nil {
		return err
	}
	agents, err := scope.Get(agent.RegistryKey)
	if err != nil {
		return err
	}
	llmRuntime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	provider, err := scope.Get(agent.FactoryKey)
	if err != nil {
		return err
	}
	factory, err := provider.Bind(scope)
	if err != nil {
		return err
	}
	p.ready <- &Runtime{Sessions: sessions, Agents: agents, Factory: factory, binding: events.Bind(scope), llm: llmRuntime}
	return nil
}

// Start builds only this test's explicit service graph with the public Gordis API.
// Product source validation, launch Ready and rollback are tested through boot.Run.
func Start(ctx context.Context, profile profileapi.Profile, wrap func(agentloop.Driver) agentloop.Driver) (*Runtime, error) {
	var plugins []gordis.Plugin
	for _, registration := range registrations(wrap) {
		plugin := registration.Plugin
		plugins = append(plugins, plugin)
	}
	ready := make(chan *Runtime, 1)
	plugins = append(plugins, &probe{ready: ready})
	host, err := gordis.NewHost(plugins...)
	if err != nil {
		return nil, err
	}
	// This fixed service-only fixture omits the product consumer. Dependencies
	// are mounted in the same explicit order even when the input is shuffled.
	for _, id := range []string{"events", "persistence", "session", "llm-runtime", "prompt-runtime", "tools", "agent", "llm-fake", "defaults"} {
		for _, instance := range profile.Instances {
			if instance.ID != id {
				continue
			}
			config := instance.Config
			if config == nil {
				config = map[string]any{}
			}
			raw, err := json.Marshal(config)
			if err != nil {
				return nil, err
			}
			if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: instance.ID, Plugin: instance.Plugin, Config: raw}); err != nil {
				_ = host.Shutdown(context.Background())
				return nil, err
			}
		}
	}
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: probeID, Plugin: probeID}); err != nil {
		_ = host.Shutdown(context.Background())
		return nil, err
	}
	runtime := <-ready
	runtime.Host = host
	model, err := runtime.llm.ResolveModel("fake")
	if err == nil {
		runtime.Config.Loop.LLM, err = model.LLMConfig()
	}
	if err == nil {
		call, callErr := runtime.llm.PrepareCall(ctx, runtime.Config.Loop.LLM)
		err = callErr
		if call != nil {
			call.Close()
		}
	}
	if err != nil {
		_ = host.Shutdown(context.Background())
		return nil, err
	}
	return runtime, nil
}
