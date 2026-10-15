package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	toolsapi "github.com/RussellLuo/qi/kernel/core/tools"
)

var _ llm.Adapter = (*Adapter)(nil)

var _ llm.ModelResolver = (*Adapter)(nil)

func TestRuntimeUsesOptionalModelResolver(t *testing.T) {
	adapter := mustAdapter(t, Config{
		Provider: "openai", Endpoint: "https://provider.invalid/v1/chat/completions", ContextWindow: 8192,
	})
	runtime := llm.NewRuntime()
	defer runtime.Close()
	if _, err := runtime.RegisterAdapter(newTestOwner("openai-resolver", 1), llm.AdapterRegistration{
		Route: "openai", Provider: "openai", Adapter: adapter,
	}); err != nil {
		t.Fatal(err)
	}
	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "openai", Model: "model-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	if want := (llm.ModelInfo{
		Ref: llm.ModelRef{Provider: "openai", Model: "model-1"}, ContextWindow: 8192, Capabilities: []string{"stream", "tools"},
	}); !reflect.DeepEqual(call.Model(), want) {
		t.Fatalf("OpenAI resolver metadata = %+v, want %+v", call.Model(), want)
	}
}

func TestAdapterConformanceMapsFragmentedToolCallUsageAndFinish(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "model-1" || payload["stream"] != true {
			t.Fatalf("request payload = %s", body)
		}
		messages := payload["messages"].([]any)
		if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["role"] != "user" {
			t.Fatalf("request messages = %#v", messages)
		}
		if tools, ok := payload["tools"].([]any); !ok || len(tools) != 1 {
			t.Fatalf("request tools = %#v", payload["tools"])
		}
		return sseResponse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":"{\"te"}}]},"finish_reason":""}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"xt\":\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
			`[DONE]`,
		), nil
	})
	adapter := mustAdapter(t, Config{
		Provider: "openai", Endpoint: "https://provider.invalid/v1/chat/completions", APIKey: "secret",
		HTTPClient: &http.Client{Transport: transport}, ContextWindow: 8192,
	})
	request := llm.Request{
		Config: protocol.CallConfig{Provider: "openai", Model: "model-1", Temperature: func() *float64 { v := 0.0; return &v }()},
		Messages: []protocol.Message{
			protocol.NewTextMessage("system-1", protocol.RoleSystem, protocol.SourceSystem, "Be concise."),
			protocol.NewTextMessage("message-1", protocol.RoleUser, protocol.SourceUser, "hello"),
		},
		Tools: []toolsapi.Schema{{
			Name: "echo", Description: "Echo text.",
			Parameters: toolsapi.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		}},
	}
	checkAdapter(t, adapterCase{
		Adapter: adapter, Ref: llm.ModelRef{Provider: "openai", Model: "model-1"}, Request: request,
		Assert: func(t *testing.T, accumulated llm.Accumulated) {
			if accumulated.Finish.Kind != protocol.FinishToolCalls || !reflect.DeepEqual(accumulated.Usage, &protocol.Usage{InputTokens: 7, OutputTokens: 3}) {
				t.Fatalf("accumulated finish/usage = %+v/%+v", accumulated.Finish, accumulated.Usage)
			}
			if len(accumulated.Message.Content) != 1 {
				t.Fatalf("content = %#v", accumulated.Message.Content)
			}
			call := accumulated.Message.Content[0].(protocol.ToolCallBlock).ToolCall
			if call.ID != "call-1" || call.Name != "echo" || string(call.Arguments) != `{"text":"hello"}` {
				t.Fatalf("Tool Call = %+v", call)
			}
		},
	})
}

