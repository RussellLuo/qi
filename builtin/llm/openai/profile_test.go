package openai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
	builtin "github.com/RussellLuo/qi/builtin/bundle"
	llmopenai "github.com/RussellLuo/qi/builtin/llm/openai/plugin"
	"github.com/RussellLuo/qi/kernel/boot"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	launchapi "github.com/RussellLuo/qi/kernel/boot/launch"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// The injected transport exercises request encoding and SSE decoding without a
// listener or a second TCP variant of the same application behavior.
type profileTransport struct{ http.Handler }

func (p profileTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	response := httptest.NewRecorder()
	p.ServeHTTP(response, request)
	return response.Result(), nil
}

type httpStore struct {
	*session.Runtime
	session  session.Session
	released int
}

func (s *httpStore) Create(ctx context.Context, request session.CreateRequest) (session.Session, error) {
	current, err := s.Runtime.Create(ctx, request)
	if err == nil {
		s.session = current
	}
	return current, err
}
func (s *httpStore) Release(ctx context.Context, current session.Session) error {
	s.released++
	return s.Runtime.Release(ctx, current)
}

type httpStorePlugin struct{ store *httpStore }

func (p *httpStorePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.http-store", Provides: []gordis.ServiceSpec{session.StoreKey.Spec()}, New: func() gordis.Plugin { return &httpStorePlugin{p.store} }}
}
func (p *httpStorePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	return scope.Provide[session.Store](session.StoreKey, p.store)
}

type httpToolPlugin struct{ executed *atomic.Int32 }

func (p *httpToolPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.http-tool", Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return &httpToolPlugin{p.executed} }}
}
func (p *httpToolPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).Register(tools.Definition{
		Name: "echo", Description: "Return the supplied text.",
		Parameters: tools.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		Execute: tools.JSONTool(func(_ context.Context, execution tools.Execution) (json.RawMessage, error) {
			if string(execution.Arguments) != `{"text":"hello"}` {
				return nil, fmt.Errorf("unexpected arguments: %s", execution.Arguments)
			}
			p.executed.Add(1)
			return json.RawMessage(`{"text":"tool-result"}`), nil
		}),
	})
	return err
}

func runHTTPConversation(t *testing.T, endpoint string, client *http.Client, questions ...string) (int, string, string, *httpStore, int32) {
	t.Helper()
	store := &httpStore{Runtime: session.NewRuntime(session.RuntimeOptions{})}
	var executed atomic.Int32
	extension := builtin.Extension()
	for _, registration := range extension.Registrations {
		if provider, ok := registration.Plugin.(*llmopenai.Plugin); ok {
			provider.HTTPClient = client
		}
	}
	app, err := applicationapi.Compose(extension, applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{
		{Plugin: &httpStorePlugin{store}, ProfileVisible: true},
		{Plugin: &httpToolPlugin{&executed}, ProfileVisible: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(t.TempDir(), "profile.yaml")
	data := fmt.Sprintf(`apiVersion: qi.profile/v1alpha1
bundles: [base]
overrides:
  - id: session
    plugin: test.http-store
instances:
  - id: provider
    plugin: qi/llm/openai-compatible
    config:
      endpoint: %q
      api_key_env: QI_TEST_PROVIDER_KEY
      reasoning_history: reasoning_content
      catalog: [{id: local, model: local-model}]
      defaults: {maxTokens: 8}
  - id: tool
    plugin: test.http-tool
  - id: conversation
    plugin: qi/cli/headless
    config: {model: local}
`, endpoint)
	if err := os.WriteFile(profile, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QI_TEST_PROVIDER_KEY", "test-only")
	var out, stderr bytes.Buffer
	args := append([]string{"--profile", profile, "--"}, questions...)
	code := boot.Run(args, launchapi.Input{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &stderr}, app)
	return code, out.String(), stderr.String(), store, executed.Load()
}

func TestOpenAIHTTPConversationAndToolContinuation(t *testing.T) {

	var requests []map[string]any

	endpoint := "https://provider.invalid/v1/chat/completions"
	client := &http.Client{Transport: profileTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-only" || r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Error("lost HTTP request metadata")
		}

		index := len(requests)
		requests = append(requests, payload)

		w.Header().Set("Content-Type", "text/event-stream")
		var events []string
		switch index {
		case 0:
			events = []string{
				`{"choices":[{"index":0,"delta":{"reasoning_content":"use echo","tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":"{\"text\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hello\"}"}}]},"finish_reason":"tool_calls"}]}`,
			}
		case 1:
			events = []string{`{"choices":[{"index":0,"delta":{"content":"tool answer"},"finish_reason":"stop"}]}`}
		default:
			t.Error("unexpected request attempt")
			http.Error(w, "unexpected attempt", 500)
			return
		}
		for _, event := range append(events, `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`, "[DONE]") {
			fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
		}
	})}}
	code, out, stderr, store, executed := runHTTPConversation(t, endpoint, client, "hello", "use tool")
	if code != 0 || out != "tool answer\n" || !strings.Contains(stderr, "use echo") || executed != 1 || store.released != 1 {
		t.Fatal(code, out, stderr, executed, store.released)
	}

	if len(requests) != 2 {
		t.Fatal(requests)
	}
	for _, request := range requests {
		if request["model"] != "local-model" || request["stream"] != true || request["max_completion_tokens"] != float64(8) || request["max_tokens"] != nil {
			t.Fatal(request)
		}
		if len(request["tools"].([]any)) != 1 {
			t.Fatal("tool schema missing")
		}
	}
	messages := requests[1]["messages"].([]any)
	assistant := messages[len(messages)-2].(map[string]any)
	tool := messages[len(messages)-1].(map[string]any)
	if assistant["reasoning_content"] != "use echo" || assistant["content"] != nil || tool["role"] != "tool" || tool["tool_call_id"] != "call-1" || !strings.Contains(tool["content"].(string), "tool-result") {
		t.Fatal(messages)
	}
	ends, toolResults, assistants := 0, 0, 0
	for event, err := range store.session.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case protocol.TurnEnded:
			if protocol.EventPayload[protocol.TurnEndData](event).Reason.Kind != "completed" {
				t.Fatal(event)
			}
			ends++
		case protocol.ToolResulted:
			toolResults++
		case protocol.AssistantMessage:
			assistants++
		}
	}
	if ends != 1 || toolResults != 1 || assistants != 2 {
		t.Fatal(ends, toolResults, assistants)
	}
}
