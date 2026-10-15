package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func schedulingCalls() []protocol.StreamChunk {
	chunks := []protocol.StreamChunk{}

	for i := 0; i < 2; i++ {
		call := protocol.ToolCall{
			ID:        protocol.ToolCallID(fmt.Sprintf("call-%d", i)),
			Name:      "schedule",
			Arguments: `{}`,
		}
		chunks = append(chunks, protocol.StreamChunk{
			Type:  protocol.ChunkBlockEnd,
			Index: int64(i),
			Block: protocol.ToolCallBlock{ToolCall: call},
		})
	}

	return append(chunks, protocol.StreamChunk{
		Type:   protocol.ChunkFinish,
		Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls},
	})
}

func TestToolConclusionSettlesBatchAndDrainsContextOrSteering(t *testing.T) {
	for _, disk := range []bool{false} {
		for _, mode := range []string{"conclude", "contexts", "steer", "failure-conclude"} {
			t.Run(fmt.Sprintf("jsonl=%t/%s", disk, mode), func(t *testing.T) {
				h := newInboxQi(t, disk, nil)
				ctx := context.Background()
				calls := 0
				if _, err := h.tools.Register(newTestOwner("tool", 1), tools.Definition{
					Name: "schedule",
					Execute: func(ctx context.Context, _ tools.Execution) (tools.ExecutionResult, error) {
						calls++
						result := tools.ExecutionResult{
							Value:         json.RawMessage(`{"executionOnlySecret":true}`),
							Content:       []protocol.ContentBlock{protocol.TextBlock{Text: "visible"}},
							Meta:          json.RawMessage(`{"native":1}`),
							ConcludesTurn: calls == 1,
						}
						if mode == "contexts" {
							m := inboxUser(fmt.Sprintf("context-%d", calls))
							m.Source = protocol.MessageSource{
								Kind:  "plugin",
								Extra: map[string]json.RawMessage{"plugin": json.RawMessage(`"schedule"`)},
							}
							result.AdditionalContexts = []protocol.Message{m}
							if calls == 2 {
								result.Value = nil
								result.IsError = true
								result.Error = &tools.Failure{
									Message: "local only failure",
									Info:    &protocol.ToolFailureInfo{Name: "BusinessError", Code: "BUSINESS"},
								}
							}
						}

						if mode == "steer" && calls == 2 {
							if err := h.a.Steer(ctx, inboxUser("steered")); err != nil {
								return tools.ExecutionResult{}, err
							}
						}

						if mode == "failure-conclude" {
							result.Value = nil
							result.IsError = true
							result.Error = &tools.Failure{Message: "failed"}
							result.ConcludesTurn = true
						}

						return result, nil
					},
				}); err != nil {
					t.Fatal(err)
				}

				if err := h.a.Followup(ctx, inboxUser("initial")); err != nil {
					t.Fatal(err)
				}

				first := nextInboxRequest(t, h)
				h.llm.release <- schedulingCalls()
				if mode != "conclude" {
					second := nextInboxRequest(t, h)
					ids := messageIDs(second.Messages)
					if mode == "contexts" && !reflect.DeepEqual(ids, []string{"initial", "context-1", "context-2"}) {
						t.Fatal(ids)
					}

					if mode == "steer" && !reflect.DeepEqual(ids, []string{"initial", "steered"}) {
						t.Fatal(ids)
					}

					toolMessages := 0

					for _, m := range second.Messages {
						if m.Role == protocol.RoleTool {
							toolMessages++
							if mode == "failure-conclude" && (m.IsError == nil || !*m.IsError) {
								t.Fatal("failure illegally concluded")
							}
						}

						if m.ID == "context-2" && (m.Source.Kind != "plugin" || string(m.Source.Extra["plugin"]) != `"schedule"`) {
							t.Fatal(m.Source)
						}
					}

					if toolMessages != 2 {
						t.Fatal("incomplete tool batch", second)
					}

					h.llm.release <- inboxStop()
				}

				awaitTestAgentIdle(t, h.a)
				if calls != 2 || len(h.llm.requests) != 0 || !reflect.DeepEqual(messageIDs(first.Messages), []string{"initial"}) {
					t.Fatal("batch stopped early or frozen request changed", calls)
				}

				results, steps, ends := 0, 0, 0

				for _, e := range sessionEvents(t, h.current) {
					encoded, err := json.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}

					if containsLocalValue(encoded) {
						t.Fatalf("execution data leaked: %s", encoded)
					}

					switch e.Type {
					case protocol.ToolResulted:
						results++
						d := protocol.EventPayload[protocol.ToolResultData](e)
						if mode == "contexts" &&
							results == 2 &&
							(d.Error == nil || d.Error.Code != "BUSINESS" || string(d.Meta) != `{"native":1}`) {
							t.Fatal(d)
						}
					case protocol.StepStarted:
						steps++
					case protocol.TurnEnded:
						ends++
					}
				}

				wantSteps := 2
				if mode == "conclude" {
					wantSteps = 1
				}

				if results != 2 || steps != wantSteps || ends != 1 || len(inboxPending(t, h.a, contracts.NextStep)) != 0 {
					t.Fatal(results, steps, ends)
				}
			})
		}
	}
}