func TestAdapterStreamsTextAndReasoningBlocks(t *testing.T) {
	adapter := mustAdapter(t, Config{
		Endpoint: "https://provider.invalid/v1/chat/completions",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return sseResponse(
				`{"choices":[{"index":0,"delta":{"reasoning_content":"think"},"finish_reason":""}]}`,
				`{"choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			), nil
		})},
	})
	request := minimalRequest()
	accumulated, err := llm.Accumulate("message", adapter.Stream(context.Background(), request), protocol.MessageSource{Kind: protocol.SourceModel, Provider: request.Config.Provider, Model: request.Config.Model})
	if err != nil {
		t.Fatal(err)
	}
	if accumulated.Finish.Kind != protocol.FinishStop || len(accumulated.Message.Content) != 2 {
		t.Fatalf("accumulated = %+v", accumulated)
	}
	if got := accumulated.Message.Content[0].(protocol.ReasoningBlock).Text; got != "think" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := accumulated.Message.Content[1].(protocol.TextBlock).Text; got != "answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestAdapterConvertsProviderErrorsWithoutCredential(t *testing.T) {
	adapter := mustAdapter(t, Config{
		Endpoint: "https://provider.invalid/v1/chat/completions", APIKey: "never-print-this",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":{"message":"key never-print-this rejected","type":"rate_limit","code":"rate_limit_exceeded"}}`)),
			}, nil
		})},
	})
	err := streamError(adapter.Stream(context.Background(), minimalRequest()))
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusTooManyRequests || providerErr.Code != "rate_limit_exceeded" {
		t.Fatalf("provider error = %#v (%v)", providerErr, err)
	}
	if strings.Contains(err.Error(), "never-print-this") {
		t.Fatal("provider error exposed the API key")
	}
}

func TestAdapterPropagatesCancellationAndRejectsIncompleteSSE(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		adapter := mustAdapter(t, Config{
			Endpoint: "https://provider.invalid/v1/chat/completions",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			})},
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := streamError(adapter.Stream(ctx, minimalRequest())); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
	})

	t.Run("missing_done", func(t *testing.T) {
		adapter := mustAdapter(t, Config{
			Endpoint: "https://provider.invalid/v1/chat/completions",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return sseResponse(`{"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"stop"}]}`), nil
			})},
		})
		if err := streamError(adapter.Stream(context.Background(), minimalRequest())); err == nil || !strings.Contains(err.Error(), "without [DONE]") {
			t.Fatalf("incomplete SSE error = %v", err)
		}
	})
}

func TestRealAPISmoke(t *testing.T) {
	endpoint := os.Getenv("QI_OPENAI_ENDPOINT")
	model := os.Getenv("QI_OPENAI_MODEL")
	apiKey := os.Getenv("QI_OPENAI_API_KEY")
	if endpoint == "" || model == "" {
		t.Skip("set QI_OPENAI_ENDPOINT and QI_OPENAI_MODEL to run the optional real API smoke test")
	}
	adapter := mustAdapter(t, Config{
		Endpoint: endpoint, APIKey: apiKey, HTTPClient: &http.Client{Timeout: 30 * time.Second},
	})
	request := minimalRequest()
	request.Config.Model = model
	accumulated, err := llm.Accumulate("real-api-message", adapter.Stream(context.Background(), request), protocol.MessageSource{Kind: protocol.SourceModel, Provider: request.Config.Provider, Model: request.Config.Model})
	if err != nil {
		t.Fatal(err)
	}
	if accumulated.Finish.Kind == "" || len(accumulated.Message.Content) == 0 {
		t.Fatalf("real API returned an empty completion: %+v", accumulated)
	}
}

func minimalRequest() llm.Request {
	return llm.Request{
		Config:   protocol.CallConfig{Provider: "openai", Model: "model-1"},
		Messages: []protocol.Message{protocol.NewTextMessage("message-1", protocol.RoleUser, protocol.SourceUser, "hello")},
	}
}

func mustAdapter(t *testing.T, config Config) *Adapter {
	t.Helper()
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func sseResponse(events ...string) *http.Response {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body.String()))}
}

