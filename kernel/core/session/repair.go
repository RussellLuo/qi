package session

import (
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

const (
	toolNotStartedText     = "The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."
	toolOutcomeUnknownText = "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
)

// InterruptedTurnClosers validates a durable prefix and returns deterministic
// missing tool outcomes and Step/Turn closers. It never executes a tool or
// reconstructs an uncommitted assistant stream or claimed input.
func InterruptedTurnClosers(events []protocol.SessionEvent) ([]protocol.SessionEvent, error) {
	if _, err := Fold(events); err != nil {
		return nil, err
	}

	type pendingCall struct {
		id      protocol.ToolCallID
		turn    protocol.TurnID
		step    protocol.StepID
		callSeq *protocol.Sequence
	}
	state := logState{}
	pending := []pendingCall{}

	for _, event := range events {
		// The pure recovery fold also accepts format-valid projection slices
		// without execution boundaries. Store loading/Append independently
		// enforce the full execution state machine before admitting a prefix.
		if event.Type == protocol.TurnStarted ||
			event.Type == protocol.TurnEnded ||
			event.Type == protocol.StepStarted ||
			event.Type == protocol.StepEnded ||
			event.Type == protocol.SessionEndSeed {
			if err := state.apply(event); err != nil {
				return nil, err
			}
		}

		switch event.Type {
		case protocol.TurnStarted, protocol.TurnEnded, protocol.StepEnded:
			pending = nil
		case protocol.AssistantMessage:
			d := protocol.EventPayload[protocol.AssistantMessageData](event)
			for _, block := range d.Message.Content {
				b, ok := block.(protocol.ToolCallBlock)
				if !ok {
					continue
				}

				call := b.ToolCall
				entry := pendingCall{
					id:   call.ID,
					turn: d.Turn,
					step: d.Step,
				}
				found := false

				for i := range pending {
					if pending[i].id == call.ID {
						pending[i] = entry
						found = true
						break
					}
				}

				if !found {
					pending = append(pending, entry)
				}
			}
		case protocol.ToolCalled:
			d := protocol.EventPayload[protocol.ToolCallData](event)
			for i := range pending {
				if pending[i].id == d.CallID {
					seq := event.Seq
					pending[i].callSeq = &seq
				}
			}
		case protocol.ToolResulted:
			d := protocol.EventPayload[protocol.ToolResultData](event)
			if event.SurfaceOp.Op == "append" {
				for i, call := range pending {
					if call.id == d.Message.Source.CallID && call.turn == d.Turn && call.step == d.Step {
						pending = append(pending[:i], pending[i+1:]...)
						break
					}
				}
			}
		}
	}

	closers := []protocol.SessionEvent{}
	if state.turn == 0 {
		return closers, nil
	}

	last := events[len(events)-1]
	add := func(event protocol.SessionEvent) {
		event.Seq = last.Seq + protocol.Sequence(len(closers)) + 1
		event.Time = last.Time
		closers = append(closers, event)
	}

	for _, call := range pending {
		seq := last.Seq + protocol.Sequence(len(closers)) + 1
		text, name, code := toolNotStartedText, "ToolNotStartedError", "TOOL_NOT_STARTED"
		if call.callSeq != nil {
			text, name, code = toolOutcomeUnknownText, "ToolOutcomeUnknownError", "TOOL_OUTCOME_UNKNOWN"
		}

		message := protocol.NewTextMessage(
			protocol.MessageID(fmt.Sprintf("interrupted-tool-result-%s-%d", call.id, seq)),
			protocol.RoleTool,
			protocol.SourceTool,
			text,
		)
		message.Source.CallID = call.id
		message.ToolCallID = call.id
		isError := true
		message.IsError = &isError
		event := protocol.NewSessionEvent(protocol.ToolResulted, protocol.ToolResultData{
			Turn:    call.turn,
			Step:    call.step,
			Message: message,
			Error:   &protocol.ToolFailureInfo{Name: name, Code: code},
		})
		event.SurfaceOp = protocol.AppendSurface()
		if call.callSeq != nil {
			event.SourceEventSeqs = []protocol.Sequence{*call.callSeq}
		}

		add(event)
	}

	if state.step != 0 {
		add(protocol.NewStepEnded(state.turn, state.step))
	}

	add(protocol.NewTurnEnded(state.turn, protocol.TurnEndReason{Kind: "interrupted"}))
	// Check the generated suffix at the same strict boundary as ordinary writes.
	for _, event := range closers {
		if err := state.apply(event); err != nil {
			return nil, err
		}
	}

	if _, err := Fold(append(cloneEvents(events), closers...)); err != nil {
		return nil, err
	}

	return closers, nil
}

// recoverPrefix is called only under exclusive ownership, before publication
// (JSONL construction) or while the Session mutex excludes Attach/Append/Run.
// Every closer commits individually: a failed recovery retains only its real
// committed prefix, and the next recovery recomputes the remaining suffix.
func recoverPrefix(events []protocol.SessionEvent, appendEvent func(protocol.SessionEvent) error, marker bool) error {
	closers, err := InterruptedTurnClosers(events)
	if err != nil {
		return err
	}

	for _, event := range closers {
		if err := appendEvent(event); err != nil {
			return err
		}
	}

	if len(closers) > 0 || (marker && (len(events) == 0 || events[len(events)-1].Type != protocol.SessionEndSeed)) {
		return appendEvent(protocol.NewSessionEvent(protocol.SessionEndSeed, protocol.EndSeedData{}))
	}

	return nil
}
