package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type Accumulated struct {
	Message protocol.Message
	Finish  protocol.FinishReason
	Usage   *protocol.Usage
}

type partialBlock struct {
	kind      protocol.BlockKind
	text      strings.Builder
	id        protocol.ToolCallID
	name      string
	arguments strings.Builder
	block     protocol.ContentBlock
}

type Accumulator struct {
	messageID protocol.MessageID
	source    protocol.MessageSource
	partials  map[int64]*partialBlock
	order     []int64
	finished  bool
	finish    protocol.FinishReason
	usage     *protocol.Usage
	replay    *protocol.ReplayEnvelope
}

func NewAccumulator(messageID protocol.MessageID, source protocol.MessageSource) (*Accumulator, error) {
	if messageID == "" {
		return nil, errors.New("assistant message id is required")
	}

	return &Accumulator{
		messageID: messageID,
		source:    source.Clone(),
		partials:  map[int64]*partialBlock{},
	}, nil
}

func (a *Accumulator) ensure(index int64, kind protocol.BlockKind) *partialBlock {
	if p := a.partials[index]; p != nil {
		return p
	}

	p := &partialBlock{kind: kind}
	a.partials[index] = p
	a.order = append(a.order, index)

	return p
}

func (a *Accumulator) Accept(chunk protocol.StreamChunk) error {
	if a == nil {
		return errors.New("accumulator is nil")
	}
	if a.finished {
		return errors.New("chunk after finish")
	}

	snapshot, err := chunk.Snapshot()
	if err != nil {
		return fmt.Errorf("invalid stream chunk: %w", err)
	}

	chunk = snapshot

	switch chunk.Type {
	case protocol.ChunkBlockStart:
		a.ensure(chunk.Index, chunk.BlockType)
	case protocol.ChunkTextDelta, protocol.ChunkReasoningDelta:
		kind := protocol.BlockText
		if chunk.Type == protocol.ChunkReasoningDelta {
			kind = protocol.BlockReasoning
		}
		p := a.ensure(chunk.Index, kind)
		if p.block == nil {
			p.text.WriteString(chunk.Text)
		}
	case protocol.ChunkToolCallDelta:
		p := a.ensure(chunk.Index, protocol.BlockToolCall)
		if p.block == nil {
			p.id = chunk.ID
			if chunk.Name != nil && *chunk.Name != "" {
				p.name = *chunk.Name
			}

			p.arguments.WriteString(chunk.ArgumentsDelta)
		}
	case protocol.ChunkBlockEnd:
		p := a.ensure(chunk.Index, chunk.Block.Kind())
		if p.block == nil {
			p.block = protocol.CloneContent([]protocol.ContentBlock{chunk.Block})[0]
		}
	case protocol.ChunkUsage:
		a.usage = chunk.Usage
	case protocol.ChunkFinish:
		a.finished = true
		a.finish = chunk.Reason
		a.replay = chunk.ReplayState
	}

	return nil
}

func assemble(p *partialBlock, index int64) (protocol.ContentBlock, error) {
	if p.block != nil {
		return protocol.CloneContent([]protocol.ContentBlock{p.block})[0], nil
	}

	switch p.kind {
	case protocol.BlockText:
		return protocol.TextBlock{Text: p.text.String()}, nil
	case protocol.BlockReasoning:
		return protocol.ReasoningBlock{Text: p.text.String()}, nil
	case protocol.BlockToolCall:
		id := p.id
		if id == "" {
			id = protocol.ToolCallID(fmt.Sprintf("call-%d", index))
		}
		return protocol.ToolCallBlock{
			ToolCall: protocol.ToolCall{
				ID:        id,
				Name:      p.name,
				Arguments: p.arguments.String(),
			},
		}, nil
	default:
		return nil, fmt.Errorf("cannot assemble incomplete block of type %q", p.kind)
	}
}

// FinishReason defaults to stop without adding a synthetic raw chunk.
func (a *Accumulator) FinishReason() protocol.FinishReason {
	if a == nil || !a.finished {
		return protocol.FinishReason{Kind: protocol.FinishStop}
	}

	r := a.finish
	if r.Failure != nil {
		data, _ := json.Marshal(r)
		_ = json.Unmarshal(data, &r)
	}

	return r
}