func streamError(stream func(func(protocol.StreamChunk, error) bool)) error {
	for _, err := range stream {
		if err != nil {
			return err
		}
	}
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAdapterPreservesRawInvalidAndEmptyToolArguments(t *testing.T) {
	for _, arguments := range []string{"{broken", "", "  { \"text\": \"hi\" }\n", `{"text":"quoted \\\" value"}`} {
		t.Run(arguments, func(t *testing.T) {
			first := len(arguments) / 2
			event := func(fragment, id string, finish string) string {
				delta := map[string]any{"index": 0, "function": map[string]any{"arguments": fragment}}
				if id != "" {
					delta["id"] = id
					delta["function"].(map[string]any)["name"] = "echo"
				}
				encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{delta}}, "finish_reason": finish}}})
				return string(encoded)
			}
			adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/v1/chat/completions", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return sseResponse(event(arguments[:first], "c", ""), event(arguments[first:], "", "tool_calls"), `[DONE]`), nil
			})}})
			request := minimalRequest()
			accumulated, err := llm.Accumulate("raw", adapter.Stream(context.Background(), request), protocol.MessageSource{Kind: protocol.SourceModel, Provider: request.Config.Provider, Model: request.Config.Model})
			if err != nil {
				t.Fatal(err)
			}
			call := accumulated.Message.Content[0].(protocol.ToolCallBlock)
			if call.Arguments != arguments {
				t.Fatalf("provider arguments changed: %q, want %q", call.Arguments, arguments)
			}
		})
	}
}

func TestAdapterEncodesCanonicalRolesAndToolResultContent(t *testing.T) {
	source := protocol.MessageSource{Kind: protocol.SourceModel, Provider: "openai", Model: "model-1"}
	raw := " {broken\n"
	messages, err := encodeMessages(llm.Request{Messages: []protocol.Message{
		protocol.NewTextMessage("s", protocol.RoleSystem, protocol.SourceSystem, ""),
		protocol.NewTextMessage("u", protocol.RoleUser, "plugin-context", "context"),
		protocol.NewTextMessage("d", protocol.RoleDeveloper, "plugin-context", "developer text"),
		protocol.NewToolCallMessage("a", protocol.ToolCall{ID: "c", Name: "echo", Arguments: raw}, source),
		protocol.NewToolResultMessage("t", "c", []protocol.ContentBlock{protocol.TextBlock{Text: "Error: invalid arguments"}}, true),
		protocol.NewToolResultMessage("empty", "other", []protocol.ContentBlock{}, false),
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || messages[1]["role"] != "developer" {
		t.Fatalf("message roles: %+v", messages)
	}
	call := messages[2]["tool_calls"].([]map[string]any)[0]
	if call["function"].(map[string]any)["arguments"] != raw {
		t.Fatalf("raw history changed: %+v", call)
	}
	if messages[3]["role"] != "tool" || messages[3]["tool_call_id"] != protocol.ToolCallID("c") || messages[3]["content"] != "Error: invalid arguments" {
		t.Fatalf("tool wire message = %+v", messages[3])
	}
	if messages[4]["content"] != "" {
		t.Fatalf("empty tool content = %+v", messages[4])
	}
}

func TestAdapterEmitsOriginalToolFragmentsAndFullUsage(t *testing.T) {
	adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return sseResponse(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":7,"id":"c","function":{"name":"echo","arguments":" { "}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"answer","tool_calls":[{"index":7,"function":{"arguments":"bad"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":7,"cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":3}}}`,
			`[DONE]`,
		), nil
	})}})
	var chunks []protocol.StreamChunk
	for chunk, err := range adapter.Stream(context.Background(), minimalRequest()) {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	var deltas []protocol.StreamChunk
	for _, chunk := range chunks {
		if chunk.Type == protocol.ChunkToolCallDelta {
			deltas = append(deltas, chunk)
		}
	}
	if len(deltas) != 2 || deltas[0].Index != 0 || deltas[0].ID != "c" || deltas[0].Name == nil || *deltas[0].Name != "echo" || deltas[0].ArgumentsDelta != " { " || deltas[1].Name != nil || deltas[1].ArgumentsDelta != "bad" {
		t.Fatalf("deltas=%+v", deltas)
	}
	usage := chunks[len(chunks)-2]
	if usage.Type != protocol.ChunkUsage || chunks[len(chunks)-1].Type != protocol.ChunkFinish {
		t.Fatalf("terminal sequence=%+v", chunks)
	}
	u := usage.Usage
	if u.InputTokens != 5 || u.OutputTokens != 5 || *u.TotalTokens != 17 || *u.CacheReadTokens != 7 || *u.CacheWriteTokens != 2 || *u.ReasoningTokens != 3 {
		t.Fatalf("usage=%+v", u)
	}
	accumulated, err := llm.Accumulate("message", func(yield func(protocol.StreamChunk, error) bool) {
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}, protocol.MessageSource{Kind: protocol.SourceModel, Provider: "openai", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if accumulated.Message.Content[0].(protocol.ToolCallBlock).Arguments != " { bad" || accumulated.Message.Content[1].(protocol.TextBlock).Text != "answer" {
		t.Fatalf("blocks=%+v", accumulated.Message.Content)
	}
}

func TestAdapterMaxTokensDropsIncompleteToolProposal(t *testing.T) {
	adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return sseResponse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]},"finish_reason":"length"}]}`, `[DONE]`), nil
	})}})
	result, err := llm.Accumulate("message", adapter.Stream(context.Background(), minimalRequest()), protocol.MessageSource{Kind: protocol.SourceModel, Provider: "openai", Model: "model"})
	if err != nil || result.Finish.Kind != protocol.FinishMaxTokens || len(result.Message.Content) != 0 {
		t.Fatalf("result/error=%+v/%v", result, err)
	}
}

