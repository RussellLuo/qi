package agent_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"strings"
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
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	promptruntime "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
	toolsruntime "github.com/RussellLuo/qi/kernel/core/tools/plugin"
)

// This deterministic model is local to these SDK integration tests. It sees the
// real AgentLoop request and produces canonical chunks, including usage.
type model struct {
	mu        sync.Mutex
	requests  []llmapi.Request
	active    int
	entered   chan struct{}
	cancelled chan struct{}
}

var _ llmapi.Adapter = (*model)(nil)

func (m *model) Stream(ctx context.Context, request llmapi.Request) contracts.ChunkStream {
	return func(yield func(protocol.StreamChunk, error) bool) {
		m.mu.Lock()
		m.requests = append(m.requests, request.Clone())
		m.active++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.active--
			m.mu.Unlock()
		}()

		input := ""

		for _, message := range request.Messages {
			if message.Role == protocol.RoleUser {
				input = text(message)
			}
		}

		if input == "hold" {
			close(m.entered)
			<-ctx.Done()
			close(m.cancelled)
			yield(protocol.StreamChunk{}, context.Cause(ctx))
			return
		}

		if input == "fail" {
			yield(protocol.StreamChunk{}, errors.New("fixture model failure"))
			return
		}

		answer := "answer: " + input

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
			{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 3, OutputTokens: 2}},
			{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
		} {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

type observations struct {
	mu      sync.Mutex
	session protocol.SessionID
	facts   []protocol.SessionEvent
}

func (o *observations) SessionAppended(id protocol.SessionID, event protocol.SessionEvent) {
	if o.session != "" && id != o.session {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	o.facts = append(o.facts, event.Clone())
}

func (*observations) AssistantStream(contracts.AssistantStreamObservation) {}

func (*observations) DriverEvent(contracts.DriverEvent) {}

func setup(t *testing.T) (*application, *model, *observations, session.Session, agent.Agent, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	m := &model{entered: make(chan struct{}), cancelled: make(chan struct{})}
	o := &observations{session: "conversation"}
	a, err := assemble(m, o)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := a.close(); err != nil {
			t.Error(err)
		}
		m.mu.Lock()
		active := m.active
		m.mu.Unlock()
		if active != 0 || len(a.agents.Snapshot()) != 0 || !factoryClosed(a) {
			t.Error("shutdown left active model calls, registered agents, or an open Factory")
		}
	})
	s, err := a.sessions.Create(ctx, session.CreateRequest{ID: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		o.mu.Lock()
		observed := append([]protocol.SessionEvent(nil), o.facts...)
		o.mu.Unlock()
		if !reflect.DeepEqual(observed, facts(t, s)) {
			t.Error("observations differ from committed facts")
		}
	})

	ag, err := a.attach(ctx, agent.CreateRequest{SessionID: s.ID(), Config: explicitConfig()})
	if err != nil {
		t.Fatal(err)
	}

	return a, m, o, s, ag, ctx
}

func collect(events iter.Seq2[contracts.DriverEvent, error]) ([]contracts.DriverEvent, error) {
	var result []contracts.DriverEvent
	var failure error

	for event, err := range events {
		if err != nil {
			failure = errors.Join(failure, err)
		} else {
			result = append(result, event)
		}
	}

	return result, failure
}

func submit(t *testing.T, ctx context.Context, ag agent.Agent, input string) ([]contracts.DriverEvent, error) {
	t.Helper()
	_, events, err := ag.Submit(ctx, agent.Input{Text: input})
	if err != nil {
		t.Fatal(err)
	}

	result, err := collect(events)
	if idleErr := ag.WaitIdle(ctx); idleErr != nil {
		t.Fatal(idleErr)
	}

	return result, err
}

func text(message protocol.Message) string {
	var result string

	for _, block := range message.Content {
		if b, ok := block.(protocol.TextBlock); ok {
			result += b.Text
		}
	}

	return result
}

func facts(t *testing.T, s session.Session) []protocol.SessionEvent {
	t.Helper()
	var result []protocol.SessionEvent

	for event, err := range s.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}

		result = append(result, event)
	}

	return result
}

func terminal(t *testing.T, s session.Session, events []contracts.DriverEvent, reason string) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Type != "ended" || events[len(events)-1].Reason.Kind != reason {
		t.Fatalf("terminal events = %+v", events)
	}

	for _, event := range events {
		if event.SessionID != s.ID() || event.InvocationID == "" {
			t.Fatal("Driver identity", event)
		}
		if event.Session != nil {
			if err := event.Session.Validate(); err != nil {
				t.Fatal(err)
			}
		}
		if event.Stream != nil {
			if err := event.Stream.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}

	stored := facts(t, s)
	last := stored[len(stored)-1]
	if last.Type != protocol.TurnEnded || protocol.EventPayload[protocol.TurnEndData](last).Reason.Kind != reason {
		t.Fatalf("terminal Session fact = %+v", last)
	}

	if _, err := s.Messages(context.Background()); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
}

