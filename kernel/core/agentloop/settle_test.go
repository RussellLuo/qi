package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Capture at the synchronous publisher, before a bounded observer can drop
// frames. Session append success must already be visible at every committed end.
type frameCapture struct {
	t       *testing.T
	current session.Session
	agent   contracts.AgentRuntime
	frames  []protocol.AssistantStreamFrame
	onFrame func(protocol.AssistantStreamFrame)
}

func (*frameCapture) SessionAppended(protocol.SessionID, protocol.SessionEvent) {}

func (*frameCapture) DriverEvent(contracts.DriverEvent) {}

func (o *frameCapture) AssistantStream(event contracts.AssistantStreamObservation) {
	o.t.Helper()
	if event.Agent != o.agent {
		o.t.Fatal("lost local Agent reference")
	}

	if err := event.Frame.Validate(); err != nil {
		o.t.Fatal(err)
	}

	f := event.Frame
	if o.current != nil && f.Type == "end" && f.Outcome.Kind == "committed" {
		facts := sessionEvents(o.t, o.current)
		last := facts[len(facts)-1]
		if last.Seq != *f.Outcome.Seq || last.Type != f.Outcome.EventType {
			o.t.Fatalf("end preceded settlement: %+v / %+v", f, last)
		}
	}

	o.frames = append(o.frames, f.Clone())
	if o.onFrame != nil {
		o.onFrame(f)
	}
	// An observer owns only its snapshot; mutations must not alter the log.
	if f.Chunk != nil && f.Chunk.Type == protocol.ChunkTextDelta {
		f.Chunk.Text = "observer mutation"
	}
}

func captureFrames(t *testing.T, h *qi) *frameCapture {
	o := &frameCapture{
		t:       t,
		current: h.current,
		agent:   h.agent,
	}
	h.loop.observer = o
	return o
}

func assertLiveDurable(t *testing.T, current session.Session, frames []protocol.AssistantStreamFrame) {
	t.Helper()
	facts := sessionEvents(t, current)
	var timed []protocol.TimedStreamChunk
	var attempt string
	starts, ends := 0, 0

	for i, f := range frames {
		if f.Revision != int64(i+1) {
			t.Fatalf("revision: %+v", f)
		}

		switch f.Type {
		case "start":
			if starts != ends {
				t.Fatal("start before prior end")
			}
			starts++
			attempt = fmt.Sprintf("%s:%d", current.ID(), starts)
			timed = []protocol.TimedStreamChunk{}
		case "chunk":
			if f.Index != int64(len(timed)) {
				t.Fatalf("frame index: %+v", f)
			}
			timed = append(timed, protocol.TimedStreamChunk{Time: f.Time, Chunk: *f.Chunk})
		case "end":
			ends++
			if starts != ends || f.Index != int64(len(timed)) {
				t.Fatalf("end count/index: %+v", f)
			}
			if f.Outcome.Kind == "abandoned" {
				if f.Outcome.Seq != nil {
					t.Fatal("abandoned seq")
				}

				continue
			}
			e := facts[*f.Outcome.Seq]
			var records []protocol.AssistantStreamRecord
			if e.Type == protocol.AssistantMessage {
				records = protocol.EventPayload[protocol.AssistantMessageData](e).Stream
			} else {
				records = protocol.EventPayload[protocol.AttemptData](e).Stream
			}
			expanded, err := protocol.ExpandAssistantStream(records)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(expanded, timed) {
				t.Fatalf("live != durable: %+v / %+v", timed, expanded)
			}
		}

		if f.AttemptID != attempt {
			t.Fatalf("attempt id: %+v", f)
		}
	}

	if starts == 0 || starts != ends {
		t.Fatalf("start/end: %d/%d", starts, ends)
	}
}

