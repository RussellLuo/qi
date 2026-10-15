package llm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Deliberately has no ResolveModel method, including through embedding.
type streamOnlyAdapter struct {
	seen []Request
	err  error
}

var _ Adapter = (*streamOnlyAdapter)(nil)

func (a *streamOnlyAdapter) Stream(_ context.Context, request Request) contracts.ChunkStream {
	a.seen = append(a.seen, request.Clone())
	if a.err != nil {
		return sequence(nil, a.err)
	}
	return sequence(textChunks("stream-only answer"), nil)
}

type resolvingAdapter struct {
	streamOnlyAdapter
	resolve func(context.Context, ModelRef) (ModelInfo, error)
}

var _ ModelResolver = (*resolvingAdapter)(nil)

func (a *resolvingAdapter) ResolveModel(ctx context.Context, ref ModelRef) (ModelInfo, error) {
	return a.resolve(ctx, ref)
}

func registerResolutionAdapter(t *testing.T, registration AdapterRegistration) (*Runtime, *testOwner) {
	t.Helper()
	runtime := NewRuntime()
	t.Cleanup(runtime.Close)
	owned := newTestOwner("model-resolution", 1)
	if _, err := runtime.RegisterAdapter(owned, registration); err != nil {
		t.Fatal(err)
	}
	return runtime, owned
}