func TestPublicLLMProviderRegistrationAndRollback(t *testing.T) {
	a, _, _, s, ag, ctx := setup(t)
	runtime, err := a.scope.Get(llmapi.RuntimeKey)
	if err != nil {
		t.Fatal(err)
	}
	registration := llmapi.AdapterRegistration{
		Route:    "secondary",
		Provider: "secondary",
		Models:   []string{"text"},
		Adapter:  &model{},
	}
	declared := []modelcatalog.Spec{
		{ID: "secondary-text", Label: "Secondary text", Route: "secondary", Provider: "secondary", Model: "text"},
	}
	if err := runtime.Bind(a.scope).RegisterProvider(registration, declared); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Bind(a.scope).RegisterProvider(registration, []modelcatalog.Spec{{ID: "orphan"}}); err == nil {
		t.Fatal("duplicate adapter registration replaced the existing provider")
	}
	failed := registration
	failed.Route, failed.Provider = "failed", "failed"
	failure := runtime.Bind(a.scope).RegisterProvider(failed, []modelcatalog.Spec{{ID: "partial"}, {ID: "secondary-text"}})
	if failure == nil {
		t.Fatal("conflicting catalog was accepted")
	}
	if call, err := runtime.PrepareCall(ctx, protocol.CallConfig{Provider: "failed", Model: "text"}); err == nil {
		call.Close()
		t.Fatal("catalog failure left a dispatchable adapter")
	}
	catalog, _, err := runtime.ModelCatalog()
	if err != nil || !reflect.DeepEqual(catalog, declared) {
		t.Fatalf("catalog conflict altered existing contributions: %+v / %v", catalog, err)
	}
	// Immediate reuse proves rollback happened while the caller Scope is live.
	dispose, err := runtime.Bind(a.scope).RegisterAdapter(failed)
	if err != nil {
		t.Fatal(err)
	}
	dispose()
	if _, err := runtime.Bind(a.scope).RegisterAdapter(failed); err != nil {
		t.Fatal(err)
	}
	dispose() // Must not withdraw the later same-route registration.
	call, err := runtime.PrepareCall(ctx, protocol.CallConfig{Provider: "failed", Model: "text"})
	if err != nil {
		t.Fatal(err)
	}
	call.Close()
	events, err := submit(t, ctx, ag, "after provider rollback")
	if err != nil {
		t.Fatal(err)
	}
	terminal(t, s, events, "completed")
	if err := a.close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Bind(a.scope).RegisterProvider(registration, declared); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("stopped Scope admitted provider registration", err)
	}
	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

func factoryClosed(a *application) bool {
	_, err := a.factory.Create(context.Background(), agent.CreateOptions{})
	return errors.Is(err, agent.ErrClosed)
}

