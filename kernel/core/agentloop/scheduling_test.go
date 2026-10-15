package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func TestRetryRepreparesSameStepWithoutReadmittingBatch(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprintf("change-binding=%t", change), func(t *testing.T) {
			status := 429
			delay := 125.0
			failure := protocol.LlmFailure{
				Message:              "rate limited",
				Code:                 "RATE_LIMIT",
				Status:               &status,
				ProviderRetryAfterMs: &delay,
			}
			spy := &requestSpy{
				adapter: scriptedAdapter{
					Scripts: []scriptedScript{
						{
							Chunks: []protocol.StreamChunk{
								{
									Type:  protocol.ChunkTextDelta,
									Index: 0,
									Text:  "failed prefix",
								},
								{
									Type:   protocol.ChunkFinish,
									Reason: protocol.FinishReason{Kind: protocol.FinishError, Failure: &failure},
								},
							},
						},
						{Chunks: textChunks("done")},
					},
				},
			}
			resolves := 0
			spy.resolve = func(ctx context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
				resolves++
				return spy.adapter.ResolveModel(ctx, ref)
			}
			h := newQi(t, &scriptedAdapter{}, nil)
			defer h.stop(t)

			n := int64(2)
			policy := &llm.ResolvedRetryPolicy{
				Mode:           "normal",
				MaxRetries:     &n,
				RetryableCodes: []string{"RATE_LIMIT"},
				InitialDelayMs: 100,
				MaxDelayMs:     1000,
			}
			if _, err := h.loop.coordinator.LLM.RegisterAdapter(h.owner, llm.AdapterRegistration{
				Route:       "policy",
				Provider:    "policy",
				Adapter:     spy,
				RetryPolicy: policy,
			}); err != nil {
				t.Fatal(err)
			}
			// Registration and hook snapshots must detach provider-owned slices.
			policy.RetryableCodes[0] = "mutated"
			preSteps, requests, errorsOffered := 0, 0, 0
			if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
				ID: "retry",
				PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
					preSteps++
					input.StartsRequestSeries = true
					return input, nil
				},
				Request: func(_ context.Context, r agentmiddleware.Request) (agentmiddleware.Request, error) {
					requests++
					r.Config.Provider = "policy"
					if change && requests == 2 {
						r.Config.Model = "second"
					}

					return r, nil
				},
				RequestError: func(
					ctx context.Context,
					e agentmiddleware.RequestError,
					action *agentmiddleware.RequestErrorAction,
				) (*agentmiddleware.RequestErrorAction, error) {
					errorsOffered++
					if ctx.Err() != nil ||
						action != nil ||
						e.Agent != h.agent ||
						e.Turn != 1 ||
						e.Step != 1 ||
						e.AttemptID != "session-1:1" ||
						e.Provider != "policy" ||
						!reflect.DeepEqual(e.Failure, failure) ||
						e.RetryPolicy.RetryableCodes[0] != "RATE_LIMIT" {
						t.Fatalf("wrong attempt payload: %+v", e)
					}

					facts := sessionEvents(t, h.current)
					if facts[len(facts)-1].Type != protocol.AssistantAttempt {
						t.Fatal("hook preceded durable failure settlement")
					}

					*e.Failure.Status = 500
					e.RetryPolicy.RetryableCodes[0] = "changed by hook"

					return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			o := captureFrames(t, h)
			if _, err := h.drive(context.Background(), Config{MaxSteps: 1, LLM: protocol.CallConfig{Provider: "fake", Model: "first"}}); err != nil {
				t.Fatal(err)
			}

			if preSteps != 1 || requests != 2 || resolves != 2 || errorsOffered != 1 || len(spy.adapter.Seen) != 2 {
				t.Fatal(preSteps, requests, resolves, errorsOffered)
			}

			counts := map[protocol.SessionEventKind]int{}

			for _, e := range sessionEvents(t, h.current) {
				counts[e.Type]++
			}

			if counts[protocol.StepStarted] != 1 ||
				counts[protocol.UserMessage] != 1 ||
				counts[protocol.AssistantAttempt] != 1 ||
				counts[protocol.AssistantMessage] != 1 {
				t.Fatal(counts)
			}

			for _, r := range spy.adapter.Seen {
				for _, m := range r.Messages {
					if m.Role == protocol.RoleAssistant {
						t.Fatal("failed attempt leaked into request")
					}
				}
			}

			hs := headers(t, h.current)
			if !hs[0].StartsSeries {
				t.Fatal("first request lost startsSeries")
			}

			if change {
				if len(hs) != 2 || hs[1].Reason != "change" || hs[1].StartsSeries || counts[protocol.RequestContextEvent] != 2 {
					t.Fatal(hs, counts)
				}
			} else if len(hs) != 1 || counts[protocol.RequestContextEvent] != 1 {
				t.Fatal("retry repeated series/header/context", hs, counts)
			}

			assertLiveDurable(t, h.current, o.frames)
			starts := []string{}

			for _, f := range o.frames {
				if f.Type == "start" {
					starts = append(starts, f.AttemptID)
				}
			}

			if !reflect.DeepEqual(starts, []string{"session-1:1", "session-1:2"}) {
				t.Fatal(starts)
			}
		})
	}
}