func TestLiveDurableAcrossStepsAndTurns(t *testing.T) {
	chunks := toolChunks(protocol.ToolCall{
		ID:        "c1",
		Name:      "echo",
		Arguments: `{"text":"one"}`,
	})
	// Model block 37 is independent of chunk-frame 0,1,2,...
	for i := range chunks {
		if chunks[i].Type != protocol.ChunkFinish {
			chunks[i].Index = 37
		}
	}

	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{{Chunks: chunks}, {Chunks: textChunks("answer")}, {Chunks: textChunks("again")}},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	o := captureFrames(t, h)
	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}

	for i := 0; i < 2; i++ {
		run := h.run(config)
		run.InvocationID = protocol.InvocationID(fmt.Sprintf("r%d", i))
		if err := h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	assertLiveDurable(t, h.current, o.frames)
	if o.frames[1].Index != 0 || o.frames[1].Chunk.Index != 37 {
		t.Fatal("frame/block index conflated")
	}

	var starts []protocol.StepData

	for _, f := range o.frames {
		if f.Type == "start" {
			starts = append(starts, protocol.StepData{Turn: f.Turn, Step: f.Step})
		}
	}

	if !reflect.DeepEqual(starts, []protocol.StepData{{Turn: 1, Step: 1}, {Turn: 1, Step: 2}, {Turn: 2, Step: 1}}) {
		t.Fatalf("starts: %+v", starts)
	}
}

func TestCancellationAndFailureProjection(t *testing.T) {
	for _, mode := range []string{
		"cancel-prefix",
		"cancel-empty",
		"cancel-whitespace",
		"cancel-after-finish",
		"model-failure",
		"model-aborted",
	} {
		t.Run(mode, func(t *testing.T) {
			prefix := []protocol.StreamChunk{}
			if mode != "cancel-empty" {
				text := "partial"
				if mode == "cancel-whitespace" {
					text = " \n"
				}

				prefix = append(prefix, protocol.StreamChunk{
					Type:  protocol.ChunkReasoningDelta,
					Index: 11,
					Text:  text,
				}, protocol.StreamChunk{
					Type:  protocol.ChunkTextDelta,
					Index: 3,
					Text:  text,
				})
			}

			tool := toolChunks(protocol.ToolCall{
				ID:        "c",
				Name:      "echo",
				Arguments: `{"text":"unsafe"}`,
			})
			prefix = append(prefix, tool[:len(tool)-1]...)
			prefix = append(prefix, protocol.StreamChunk{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 2, OutputTokens: 3}})
			script := scriptedScript{Chunks: prefix}

			switch mode {
			case "model-failure":
				script.Err = errors.New("model failed")
			case "model-aborted":
				script.Chunks = append(script.Chunks, protocol.StreamChunk{
					Type: protocol.ChunkFinish,
					Reason: protocol.FinishReason{
						Kind:    protocol.FinishAborted,
						Failure: &protocol.LlmFailure{Code: "ABORTED", Message: "aborted"},
					},
				})
			case "cancel-after-finish":
				script.Chunks = append(script.Chunks, protocol.StreamChunk{
					Type:        protocol.ChunkFinish,
					Reason:      protocol.FinishReason{Kind: protocol.FinishStop},
					ReplayState: &protocol.ReplayEnvelope{Response: map[string]any{"response": "opaque"}},
				})
			}

			h := newQi(t, &scriptedAdapter{Scripts: []scriptedScript{script}}, nil)
			defer h.stop(t)

			o := captureFrames(t, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if mode != "model-failure" && mode != "model-aborted" {
				o.onFrame = func(f protocol.AssistantStreamFrame) {
					if f.Chunk != nil &&
						((mode != "cancel-after-finish" && f.Chunk.Type == protocol.ChunkUsage) ||
							(mode == "cancel-after-finish" && f.Chunk.Type == protocol.ChunkFinish)) {
						cancel()
					}
				}
			}

			events, err := h.drive(ctx, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if err == nil {
				t.Fatal("failure became success")
			}

			wantMessage := mode == "cancel-prefix" || mode == "cancel-after-finish"
			var settlement protocol.SessionEvent

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.AssistantMessage || e.Type == protocol.AssistantAttempt {
					settlement = e
				}

				if e.Type == protocol.ToolCalled {
					t.Fatal("failed/canceled proposal dispatched")
				}
			}

			if wantMessage {
				if settlement.Type != protocol.AssistantMessage {
					t.Fatalf("no interrupted message: %+v", settlement)
				}

				d := protocol.EventPayload[protocol.AssistantMessageData](settlement)
				if !d.Interrupted ||
					len(d.Message.Content) != 2 ||
					len(toolCalls(d.Message)) != 0 ||
					len(d.Message.Source.ReplayState) != 0 ||
					d.Usage.OutputTokens != 3 {
					t.Fatalf("unsafe prefix: %+v", d)
				}
			} else if settlement.Type != protocol.AssistantAttempt {
				t.Fatalf("not an attempt: %+v", settlement)
			}

			messages, err := h.current.Messages(context.Background())
			if err != nil {
				t.Fatal(err)
			}

			assistantCount := 0

			for _, m := range messages {
				if m.Role == protocol.RoleAssistant {
					assistantCount++
				}
			}

			if (assistantCount == 1) != wantMessage {
				t.Fatalf("projection leaked attempt: %+v", messages)
			}

			assertLiveDurable(t, h.current, o.frames)
			reason := protocol.ReasonCancelled
			if mode == "model-failure" || mode == "model-aborted" {
				reason = protocol.ReasonFailed
			}

			assertTerminalConsistency(t, h.current, events, reason)
		})
	}
}