func TestConversation(t *testing.T) {
	a, m, o, s, ag, ctx := setup(t)
	if _, ok := any(m).(llmapi.ModelResolver); ok {
		t.Fatal("minimal external model must exercise the stream-only fallback")
	}

	for _, input := range []string{"first question", "follow-up"} {
		events, err := submit(t, ctx, ag, input)
		if err != nil {
			t.Fatal(err)
		}

		terminal(t, s, events, "completed")
		var answer string

		for _, e := range events {
			if e.Type == "stream" && e.Stream != nil && e.Stream.Chunk != nil && e.Stream.Chunk.Type == protocol.ChunkTextDelta {
				answer += e.Stream.Chunk.Text
			}
		}

		if answer != "answer: "+input {
			t.Fatalf("answer = %q", answer)
		}

	}

	if s.LastTurn() != 2 || ag.State().Status != agent.StatusIdle {
		t.Fatal("conversation did not finish two turns")
	}

	messages, err := s.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var users, answers []string

	for _, message := range messages {
		if message.Role == protocol.RoleUser {
			users = append(users, text(message))
		}

		if message.Role == protocol.RoleAssistant {
			answers = append(answers, text(message))
			if message.Source.Provider != "fixture" || message.Source.Model != "text" {
				t.Fatal("lost model identity")
			}
		}
	}

	if !reflect.DeepEqual(users, []string{"first question", "follow-up"}) ||
		!reflect.DeepEqual(answers, []string{"answer: first question", "answer: follow-up"}) {
		t.Fatalf("messages = %+v", messages)
	}

	m.mu.Lock()
	requests := append([]llmapi.Request(nil), m.requests...)
	m.mu.Unlock()
	if len(requests) != 2 ||
		requests[1].SessionID != s.ID() ||
		requests[1].Config.Provider != "fixture" ||
		requests[1].Config.Model != "text" {
		t.Fatalf("requests = %+v", requests)
	}

	var history []string

	for _, message := range requests[1].Messages {
		if message.Role == protocol.RoleUser || message.Role == protocol.RoleAssistant {
			history = append(history, text(message))
		}
	}

	if !reflect.DeepEqual(history, []string{"first question", "answer: first question", "follow-up"}) {
		t.Fatalf("history = %v", history)
	}

	o.mu.Lock()
	observed := append([]protocol.SessionEvent(nil), o.facts...)
	o.mu.Unlock()
	if !reflect.DeepEqual(observed, facts(t, s)) {
		t.Fatal("observations differ from committed facts")
	}

	var usages int

	for _, event := range facts(t, s) {
		if event.Type != protocol.AssistantMessage {
			continue
		}

		stream, err := protocol.ExpandAssistantStream(protocol.EventPayload[protocol.AssistantMessageData](event).Stream)
		if err != nil {
			t.Fatal(err)
		}

		for _, timed := range stream {
			if timed.Chunk.Type == protocol.ChunkUsage {
				usages++
				if timed.Chunk.Usage == nil || timed.Chunk.Usage.InputTokens != 3 || timed.Chunk.Usage.OutputTokens != 2 {
					t.Fatalf("committed usage = %+v", timed.Chunk.Usage)
				}
			}
		}
	}

	if usages != 2 {
		t.Fatalf("committed usage chunks = %d", usages)
	}

	if err := a.sessions.Delete(ctx, s.ID()); !errors.Is(err, session.ErrAttached) {
		t.Fatalf("attached delete = %v", err)
	}

	if err := a.close(); err != nil {
		t.Fatal(err)
	}

	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

func TestCancel(t *testing.T) {
	a, m, _, s, ag, ctx := setup(t)
	id, stream, err := ag.Submit(ctx, agent.Input{Text: "hold"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-m.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if err := ag.Cancel(ctx, id, agent.CancelRequested); err != nil {
		t.Fatal(err)
	}

	events, err := collect(stream)
	if err == nil {
		t.Fatal("cancelled stream returned no failure")
	}

	if err := ag.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case <-m.cancelled:
	default:
		t.Fatal("cancellation did not reach model context")
	}

	terminal(t, s, events, "aborted")
	continued, err := submit(t, ctx, ag, "after cancellation")
	if err != nil {
		t.Fatal(err)
	}

	terminal(t, s, continued, "completed")
	if err := a.close(); err != nil {
		t.Fatal(err)
	}

	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseActiveAndRepeated(t *testing.T) {
	a, m, _, s, ag, ctx := setup(t)
	_, stream, err := ag.Submit(ctx, agent.Input{Text: "hold"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-m.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Close does not need the application to read its observation iterator.
	done := make(chan error, 2)
	go func() {
		done <- a.close()
	}()
	go func() {
		done <- a.close()
	}()

	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("close failed to drain", ctx.Err())
		}
	}

	select {
	case <-m.cancelled:
	default:
		t.Fatal("close did not cancel model")
	}

	events, err := collect(stream)
	if err == nil {
		t.Fatal("closed active stream returned no failure")
	}

	terminal(t, s, events, "aborted")
	if len(a.agents.Snapshot()) != 0 || !ag.State().Disposed {
		t.Fatal("close left owned resources")
	}

	if _, _, err := ag.Submit(ctx, agent.Input{Text: "late"}); !errors.Is(err, agent.ErrClosed) {
		t.Fatalf("late Submit = %v", err)
	}

	if _, err := a.attach(ctx, agent.CreateRequest{SessionID: s.ID()}); !errors.Is(err, agent.ErrClosed) {
		t.Fatalf("late Create = %v", err)
	}

	if _, err := a.RegisterAdapter(llmapi.AdapterRegistration{}); !errors.Is(err, gordis.ErrClosed) {
		t.Fatal("late registration", err)
	}

	if _, err := a.factory.Create(ctx, agent.CreateOptions{}); !errors.Is(err, agent.ErrClosed) {
		t.Fatal("late factory creation", err)
	}

	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

func TestModelFailureAndReuse(t *testing.T) {
	a, _, _, s, ag, ctx := setup(t)
	events, err := submit(t, ctx, ag, "fail")
	if err == nil || !strings.Contains(err.Error(), "fixture model failure") {
		t.Fatalf("model failure = %v", err)
	}

	terminal(t, s, events, "error")
	continued, err := submit(t, ctx, ag, "after failure")
	if err != nil {
		t.Fatal(err)
	}

	terminal(t, s, continued, "completed")
	if err := a.dispose(ctx, ag.ID()); err != nil {
		t.Fatal(err)
	}

	if _, err := a.agents.Get(ctx, ag.ID()); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("deleted Get = %v", err)
	}

	if err := a.close(); err != nil {
		t.Fatal(err)
	}

	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

func TestPublicCompositionAndRollback(t *testing.T) {
	a, _, _, s, ag, ctx := setup(t)
	if err := a.dispose(ctx, ag.ID()); err != nil {
		t.Fatal(err)
	}

	var released atomic.Int32
	request := agent.CreateRequest{
		SessionID:     s.ID(),
		Config:        explicitConfig(),
		ComposeConfig: compose(func() { released.Add(1) }),
	}
	created, err := a.attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.attach(ctx, request); !errors.Is(err, agent.ErrExists) {
		t.Fatalf("duplicate Create = %v", err)
	}

	if err := a.dispose(ctx, created.ID()); err != nil {
		t.Fatal(err)
	}

	if released.Load() != 1 {
		t.Fatal("configuration did not release exactly once")
	}

	want := errors.New("configuration failed")
	request.ComposeConfig = func(context.Context, agent.Creation) (agent.Config, agent.Cleanup, error) {
		return agent.Config{}, func(context.Context) error { released.Add(1); return nil }, want
	}
	if _, err := a.attach(ctx, request); !errors.Is(err, want) {
		t.Fatalf("rollback = %v", err)
	}

	if released.Load() != 2 {
		t.Fatal("failed composition did not release")
	}
	// Rollback must also detach its reservation, so creation can succeed again.
	request.ComposeConfig = compose(func() { released.Add(1) })
	created, err = a.attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.dispose(ctx, created.ID()); err != nil {
		t.Fatal(err)
	}

	if released.Load() != 3 {
		t.Fatal("second configuration release count")
	}
	// Explicitly name Prompt/Middleware callbacks and PreparedRun from outside.
	_, err = a.RegisterPrompt(prompt.Section{
		ID:     "system",
		Render: func(context.Context, prompt.Context) (string, error) { return "Be concise.", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	var seen atomic.Int32
	_, err = a.RegisterMiddleware(agentmiddleware.Middleware{
		ID: "stream",
		Stream: func(_ context.Context, stream contracts.ChunkStream) (contracts.ChunkStream, error) {
			seen.Add(1)
			return stream, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request.Config.Loop.Selection = runprep.Selection{
		Tools:           []string{},
		PromptSections:  []string{"system"},
		AgentMiddleware: []string{"stream"},
	}
	created, err = a.attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}

	events, err := submit(t, ctx, created, "with contributions")
	if err != nil {
		t.Fatal(err)
	}

	terminal(t, s, events, "completed")

	if seen.Load() != 1 {
		t.Fatal("public middleware did not execute")
	}

	var snapshots []agent.Snapshot = a.agents.Snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v", snapshots)
	}

	if err := a.close(); err != nil {
		t.Fatal(err)
	}

	if released.Load() != 4 {
		t.Fatal("close did not release retained configuration")
	}

	if err := a.sessions.Delete(ctx, s.ID()); err != nil {
		t.Fatal(err)
	}
}

// Private fixture assembly; production consumers obtain the same bound Factory.
type application struct {
	host     *gordis.Host
	scope    *gordis.Scope
	factory  agent.Factory
	sessions session.Store
	agents   agent.Registry
	handles  map[agent.ID]agent.Handle
	adapter  llmapi.Adapter
	observer contracts.ObservationDispatcher
}

func (a *application) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID: "test.consumer",
		Requires: []gordis.ServiceSpec{
			events.BusKey.Spec(),
			agent.FactoryKey.Spec(),
			agent.RegistryKey.Spec(),
			llmapi.RuntimeKey.Spec(),
			prompt.RuntimeKey.Spec(),
			tools.RuntimeKey.Spec(),
			agentmiddleware.RuntimeKey.Spec(),
		},
		New: func() gordis.Plugin { return a },
	}
}

func (a *application) Activate(_ context.Context, s *gordis.Scope) (err error) {
	a.scope = s
	provider, err := s.Get(agent.FactoryKey)
	if err != nil {
		return err
	}
	if a.factory, err = provider.Bind(s); err != nil {
		return err
	}
	if a.agents, err = s.Get(agent.RegistryKey); err != nil {
		return err
	}

	runtime, err := s.Get(llmapi.RuntimeKey)
	if err != nil {
		return err
	}
	if _, err = runtime.Bind(s).RegisterAdapter(llmapi.AdapterRegistration{
		Route:    "fixture-text",
		Provider: "fixture",
		Models:   []string{"text"},
		Adapter:  a.adapter,
	}); err != nil {
		return err
	}

	_, err = events.Bind(s).On(gordisbridge.AssistantStreamTopic, func(_ context.Context, event contracts.AssistantStreamObservation) error {
		a.observer.AssistantStream(event)
		return nil
	}, events.Global())

	return err
}

// The fixture owns borrowed histories separately, so shutdown can be checked
// against their retained facts. This controlled Store is published by a plugin.
type storePlugin struct{ store session.Store }

func (p *storePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.store",
		Provides: []gordis.ServiceSpec{session.StoreKey.Spec()},
		New:      func() gordis.Plugin { return p },
	}
}

func (p *storePlugin) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(session.StoreKey, p.store)
}

func assemble(adapter llmapi.Adapter, observer contracts.ObservationDispatcher) (*application, error) {
	a := &application{
		adapter:  adapter,
		observer: observer,
		sessions: session.NewRuntime(session.RuntimeOptions{Observer: observer}),
		handles:  make(map[agent.ID]agent.Handle),
	}
	host, err := gordis.NewHost(
		&events.Plugin{},
		&storePlugin{a.sessions},
		&llm.Plugin{},
		&promptruntime.Plugin{},
		&toolsruntime.Plugin{},
		&loopplugin.Plugin{},
		a,
	)
	if err != nil {
		return nil, err
	}
	a.host = host
	changes := host.Changes()
	for _, spec := range []gordis.InstanceSpec{
		{ID: "events", Plugin: events.PluginID},
		{ID: "sessions", Plugin: "test.store"},
		{ID: "llm", Plugin: llm.PluginID},
		{ID: "prompts", Plugin: promptruntime.PluginID},
		{ID: "tools", Plugin: toolsruntime.PluginID},
		{ID: "agents", Plugin: loopplugin.PluginID},
		{ID: "consumer", Plugin: a.Spec().ID},
	} {
		changes.Mount(host.Env(), spec)
	}
	op, err := changes.Apply(context.Background())
	if err == nil {
		err = op.WaitReady(context.Background())
	}
	if err != nil {
		return nil, errors.Join(err, host.Shutdown(context.Background()))
	}

	return a, nil
}

func (a *application) close() error { return a.host.Shutdown(context.Background()) }

func (a *application) attach(ctx context.Context, r agent.CreateRequest) (agent.Agent, error) {
	current, err := a.sessions.Get(ctx, r.SessionID)
	if err != nil {
		return nil, err
	}
	h, err := a.factory.Attach(ctx, agent.AttachOptions{Session: current, Agent: r})
	if err != nil {
		return nil, err
	}

	a.handles[h.Agent().ID()] = h
	return h.Agent(), nil
}

func (a *application) dispose(ctx context.Context, id agent.ID) error {
	return a.handles[id].Dispose(ctx)
}

func (a *application) RegisterAdapter(r llmapi.AdapterRegistration) (func(), error) {
	runtime, err := a.scope.Get(llmapi.RuntimeKey)
	if err != nil {
		return nil, err
	}
	return runtime.Bind(a.scope).RegisterAdapter(r)
}

func (a *application) RegisterPrompt(p prompt.Section) (func(), error) {
	runtime, err := a.scope.Get(prompt.RuntimeKey)
	if err != nil {
		return nil, err
	}

	return runtime.Bind(a.scope).Register(p)
}

func (a *application) RegisterMiddleware(m agentmiddleware.Middleware) (func(), error) {
	runtime, err := a.scope.Get(agentmiddleware.RuntimeKey)
	if err != nil {
		return nil, err
	}

	return runtime.Bind(a.scope).Register(m)
}

func explicitConfig() agent.Config {
	return agent.Config{
		Loop: agentloop.Config{
			LLM: protocol.CallConfig{Provider: "fixture", Model: "text"},
			Selection: runprep.Selection{
				Tools:           []string{},
				PromptSections:  []string{},
				AgentMiddleware: []string{},
			},
		},
	}
}

func compose(release func()) agent.ConfigComposer {
	return func(ctx context.Context, creation agent.Creation) (agent.Config, agent.Cleanup, error) {
		return creation.Config, func(context.Context) error { release(); return nil }, ctx.Err()
	}
}
