package headless

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type observe func(func(context.Context, contracts.AssistantStreamObservation) error) (func(), error)

type reasoning struct {
	mu                    sync.Mutex
	writer                io.Writer
	open, newline, closed bool
	err                   error
	cancel                func()
}

func (r *reasoning) write(text string) {
	if r.err != nil {
		return
	}
	n, err := io.WriteString(r.writer, text)
	if err == nil && n != len(text) {
		err = io.ErrShortWrite
	}
	if err != nil {
		r.err = err
		if r.cancel != nil {
			r.cancel()
		}
	}
}
func (r *reasoning) closeLine() {
	if !r.open {
		return
	}
	if !r.newline {
		r.write("\n")
	}
	r.open = false
	r.newline = true
}
func (r *reasoning) frame(f protocol.AssistantStreamFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if f.Type == "start" || f.Type == "end" {
		r.closeLine()
		return
	}
	if f.Chunk == nil {
		return
	}
	c := f.Chunk
	switch c.Type {
	case protocol.ChunkReasoningDelta:
		if c.Text == "" {
			return
		}
		if !r.open {
			r.write("qi: reasoning:\n")
			r.open = true
		}
		r.write(c.Text)
		r.newline = strings.HasSuffix(c.Text, "\n")
	case protocol.ChunkBlockStart:
		if c.BlockType != "reasoning" {
			r.closeLine()
		}
	case protocol.ChunkBlockEnd:
		if c.Block != nil && c.Block.Kind() != "reasoning" {
			r.closeLine()
		}
	case protocol.ChunkUsage:
	default:
		r.closeLine()
	}
}
func (r *reasoning) finish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLine()
	r.closed = true
	return r.err
}