func TestStreamOnlyAdapterUsesConservativeModelInfo(t *testing.T) {
	a := &streamOnlyAdapter{}
	runtime, _ := registerResolutionAdapter(t, AdapterRegistration{
		Route: "text", Provider: "local", Models: []string{"text"}, Adapter: a,
	})
	config := protocol.CallConfig{Provider: "local", Model: "text"}
	call, err := runtime.PrepareCall(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	if want := (ModelInfo{Ref: ModelRef{Provider: "local", Model: "text"}}); !reflect.DeepEqual(call.Model(), want) {
		t.Fatalf("fallback metadata = %+v, want only %+v", call.Model(), want)
	}
	if !reflect.DeepEqual(call.Config(), config) || call.AdapterDefaults() != nil {
		t.Fatalf("invented defaults: %+v / %+v", call.Config(), call.AdapterDefaults())
	}
	if got := call.Context(); got.ContextWindow != nil || got.SystemPromptUpdate != nil || got.Provider != "local" || got.Model != "text" {
		t.Fatalf("request context = %+v", got)
	}
	result, err := Accumulate("answer", call.Stream(context.Background(), Request{Config: call.Config()}), modelSource())
	if err != nil || result.Finish.Kind != protocol.FinishStop || result.Message.Content[0].(protocol.TextBlock).Text != "stream-only answer" {
		t.Fatalf("result/error = %+v / %v", result, err)
	}
	if len(a.seen) != 1 || !reflect.DeepEqual(a.seen[0].Config, config) {
		t.Fatalf("requests = %+v", a.seen)
	}
}

func TestStreamOnlyAdapterKeepsRegistrationDefaultsAndCallerOverrides(t *testing.T) {
	runtime, _ := registerResolutionAdapter(t, AdapterRegistration{
		Route: "text", Provider: "local", Adapter: &streamOnlyAdapter{},
		Defaults: json.RawMessage(`{"maxTokens":64,"temperature":0.2,"reasoningEffort":"low","stop":["end"]}`),
	})
	temperature := 0.8
	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{
		Provider: "local", Model: "text", Temperature: &temperature,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	config := call.Config()
	if config.MaxTokens == nil || *config.MaxTokens != 64 || config.Temperature == nil || *config.Temperature != temperature ||
		config.ReasoningEffort == nil || *config.ReasoningEffort != "low" || !reflect.DeepEqual(config.Stop, []string{"end"}) {
		t.Fatalf("resolved config = %+v", config)
	}
	if want := (&protocol.AdapterDefaults{MaxTokens: true, ReasoningEffort: true}); !reflect.DeepEqual(call.AdapterDefaults(), want) {
		t.Fatalf("default marks = %+v", call.AdapterDefaults())
	}
	if !reflect.DeepEqual(call.Model().Defaults, protocol.CallConfig{}) {
		t.Fatalf("registration defaults became model metadata: %+v", call.Model())
	}
}

func TestStreamOnlyAdapterRoutingAndUpstreamRejection(t *testing.T) {
	a := &streamOnlyAdapter{}
	runtime, _ := registerResolutionAdapter(t, AdapterRegistration{
		Route: "exact", Provider: "local", Models: []string{"allowed"}, Adapter: a,
	})
	for _, config := range []protocol.CallConfig{
		{Provider: "local", Model: "unlisted"},
		{Provider: "other", Model: "allowed"},
	} {
		if call, err := runtime.PrepareCall(context.Background(), config); !errors.Is(err, ErrRouteNotFound) {
			if call != nil {
				call.Close()
			}
			t.Fatalf("Models/provider restriction bypassed: %v", err)
		}
	}
	upstream := &streamOnlyAdapter{err: errors.New("model does not exist upstream")}
	if _, err := runtime.RegisterAdapter(newTestOwner("wildcard", 1), AdapterRegistration{
		Route: "wildcard", Provider: "local", Adapter: upstream,
	}); err != nil {
		t.Fatal(err)
	}
	// Exact routes still win over a provider-wide stream-only adapter.
	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if call.adapter != a {
		t.Fatal("provider-wide binding displaced exact Models binding")
	}
	call.Close()
	// Fallback resolution does not assert model existence. Stream owns rejection.
	call, err = runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	var chunks []protocol.StreamChunk
	for chunk, err := range call.Stream(context.Background(), Request{Config: call.Config()}) {
		if err != nil {
			t.Fatalf("upstream error escaped normalized finish: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) != 1 || chunks[0].Type != protocol.ChunkFinish || chunks[0].Reason.Kind != protocol.FinishError ||
		chunks[0].Reason.Failure == nil || chunks[0].Reason.Failure.Message != upstream.err.Error() {
		t.Fatalf("upstream rejection = %+v", chunks)
	}
	if _, err := runtime.RegisterAdapter(newTestOwner("duplicate", 1), AdapterRegistration{
		Route: "duplicate", Provider: "local", Models: []string{"allowed"}, Adapter: &streamOnlyAdapter{},
	}); err != nil {
		t.Fatal(err)
	}
	if call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "allowed"}); err == nil {
		call.Close()
		t.Fatal("ambiguous exact routes were accepted")
	}
}

func TestOptionalModelResolverPreservesMetadataAndDefaults(t *testing.T) {
	maxTokens, temperature := int64(256), 0.7
	want := ModelInfo{
		Ref: ModelRef{Provider: "local", Model: "text"}, ContextWindow: 4096,
		Capabilities: []string{"text", "tools"}, SystemPromptUpdate: "in-history", ToolUpdate: true,
		Defaults: protocol.CallConfig{MaxTokens: &maxTokens},
	}
	resolutions := 0
	a := &resolvingAdapter{resolve: func(_ context.Context, ref ModelRef) (ModelInfo, error) {
		resolutions++
		if ref != want.Ref {
			t.Fatalf("resolver target = %+v", ref)
		}
		return want, nil
	}}
	runtime, _ := registerResolutionAdapter(t, AdapterRegistration{
		Route: "text", Provider: "local", Adapter: a, Defaults: json.RawMessage(`{"maxTokens":64,"temperature":0.2}`),
	})
	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "text", Temperature: &temperature})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	if resolutions != 1 || !reflect.DeepEqual(call.Model(), want) {
		t.Fatalf("resolved metadata/count = %+v / %d", call.Model(), resolutions)
	}
	if got := call.Config(); got.MaxTokens == nil || *got.MaxTokens != maxTokens || got.Temperature == nil || *got.Temperature != temperature {
		t.Fatalf("model/registration/caller defaults = %+v", got)
	}
	if got := call.Context(); got.ContextWindow == nil || *got.ContextWindow != 4096 || got.SystemPromptUpdate == nil ||
		*got.SystemPromptUpdate != "in-history" {
		t.Fatalf("resolved request context = %+v", got)
	}
	want.Capabilities[0] = "mutated"
	maxTokens = 999
	if got := call.Model(); got.Capabilities[0] != "text" || *got.Defaults.MaxTokens != 256 || *call.Config().MaxTokens != 256 {
		t.Fatalf("resolver metadata was not frozen: %+v", got)
	}
}

func TestModelResolutionErrorsAndCancellationReleaseLease(t *testing.T) {
	sentinel := errors.New("resolver refused target")
	for _, name := range []string{"resolver error", "resolver cancellation", "cancelled stream-only", "cancelled resolver"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := error(context.Canceled)
			resolutions := 0
			a := &resolvingAdapter{resolve: func(ctx context.Context, ref ModelRef) (ModelInfo, error) {
				resolutions++
				if name == "resolver error" {
					return ModelInfo{}, sentinel
				}
				cancel()
				return ModelInfo{Ref: ref}, nil
			}}
			var adapter Adapter = a
			switch name {
			case "resolver error":
				expected = sentinel
			case "cancelled stream-only":
				adapter = &streamOnlyAdapter{}
				cancel()
			case "cancelled resolver":
				cancel()
			}
			runtime, owned := registerResolutionAdapter(t, AdapterRegistration{Route: "text", Provider: "local", Adapter: adapter})
			call, err := runtime.PrepareCall(ctx, protocol.CallConfig{Provider: "local", Model: "text"})
			if call != nil {
				call.Close()
				t.Fatal("failed resolution returned a prepared call")
			}
			if !errors.Is(err, expected) {
				t.Fatalf("error = %v, want %v", err, expected)
			}
			if name == "resolver error" && !strings.Contains(err.Error(), "resolve model local/text") {
				t.Fatalf("resolver error lost target context: %v", err)
			}
			if strings.HasPrefix(name, "cancelled") && resolutions != 0 {
				t.Fatal("resolver called despite pre-cancelled context")
			}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
			defer stopCancel()
			if err := owned.Stop(stopCtx); err != nil {
				t.Fatalf("failed resolution leaked a lease: %v", err)
			}
		})
	}
}

func TestOptionalModelResolverRejectsInconsistentMetadata(t *testing.T) {
	for _, name := range []string{"provider", "model", "context window", "system prompt update", "defaults"} {
		t.Run(name, func(t *testing.T) {
			a := &resolvingAdapter{resolve: func(_ context.Context, ref ModelRef) (ModelInfo, error) {
				info := ModelInfo{Ref: ref}
				switch name {
				case "provider":
					info.Ref.Provider = "other"
				case "model":
					info.Ref.Model = "other"
				case "context window":
					info.ContextWindow = -1
				case "system prompt update":
					info.SystemPromptUpdate = "unsupported"
				case "defaults":
					n := int64(-1)
					info.Defaults.MaxTokens = &n
				}
				return info, nil
			}}
			runtime, _ := registerResolutionAdapter(t, AdapterRegistration{Route: "text", Provider: "local", Adapter: a})
			if call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "local", Model: "text"}); err == nil {
				call.Close()
				t.Fatal("invalid resolver metadata was accepted")
			}
			if len(a.seen) != 0 {
				t.Fatal("invalid metadata reached Stream")
			}
		})
	}
}