func containsLocalValue(data []byte) bool {
	return bytes.Contains(data, []byte("executionOnlySecret")) || bytes.Contains(data, []byte("local only failure"))
}

func TestTurnStoppingAwaitsSerialListenersAndRechecksSteering(t *testing.T) {
	for _, disk := range []bool{false} {
		t.Run(fmt.Sprintf("jsonl=%t", disk), func(t *testing.T) {
			h := newInboxQi(t, disk, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			stops := 0
			if _, err := h.middleware.Register(newTestOwner("first", 1), agentmiddleware.Middleware{
				ID:    "first",
				Order: 1,
				TurnStopping: func(ctx context.Context, e agentmiddleware.TurnStop) error {
					if e.Agent != h.a || e.Turn != 1 {
						return errors.New("lost local Agent/Turn")
					}

					stops++
					if stops == 1 {
						close(entered)

						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}

						return e.Agent.(contracts.AgentControl).Steer(ctx, inboxUser("hook-steer"))
					}

					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			second := make(chan struct{}, 2)
			if _, err := h.middleware.Register(newTestOwner("second", 1), agentmiddleware.Middleware{
				ID:    "second",
				Order: 2,
				TurnStopping: func(context.Context, agentmiddleware.TurnStop) error {
					second <- struct{}{}
					return nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
				t.Fatal(err)
			}

			_ = nextInboxRequest(t, h)
			h.llm.release <- inboxStop()

			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("stop hook not entered")
			}

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.TurnEnded {
					t.Fatal("closed before listener returned")
				}
			}

			select {
			case <-second:
				t.Fatal("listeners were concurrent")
			default:
			}

			close(release)
			r := nextInboxRequest(t, h)
			if !reflect.DeepEqual(messageIDs(r.Messages), []string{"initial", "hook-steer"}) {
				t.Fatal(messageIDs(r.Messages))
			}

			h.llm.release <- inboxStop()
			awaitTestAgentIdle(t, h.a)
			if stops != 2 || len(second) != 2 {
				t.Fatal(stops, len(second))
			}

			turns, steps := 0, 0

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.TurnStarted {
					turns++
				}

				if e.Type == protocol.StepStarted {
					steps++
				}
			}

			if turns != 1 || steps != 2 {
				t.Fatal(turns, steps)
			}
		})
	}
}

func TestRetryClaimsDurableInputOnlyOnce(t *testing.T) {
	for _, disk := range []bool{false} {
		t.Run(fmt.Sprintf("jsonl=%t", disk), func(t *testing.T) {
			h := newInboxQi(t, disk, nil)
			attempts := []string{}
			if _, err := h.middleware.Register(newTestOwner("retry", 1), agentmiddleware.Middleware{
				ID: "retry",
				RequestError: func(ctx context.Context, e agentmiddleware.RequestError, _ *agentmiddleware.RequestErrorAction) (*agentmiddleware.RequestErrorAction, error) {
					if e.Agent != h.a || e.Turn != 1 || e.Step != 1 || ctx.Err() != nil {
						t.Errorf("wrong retry input: %+v", e)
					}

					attempts = append(attempts, e.AttemptID)
					return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
				t.Fatal(err)
			}

			first := nextInboxRequest(t, h)
			h.llm.release <- []protocol.StreamChunk{
				{
					Type: protocol.ChunkFinish,
					Reason: protocol.FinishReason{
						Kind:    protocol.FinishError,
						Failure: &protocol.LlmFailure{Code: "SERVER", Message: "try again"},
					},
				},
			}
			second := nextInboxRequest(t, h)
			if !reflect.DeepEqual(first.Messages, second.Messages) {
				t.Fatal("retry changed admitted user batch")
			}

			h.llm.release <- inboxStop()
			awaitTestAgentIdle(t, h.a)
			counts := map[protocol.SessionEventKind]int{}

			for _, e := range sessionEvents(t, h.current) {
				counts[e.Type]++
			}

			if counts[protocol.InboxSpliced] != 2 ||
				counts[protocol.UserMessage] != 1 ||
				counts[protocol.StepStarted] != 1 ||
				counts[protocol.AssistantAttempt] != 1 ||
				len(attempts) != 1 {
				t.Fatal(counts, attempts)
			}
		})
	}
}

func TestTurnStoppingCancellationAndOriginalErrorObservation(t *testing.T) {
	for _, cancelHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelHook), func(t *testing.T) {
			h := newInboxQi(t, false, nil)
			native := newFixtureEvents()
			defer native.Close()

			h.service.SetLifecycle(native)
			observed := make(chan contracts.AgentErrorObservation, 1)
			if _, err := native.Register(newTestOwner("error-listener", 1), fixtureHandlers{ID: "errors", Error: func(e contracts.AgentErrorObservation) {
				observed <- e
			}}); err != nil {
				t.Fatal(err)
			}

			entered := make(chan struct{})
			original := errors.New("original local hook error")
			if _, err := h.middleware.Register(newTestOwner("stopping", 1), agentmiddleware.Middleware{
				ID: "stopping",
				TurnStopping: func(ctx context.Context, _ agentmiddleware.TurnStop) error {
					close(entered)
					if cancelHook {
						<-ctx.Done()
						return nil
					}

					return original
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
				t.Fatal(err)
			}

			_ = nextInboxRequest(t, h)
			h.llm.release <- inboxStop()
			waitTestSignal(t, entered)
			if cancelHook {
				if err := h.a.CancelActivity(CancelCause{Kind: "hook", Reason: "stop test"}); err != nil {
					t.Fatal(err)
				}
			}

			awaitTestAgentIdle(t, h.a)

			select {
			case e := <-observed:
				if e.Agent != h.a || e.Turn != 1 || e.Step != 1 || e.Error == nil {
					t.Fatal(e)
				}
				if !cancelHook && !errors.Is(e.Error, original) {
					t.Fatal("serialized or replaced local error", e.Error)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no native agent/error")
			}

			ends := 0

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.TurnEnded {
					ends++
					d := protocol.EventPayload[protocol.TurnEndData](e)
					if cancelHook {
						if d.Reason.Kind != "aborted" || d.Reason.Reason.Kind != "hook" || d.Reason.Reason.Reason != "stop test" {
							t.Fatal(d)
						}
					} else if d.Reason.Kind != "error" || d.Reason.Error.Code != "UNKNOWN" {
						t.Fatal(d)
					}
				}
			}

			if ends != 1 {
				t.Fatal(ends)
			}
			// Native disposal drains errors before its registry is closed.
			if err := h.service.Delete(context.Background(), h.a.ID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaxTokensIsStickyAcrossStopListenerSteering(t *testing.T) {
	h := newInboxQi(t, false, nil)
	stops := 0
	if _, err := h.middleware.Register(newTestOwner("steer", 1), agentmiddleware.Middleware{
		ID: "steer",
		TurnStopping: func(ctx context.Context, stop agentmiddleware.TurnStop) error {
			stops++
			if stops == 1 {
				return stop.Agent.(contracts.AgentControl).Steer(ctx, inboxUser("more"))
			}

			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
		t.Fatal(err)
	}

	_ = nextInboxRequest(t, h)
	h.llm.release <- []protocol.StreamChunk{{
		Type:   protocol.ChunkFinish,
		Reason: protocol.FinishReason{Kind: protocol.FinishMaxTokens},
	}}
	_ = nextInboxRequest(t, h)
	h.llm.release <- inboxStop()
	awaitTestAgentIdle(t, h.a)
	ends := 0

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.TurnEnded {
			ends++
			if protocol.EventPayload[protocol.TurnEndData](e).Reason.Kind != "max-tokens" {
				t.Fatal("downgraded max-tokens", e)
			}
		}
	}

	if ends != 1 || stops != 2 {
		t.Fatal(ends, stops)
	}
}