func TestEncodesCanonicalSamplingAndKeepsPrivateOptionsInAdapter(t *testing.T) {
	effort := "high"
	temperature := 0.0
	max := int64(8)
	request := llm.Request{Config: protocol.CallConfig{Provider: "openai", Model: "exact", ReasoningEffort: &effort, Temperature: &temperature, MaxTokens: &max, Stop: []string{}}, Messages: []protocol.Message{protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "hello")}}
	b, err := encodeRequest(request, Config{Parameters: json.RawMessage(`{"top_p":0.7}`)})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["temperature"] != float64(0) || payload["max_completion_tokens"] != float64(8) || payload["reasoning_effort"] != "high" || payload["top_p"] != 0.7 || len(payload["stop"].([]any)) != 0 {
		t.Fatalf("provider payload=%s", b)
	}
	core, err := json.Marshal(request.Config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(core), "top_p") || strings.Contains(string(core), "route") || strings.Contains(string(core), "parameters") {
		t.Fatalf("private options leaked into config=%s", core)
	}
	if _, err := encodeRequest(request, Config{Parameters: json.RawMessage(`{"max_tokens":99}`)}); err == nil {
		t.Fatal("private options silently overrode recorded config")
	}
}

type adapterCase struct {
	Adapter llm.Adapter
	Ref     llm.ModelRef
	Request llm.Request
	Assert  func(*testing.T, llm.Accumulated)
}

