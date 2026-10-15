package headless

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestReasoningSegmentsFollowNativeFrames(t *testing.T) {
	var out bytes.Buffer
	r := reasoning{writer: &out, newline: true}
	chunk := func(c protocol.StreamChunk) { r.frame(protocol.AssistantStreamFrame{Type: "chunk", Chunk: &c}) }
	r.frame(protocol.AssistantStreamFrame{Type: "start"})
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: ""})
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "think"})
	chunk(protocol.StreamChunk{Type: protocol.ChunkUsage})
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: " more"})
	chunk(protocol.StreamChunk{Type: protocol.ChunkBlockEnd, Block: protocol.ReasoningBlock{Text: "think more"}})
	chunk(protocol.StreamChunk{Type: protocol.ChunkBlockStart, BlockType: protocol.BlockText})
	chunk(protocol.StreamChunk{Type: protocol.ChunkTextDelta, Text: "answer"})
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "again\n"})
	r.frame(protocol.AssistantStreamFrame{Type: "end"})
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "unterminated"})
	if err := r.finish(); err != nil {
		t.Fatal(err)
	}
	chunk(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "late"})
	want := "qi: reasoning:\nthink more\nqi: reasoning:\nagain\nqi: reasoning:\nunterminated\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }
func TestReasoningWriteFailureCancelsExplicitly(t *testing.T) {
	failure := errors.New("broken stderr")
	canceled := false
	r := reasoning{writer: failedWriter{failure}, cancel: func() { canceled = true }}
	c := protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "x"}
	r.frame(protocol.AssistantStreamFrame{Type: "chunk", Chunk: &c})
	if err := r.finish(); !errors.Is(err, failure) || !canceled {
		t.Fatalf("failure=%v canceled=%t", err, canceled)
	}
}

var _ io.Writer = failedWriter{}

func TestNoReasoningKeepsStderrEmpty(t *testing.T) {
	var out bytes.Buffer
	r := reasoning{writer: &out}
	if err := r.finish(); err != nil || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
}