type injectedSession struct {
	session.Session
	failType  protocol.SessionEventKind
	appendErr error
	appends   []protocol.SessionEventKind
}

func (s *injectedSession) Append(ctx context.Context, event protocol.SessionEvent) (protocol.Sequence, error) {
	s.appends = append(s.appends, event.Type)
	if event.Type == s.failType {
		return -1, s.appendErr
	}

	return s.Session.Append(ctx, event)
}

type injectedStore struct {
	session.Store
	current session.Session
}

func (s injectedStore) Get(context.Context, protocol.SessionID) (session.Session, error) {
	return s.current, nil
}

func TestSettlementAppendFailureAbandonsAndNeverDispatches(t *testing.T) {
	for _, kind := range []protocol.SessionEventKind{protocol.AssistantMessage, protocol.AssistantAttempt} {
		t.Run(string(kind), func(t *testing.T) {
			script := scriptedScript{
				Chunks: toolChunks(protocol.ToolCall{
					ID:        "c",
					Name:      "echo",
					Arguments: `{"text":"side effect"}`,
				}),
			}
			if kind == protocol.AssistantAttempt {
				script.Err = errors.New("model failed")
				script.Chunks = script.Chunks[:len(script.Chunks)-1]
			}

			executed := false
			h := newQi(
				t,
				&scriptedAdapter{Scripts: []scriptedScript{script}},
				func(context.Context, tools.Execution) (json.RawMessage, error) {
					executed = true
					return json.RawMessage(`{}`), nil
				},
			)
			defer h.stop(t)

			o := captureFrames(t, h)
			failure := errors.New("settlement append rejected")
			injected := &injectedSession{
				Session:   h.current,
				failType:  kind,
				appendErr: failure,
			}
			h.current = injected
			_, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if !errors.Is(err, failure) || executed {
				t.Fatalf("error=%v executed=%v", err, executed)
			}

			end := o.frames[len(o.frames)-1]
			if end.Outcome.Kind != "abandoned" || end.Outcome.Seq != nil {
				t.Fatalf("false commitment: %+v", end)
			}

			for _, k := range injected.appends {
				if k == protocol.StepEnded || k == protocol.TurnEnded || k == protocol.ToolCalled {
					t.Fatalf("false closure/dispatch after rejected settlement: %s", k)
				}
			}

			assertLiveDurable(t, h.current, o.frames)
		})
	}
}

func TestLegalEmptySuccessAndMaxTokens(t *testing.T) {
	for _, finish := range []protocol.FinishKind{"", protocol.FinishStop, protocol.FinishToolCalls, protocol.FinishMaxTokens} {
		t.Run(string(finish), func(t *testing.T) {
			chunks := []protocol.StreamChunk{}
			if finish != "" {
				chunks = append(chunks, protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: finish}})
			}

			h := newQi(t, &scriptedAdapter{Scripts: []scriptedScript{{Chunks: chunks}}}, nil)
			defer h.stop(t)

			o := captureFrames(t, h)
			events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if err != nil {
				t.Fatal(err)
			}

			reason := protocol.ReasonCompleted
			if finish == protocol.FinishMaxTokens {
				reason = protocol.ReasonMaxTokens
			}

			facts := sessionEvents(t, h.current)
			if protocol.EventPayload[protocol.TurnEndData](facts[len(facts)-1]).Reason.Kind != string(reason) ||
				events[len(events)-1].Reason.Kind != string(reason) {
				t.Fatal("incorrect empty/max-tokens result")
			}

			assertLiveDurable(t, h.current, o.frames)
		})
	}
}

