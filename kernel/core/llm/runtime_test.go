package llm

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestPreparedLLMCallKeepsAdapterAndConfigGeneration(t *testing.T) {
	runtime := NewRuntime()
	firstAdapter := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: textChunks("old")}}}
	firstOwner := newTestOwner("adapter", 1)
	disposeFirst, err := runtime.RegisterAdapter(firstOwner, AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Defaults: json.RawMessage(`{"temperature":0,"maxTokens":1024}`),
		Adapter:  firstAdapter,
	})
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{
		Provider: "fake",
		Model:    "model",
		Temperature: func() *float64 {
			v := 1.0
			return &v
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}

	disposeFirst()
	secondAdapter := &scriptedAdapter{Version: "v2", Scripts: []scriptedScript{{Chunks: textChunks("new")}}}
	if _, err := runtime.RegisterAdapter(newTestOwner("adapter", 2), AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Defaults: json.RawMessage(`{"temperature":0}`),
		Adapter:  secondAdapter,
	}); err != nil {
		t.Fatal(err)
	}

	config := prepared.Config()
	if config.Temperature == nil || *config.Temperature != 1 || config.MaxTokens == nil || *config.MaxTokens != 1024 {
		t.Fatalf("resolved config = %+v", config)
	}

	stopped := make(chan error, 1)
	go func() {
		stopped <- firstOwner.Stop(context.Background())
	}()

	select {
	case err := <-stopped:
		t.Fatalf("adapter owner stopped before prepared call release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	accumulated, err := Accumulate("message-1", prepared.Stream(context.Background(), Request{Config: config}), protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: "test",
		Model:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := accumulated.Message.Content[0].(protocol.TextBlock).Text; got != "old" {
		t.Fatalf("prepared call used replacement adapter: %q", got)
	}

	if len(firstAdapter.Seen) != 1 || len(secondAdapter.Seen) != 0 {
		t.Fatalf("adapter dispatches = %d/%d", len(firstAdapter.Seen), len(secondAdapter.Seen))
	}

	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestPreparedLLMCallIsSingleDispatchAndValidatesFrozenConfig(t *testing.T) {
	runtime := NewRuntime()
	adapter := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: textChunks("ok")}}}
	if _, err := runtime.RegisterAdapter(newTestOwner("adapter", 1), AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}

	bad := call.Config()
	bad.Model = "other"
	if _, err := Accumulate("message-1", call.Stream(context.Background(), Request{Config: bad}), protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: "test",
		Model:    "test",
	}); err == nil {
		t.Fatal("mismatched config was accepted")
	}

	if _, err := Accumulate("message-2", call.Stream(context.Background(), Request{Config: call.Config()}), protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: "test",
		Model:    "test",
	}); !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("second dispatch error = %v", err)
	}
}

func TestPreparedLLMCallCloseDoesNotRequireIteratorConsumption(t *testing.T) {
	runtime := NewRuntime()
	adapter := &scriptedAdapter{Version: "v1", Scripts: []scriptedScript{{Chunks: textChunks("unused")}}}
	if _, err := runtime.RegisterAdapter(newTestOwner("adapter", 1), AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}

	stream := call.Stream(context.Background(), Request{Config: call.Config()})
	done := make(chan struct{})
	go func() {
		call.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close waited for an iterator that never started")
	}

	if _, err := Accumulate("message-1", stream, protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: "test",
		Model:    "test",
	}); !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("closed iterator error = %v", err)
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

func TestRuntimeNormalizesTerminalFailuresAndStopsAtFinish(t *testing.T) {
	for _, test := range []struct {
		name       string
		script     scriptedScript
		cancel     bool
		kind       protocol.FinishKind
		code       string
		wantChunks int
	}{
		{
			name: "adapter error",
			script: scriptedScript{
				Chunks: []protocol.StreamChunk{{
					Type:  protocol.ChunkTextDelta,
					Index: 0,
					Text:  "prefix",
				}},
				Err: errors.New("provider failed"),
			},
			kind:       protocol.FinishError,
			code:       "UNKNOWN",
			wantChunks: 2,
		},
		{
			name:       "cancel",
			script:     scriptedScript{WaitForCancel: true},
			cancel:     true,
			kind:       protocol.FinishAborted,
			code:       "ABORTED",
			wantChunks: 1,
		},
		{
			name: "invalid replay",
			script: scriptedScript{
				Chunks: []protocol.StreamChunk{
					{
						Type:        protocol.ChunkFinish,
						Reason:      protocol.FinishReason{Kind: protocol.FinishStop},
						ReplayState: &protocol.ReplayEnvelope{Response: func() {}},
					},
				},
			},
			kind:       protocol.FinishError,
			code:       "UNKNOWN",
			wantChunks: 1,
		},
		{
			name: "stop at finish",
			script: scriptedScript{
				Chunks: append(textChunks("answer"), protocol.StreamChunk{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 999}}),
				Err:    errors.New("late error"),
			},
			kind:       protocol.FinishStop,
			wantChunks: 4,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime()
			defer runtime.Close()

			if _, err := runtime.RegisterAdapter(newTestOwner("adapter", 1), AdapterRegistration{
				Route:    "chat",
				Provider: "fake",
				Adapter:  &scriptedAdapter{Scripts: []scriptedScript{test.script}},
			}); err != nil {
				t.Fatal(err)
			}

			call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if test.cancel {
				cancel()
			}

			var chunks []protocol.StreamChunk

			for c, err := range call.Stream(ctx, Request{Config: call.Config()}) {
				if err != nil {
					t.Fatalf("adapter failure escaped normalized finish: %v", err)
				}

				chunks = append(chunks, c)
			}

			if len(chunks) != test.wantChunks {
				t.Fatalf("chunks=%+v", chunks)
			}

			last := chunks[len(chunks)-1]
			if last.Type != protocol.ChunkFinish || last.Reason.Kind != test.kind {
				t.Fatalf("terminal=%+v", last)
			}

			if test.code != "" && (last.Reason.Failure == nil || last.Reason.Failure.Code != test.code) {
				t.Fatalf("failure=%+v", last.Reason.Failure)
			}

			for _, c := range chunks[:len(chunks)-1] {
				if c.Type == protocol.ChunkFinish {
					t.Fatal("multiple terminal finishes")
				}
			}
		})
	}
}

