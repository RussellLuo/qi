package agentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func TestCompleteLoopStreamsAndExecutesMultipleToolsSerially(t *testing.T) {
	var executionOrder []string
	var executionMu sync.Mutex
	adapter := &scriptedAdapter{
		Version: "v1",
		Scripts: []scriptedScript{
			{
				Chunks: toolChunks(
					protocol.ToolCall{
						ID:        "call-1",
						Name:      "echo",
						Arguments: string(`{"text":"one"}`),
					},
					protocol.ToolCall{
						ID:        "call-2",
						Name:      "echo",
						Arguments: string(`{"text":"two"}`),
					},
				),
			},
			{Chunks: textChunks("done")},
		},
	}
	h := newQi(t, adapter, func(_ context.Context, execution tools.Execution) (json.RawMessage, error) {
		executionMu.Lock()
		executionOrder = append(executionOrder, execution.Call.ID.String())
		executionMu.Unlock()
		return json.Marshal(map[string]string{"text": string(execution.Arguments)})
	})
	defer h.stop(t)

	events, err := h.drive(context.Background(), Config{MaxSteps: 8, LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(events) == 0 || events[len(events)-1].Type != "ended" {
		t.Fatal(events)
	}

	started := 0
	for _, e := range events {
		if e.Type == "started" {
			started++
		}
		if e.SessionID != h.current.ID() || e.InvocationID != "run-1" {
			t.Fatal("wrong Driver identity", e)
		}
	}
	if started != 1 {
		t.Fatal("Driver start count", started)
	}
	if countSessionKind(events, protocol.ToolCalled) != 2 || countSessionKind(events, protocol.ToolResulted) != 2 {
		t.Fatal("missing tool facts", events)
	}
	if textDelta(events) != "done" {
		t.Fatal("raw text chunks", textDelta(events))
	}
	if !reflect.DeepEqual(executionOrder, []string{"call-1", "call-2"}) {
		t.Fatalf("tool execution order = %v", executionOrder)
	}

	kinds := sessionKinds(t, h.current)
	wantSession := []protocol.SessionEventKind{
		protocol.InboxSpliced,
		protocol.TurnStarted,
		protocol.InboxSpliced,
		protocol.StepStarted,
		protocol.SystemMessage,
		protocol.UserMessage,
		protocol.RequestHeader,
		protocol.RequestContextEvent,
		protocol.AssistantMessage,
		protocol.ToolCalled,
		protocol.ToolResulted,
		protocol.ToolCalled,
		protocol.ToolResulted,
		protocol.StepEnded,
		protocol.StepStarted,
		protocol.AssistantMessage,
		protocol.StepEnded,
		protocol.TurnEnded,
	}
	if !reflect.DeepEqual(kinds, wantSession) {
		t.Fatalf("session kinds = %v, want %v", kinds, wantSession)
	}

	messages, err := h.current.Messages(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if got := messageSources(messages); !reflect.DeepEqual(got, []string{
		protocol.SourceSystem,
		protocol.SourceUser,
		protocol.SourceModel,
		protocol.SourceTool,
		protocol.SourceTool,
		protocol.SourceModel,
	}) {
		t.Fatalf("message sources = %v", got)
	}

	if len(adapter.Seen) != 2 || len(adapter.Seen[1].Messages) != 5 {
		t.Fatalf("second LLM request did not contain system, user, assistant, and two results: %#v", adapter.Seen)
	}

	assertTerminalConsistency(t, h.current, events, protocol.ReasonCompleted)
}

func TestToolFailuresAreModelVisibleAndDoNotAbortByDefault(t *testing.T) {
	tests := []struct {
		name string
		call protocol.ToolCall
	}{
		{
			name: "unknown tool",
			call: protocol.ToolCall{
				ID:        "call-unknown",
				Name:      "missing",
				Arguments: string(`{}`),
			},
		},
		{
			name: "invalid JSON",
			call: protocol.ToolCall{
				ID:        "call-invalid-json",
				Name:      "echo",
				Arguments: "{broken",
			},
		},
		{
			name: "empty arguments",
			call: protocol.ToolCall{
				ID:        "call-empty",
				Name:      "echo",
				Arguments: "",
			},
		},
		{
			name: "invalid arguments",
			call: protocol.ToolCall{
				ID:        "call-invalid",
				Name:      "echo",
				Arguments: string(`{"wrong":true}`),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &scriptedAdapter{
				Version: "v1",
				Scripts: []scriptedScript{
					{Chunks: toolChunks(test.call)}, {Chunks: textChunks("recovered")},
				},
			}
			h := newQi(t, adapter, nil)
			defer h.stop(t)

			events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if err != nil {
				t.Fatal(err)
			}

			var result *protocol.ToolResult

			for _, event := range events {
				if event.Type == "session" && event.Session != nil && event.Session.Type == protocol.ToolResulted {
					data := protocol.EventPayload[protocol.ToolResultData](*event.Session)
					result = &protocol.ToolResult{Message: data.Message, Meta: data.Meta, Error: data.Error}
				}
			}

			if result == nil || !(result.Message.IsError != nil && *result.Message.IsError) {
				t.Fatalf("tool failure was not materialized: %+v", result)
			}

			if result.Message.Role != protocol.RoleTool ||
				result.Message.ToolCallID != test.call.ID ||
				result.Message.Source.CallID != test.call.ID {
				t.Fatalf("bad tool correlation: %+v", result.Message)
			}

			if len(adapter.Seen) != 2 {
				t.Fatalf("no second model request: %+v", adapter.Seen)
			}

			last := adapter.Seen[1].Messages[len(adapter.Seen[1].Messages)-1]
			if last.Role != protocol.RoleTool || last.ToolCallID != test.call.ID {
				t.Fatalf("tool result context = %+v", last)
			}

			for _, event := range sessionEvents(t, h.current) {
				if event.Type == protocol.ToolCalled &&
					protocol.EventPayload[protocol.ToolCallData](event).Arguments != test.call.Arguments {
					t.Fatalf("persisted raw args changed: %+v", event)
				}
			}

			assertTerminalConsistency(t, h.current, events, protocol.ReasonCompleted)
		})
	}
}

func TestStreamFailurePersistsPartialAssistantAttempt(t *testing.T) {
	streamErr := errors.New("stream broke")
	adapter := &scriptedAdapter{
		Version: "v1",
		Scripts: []scriptedScript{
			{
				Chunks: []protocol.StreamChunk{
					{
						Type:      protocol.ChunkBlockStart,
						Index:     0,
						BlockType: protocol.BlockText,
					},
					{
						Type:  protocol.ChunkTextDelta,
						Index: 0,
						Text:  "partial",
					},
				},
				Err: streamErr,
			},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	var failure *llm.StreamFailure
	if !errors.As(err, &failure) ||
		failure.Reason.Failure.Message != streamErr.Error() ||
		failure.Reason.Failure.Code != "UNKNOWN" {
		t.Fatalf("Drive error = %v", err)
	}

	persisted := sessionEvents(t, h.current)
	var attempt *protocol.AttemptData

	for _, event := range persisted {
		if event.Type == protocol.AssistantAttempt {
			d := protocol.EventPayload[protocol.AttemptData](event)
			attempt = &d
		}
	}

	if attempt == nil || len(attempt.Stream) < 2 || attempt.Stream[1].Chunk.Text != "partial" {
		t.Fatalf("partial attempt = %+v", attempt)
	}

	assertTerminalConsistency(t, h.current, events, protocol.ReasonFailed)
}

func TestMaxStepsSettlesAfterExecutingLastToolBatch(t *testing.T) {
	adapter := &scriptedAdapter{
		Version: "v1",
		Scripts: []scriptedScript{
			{
				Chunks: toolChunks(protocol.ToolCall{
					ID:        "call-1",
					Name:      "echo",
					Arguments: string(`{"text":"one"}`),
				}),
			},
			{
				Chunks: toolChunks(protocol.ToolCall{
					ID:        "call-2",
					Name:      "echo",
					Arguments: string(`{"text":"two"}`),
				}),
			},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	events, err := h.drive(context.Background(), Config{MaxSteps: 2, LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("Drive error = %v", err)
	}

	if got := countSessionKind(events, protocol.ToolResulted); got != 2 {
		t.Fatalf("tool result count = %d", got)
	}

	assertTerminalConsistency(t, h.current, events, protocol.ReasonMaxSteps)
}

func TestAgentMiddlewareWaterfallsAreAppliedFromPreparedRun(t *testing.T) {
	adapter := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: textChunks("answer")}}}
	h := newQi(t, adapter, nil)
	stopped := false
	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "test",
		PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
			for i := range input.Messages {
				if input.Messages[i].Source.Kind == protocol.SourceUser {
					input.Messages[i] = protocol.NewTextMessage(input.Messages[i].ID, protocol.RoleUser, protocol.SourceUser, "transformed")
				}
			}

			return input, nil
		},
		Request: func(_ context.Context, request agentmiddleware.Request) (agentmiddleware.Request, error) {
			request.Config.Provider = "fake"
			return request, nil
		},
		Stream: func(_ context.Context, upstream contracts.ChunkStream) (contracts.ChunkStream, error) {
			return func(yield func(protocol.StreamChunk, error) bool) {
				for chunk, err := range upstream {
					if chunk.Type == protocol.ChunkTextDelta {
						chunk.Text = "wrapped:" + chunk.Text
					} else if chunk.Type == protocol.ChunkBlockEnd {
						if b, ok := chunk.Block.(protocol.TextBlock); ok {
							b.Text = "wrapped:" + b.Text
							chunk.Block = b
						}
					}

					if !yield(chunk, err) {
						return
					}
				}
			}, nil
		},
		TurnStopping: func(context.Context, agentmiddleware.TurnStop) error {
			stopped = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	defer h.stop(t)

	events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "replaced", Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}

	if !stopped || len(adapter.Seen) != 1 || firstText(conversationMessages(adapter.Seen[0].Messages)) != "transformed" {
		t.Fatalf("middleware effects missing: stopped=%v seen=%#v", stopped, adapter.Seen)
	}

	if got := textDelta(events); got != "wrapped:answer" {
		t.Fatalf("text delta = %q", got)
	}

	messages, err := h.current.Messages(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if got := firstText(messages); got != "You are a test assistant." {
		t.Fatalf("first persisted text = %q", got)
	}

	if got := firstText(messages[1:]); got != "transformed" {
		t.Fatalf("persisted user input = %q", got)
	}
}

func TestCancellationSettlesAndReleasesPreparedOwners(t *testing.T) {
	entered := make(chan struct{})
	adapter := &blockingAdapter{entered: entered}
	h := newQi(t, adapter, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var events []contracts.DriverEvent
	var runErr error
	go func() {
		events, runErr = h.drive(ctx, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("LLM stream did not start")
	}

	secondErr := h.loop.Drive(context.Background(), Run{
		Agent:        h.agent,
		Attached:     AttachedRun{Session: h.current, Reservation: h.reservation, Inbox: session.NewInbox(h.current), RequestState: &h.agent.requestState},
		InvocationID: "run-2",
		Config:       Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}},
	}, func(contracts.DriverEvent) error { return nil })
	if !errors.Is(secondErr, session.ErrRunActive) {
		t.Fatalf("concurrent session run error = %v", secondErr)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled run did not settle")
	}

	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Drive error = %v", runErr)
	}

	assertTerminalConsistency(t, h.current, events, protocol.ReasonCancelled)
	h.stop(t)
}

func TestDriverSinkFailureDoesNotEraseSessionFacts(t *testing.T) {
	adapter := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: textChunks("persisted")}}}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	sinkErr := errors.New("client disconnected")
	err := h.loop.Drive(
		context.Background(),
		h.run(Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}),
		func(contracts.DriverEvent) error {
			return sinkErr
		},
	)
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Drive error = %v", err)
	}

	events := sessionEvents(t, h.current)
	if last := events[len(events)-1]; last.Type != protocol.TurnEnded ||
		protocol.EventPayload[protocol.TurnEndData](last).Reason.Kind != "completed" {
		t.Fatalf("last session event = %+v", last)
	}
}