func TestStreamMiddlewareFailureSettlesStartedAttemptOnce(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprint(immediate), func(t *testing.T) {
			h := newQi(t, &scriptedAdapter{Scripts: []scriptedScript{{Chunks: textChunks("answer")}}}, nil)
			defer h.stop(t)

			failure := errors.New("stream middleware failed")
			_, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
				ID: "stream-failure",
				Stream: func(_ context.Context, upstream contracts.ChunkStream) (contracts.ChunkStream, error) {
					if immediate {
						return nil, failure
					}

					return func(yield func(protocol.StreamChunk, error) bool) {
						for chunk, err := range upstream {
							if !yield(chunk, err) {
								return
							}
							if chunk.Type == protocol.ChunkTextDelta {
								yield(protocol.StreamChunk{}, failure)
								return
							}
						}
					}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			o := captureFrames(t, h)
			_, err = h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if !errors.Is(err, failure) {
				t.Fatalf("error lost: %v", err)
			}

			assertLiveDurable(t, h.current, o.frames)
			last := o.frames[len(o.frames)-1]
			if last.Outcome.EventType != protocol.AssistantAttempt {
				t.Fatal("middleware failure entered message projection")
			}

			if immediate && last.Index != 0 {
				t.Fatal("manufactured raw chunks for middleware failure")
			}
		})
	}
}

func TestDriverSinkFailureDoesNotChangeSettlement(t *testing.T) {
	h := newQi(t, &scriptedAdapter{Scripts: []scriptedScript{{Chunks: textChunks("answer")}}}, nil)
	defer h.stop(t)

	o := captureFrames(t, h)
	failure := errors.New("compatibility observer exited")
	err := h.loop.Drive(
		context.Background(),
		h.run(Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}),
		func(contracts.DriverEvent) error { return failure },
	)
	if !errors.Is(err, failure) {
		t.Fatalf("sink error lost: %v", err)
	}

	assertLiveDurable(t, h.current, o.frames)
	if o.frames[len(o.frames)-1].Outcome.EventType != protocol.AssistantMessage {
		t.Fatal("sink failure changed model settlement")
	}
}

type cancelAfterAppend struct {
	session.Session
	kind   protocol.SessionEventKind
	cancel context.CancelCauseFunc
}

func (s cancelAfterAppend) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	seq, err := s.Session.Append(ctx, e)
	if err == nil && e.Type == s.kind {
		s.cancel(contracts.ActivityCancellation{Cause: protocol.AgentCancelCause{Kind: "hook", Reason: "append barrier"}})
	}

	return seq, err
}

// Abort after successful assistant commit, before dispatch or between results.
func TestCanceledCommittedBatchPairsEveryProposal(t *testing.T) {
	for _, boundary := range []protocol.SessionEventKind{protocol.AssistantMessage, protocol.ToolCalled, protocol.ToolResulted} {
		t.Run(string(boundary), func(t *testing.T) {
			executed := 0
			h := newQi(t, &scriptedAdapter{
				Scripts: []scriptedScript{
					{
						Chunks: toolChunks(
							protocol.ToolCall{
								ID:        "one",
								Name:      "echo",
								Arguments: `{"text":"one"}`,
							},
							protocol.ToolCall{
								ID:        "two",
								Name:      "echo",
								Arguments: `{"text":"two"}`,
							},
						),
					},
				},
			}, func(context.Context, tools.Execution) (json.RawMessage, error) {
				executed++
				return json.RawMessage(`{"text":"done"}`), nil
			})
			defer h.stop(t)

			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)

			wrapped := cancelAfterAppend{
				Session: h.current,
				kind:    boundary,
				cancel:  cancel,
			}
			h.current = wrapped
			events, err := h.drive(ctx, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}

			wantExecuted := 0
			if boundary == protocol.ToolResulted {
				wantExecuted = 1
			}

			if executed != wantExecuted {
				t.Fatal("unexpected side effects", executed)
			}

			calls := map[protocol.ToolCallID]protocol.Sequence{}
			results := 0

			for _, e := range sessionEvents(t, h.current) {
				switch e.Type {
				case protocol.ToolCalled:
					calls[protocol.EventPayload[protocol.ToolCallData](e).CallID] = e.Seq
				case protocol.ToolResulted:
					results++
					d := protocol.EventPayload[protocol.ToolResultData](e)
					if len(e.SourceEventSeqs) != 1 ||
						e.SourceEventSeqs[0] != calls[d.Message.ToolCallID] ||
						e.SurfaceOp.Op != "append" ||
						d.Message.Source.CallID != d.Message.ToolCallID {
						t.Fatal(e)
					}
					if results > wantExecuted &&
						(d.Error == nil ||
							d.Error.Name != "AbortError" ||
							d.Error.Code != "ABORTED_BEFORE_DISPATCH" ||
							d.Message.IsError == nil ||
							!*d.Message.IsError ||
							d.Message.Content[0].(protocol.TextBlock).Text != "Error: tool call aborted before dispatch") {
						t.Fatal(d)
					}
				case protocol.TurnEnded:
					r := protocol.EventPayload[protocol.TurnEndData](e).Reason
					if r.Kind != "aborted" || r.Reason == nil || r.Reason.Kind != "hook" || r.Reason.Reason != "append barrier" {
						t.Fatal(r)
					}
				}
			}

			if len(calls) != 2 || results != 2 {
				t.Fatal(calls, results)
			}

			assertTerminalConsistency(t, h.current, events, protocol.ReasonCancelled)
		})
	}
}