func (a *Accumulator) assembled() ([]protocol.ContentBlock, *protocol.ReplayEnvelope, error) {
	blocks := make([]protocol.ContentBlock, 0, len(a.order))
	kept := make([]int, 0, len(a.order))

	for pos, index := range a.order {
		block, err := assemble(a.partials[index], index)
		if err != nil {
			return nil, nil, err
		}
		if a.FinishReason().Kind == protocol.FinishMaxTokens && block.Kind() == protocol.BlockToolCall {
			continue
		}

		blocks = append(blocks, block)
		kept = append(kept, pos)
	}

	replay := a.replay
	if replay != nil && replay.Blocks != nil {
		if len(replay.Blocks) != len(a.order) {
			replay = nil
		} else {
			entries := make([]any, 0, len(kept))

			for _, i := range kept {
				entries = append(entries, replay.Blocks[i])
			}

			replay = &protocol.ReplayEnvelope{Response: replay.Response, Blocks: entries}
		}
	}

	if replay != nil {
		data, err := json.Marshal(replay)
		if err != nil {
			return nil, nil, err
		}

		var cloned protocol.ReplayEnvelope
		if err = json.Unmarshal(data, &cloned); err != nil {
			return nil, nil, err
		}

		replay = &cloned
	}

	return blocks, replay, nil
}

func (a *Accumulator) Blocks() ([]protocol.ContentBlock, error) {
	b, _, err := a.assembled()
	return b, err
}

func (a *Accumulator) ReplayState() (*protocol.ReplayEnvelope, error) {
	_, r, err := a.assembled()
	return r, err
}

func (a *Accumulator) Usage() *protocol.Usage {
	if a.usage == nil {
		return nil
	}

	data, _ := json.Marshal(a.usage)
	var u protocol.Usage
	_ = json.Unmarshal(data, &u)

	return &u
}

func (a *Accumulator) InterruptedBlocks() []protocol.ContentBlock {
	blocks := make([]protocol.ContentBlock, 0)

	for _, index := range a.order {
		p := a.partials[index]
		kind := p.kind
		if p.block != nil {
			kind = p.block.Kind()
		}

		if kind != protocol.BlockText && kind != protocol.BlockReasoning {
			continue
		}

		b, err := assemble(p, index)
		if err != nil {
			continue
		}

		text := ""

		switch v := b.(type) {
		case protocol.TextBlock:
			text = v.Text
		case protocol.ReasoningBlock:
			text = v.Text
		}

		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, b)
		}
	}

	return blocks
}

func (a *Accumulator) Finish() (Accumulated, error) {
	if a == nil {
		return Accumulated{}, errors.New("accumulator is nil")
	}

	reason := a.FinishReason()
	if reason.Kind == protocol.FinishError || reason.Kind == protocol.FinishAborted {
		return Accumulated{}, &StreamFailure{Reason: reason}
	}

	blocks, replay, err := a.assembled()
	if err != nil {
		return Accumulated{}, err
	}

	source := a.source.Clone()
	source.ReplayState = nil
	if replay != nil {
		source.ReplayState, err = json.Marshal(replay)
		if err != nil {
			return Accumulated{}, err
		}
	}

	message := protocol.Message{
		ID:      a.messageID,
		Role:    protocol.RoleAssistant,
		Source:  source,
		Content: blocks,
	}
	if err := message.Validate(); err != nil {
		return Accumulated{}, err
	}

	return Accumulated{
		Message: message,
		Finish:  reason,
		Usage:   a.Usage(),
	}, nil
}

// StreamFailure preserves the structured terminal failure while remaining an error.
type StreamFailure struct{ Reason protocol.FinishReason }

func (e *StreamFailure) Error() string { return e.Reason.Failure.Message }

func (a *Accumulator) Attempt() *protocol.Message {
	if a == nil {
		return nil
	}

	blocks := a.InterruptedBlocks()
	if len(blocks) == 0 {
		return nil
	}

	source := a.source.Clone()
	source.ReplayState = nil
	message := protocol.Message{
		ID:      a.messageID,
		Role:    protocol.RoleAssistant,
		Source:  source,
		Content: blocks,
	}
	if message.Validate() != nil {
		return nil
	}

	return &message
}

func Accumulate(
	messageID protocol.MessageID,
	stream iter.Seq2[protocol.StreamChunk, error],
	source protocol.MessageSource,
) (Accumulated, error) {
	a, err := NewAccumulator(messageID, source)
	if err != nil {
		return Accumulated{}, err
	}

	for chunk, err := range stream {
		if err != nil {
			return Accumulated{}, err
		}
		if err = a.Accept(chunk); err != nil {
			return Accumulated{}, err
		}
	}

	return a.Finish()
}