func TestDefaultStopAndProviderAbortDoNotRetryOrProjectFailure(t *testing.T) {
	for _, kind := range []protocol.FinishKind{protocol.FinishError, protocol.FinishAborted} {
		t.Run(string(kind), func(t *testing.T) {
			adapter := &scriptedAdapter{
				Scripts: []scriptedScript{
					{
						Chunks: []protocol.StreamChunk{
							{
								Type:  protocol.ChunkTextDelta,
								Index: 0,
								Text:  "failed",
							},
							{
								Type:   protocol.ChunkFinish,
								Reason: protocol.FinishReason{Kind: kind, Failure: &protocol.LlmFailure{Code: "ABORTED", Message: "provider failed"}},
							},
						},
					},
					{Chunks: textChunks("unexpected")},
				},
			}
			h := newQi(t, adapter, nil)
			defer h.stop(t)

			offered := 0
			if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
				ID: "default-stop",
				RequestError: func(
					_ context.Context,
					e agentmiddleware.RequestError,
					action *agentmiddleware.RequestErrorAction,
				) (*agentmiddleware.RequestErrorAction, error) {
					offered++
					if e.RetryPolicy != nil {
						t.Fatal("kernel supplied a policy")
					}

					return action, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if err == nil || offered != 1 || len(adapter.Seen) != 1 {
				t.Fatal(err, offered, len(adapter.Seen))
			}

			messages, err := h.current.Messages(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			for _, m := range messages {
				if m.Role == protocol.RoleAssistant {
					t.Fatal("provider failure was projected")
				}
			}

			assertTerminalConsistency(t, h.current, events, protocol.ReasonFailed)
		})
	}
}

func TestRetryWaitCanBeCanceled(t *testing.T) {
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{{Err: errors.New("transient")}, {Chunks: textChunks("unexpected")}},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})
	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "waiting",
		RequestError: func(ctx context.Context, _ agentmiddleware.RequestError, _ *agentmiddleware.RequestErrorAction) (*agentmiddleware.RequestErrorAction, error) {
			close(entered)
			<-ctx.Done()
			return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.drive(ctx, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retry hook not entered")
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retry wait did not exit")
	}

	if len(adapter.Seen) != 1 {
		t.Fatal("dispatched after cancel")
	}

	ends := 0

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.TurnEnded {
			ends++
			d := protocol.EventPayload[protocol.TurnEndData](e)
			if d.Reason.Kind != "aborted" {
				t.Fatal(d)
			}
		}
	}

	if ends != 1 {
		t.Fatal(ends)
	}
}

func TestDirectDriverPersistsToolContextAndHonorsFilteredBatch(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprintf("filter-context=%t", drop), func(t *testing.T) {
			adapter := &scriptedAdapter{
				Scripts: []scriptedScript{
					{
						Chunks: toolChunks(protocol.ToolCall{
							ID:        "c",
							Name:      "finish",
							Arguments: `{}`,
						}),
					},
					{Chunks: textChunks("done")},
				},
			}
			h := newQi(t, adapter, nil)
			defer h.stop(t)

			if _, err := h.loop.coordinator.Tools.Register(h.owner, tools.Definition{
				Name: "finish",
				Execute: func(context.Context, tools.Execution) (tools.ExecutionResult, error) {
					return tools.ExecutionResult{
						Value:              json.RawMessage(`null`),
						Content:            []protocol.ContentBlock{},
						ConcludesTurn:      true,
						AdditionalContexts: []protocol.Message{protocol.NewTextMessage("extra", protocol.RoleUser, protocol.SourceUser, "context")},
					}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
				ID: "filter",
				PreStep: func(_ context.Context, i agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
					if i.Index == 2 && drop {
						i.Messages = nil
					}

					return i, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}); err != nil {
				t.Fatal(err)
			}

			wanted := 2
			if drop {
				wanted = 1
			}

			if len(adapter.Seen) != wanted {
				t.Fatal("empty filtered batch spent another request", len(adapter.Seen))
			}

			if !drop {
				if _, found := messageByID(adapter.Seen[1].Messages, "extra"); !found {
					t.Fatal("direct Drive lost durable context")
				}
			}

			splices, users := 0, 0

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.InboxSpliced {
					splices++
				}

				if e.Type == protocol.UserMessage {
					users++
				}
			}

			if splices != 4 || users != wanted {
				t.Fatal(splices, users)
			}
		})
	}
}

func TestAbortedProviderAttemptCanRetryWithoutInterruptedProjection(t *testing.T) {
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{
				Chunks: []protocol.StreamChunk{
					{
						Type:  protocol.ChunkTextDelta,
						Index: 0,
						Text:  "failed prefix",
					},
					{
						Type: protocol.ChunkFinish,
						Reason: protocol.FinishReason{
							Kind:    protocol.FinishAborted,
							Failure: &protocol.LlmFailure{Message: "provider abort", Code: "ABORTED"},
						},
					},
				},
			},
			{Chunks: textChunks("done")},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "retry",
		RequestError: func(context.Context, agentmiddleware.RequestError, *agentmiddleware.RequestErrorAction) (*agentmiddleware.RequestErrorAction, error) {
			return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.drive(context.Background(), Config{MaxSteps: 1, LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}); err != nil {
		t.Fatal(err)
	}

	if len(adapter.Seen) != 2 {
		t.Fatal(len(adapter.Seen))
	}

	for _, m := range adapter.Seen[1].Messages {
		if m.Role == protocol.RoleAssistant {
			t.Fatal("provider abort prefix became model context")
		}
	}

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.AssistantMessage && protocol.EventPayload[protocol.AssistantMessageData](e).Interrupted {
			t.Fatal("provider abort became interrupted user cancellation")
		}
	}
}