type testAgentRuntime struct {
	contracts.AssistantStreamState
	requestState RequestState
	sessionID    protocol.SessionID
}

func (a *testAgentRuntime) SessionID() protocol.SessionID { return a.sessionID }

type qi struct {
	agent       *testAgentRuntime
	loop        *Loop
	store       session.Store
	reservation contracts.SessionReservation
	current     session.Session
	owner       *testOwner
	middleware  *agentmiddleware.Runtime
}

func newQi(t *testing.T, adapter llm.Adapter, execute func(context.Context, tools.Execution) (json.RawMessage, error)) *qi {
	t.Helper()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, err := store.Create(context.Background(), session.CreateRequest{ID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}

	owned := newTestOwner("core", 1)
	prompts := prompt.NewRuntime()
	if _, err := prompts.Register(owned, prompt.Section{
		ID: "identity",
		Render: func(context.Context, prompt.Context) (string, error) {
			return "You are a test assistant.", nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	toolRuntime := tools.NewRuntime()
	if execute == nil {
		execute = func(_ context.Context, execution tools.Execution) (json.RawMessage, error) {
			return json.Marshal(map[string]json.RawMessage{"text": execution.Arguments})
		}
	}

	if _, err := toolRuntime.Register(owned, tools.Definition{
		Name:        "echo",
		Description: "echo",
		Parameters:  tools.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Output:      tools.JSONSchema(`{"type":"object"}`),
		Execute:     tools.JSONTool(execute),
	}); err != nil {
		t.Fatal(err)
	}

	llmRuntime := llm.NewRuntime()
	if _, err := llmRuntime.RegisterAdapter(owned, llm.AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	middleware := agentmiddleware.NewRuntime()
	loop := New(runprep.Coordinator{
		Prompts:    prompts,
		Tools:      toolRuntime,
		LLM:        llmRuntime,
		Middleware: middleware,
	})

	return &qi{
		agent:      &testAgentRuntime{sessionID: current.ID()},
		loop:       loop,
		store:      store,
		current:    current,
		owner:      owned,
		middleware: middleware,
	}
}

func (h *qi) run(config Config) Run {
	if h.reservation == nil {
		var err error
		h.reservation, err = h.current.Attach()
		if err != nil {
			panic(err)
		}
	}
	inbox := session.NewInbox(h.current)
	message := protocol.NewTextMessage(protocol.MessageID(fmt.Sprintf("input-%d", h.current.Watermark())), protocol.RoleUser, protocol.SourceUser, "hello")
	if _, err := inbox.Splice(context.Background(), contracts.NextTurn, 0, 0, []protocol.Message{message}, false); err != nil {
		panic(err)
	}
	return Run{Agent: h.agent, Attached: AttachedRun{Session: h.current, Reservation: h.reservation, Inbox: inbox, RequestState: &h.agent.requestState}, InvocationID: "run-1", Config: config}
}

func (h *qi) drive(ctx context.Context, config Config) ([]contracts.DriverEvent, error) {
	var events []contracts.DriverEvent
	err := h.loop.Drive(ctx, h.run(config), func(event contracts.DriverEvent) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func (h *qi) stop(t *testing.T) {
	if h.reservation != nil {
		if err := h.reservation.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := h.owner.Stop(ctx); err != nil {
		t.Fatalf("prepared resource lease leaked: %v", err)
	}
}

type blockingAdapter struct {
	entered chan struct{}
	once    sync.Once
}

func (a *blockingAdapter) ResolveModel(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	return llm.ModelInfo{Ref: ref, ContextWindow: 1024}, nil
}

func (a *blockingAdapter) Stream(ctx context.Context, _ llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	return func(yield func(protocol.StreamChunk, error) bool) {
		a.once.Do(func() { close(a.entered) })
		<-ctx.Done()
		yield(protocol.StreamChunk{}, ctx.Err())
	}
}

func textChunks(text string) []protocol.StreamChunk {
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

func toolChunks(calls ...protocol.ToolCall) []protocol.StreamChunk {
	var chunks []protocol.StreamChunk

	for i, call := range calls {
		chunks = append(chunks,
			protocol.StreamChunk{
				Type:      protocol.ChunkBlockStart,
				Index:     int64(i),
				BlockType: protocol.BlockToolCall,
			},
			protocol.StreamChunk{
				Type:           protocol.ChunkToolCallDelta,
				Index:          int64(i),
				ID:             call.ID,
				Name:           &call.Name,
				ArgumentsDelta: call.Arguments,
			},
			protocol.StreamChunk{
				Type:  protocol.ChunkBlockEnd,
				Index: int64(i),
				Block: protocol.ToolCallBlock{ToolCall: call},
			},
		)
	}

	return append(chunks, protocol.StreamChunk{
		Type:   protocol.ChunkFinish,
		Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls},
	})
}

func sessionEvents(t *testing.T, target session.Session) []protocol.SessionEvent {
	t.Helper()
	var events []protocol.SessionEvent

	for event, err := range target.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}

		events = append(events, event)
	}

	return events
}

func sessionKinds(t *testing.T, target session.Session) []protocol.SessionEventKind {
	events := sessionEvents(t, target)
	kinds := make([]protocol.SessionEventKind, len(events))

	for i, event := range events {
		kinds[i] = event.Type
	}

	return kinds
}

func messageSources(messages []protocol.Message) []string {
	result := make([]string, len(messages))

	for i, message := range messages {
		result[i] = message.Source.Kind
	}

	return result
}

func assertTerminalConsistency(t *testing.T, target session.Session, events []contracts.DriverEvent, reason protocol.CompletionReason) {
	t.Helper()
	persisted := sessionEvents(t, target)
	lastFact := persisted[len(persisted)-1]
	lastLive := events[len(events)-1]
	expected := map[protocol.CompletionReason]string{
		protocol.ReasonCompleted: "completed",
		protocol.ReasonFailed:    "error",
		protocol.ReasonCancelled: "aborted",
		protocol.ReasonMaxSteps:  "blocked",
	}[reason]
	if lastFact.Type != protocol.TurnEnded || protocol.EventPayload[protocol.TurnEndData](lastFact).Reason.Kind != expected {
		t.Fatalf("session terminal = %+v", lastFact)
	}

	if lastLive.Type != "ended" || lastLive.Reason.Kind != expected {
		t.Fatalf("run terminal = %+v", lastLive)
	}

	if _, err := target.Messages(context.Background()); err != nil {
		t.Fatalf("session replay: %v", err)
	}
}

func countSessionKind(events []contracts.DriverEvent, kind protocol.SessionEventKind) int {
	count := 0
	for _, e := range events {
		if e.Type == "session" && e.Session != nil && e.Session.Type == kind {
			count++
		}
	}
	return count
}
func textDelta(events []contracts.DriverEvent) string {
	result := ""
	for _, e := range events {
		if e.Type == "stream" && e.Stream != nil && e.Stream.Chunk != nil && e.Stream.Chunk.Type == protocol.ChunkTextDelta {
			result += e.Stream.Chunk.Text
		}
	}
	return result
}

func conversationMessages(messages []protocol.Message) []protocol.Message {
	result := make([]protocol.Message, 0, len(messages))

	for _, message := range messages {
		if message.Role != protocol.RoleSystem {
			result = append(result, message.Clone())
		}
	}

	return result
}

func firstText(messages []protocol.Message) string {
	for _, message := range messages {
		for _, block := range message.Content {
			if text, ok := block.(protocol.TextBlock); ok {
				return text.Text
			}
		}
	}

	return ""
}

func TestToolContentMetaFailureAndLocalValueBoundary(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			raw := ` { "text": "hi" } `
			adapter := &scriptedAdapter{
				Version: "v1",
				Scripts: []scriptedScript{
					{
						Chunks: toolChunks(protocol.ToolCall{
							ID:        "custom-call",
							Name:      "custom",
							Arguments: raw,
						}),
					},
					{Chunks: textChunks("done")},
				},
			}
			h := newQi(t, adapter, nil)
			defer h.stop(t)

			_, err := h.loop.coordinator.Tools.Register(h.owner, tools.Definition{
				Name:       "custom",
				Parameters: tools.JSONSchema(`{"type":"object"}`),
				Execute: func(_ context.Context, e tools.Execution) (tools.ExecutionResult, error) {
					if e.Call.Arguments != raw || string(e.Arguments) != raw {
						t.Errorf("arguments changed: %+v", e)
					}

					result := tools.ExecutionResult{
						Value:   json.RawMessage(`{"executionOnlySecret":"local"}`),
						Content: []protocol.ContentBlock{protocol.TextBlock{Text: "model-facing content"}},
						Meta:    json.RawMessage(`{"display":"native"}`),
					}
					if failure {
						result.Value = nil
						result.IsError = true
						result.Error = &tools.Failure{
							Message: "private failure description",
							Info: &protocol.ToolFailureInfo{
								Name:   "BusinessError",
								Code:   "BUSINESS",
								Reason: stringPointer("quota"),
							},
						}
					}

					return result, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			_, err = h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if err != nil {
				t.Fatal(err)
			}

			facts := sessionEvents(t, h.current)
			found := false

			for _, event := range facts {
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}

				if bytes.Contains(encoded, []byte("executionOnlySecret")) ||
					bytes.Contains(encoded, []byte("private failure description")) {
					t.Fatalf("local execution data leaked: %s", encoded)
				}

				if event.Type != protocol.ToolResulted {
					continue
				}

				found = true
				result := protocol.EventPayload[protocol.ToolResultData](event)
				if result.Message.Role != protocol.RoleTool ||
					result.Message.ToolCallID != "custom-call" ||
					result.Message.Source.CallID != "custom-call" ||
					result.Message.Content[0].(protocol.TextBlock).Text != "model-facing content" ||
					string(result.Meta) != `{"display":"native"}` {
					t.Fatalf("result fact = %+v", result)
				}

				if failure &&
					(result.Error == nil ||
						result.Error.Name != "BusinessError" ||
						result.Error.Code != "BUSINESS" ||
						(result.Error.Reason == nil || *result.Error.Reason != "quota")) {
					t.Fatalf("failure info = %+v", result.Error)
				}
			}

			if !found {
				t.Fatal("no tool result fact")
			}

			next := adapter.Seen[1].Messages
			message := next[len(next)-1]
			if message.Role != protocol.RoleTool || message.Content[0].(protocol.TextBlock).Text != "model-facing content" {
				t.Fatalf("next-step context = %+v", message)
			}

			for _, event := range facts {
				if event.Type == protocol.AssistantMessage {
					m := protocol.EventPayload[protocol.AssistantMessageData](event).Message
					if m.Source.Provider != "fake" || m.Source.Model != "model" {
						t.Fatalf("model source identity = %+v", m.Source)
					}
				}
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

func TestMaxTokensProposalIsNotDispatched(t *testing.T) {
	chunks := toolChunks(protocol.ToolCall{
		ID:        "c-truncated",
		Name:      "echo",
		Arguments: `{"text":`,
	})
	chunks[len(chunks)-1].Reason = protocol.FinishReason{Kind: protocol.FinishMaxTokens}
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: chunks}}}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}

	for _, event := range sessionEvents(t, h.current) {
		if event.Type == protocol.ToolCalled || event.Type == protocol.ToolResulted {
			t.Fatalf("dispatched truncated proposal: %+v", event)
		}
	}

	for _, event := range events {
		if event.Type == "session" && event.Session != nil && event.Session.Type == protocol.ToolCalled {
			t.Fatalf("live tool call after max-tokens: %+v", event)
		}
	}
}

func TestLoopPersistsRawStreamsReferencesAndContinuesTurnNumbers(t *testing.T) {
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{
				Chunks: toolChunks(protocol.ToolCall{
					ID:        "c1",
					Name:      "echo",
					Arguments: `{"text":"one"}`,
				}),
			},
			{Chunks: textChunks("first")},
			{Chunks: textChunks("second")},
		},
	}
	scripts := append([]scriptedScript{}, adapter.Scripts...)
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	first := h.run(config)
	if err := h.loop.Drive(context.Background(), first, func(contracts.DriverEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}

	second := h.run(config)
	second.InvocationID = "different-invocation"
	if err := h.loop.Drive(context.Background(), second, func(contracts.DriverEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}

	facts := sessionEvents(t, h.current)
	turns := []protocol.TurnID{}
	starts := []protocol.StepData{}
	streams := 0

	for _, e := range facts {
		if e.Type == protocol.TurnStarted {
			turns = append(turns, protocol.EventPayload[protocol.TurnData](e).Turn)
		}

		if e.Type == protocol.StepStarted {
			starts = append(starts, protocol.EventPayload[protocol.StepData](e))
		}

		if e.Type == protocol.AssistantMessage {
			d := protocol.EventPayload[protocol.AssistantMessageData](e)
			expanded, err := protocol.ExpandAssistantStream(d.Stream)
			if err != nil {
				t.Fatal(err)
			}

			if len(expanded) != len(scripts[streams].Chunks) {
				t.Fatal("lost raw chunk")
			}

			for i, timed := range expanded {
				if timed.Time < 0 || !reflect.DeepEqual(timed.Chunk, scripts[streams].Chunks[i]) {
					t.Fatalf("raw stream changed: %+v", timed)
				}
			}

			streams++
		}

		if e.Type == protocol.ToolResulted {
			if len(e.SourceEventSeqs) != 1 {
				t.Fatalf("tool result missing source call: %+v", e)
			}

			call := facts[e.SourceEventSeqs[0]]
			result := protocol.EventPayload[protocol.ToolResultData](e)
			if call.Type != protocol.ToolCalled ||
				protocol.EventPayload[protocol.ToolCallData](call).CallID != result.Message.ToolCallID {
				t.Fatal("tool result references wrong call")
			}
		}

		encoded, err := protocol.EncodeSessionEvent(e)
		if err != nil {
			t.Fatal(err)
		}

		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(encoded, &envelope)

		for _, key := range []string{"apiVersion", "sessionId", "invocationId", "turnId", "stepId", "sequence", "kind", "message"} {
			if envelope[key] != nil {
				t.Fatalf("legacy envelope field %s", key)
			}
		}
	}

	if streams != 3 ||
		!reflect.DeepEqual(turns, []protocol.TurnID{1, 2}) ||
		!reflect.DeepEqual(starts, []protocol.StepData{{Turn: 1, Step: 1}, {Turn: 1, Step: 2}, {Turn: 2, Step: 1}}) {
		t.Fatalf("turn/step/stream: %v %v %d", turns, starts, streams)
	}
}
