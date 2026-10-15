package agentloop

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type attemptAppend struct {
	session.Session
	seq  protocol.Sequence
	fail bool
}

func (s attemptAppend) Append(context.Context, protocol.SessionEvent) (protocol.Sequence, error) {
	if s.fail {
		return -1, errors.New("injected append failure")
	}
	return s.seq, nil
}

func TestAttemptFramesAndExpandedStreams(t *testing.T) {
	type attemptCase struct {
		id          string
		text        string
		time        int64
		finish      protocol.FinishReason
		event       protocol.SessionEventKind
		seq         protocol.Sequence
		fail        bool
		frames      []protocol.AssistantStreamFrame
		interrupted []protocol.ContentBlock
	}
	stop := protocol.FinishReason{Kind: protocol.FinishStop}
	aborted := protocol.FinishReason{Kind: protocol.FinishAborted, Failure: &protocol.LlmFailure{Message: "request canceled", Code: "ABORTED"}}
	textChunk := protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: "你好！"}
	stopChunk := protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: stop}
	whitespace := protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: " \n"}
	abortChunk := protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: aborted}
	seq8, seq9 := protocol.Sequence(8), protocol.Sequence(9)
	committed := attemptCase{
		id: "s1:1", text: "你好！", time: 1008, finish: stop, event: protocol.AssistantMessage, seq: 8,
		interrupted: []protocol.ContentBlock{protocol.TextBlock{Text: "你好！"}},
		frames: []protocol.AssistantStreamFrame{
			{Type: "start", AttemptID: "s1:1", Revision: 1, Turn: 1, Step: 1},
			{Type: "chunk", AttemptID: "s1:1", Revision: 2, Index: 0, Time: 1008, Chunk: &textChunk},
			{Type: "chunk", AttemptID: "s1:1", Revision: 3, Index: 1, Time: 1009, Chunk: &stopChunk},
			{Type: "end", AttemptID: "s1:1", Revision: 4, Index: 2, Outcome: &protocol.AssistantStreamOutcome{Kind: "committed", EventType: protocol.AssistantMessage, Seq: &seq8}},
		},
	}
	failed := committed
	failed.fail = true
	failed.frames = append([]protocol.AssistantStreamFrame(nil), committed.frames...)
	failed.frames[3].Outcome = &protocol.AssistantStreamOutcome{Kind: "abandoned"}
	retry := committed
	retry.id, retry.time, retry.seq = "s1:2", 1010, 9
	retry.frames = []protocol.AssistantStreamFrame{
		{Type: "start", AttemptID: "s1:2", Revision: 5, Turn: 1, Step: 1},
		{Type: "chunk", AttemptID: "s1:2", Revision: 6, Index: 0, Time: 1010, Chunk: &textChunk},
		{Type: "chunk", AttemptID: "s1:2", Revision: 7, Index: 1, Time: 1011, Chunk: &stopChunk},
		{Type: "end", AttemptID: "s1:2", Revision: 8, Index: 2, Outcome: &protocol.AssistantStreamOutcome{Kind: "committed", EventType: protocol.AssistantMessage, Seq: &seq9}},
	}
	canceled := attemptCase{
		id: "s1:1", text: " \n", time: 1008, finish: aborted, event: protocol.AssistantAttempt, seq: 8,
		interrupted: []protocol.ContentBlock{},
		frames: []protocol.AssistantStreamFrame{
			{Type: "start", AttemptID: "s1:1", Revision: 1, Turn: 1, Step: 1},
			{Type: "chunk", AttemptID: "s1:1", Revision: 2, Index: 0, Time: 1008, Chunk: &whitespace},
			{Type: "chunk", AttemptID: "s1:1", Revision: 3, Index: 1, Time: 1009, Chunk: &abortChunk},
			{Type: "end", AttemptID: "s1:1", Revision: 4, Index: 2, Outcome: &protocol.AssistantStreamOutcome{Kind: "committed", EventType: protocol.AssistantAttempt, Seq: &seq8}},
		},
	}
	for _, tc := range []struct {
		name     string
		attempts []attemptCase
	}{
		{"committed", []attemptCase{committed}},
		{"append failure", []attemptCase{failed}},
		{"canceled then committed", []attemptCase{canceled, retry}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, err := session.CreateHeader("s1", nil)
			if err != nil {
				t.Fatal(err)
			}
			current, err := session.Restore(header, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			agent := &testAgentRuntime{sessionID: "s1"}
			observer := &frameCapture{t: t, agent: agent}
			emitter := newEmitter(Run{Agent: agent, Attached: AttachedRun{Session: current}, InvocationID: "test"}, func(contracts.DriverEvent) error { return nil }, observer)
			var frames []protocol.AssistantStreamFrame
			for _, in := range tc.attempts {
				acc, err := llm.NewAccumulator("message", protocol.MessageSource{Kind: protocol.SourceModel, Provider: "fake", Model: "model"})
				if err != nil {
					t.Fatal(err)
				}
				a := newAssistantAttempt(agent, 1, 1, acc, emitter)
				if a.id != in.id {
					t.Fatalf("attempt ID = %s, want %s", a.id, in.id)
				}
				a.start()
				chunks := []protocol.TimedStreamChunk{
					{Time: in.time, Chunk: protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: in.text}},
					{Time: in.time + 1, Chunk: protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: in.finish}},
				}
				for _, chunk := range chunks {
					a.now = func() int64 { return chunk.Time }
					if err := a.push(chunk.Chunk); err != nil {
						t.Fatal(err)
					}
				}
				err = a.settle(context.Background(), attemptAppend{seq: in.seq, fail: in.fail}, protocol.NewSessionEvent(in.event, nil))
				if (err != nil) != in.fail {
					t.Fatalf("settlement error = %v, want failure %t", err, in.fail)
				}
				a.end(protocol.AssistantStreamOutcome{Kind: "abandoned"})
				if !a.ended {
					t.Fatal("attempt did not end")
				}
				expanded, err := protocol.ExpandAssistantStream(a.records)
				if err != nil || !reflect.DeepEqual(expanded, chunks) {
					t.Fatalf("expanded stream = %#v, error = %v", expanded, err)
				}
				blocks, err := acc.Blocks()
				if err != nil || !reflect.DeepEqual(blocks, []protocol.ContentBlock{protocol.TextBlock{Text: in.text}}) {
					t.Fatalf("blocks = %#v, error = %v", blocks, err)
				}
				if got := acc.InterruptedBlocks(); !reflect.DeepEqual(got, in.interrupted) {
					t.Fatalf("interrupted blocks = %#v, want %#v", got, in.interrupted)
				}
				if got := acc.FinishReason(); !reflect.DeepEqual(got, in.finish) {
					t.Fatalf("finish = %#v, want %#v", got, in.finish)
				}
				frames = append(frames, in.frames...)
			}
			if !reflect.DeepEqual(observer.frames, frames) {
				t.Fatalf("frames = %#v, want %#v", observer.frames, frames)
			}
		})
	}
}
