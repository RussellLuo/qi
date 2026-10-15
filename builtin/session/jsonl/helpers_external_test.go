package jsonl_test

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/RussellLuo/qi/builtin/session/jsonl"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// runtimeFixture composes real domain and file owners for this package's tests.
// Construction never opens history. Tests explicitly Open and Attach it.
type runtimeFixture struct {
	*session.Runtime
	owner *jsonl.Owner
}

func resolveOwner(owner *jsonl.Owner) session.PersistenceResolver {
	return func(ctx context.Context) (session.PersistenceLease, bool, error) {
		lease, err := owner.Acquire(ctx)
		return lease, err == nil, err
	}
}

func newRuntimeFixture(dir string, observer contracts.ObservationDispatcher) (*runtimeFixture, error) {
	o, err := jsonl.NewOwner(dir)
	if err != nil {
		return nil, err
	}
	return &runtimeFixture{Runtime: session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolveOwner(o), Observer: observer}), owner: o}, nil
}
func (f *runtimeFixture) Close() error {
	if err := f.Runtime.Close(context.Background()); err != nil {
		return err
	}
	return f.owner.Close()
}

// openRecovered exercises physical admission followed by a borrowed reservation
// for logical repair, then gives that reservation back before returning.
func openRecovered(ctx context.Context, r session.StoreOpener, id protocol.SessionID) (session.Session, error) {
	current, err := r.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	lease, err := current.Attach()
	if err != nil {
		return nil, err
	}
	if err := lease.Close(); err != nil {
		return nil, err
	}
	return current, nil
}

type fixtureOwner struct {
	agent.Registry
	Store        session.Store
	Factory      agent.Factory
	Initializers agent.InitializerRegistry
	Events       agent.AgentEvents
	mu           sync.Mutex
	handles      map[agent.ID]agent.Handle
}

func newFixtureOwner(driver agentloop.Driver, store session.Store) *fixtureOwner {
	o := &fixtureOwner{Registry: agent.NewRegistry(), Store: store, Initializers: agent.NewInitializers(), Events: agent.NewAgentEvents(), handles: make(map[agent.ID]agent.Handle)}
	f, err := agent.NewFactory(agent.FactoryOptions{Registry: o.Registry, Store: store, Driver: driver, AttachOnly: true, Initialize: o.Initializers, Observe: o.Events})
	if err != nil {
		panic(err)
	}
	o.Factory = f
	return o
}
func (o *fixtureOwner) Attach(ctx context.Context, options agent.AgentOptions) (agent.Agent, error) {
	current, err := o.Store.Get(ctx, options.SessionID)
	if err != nil {
		return nil, err
	}
	h, err := o.Factory.Attach(ctx, agent.AttachOptions{Session: current, Agent: options})
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.handles[h.Agent().ID()] = h
	o.mu.Unlock()
	return h.Agent(), nil
}
func (o *fixtureOwner) Dispose(ctx context.Context, id agent.ID) error {
	o.mu.Lock()
	h := o.handles[id]
	o.mu.Unlock()
	if h == nil {
		return agent.ErrNotFound
	}
	return h.Dispose(ctx)
}
func (o *fixtureOwner) Close() {
	_ = o.Factory.Close(context.Background())
	o.Initializers.Close()
	_ = o.Events.Close(context.Background())
}

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

func textChunks(text string) []protocol.StreamChunk {
	return []protocol.StreamChunk{
		{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText},
		{Type: protocol.ChunkTextDelta, Index: 0, Text: text},
		{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: text}},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
	}
}