func TestRequiredAppendFailureLeavesHonestPrefix(t *testing.T) {
	for _, kind := range []protocol.SessionEventKind{protocol.ToolCalled, protocol.ToolResulted, protocol.StepEnded, protocol.TurnEnded} {
		t.Run(string(kind), func(t *testing.T) {
			executed := 0
			h := newQi(t, &scriptedAdapter{
				Scripts: []scriptedScript{
					{
						Chunks: toolChunks(
							protocol.ToolCall{
								ID:        "one",
								Name:      "echo",
								Arguments: `{"text":"one"}`,
							},
							protocol.ToolCall{
								ID:        "two",
								Name:      "echo",
								Arguments: `{"text":"two"}`,
							},
						),
					},
					{Chunks: textChunks("done")},
				},
			}, func(context.Context, tools.Execution) (json.RawMessage, error) {
				executed++
				return json.RawMessage(`{"text":"done"}`), nil
			})
			defer h.stop(t)

			failure := errors.New("required append failed")
			wrapped := &injectedSession{
				Session:   h.current,
				failType:  kind,
				appendErr: failure,
			}
			h.current = wrapped
			events, err := h.drive(context.Background(), Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}

			wantExecuted := 2
			if kind == protocol.ToolCalled {
				wantExecuted = 0
			}

			if kind == protocol.ToolResulted {
				wantExecuted = 1
			}

			if executed != wantExecuted {
				t.Fatal("executed or retried uncommitted side effect", executed)
			}

			failed := false

			for _, attempted := range wrapped.appends {
				if failed {
					t.Fatal("continued writing after required rejection", wrapped.appends)
				}

				failed = attempted == kind
			}

			for _, e := range events {
				if e.Type == "ended" && e.Reason.Kind == "completed" {
					t.Fatal("false success", e)
				}
			}
		})
	}
}

func TestCanceledResultAppendFailureReportsUncommitted(t *testing.T) {
	executed := false
	h := newQi(t, &scriptedAdapter{
		Scripts: []scriptedScript{
			{
				Chunks: toolChunks(
					protocol.ToolCall{
						ID:        "one",
						Name:      "echo",
						Arguments: `{"text":"one"}`,
					},
					protocol.ToolCall{
						ID:        "two",
						Name:      "echo",
						Arguments: `{"text":"two"}`,
					},
				),
			},
		},
	}, func(context.Context, tools.Execution) (json.RawMessage, error) {
		executed = true
		return json.RawMessage(`{}`), nil
	})
	defer h.stop(t)

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	failure := errors.New("canceled result append failed")
	rejected := &injectedSession{
		Session:   h.current,
		failType:  protocol.ToolResulted,
		appendErr: failure,
	}
	h.current = cancelAfterAppend{
		Session: rejected,
		kind:    protocol.ToolCalled,
		cancel:  cancel,
	}
	events, err := h.drive(ctx, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	if !errors.Is(err, failure) || executed {
		t.Fatal(err, executed)
	}

	for _, kind := range rejected.appends {
		if kind == protocol.StepEnded || kind == protocol.TurnEnded {
			t.Fatal("false cancellation closure", rejected.appends)
		}
	}

	if events[len(events)-1].Type != "ended" || events[len(events)-1].Err == nil {
		t.Fatal("reported committed abort after rejected result", events)
	}
}
