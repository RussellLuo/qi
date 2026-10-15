package gordisbridge

import (
	"errors"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type streamAgent struct{ contracts.AssistantStreamState }

func (*streamAgent) SessionID() protocol.SessionID { return "s" }

func TestBoundedDriverObservationPreservesRawFactAndError(t *testing.T) {
	d := &EventDispatcher{queue: make(chan observation, 1)}
	original := errors.New("local Driver failure")
	event := contracts.DriverEvent{
		Type:         "ended",
		SessionID:    "s",
		InvocationID: "invocation",
		Reason: protocol.TurnEndReason{
			Kind:   "aborted",
			Reason: &protocol.AgentCancelCause{Kind: "user"},
		},
		Err:       original,
		ErrorCode: protocol.ErrorCancelled,
	}
	d.DriverEvent(event)
	event.Reason.Reason.Kind = "mutated"
	d.DriverEvent(event)

	queued := <-d.queue
	if queued.driver == nil ||
		queued.driver.Type != "ended" ||
		queued.driver.SessionID != "s" ||
		queued.driver.InvocationID != "invocation" ||
		queued.driver.Reason.Reason.Kind != "user" ||
		queued.driver.Err != original ||
		queued.driver.ErrorCode != protocol.ErrorCancelled ||
		d.Dropped() != 1 || len(d.queue) != 0 {
		t.Fatal(queued.driver, d.Dropped())
	}
}

func TestBoundedFrameObservationPreservesReferenceAndSnapshot(t *testing.T) {
	if AssistantStreamTopic.ID() != "agent/assistant-stream" || DriverEventTopic.ID() != "agent/driver" {
		t.Fatal("raw observation topics")
	}

	d := &EventDispatcher{queue: make(chan observation, 1)}
	agent := new(streamAgent)
	chunk := protocol.StreamChunk{
		Type:  protocol.ChunkTextDelta,
		Index: 37,
		Text:  "original",
	}
	event := contracts.AssistantStreamObservation{
		Agent: agent,
		Frame: protocol.AssistantStreamFrame{
			Type:      "chunk",
			AttemptID: "s:1",
			Revision:  1,
			Index:     0,
			Time:      100,
			Chunk:     &chunk,
		},
	}
	d.AssistantStream(event)
	chunk.Text = "mutated"

	for i := 0; i < 100; i++ {
		d.AssistantStream(event)
	}

	if d.Dropped() != 100 {
		t.Fatalf("dropped=%d", d.Dropped())
	}

	queued := <-d.queue
	if queued.stream.Agent != agent || queued.stream.Frame.Chunk.Text != "original" {
		t.Fatal("reference/snapshot changed")
	}
}

func TestErrorTopicPreservesLocalErrorAndAgent(t *testing.T) {
	if AgentErrorTopic.ID() != "agent/error" {
		t.Fatal(AgentErrorTopic.ID())
	}

	d := &EventDispatcher{queue: make(chan observation, 1)}
	a := new(streamAgent)
	err := errors.New("local sentinel")
	d.AgentError(contracts.AgentErrorObservation{
		Agent: a,
		Turn:  2,
		Step:  3,
		Error: err,
	})
	d.AgentError(contracts.AgentErrorObservation{Agent: a, Error: err})
	e := <-d.queue
	if e.error == nil ||
		e.error.Agent != a ||
		e.error.Error != err ||
		e.error.Turn != 2 ||
		e.error.Step != 3 ||
		d.Dropped() != 1 {
		t.Fatal(e.error, d.Dropped())
	}
}
