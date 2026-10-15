package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Each request proposes one workspace read until this Turn has two results.
// Call IDs intentionally collide across Sessions; attribution must include the
// execution identity rather than rely on model-provided call IDs alone.
type workspaceModel struct{}

func (workspaceModel) Stream(_ context.Context, request llm.Request) contracts.ChunkStream {
	return func(yield func(protocol.StreamChunk, error) bool) {
		input, results := "", 0
		for _, message := range request.Messages {
			if message.Role == protocol.RoleUser {
				input, results = text(message), 0
			}
			if message.Role == protocol.RoleTool {
				results++
			}
		}
		var chunks []protocol.StreamChunk
		if results < 2 {
			call := protocol.ToolCall{
				ID:        protocol.ToolCallID(fmt.Sprintf("%s-%d", input, results+1)),
				Name:      "workspace_read",
				Arguments: `{}`,
			}
			chunks = []protocol.StreamChunk{
				{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockToolCall},
				{Type: protocol.ChunkToolCallDelta, Index: 0, ID: call.ID, Name: &call.Name, ArgumentsDelta: call.Arguments},
				{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.ToolCallBlock{ToolCall: call}},
				{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}},
			}
		} else {
			chunks = []protocol.StreamChunk{
				{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText},
				{Type: protocol.ChunkTextDelta, Index: 0, Text: "done"},
				{Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: "done"}},
				{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
			}
		}
		for _, chunk := range chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

type workspaceAuditKey struct {
	Identity tools.ExecutionIdentity
	CallID   protocol.ToolCallID
}

func TestToolIdentityWorkspaceAuditAndCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := assemble(workspaceModel{}, &observations{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.close(); err != nil {
			t.Error(err)
		}
	}()

	// Workspace selection and audit policy belong to this consumer. Core only
	// carries the typed attribution supplied by the actual running Loop.
	workspaces := map[string]string{}
	directories := map[string]string{}
	for _, name := range []string{"left", "right"} {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "value.txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		directories[name] = directory
	}
	var mu sync.Mutex
	requests := map[tools.ExecutionIdentity]bool{}
	audit := map[workspaceAuditKey][]string{}
	values := map[workspaceAuditKey]string{}
	record := func(stage string, execution tools.Execution) {
		mu.Lock()
		defer mu.Unlock()
		if !requests[execution.Identity] {
			t.Errorf("%s received identity without matching Agent request: %+v", stage, execution.Identity)
		}
		key := workspaceAuditKey{execution.Identity, execution.Call.ID}
		audit[key] = append(audit[key], stage)
	}
	if _, err := a.RegisterMiddleware(agentmiddleware.Middleware{
		ID: "request-audit",
		Request: func(_ context.Context, request agentmiddleware.Request) (agentmiddleware.Request, error) {
			mu.Lock()
			requests[tools.ExecutionIdentity{
				AgentID: request.AgentID, SessionID: request.SessionID, Turn: request.Turn, Step: request.Step,
			}] = true
			mu.Unlock()
			return request, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	toolRuntime := a.tools
	owned := a.owner
	arrived := make(chan struct{}, 2)
	barrier := make(chan struct{})
	held := make(chan struct{})
	if _, err := toolRuntime.Register(owned, tools.Definition{
		Name:       "workspace_read",
		Parameters: tools.JSONSchema(`{"type":"object","additionalProperties":false}`),
		Execute: tools.JSONTool(func(callCtx context.Context, execution tools.Execution) (json.RawMessage, error) {
			record("tool", execution)
			if execution.Call.ID == "round-1-1" {
				arrived <- struct{}{}
				select {
				case <-barrier:
				case <-callCtx.Done():
					return nil, context.Cause(callCtx)
				}
			}
			if execution.Call.ID == "hold-1" {
				close(held)
				<-callCtx.Done()
				if callCtx.Err() != context.Canceled {
					return nil, fmt.Errorf("wrong cancellation: %v", callCtx.Err())
				}
			}
			directory, ok := workspaces[execution.Identity.AgentID]
			if !ok {
				t.Errorf("no workspace for %+v", execution.Identity)
				return nil, fmt.Errorf("no workspace for %+v", execution.Identity)
			}
			value, err := os.ReadFile(filepath.Join(directory, "value.txt"))
			if err != nil {
				t.Error(err)
				return nil, err
			}
			mu.Lock()
			values[workspaceAuditKey{execution.Identity, execution.Call.ID}] = string(value)
			mu.Unlock()
			// Actual late success is still settled after cancellation.
			return json.Marshal(string(value))
		}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolRuntime.RegisterMiddleware(owned, tools.Middleware{
		ID: "workspace-audit",
		Pre: func(_ context.Context, execution tools.Execution) (tools.Execution, error) {
			record("pre", execution)
			return execution, nil
		},
		Execute: func(_ context.Context, plan tools.ExecutePlan) (tools.ExecutePlan, error) {
			record("execute", plan.Execution)
			invoke := plan.Invoke
			plan.Invoke = func(ctx context.Context, execution tools.Execution) (tools.ExecutionResult, error) {
				record("wrapper", execution)
				return invoke(ctx, execution)
			}
			return plan, nil
		},
		Post: func(ctx context.Context, execution tools.Execution, result tools.ExecutionResult) (tools.ExecutionResult, error) {
			record("post", execution)
			if ctx.Err() != nil {
				t.Error("post settlement inherited activity cancellation")
			}
			return result, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	agents := map[string]agent.Agent{}
	sessions := map[string]session.Session{}
	for _, name := range []string{"left", "right"} {
		current, err := a.sessions.Create(ctx, session.CreateRequest{ID: protocol.SessionID("session-" + name)})
		if err != nil {
			t.Fatal(err)
		}
		config := explicitConfig()
		config.Loop.MaxSteps = 5
		config.Loop.Selection.Tools = []string{"workspace_read"}
		config.Loop.Selection.AgentMiddleware = []string{"request-audit"}
		ag, err := a.attach(ctx, agent.CreateRequest{ID: agent.ID("agent-" + name), SessionID: current.ID(), Config: config})
		if err != nil {
			t.Fatal(err)
		}
		agents[name], sessions[name] = ag, current
		workspaces[ag.ID().String()] = directories[name]
	}

	// Start both jobs and wait until both are inside the Tool before releasing
	// either. This demonstrates overlapping execution, not just goroutine use.
	done := make(chan error, 2)
	start := func(ag agent.Agent, input string) {
		go func() {
			_, events, err := ag.Submit(ctx, agent.Input{Text: input})
			if err == nil {
				_, err = collect(events)
			}
			if err == nil {
				err = ag.WaitIdle(ctx)
			}
			done <- err
		}()
	}
	waitDone := func() {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(context.Cause(ctx))
		}
	}
	for _, ag := range agents {
		start(ag, "round-1")
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("both workspace Tools did not enter", context.Cause(ctx))
		}
	}
	close(barrier)
	waitDone()
	waitDone()
	for _, ag := range agents {
		start(ag, "round-2")
	}
	waitDone()
	waitDone()

	invocation, events, err := agents["left"].Submit(ctx, agent.Input{Text: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	cancelledResult := make(chan error, 1)
	go func() {
		_, err := collect(events)
		cancelledResult <- err
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("held tool did not enter", context.Cause(ctx))
	}
	// The sibling remains usable while the left execution receives cancellation.
	start(agents["right"], "round-3")
	if err := agents["left"].Cancel(ctx, invocation, agent.CancelRequested); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-cancelledResult:
		if err == nil {
			t.Error("cancelled invocation returned no cancellation failure")
		}
	case <-ctx.Done():
		t.Fatal("cancelled tool did not drain", context.Cause(ctx))
	}
	if err := agents["left"].WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	waitDone()
	start(agents["left"], "round-4")
	waitDone()

	mu.Lock()
	defer mu.Unlock()
	if len(audit) != 13 || len(values) != 13 {
		t.Fatalf("audit/values count = %d/%d, want 13; audit=%+v", len(audit), len(values), audit)
	}
	for key, stages := range audit {
		if !reflect.DeepEqual(stages, []string{"pre", "execute", "wrapper", "tool", "post"}) {
			t.Errorf("%+v stages = %v", key, stages)
		}
		name := "left"
		if key.Identity.AgentID == agents["right"].ID().String() {
			name = "right"
		}
		if key.Identity.AgentID != agents[name].ID().String() ||
			key.Identity.SessionID != protocol.SessionID("session-"+name) ||
			values[key] != name {
			t.Errorf("workspace attribution = %+v, value = %q", key, values[key])
		}
	}
	for _, name := range []string{"left", "right"} {
		calls := 0
		lateSuccess := false
		for event, err := range sessions[name].Events(ctx, -1) {
			if err != nil {
				t.Fatal(err)
			}
			if event.Type == protocol.ToolCalled {
				payload := protocol.EventPayload[protocol.ToolCallData](event)
				key := workspaceAuditKey{tools.ExecutionIdentity{
					AgentID: agents[name].ID().String(), SessionID: sessions[name].ID(), Turn: payload.Turn, Step: payload.Step,
				}, payload.CallID}
				if _, ok := audit[key]; !ok {
					t.Errorf("durable call has no matching execution: %+v", key)
				}
				// ToolExecutionIdentity remains local; the wire call retains its
				// existing Turn/Step/ID/Name/Arguments fields.
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(event.Data, &fields); err != nil {
					t.Fatal(err)
				}
				if len(fields) != 5 {
					t.Errorf("unexpected durable tool-call fields: %s", event.Data)
				}
				calls++
			}
			if event.Type == protocol.ToolResulted && name == "left" {
				payload := protocol.EventPayload[protocol.ToolResultData](event)
				if payload.Turn == 3 {
					lateSuccess = payload.Message.IsError != nil && !*payload.Message.IsError
				}
			}
		}
		want := 6
		if name == "left" {
			want = 7
			if !lateSuccess || sessions[name].LastTurn() != 4 {
				t.Error("cancellation failed to preserve late success or subsequent Turn")
			}
		}
		if calls != want {
			t.Errorf("%s durable calls = %d, want %d", name, calls, want)
		}
	}
}

// Public domain consumers construct the same services without a Host or SDK.
type application struct {
	factory    agent.Factory
	sessions   session.Store
	agents     agent.Registry
	handles    map[agent.ID]agent.Handle
	owner      *testOwner
	llm        *llm.Runtime
	prompts    *prompt.Runtime
	tools      *tools.Runtime
	middleware *agentmiddleware.Runtime
}

func assemble(adapter llm.Adapter, observer contracts.ObservationDispatcher) (*application, error) {
	a := &application{sessions: session.NewRuntime(session.RuntimeOptions{Observer: observer}), agents: agent.NewRegistry(), handles: make(map[agent.ID]agent.Handle), owner: newTestOwner("public-consumer", 1), llm: llm.NewRuntime(), prompts: prompt.NewRuntime(), tools: tools.NewRuntime(), middleware: agentmiddleware.NewRuntime()}
	if _, err := a.llm.RegisterAdapter(a.owner, llm.AdapterRegistration{Route: "fixture-text", Provider: "fixture", Models: []string{"text"}, Adapter: adapter}); err != nil {
		_ = a.close()
		return nil, err
	}
	f, err := agent.NewFactory(agent.FactoryOptions{Registry: a.agents, Store: a.sessions, AttachOnly: true, Driver: agentloop.New(runprep.Coordinator{LLM: a.llm, Prompts: a.prompts, Tools: a.tools, Middleware: a.middleware}, observer)})
	if err != nil {
		_ = a.close()
		return nil, err
	}
	a.factory = f
	return a, nil
}

func (a *application) close() error {
	var err error
	if a.factory != nil {
		err = a.factory.Close(context.Background())
	}
	err = errors.Join(err, a.owner.Stop(context.Background()))
	a.middleware.Close()
	a.llm.Close()
	a.prompts.Close()
	a.tools.Close()
	return err
}

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

func (a *application) RegisterMiddleware(m agentmiddleware.Middleware) (func(), error) {
	return a.middleware.Register(a.owner, m)
}

func explicitConfig() agent.Config {
	return agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fixture", Model: "text"}, Selection: runprep.Selection{Tools: []string{}, PromptSections: []string{}, AgentMiddleware: []string{}}}}
}

type model struct {
	mu        sync.Mutex
	requests  []llm.Request
	active    int
	entered   chan struct{}
	cancelled chan struct{}
}

func (m *model) Stream(ctx context.Context, request llm.Request) contracts.ChunkStream {
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

func text(message protocol.Message) string {
	var result string

	for _, block := range message.Content {
		if b, ok := block.(protocol.TextBlock); ok {
			result += b.Text
		}
	}

	return result
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