func TestRuntimeEOFDoesNotFabricateFinish(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{
				Chunks: []protocol.StreamChunk{{
					Type:  protocol.ChunkTextDelta,
					Index: 3,
					Text:  "answer",
				}},
			},
		},
	}
	if _, err := runtime.RegisterAdapter(newTestOwner("adapter", 1), AdapterRegistration{
		Route:    "chat",
		Provider: "fake",
		Adapter:  adapter,
	}); err != nil {
		t.Fatal(err)
	}

	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}

	a, _ := NewAccumulator("message", modelSource())
	count := 0

	for chunk, err := range call.Stream(context.Background(), Request{Config: call.Config()}) {
		if err != nil {
			t.Fatal(err)
		}

		count++
		if chunk.Type == protocol.ChunkFinish {
			t.Fatal("fabricated terminal chunk")
		}

		if err := a.Accept(chunk); err != nil {
			t.Fatal(err)
		}
	}

	result, err := a.Finish()
	if err != nil || count != 1 || result.Finish.Kind != protocol.FinishStop {
		t.Fatalf("result/count/error = %+v/%d/%v", result, count, err)
	}
}

func TestExactModelsAndAmbiguousProviderBindings(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()

	owned := newTestOwner("models", 1)
	wildcard := &scriptedAdapter{}
	exact := &scriptedAdapter{}

	for _, r := range []AdapterRegistration{
		{
			Route:    "wildcard",
			Provider: "fake",
			Adapter:  wildcard,
		},
		{
			Route:    "exact",
			Provider: "fake",
			Models:   []string{"specific"},
			Adapter:  exact,
		},
	} {
		if _, err := runtime.RegisterAdapter(owned, r); err != nil {
			t.Fatal(err)
		}
	}

	call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "specific"})
	if err != nil {
		t.Fatal(err)
	}

	if call.adapter != exact {
		t.Fatal("exact binding not selected")
	}

	call.Close()
	if _, err := runtime.RegisterAdapter(owned, AdapterRegistration{
		Route:    "duplicate",
		Provider: "fake",
		Models:   []string{"specific"},
		Adapter:  &scriptedAdapter{},
	}); err != nil {
		t.Fatal(err)
	}

	if call, err := runtime.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "specific"}); err == nil {
		call.Close()
		t.Fatal("ambiguous binding silently selected")
	}

	if _, err := runtime.RegisterAdapter(owned, AdapterRegistration{
		Route:    "private",
		Provider: "private",
		Defaults: json.RawMessage(`{"top_p":1}`),
		Adapter:  &scriptedAdapter{},
	}); err == nil {
		t.Fatal("private defaults entered core config")
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
	Seen    []Request
}

func (a *scriptedAdapter) ResolveModel(_ context.Context, ref ModelRef) (ModelInfo, error) {
	if ref.Provider == "" || ref.Model == "" {
		return ModelInfo{}, errors.New("provider and model are required")
	}

	return ModelInfo{
		Ref:           ref,
		ContextWindow: 4096,
		Capabilities:  []string{"tools", a.Version},
	}, nil
}

func (a *scriptedAdapter) Stream(ctx context.Context, request Request) iter.Seq2[protocol.StreamChunk, error] {
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

type retryProviderError struct{ failure protocol.LlmFailure }

func (e retryProviderError) Error() string { return e.failure.Message }

func (e retryProviderError) LlmFailure() protocol.LlmFailure { return e.failure }

func TestRuntimeRetainsCompleteProviderFailureIncludingAbort(t *testing.T) {
	for _, code := range []string{"RATE_LIMIT", "ABORTED"} {
		t.Run(code, func(t *testing.T) {
			status, delay, id := 429, 250.0, "provider-request"
			failure := protocol.LlmFailure{
				Message:              "provider failure",
				Code:                 code,
				Status:               &status,
				ProviderRetryAfterMs: &delay,
				RequestID:            &id,
			}
			r := NewRuntime()
			defer r.Close()

			adapter := &scriptedAdapter{Scripts: []scriptedScript{{Err: retryProviderError{failure}}}}
			if _, err := r.RegisterAdapter(newTestOwner("adapter", 1), AdapterRegistration{
				Route:    "chat",
				Provider: "fake",
				Adapter:  adapter,
			}); err != nil {
				t.Fatal(err)
			}

			call, err := r.PrepareCall(context.Background(), protocol.CallConfig{Provider: "fake", Model: "model"})
			if err != nil {
				t.Fatal(err)
			}

			defer call.Close()

			count := 0

			for chunk, err := range call.Stream(context.Background(), Request{Config: call.Config()}) {
				if err != nil {
					t.Fatal(err)
				}

				count++
				kind := protocol.FinishError
				if code == "ABORTED" {
					kind = protocol.FinishAborted
				}

				if chunk.Type != protocol.ChunkFinish ||
					chunk.Reason.Kind != kind ||
					chunk.Reason.Failure == nil ||
					!reflect.DeepEqual(*chunk.Reason.Failure, failure) {
					t.Fatal("provider failure facts lost", chunk)
				}
			}

			if count != 1 {
				t.Fatal(count)
			}
		})
	}
}