func checkAdapter(t *testing.T, testCase adapterCase) {
	t.Helper()
	ctx := context.Background()
	if resolver, ok := testCase.Adapter.(llm.ModelResolver); ok {
		model, err := resolver.ResolveModel(ctx, testCase.Ref)
		if err != nil {
			t.Fatal(err)
		}

		if model.Ref != testCase.Ref || model.ContextWindow < 0 {
			t.Fatalf("inconsistent ModelInfo: %+v", model)
		}
	}

	accumulated, err := llm.Accumulate("conformance-message", testCase.Adapter.Stream(ctx, testCase.Request), protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: testCase.Ref.Provider,
		Model:    testCase.Ref.Model,
	})
	if err != nil {
		t.Fatal(err)
	}

	if testCase.Assert != nil {
		testCase.Assert(t, accumulated)
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

func TestTokenLimitUsesSelectedWireFieldAndRejectsPrivateOverrides(t *testing.T) {
	max := int64(8)
	request := minimalRequest()
	request.Config.MaxTokens = &max
	for _, field := range []string{"", "max_tokens", "max_completion_tokens"} {
		t.Run(field, func(t *testing.T) {
			body, err := encodeRequest(request, Config{MaxTokensParameter: field})
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			want := field
			if want == "" {
				want = "max_completion_tokens"
			}
			other := "max_tokens"
			if want == other {
				other = "max_completion_tokens"
			}
			if payload[want] != float64(8) || payload[other] != nil {
				t.Fatalf("ambiguous token budget: %s", body)
			}
		})
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		_, err := New(Config{Endpoint: "https://provider.invalid/chat", Parameters: json.RawMessage(`{"` + field + `":99}`)})
		if err == nil {
			t.Fatalf("private %s overrode canonical budget", field)
		}
	}
}

func TestReasoningHistoryIsOmittedOrSentSeparately(t *testing.T) {
	message := protocol.NewAssistantTextMessage("a", "openai", "model", "answer")
	message.Content = append([]protocol.ContentBlock{protocol.ReasoningBlock{Text: "reason"}}, message.Content...)
	request := llm.Request{Messages: []protocol.Message{message}}
	for _, policy := range []string{"", "omit", "reasoning_content"} {
		t.Run(policy, func(t *testing.T) {
			messages, err := encodeMessages(request, policy)
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 || messages[0]["content"] != "answer" {
				t.Fatalf("reasoning polluted visible history: %+v", messages)
			}
			if policy == "reasoning_content" {
				if messages[0]["reasoning_content"] != "reason" {
					t.Fatal(messages)
				}
			} else if _, ok := messages[0]["reasoning_content"]; ok {
				t.Fatal("reasoning sent without opting in")
			}
		})
	}
}

func TestRefusalAndContentFilterCannotBecomeEmptySuccess(t *testing.T) {
	for _, test := range []struct{ name, event, code string }{
		{"refusal", `{"choices":[{"index":0,"delta":{"refusal":"request denied"},"finish_reason":"stop"}]}`, "REFUSAL"},
		{"filter", `{"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"content_filter"}]}`, "CONTENT_FILTER"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return sseResponse(test.event, "[DONE]"), nil
			})}})
			_, err := llm.Accumulate("message", adapter.Stream(context.Background(), minimalRequest()), protocol.MessageSource{Kind: protocol.SourceModel, Provider: "openai", Model: "model-1"})
			var providerError *ProviderError
			if !errors.As(err, &providerError) || providerError.Code != test.code || providerError.LlmFailure().Code != test.code {
				t.Fatalf("lost refusal classification: %v", err)
			}
		})
	}
}

func TestRequestTimeoutCancelsInjectedTransport(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat", HTTPClient: client, Timeout: 10 * time.Millisecond})
	if err := streamError(adapter.Stream(context.Background(), minimalRequest())); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	if client.Timeout != 0 {
		t.Fatal("modified caller-owned client")
	}
}

func TestAdapterRejectsInvalidCompatibilitySettingsBeforeHTTP(t *testing.T) {
	for _, config := range []Config{
		{MaxTokensParameter: "guess"}, {ReasoningHistory: "content"}, {Timeout: -time.Second},
		{Parameters: json.RawMessage(`{} {}`)}, {Parameters: json.RawMessage(`null`)},
	} {
		config.Endpoint = "https://provider.invalid/chat"
		if _, err := New(config); err == nil {
			t.Fatalf("accepted invalid settings: %+v", config)
		}
	}
	adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat"})
	if adapter.timeout != 2*time.Minute || !strings.Contains(adapter.maxTokensParameter, "completion") {
		t.Fatal("missing bounded modern defaults")
	}
}

func TestRequestTimeoutStopsAnIncompleteResponseBody(t *testing.T) {
	done := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer close(done)
			fmt.Fprint(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			<-request.Context().Done()
			writer.CloseWithError(request.Context().Err())
		}()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	})}
	adapter := mustAdapter(t, Config{Endpoint: "https://provider.invalid/chat", HTTPClient: client, Timeout: 200 * time.Millisecond})
	var streamErr error
	started, finished := false, false
	for chunk, err := range adapter.Stream(context.Background(), minimalRequest()) {
		if err != nil {
			streamErr = err
			break
		}
		started = started || chunk.Type == protocol.ChunkTextDelta
		finished = finished || chunk.Type == protocol.ChunkFinish
	}
	if !started || finished || !errors.Is(streamErr, context.DeadlineExceeded) {
		t.Fatal("incomplete body timeout", started, finished, streamErr)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout left response writer running")
	}
}
