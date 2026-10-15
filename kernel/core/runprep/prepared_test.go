package runprep

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func TestPreparedRunFreezesPromptToolLLMAndMiddleware(t *testing.T) {
	prompts := prompt.NewRuntime()
	toolRuntime := tools.NewRuntime()
	llmRuntime := llm.NewRuntime()
	first := newTestOwner("core", 1)
	if _, err := prompts.Register(first, prompt.Section{
		ID: "identity",
		Render: func(context.Context, prompt.Context) (string, error) {
			return "prompt-v1", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := toolRuntime.Register(first, frozenTool("v1")); err != nil {
		t.Fatal(err)
	}

	if _, err := toolRuntime.RegisterMiddleware(first, tools.Middleware{
		ID: "v1",
		Pre: func(_ context.Context, execution tools.Execution) (tools.Execution, error) {
			return execution, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	adapterV1 := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: frozenTextChunks("v1")}}}
	if _, err := llmRuntime.RegisterAdapter(first, llm.AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapterV1,
	}); err != nil {
		t.Fatal(err)
	}

	prepared, err := (Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: agentmiddleware.NewRuntime(),
	}).PrepareRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	second := newTestOwner("late", 2)
	if _, err := prompts.Register(second, prompt.Section{
		ID:    "late",
		Order: -1,
		Render: func(context.Context, prompt.Context) (string, error) {
			return "prompt-v2", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := toolRuntime.Register(second, frozenTool("late")); err == nil {
		t.Fatal("duplicate live tool registration was accepted")
	}

	step, err := prepared.PrepareStep(
		context.Background(),
		prompt.Context{},
		nil,
		protocol.CallConfig{Provider: "fake", Model: "model"},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer step.Close()

	if got := step.Prompt().Text; got != "prompt-v1" {
		t.Fatalf("prompt = %q", got)
	}

	request, err := step.Request()
	if err != nil {
		t.Fatal(err)
	}

	if len(request.Tools) != 1 || request.Tools[0].Description != "v1" {
		t.Fatalf("tool schemas = %+v", request.Tools)
	}

	if revisions := prepared.Revisions(); revisions.Tool == 0 || revisions.LLM == 0 {
		t.Fatalf("revisions = %+v", revisions)
	}

	var text string

	for chunk, err := range step.LLM().Stream(context.Background(), request) {
		if err != nil {
			t.Fatal(err)
		}

		if chunk.Type == protocol.ChunkTextDelta {
			text += chunk.Text
		}
	}

	if text != "v1" || len(adapterV1.Seen) != 1 {
		t.Fatalf("LLM text/seen = %q/%d", text, len(adapterV1.Seen))
	}

	call, err := prepared.PrepareTool(protocol.ToolCall{
		ID:        "call-1",
		Name:      "echo",
		Arguments: string(`{"text":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := call.Execute(context.Background())
	if err != nil || string(result.Value) != `{"value":"v1"}` {
		t.Fatalf("tool result/error = %s/%v", result.Content, err)
	}
}

func TestPreparedRunPartialFailureReleasesEarlierSnapshots(t *testing.T) {
	prompts := prompt.NewRuntime()
	owned := newTestOwner("prompt", 1)
	if _, err := prompts.Register(owned, prompt.Section{
		ID: "identity",
		Render: func(context.Context, prompt.Context) (string, error) {
			return "prompt", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	toolRuntime := tools.NewRuntime()
	toolRuntime.Close()
	_, err := (Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llm.NewRuntime(),
		Middleware: agentmiddleware.NewRuntime(),
	}).PrepareRun(context.Background())
	if !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("PrepareRun error = %v", err)
	}

	if err := owned.Stop(context.Background()); err != nil {
		t.Fatalf("prompt snapshot lease leaked: %v", err)
	}
}

func TestPreparedRunAndStepCloseAreIdempotent(t *testing.T) {
	prompts := prompt.NewRuntime()
	toolsRuntime := tools.NewRuntime()
	llmRuntime := llm.NewRuntime()
	prepared, err := (Coordinator{
		Prompts:    prompts,
		Tools:      toolsRuntime,
		LLM:        llmRuntime,
		Middleware: agentmiddleware.NewRuntime(),
	}).PrepareRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	prepared.Close()
	prepared.Close()
	if _, err := prepared.PrepareStep(
		context.Background(),
		prompt.Context{},
		nil,
		protocol.CallConfig{Provider: "missing", Model: "model"},
	); !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("PrepareStep after close error = %v", err)
	}
}

func TestPreparedRunAppliesPresetSelectionBeforeLeasing(t *testing.T) {
	prompts := prompt.NewRuntime()
	toolRuntime := tools.NewRuntime()
	middleware := agentmiddleware.NewRuntime()
	llmRuntime := llm.NewRuntime()
	owned := newTestOwner("composition", 1)

	for _, section := range []string{"identity", "optional"} {
		if _, err := prompts.Register(owned, prompt.Section{ID: section, Render: staticPrompt(section)}); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"first", "second"} {
		definition := frozenTool(name)
		definition.Name = name
		if _, err := toolRuntime.Register(owned, definition); err != nil {
			t.Fatal(err)
		}
	}

	var middlewareCalls []string

	for _, id := range []string{"keep", "drop"} {
		id := id
		if _, err := middleware.Register(owned, agentmiddleware.Middleware{
			ID: id,
			PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
				middlewareCalls = append(middlewareCalls, id)
				return input, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: frozenTextChunks("ok")}}}
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	prepared, err := (Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: middleware,
	}).PrepareRun(
		context.Background(), Selection{
			Tools:           []string{"second"},
			PromptSections:  []string{"identity"},
			AgentMiddleware: []string{"keep"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	if schemas := prepared.ToolSchemas(); len(schemas) != 1 || schemas[0].Name != "second" {
		t.Fatalf("selected Tool schemas = %+v", schemas)
	}

	if _, err := prepared.PreStep(context.Background(), agentmiddleware.StepInput{}); err != nil {
		t.Fatal(err)
	}

	if len(middlewareCalls) != 1 || middlewareCalls[0] != "keep" {
		t.Fatalf("selected Agent middleware = %v", middlewareCalls)
	}

	step, err := prepared.PrepareStep(
		context.Background(),
		prompt.Context{},
		nil,
		protocol.CallConfig{Provider: "fake", Model: "model"},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer step.Close()

	if got := step.Prompt().SectionIDs; len(got) != 1 || got[0] != "identity" {
		t.Fatalf("selected Prompt sections = %v", got)
	}
}

func staticPrompt(value string) func(context.Context, prompt.Context) (string, error) {
	return func(context.Context, prompt.Context) (string, error) { return value, nil }
}

func frozenTool(version string) tools.Definition {
	return tools.Definition{
		Name:        "echo",
		Description: version,
		Parameters:  tools.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Output:      tools.JSONSchema(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
		Execute: tools.JSONTool(func(context.Context, tools.Execution) (json.RawMessage, error) {
			return json.RawMessage(`{"value":"` + version + `"}`), nil
		}),
	}
}

func frozenTextChunks(text string) []protocol.StreamChunk {
	return []protocol.StreamChunk{
		{
			Type:      protocol.ChunkBlockStart,
			Index:     0,
			BlockType: protocol.BlockText,
		},
		{
			Type:  protocol.ChunkTextDelta,
			Index: 0,
			Text:  text,
		},
		{
			Type:  protocol.ChunkBlockEnd,
			Index: 0,
			Block: protocol.TextBlock{Text: text},
		},
		{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
	}
}

func TestFrozenRequestCopiesConfigSurfaceAndHistoricalDeclarations(t *testing.T) {
	prompts := prompt.NewRuntime()
	toolRuntime := tools.NewRuntime()
	llmRuntime := llm.NewRuntime()
	adapter := &scriptedAdapter{}
	if _, err := llmRuntime.RegisterAdapter(newTestOwner("request", 1), llm.AdapterRegistration{
		Route:    "outer",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	prepared, err := (Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: agentmiddleware.NewRuntime(),
	}).PrepareRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	max := int64(12)
	config := protocol.CallConfig{
		Provider:  "fake",
		Model:     "model",
		MaxTokens: &max,
		Stop:      []string{},
	}
	call, err := prepared.PrepareCall(context.Background(), prompt.Snapshot{}, nil, config)
	if err != nil {
		t.Fatal(err)
	}

	defer call.Close()

	request := llm.Request{
		Config:    call.LLM().Config(),
		SessionID: "session",
		Messages:  []protocol.Message{protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "original")},
		Tools: []tools.Schema{{
			Name:         "current",
			Parameters:   tools.JSONSchema(`{"type":"object"}`),
			DeferLoading: true,
		}},
		ToolHistory: llm.ToolHistory{
			Tools: []tools.Schema{{Name: "old", Parameters: tools.JSONSchema(`{"type":"object"}`)}},
			Updates: []llm.ToolHistoryUpdate{
				{
					MessageID: "dev",
					Additions: []tools.Schema{{Name: "new", Parameters: tools.JSONSchema(`{"type":"object"}`)}},
				},
			},
		},
	}
	if err := call.FreezeRequest(request); err != nil {
		t.Fatal(err)
	}

	max = 999
	*request.Config.MaxTokens = 888
	request.Messages[0].Content[0] = protocol.TextBlock{Text: "changed"}
	request.Tools[0].Parameters[0] = '['
	request.ToolHistory.Tools[0].Parameters[0] = '['
	request.ToolHistory.Updates[0].Additions[0].Parameters[0] = '['
	frozen, err := call.Request()
	if err != nil {
		t.Fatal(err)
	}

	if *frozen.Config.MaxTokens != 12 ||
		frozen.Config.Stop == nil ||
		len(frozen.Config.Stop) != 0 ||
		frozen.Messages[0].Content[0].(protocol.TextBlock).Text != "original" ||
		frozen.Tools[0].Parameters[0] != '{' ||
		!frozen.Tools[0].DeferLoading ||
		frozen.ToolHistory.Tools[0].Parameters[0] != '{' ||
		frozen.ToolHistory.Updates[0].Additions[0].Parameters[0] != '{' {
		t.Fatalf("request changed after freeze: %+v", frozen)
	}
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
