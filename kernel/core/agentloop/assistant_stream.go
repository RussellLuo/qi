package agentloop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// assistantAttempt owns the single timed stream used for assembly, persistence
// and local publication. Compatibility deltas are derived from its chunk frames.
type assistantAttempt struct {
	agent       contracts.AgentRuntime
	id          string
	turn        protocol.TurnID
	step        protocol.StepID
	index       int64
	ended       bool
	records     []protocol.AssistantStreamRecord
	accumulator *llm.Accumulator
	emitter     *eventEmitter
	now         func() int64
}

func newAssistantAttempt(
	agent contracts.AgentRuntime,
	turn protocol.TurnID,
	step protocol.StepID,
	accumulator *llm.Accumulator,
	emitter *eventEmitter,
) *assistantAttempt {
	return &assistantAttempt{
		agent:       agent,
		id:          fmt.Sprintf("%s:%d", agent.SessionID(), agent.NextAssistantAttempt()),
		turn:        turn,
		step:        step,
		accumulator: accumulator,
		emitter:     emitter,
		records:     []protocol.AssistantStreamRecord{},
		now:         func() int64 { return time.Now().UnixMilli() },
	}
}

func (a *assistantAttempt) publish(frame protocol.AssistantStreamFrame) {
	frame.AttemptID = a.id
	frame.Revision = a.agent.NextAssistantStreamRevision()
	event := contracts.AssistantStreamObservation{Agent: a.agent, InstanceID: a.agent.InstanceID(), Frame: frame.Clone()}

	if a.emitter.observer != nil {
		a.emitter.observe("assistant/stream", func() { a.emitter.observer.AssistantStream(event.Clone()) })
	}

	a.emitter.Emit(contracts.DriverEvent{Type: "stream", Stream: &frame})
}

func (a *assistantAttempt) start() {
	a.publish(protocol.AssistantStreamFrame{
		Type: "start",
		Turn: a.turn,
		Step: a.step,
	})
}

func (a *assistantAttempt) consume(stream contracts.ChunkStream) error {
	if stream == nil {
		return errors.New("llm/stream returned a nil stream")
	}

	for chunk, err := range stream {
		if err != nil {
			return err
		}
		if err := a.push(chunk); err != nil {
			return err
		}
		if chunk.Type == protocol.ChunkFinish {
			break
		}
	}

	return nil
}

func (a *assistantAttempt) push(chunk protocol.StreamChunk) error {
	record, err := protocol.NewAssistantStreamRecord(a.now(), chunk)
	if err != nil {
		return err
	}

	a.records = append(a.records, record)
	if err := a.accumulator.Accept(*record.Chunk); err != nil {
		return err
	}

	frame := protocol.AssistantStreamFrame{
		Type:  "chunk",
		Index: a.index,
		Time:  record.Time,
		Chunk: record.Chunk,
	}
	a.index++
	a.publish(frame)

	return nil
}

func (a *assistantAttempt) end(outcome protocol.AssistantStreamOutcome) {
	if a.ended {
		return
	}

	a.ended = true
	a.publish(protocol.AssistantStreamFrame{
		Type:    "end",
		Index:   a.index,
		Outcome: &outcome,
	})
}

func (a *assistantAttempt) settle(ctx context.Context, current session.Session, event protocol.SessionEvent) error {
	seq, err := current.Append(ctx, event)
	if err != nil {
		a.end(protocol.AssistantStreamOutcome{Kind: "abandoned"})
		return err
	}

	a.end(protocol.AssistantStreamOutcome{
		Kind:      "committed",
		EventType: event.Type,
		Seq:       &seq,
	})

	return nil
}

func reportUncommitted(emitter *eventEmitter, run Run, cause error) error {
	emitter.Emit(contracts.DriverEvent{
		Type:      "ended",
		Reason:    durableTurnReason(cause, protocol.ReasonFailed),
		Err:       cause,
		ErrorCode: protocol.ErrorInternal,
	})
	return errors.Join(cause, emitter.Err())
}
