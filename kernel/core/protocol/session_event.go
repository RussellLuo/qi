package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type SessionEventKind string

const (
	TurnStarted         SessionEventKind = "turn/start"
	TurnEnded           SessionEventKind = "turn/end"
	StepStarted         SessionEventKind = "step/start"
	StepEnded           SessionEventKind = "step/end"
	SystemMessage       SessionEventKind = "system/message"
	DeveloperMessage    SessionEventKind = "developer/message"
	UserMessage         SessionEventKind = "user/message"
	AssistantMessage    SessionEventKind = "assistant/message"
	AssistantAttempt    SessionEventKind = "assistant/attempt"
	ToolCalled          SessionEventKind = "tool/call"
	ToolResulted        SessionEventKind = "tool/result"
	RequestHeader       SessionEventKind = "request/header"
	RequestContextEvent SessionEventKind = "request/context"
	InboxSpliced        SessionEventKind = "agent/inbox/spliced"
	SessionEndSeed      SessionEventKind = "session/end-seed"
)

// CompletionReason is the reason used by Agent turn hooks.
// Core TurnEndReason uses the protocol vocabulary below.
type CompletionReason string

const (
	ReasonCompleted CompletionReason = "completed"
	ReasonBlocked   CompletionReason = "blocked"
	ReasonFailed    CompletionReason = "failed"
	ReasonCancelled CompletionReason = "cancelled"
	ReasonMaxSteps  CompletionReason = "max_steps"
	ReasonMaxTokens CompletionReason = "max-tokens"
)

type AgentCancelCause struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

// A hook reason is present even when it is the legal empty string.
func (c AgentCancelCause) MarshalJSON() ([]byte, error) {
	var reason *string
	if c.Kind == "hook" || c.Reason != "" {
		reason = &c.Reason
	}

	return json.Marshal(struct {
		Kind   string  `json:"kind"`
		Reason *string `json:"reason,omitempty"`
	}{c.Kind, reason})
}

type TurnEndReason struct {
	Kind   string            `json:"kind"`
	Reason *AgentCancelCause `json:"reason,omitempty"`
	Error  *LlmFailure       `json:"error,omitempty"`
}

type TurnData struct {
	Turn TurnID `json:"turn"`
}

type TurnEndData struct {
	Turn   TurnID        `json:"turn"`
	Reason TurnEndReason `json:"reason"`
}

type StepData struct {
	Turn TurnID `json:"turn"`
	Step StepID `json:"step"`
}

type MessageData struct {
	Turn      TurnID    `json:"turn"`
	Step      StepID    `json:"step"`
	Message   Message   `json:"message"`
	HeaderSeq *Sequence `json:"headerSeq,omitempty"`
}

type AssistantMessageData struct {
	Turn        TurnID                  `json:"turn"`
	Step        StepID                  `json:"step"`
	Message     Message                 `json:"message"`
	Stream      []AssistantStreamRecord `json:"stream"`
	Usage       *Usage                  `json:"usage,omitempty"`
	Interrupted bool                    `json:"interrupted,omitempty"`
}

type AttemptData struct {
	Turn   TurnID                  `json:"turn"`
	Step   StepID                  `json:"step"`
	Stream []AssistantStreamRecord `json:"stream"`
}

type ToolCallData struct {
	Turn      TurnID     `json:"turn"`
	Step      StepID     `json:"step"`
	CallID    ToolCallID `json:"callId"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
}

type ToolResultData struct {
	Turn    TurnID           `json:"turn"`
	Step    StepID           `json:"step"`
	Message Message          `json:"message"`
	Error   *ToolFailureInfo `json:"error,omitempty"`
	Meta    json.RawMessage  `json:"meta,omitempty"`
}

type InboxSpliceData struct {
	Target       string    `json:"target"`
	Start        int64     `json:"start"`
	RemovedCount *int64    `json:"removedCount,omitempty"`
	Inserted     []Message `json:"inserted"`
	Outcome      string    `json:"outcome,omitempty"`
}

type EndSeedData struct {
	Inherited bool `json:"inherited,omitempty"`
}

// SurfaceOp is either the literal "append" or a positional replacement.
type SurfaceOp struct {
	Op       string   `json:"op"`
	StartSeq Sequence `json:"startSeq"`
	EndSeq   Sequence `json:"endSeq"`
}

func AppendSurface() *SurfaceOp { return &SurfaceOp{Op: "append"} }

func (o SurfaceOp) MarshalJSON() ([]byte, error) {
	if o.Op == "append" {
		return []byte(`"append"`), nil
	}

	type wire SurfaceOp
	return json.Marshal(wire(o))
}

func (o *SurfaceOp) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte(`"append"`)) {
		*o = SurfaceOp{Op: "append"}
		return nil
	}

	type wire SurfaceOp
	var w wire
	if err := sessionObject(b, &w, []string{"op", "startSeq", "endSeq"}, nil); err != nil {
		return err
	}
	if w.Op != "replace" || !safeIndex(int64(w.StartSeq)) || !safeIndex(int64(w.EndSeq)) {
		return fmt.Errorf("%w: invalid surface replace", ErrInvalidProtocol)
	}

	*o = SurfaceOp(w)

	return nil
}

type SessionEvent struct {
	Type            SessionEventKind `json:"type"`
	Seq             Sequence         `json:"seq"`
	Time            int64            `json:"time"`
	Data            json.RawMessage  `json:"data"`
	Ignorable       bool             `json:"ignorable,omitempty"`
	SurfaceOp       *SurfaceOp       `json:"surfaceOp,omitempty"`
	SourceEventSeqs []Sequence       `json:"sourceEventSeqs,omitempty"`
	opaqueSurface   json.RawMessage
	opaqueSources   json.RawMessage
}

func (e *SessionEvent) UnmarshalJSON(b []byte) error {
	type wire SessionEvent
	var fields map[string]json.RawMessage
	if err := sessionObject(b, &fields, []string{"type", "seq", "time", "data"}, []string{"ignorable", "surfaceOp", "sourceEventSeqs"}); err != nil {
		return err
	}

	var kind SessionEventKind
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return err
	}
	if raw, ok := fields["ignorable"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return invalid("ignorable must be true")
	}

	candidate := SessionEvent{Type: kind}
	if candidate.IsKnown() {
		for _, key := range []string{"surfaceOp", "sourceEventSeqs"} {
			if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
				return invalid("invalid field " + key)
			}
		}
	}

	var opaqueSurface, opaqueSources json.RawMessage
	if !candidate.IsKnown() {
		opaqueSurface = fields["surfaceOp"]
		opaqueSources = fields["sourceEventSeqs"]
		delete(fields, "surfaceOp")
		delete(fields, "sourceEventSeqs")
		b, _ = json.Marshal(fields)
	}

	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	for _, r := range w.SourceEventSeqs {
		if !safeIndex(int64(r)) {
			return invalid("invalid source seq")
		}
	}

	if raw := fields["sourceEventSeqs"]; raw != nil {
		var refs []json.RawMessage
		if err := json.Unmarshal(raw, &refs); err != nil {
			return err
		}

		for _, r := range refs {
			if bytes.Equal(bytes.TrimSpace(r), []byte("null")) || bytes.Equal(bytes.TrimSpace(r), []byte("-0")) {
				return invalid("invalid source seq")
			}
		}
	}

	v := SessionEvent(w)
	v.opaqueSurface = bytes.Clone(opaqueSurface)
	v.opaqueSources = bytes.Clone(opaqueSources)
	if err := v.Validate(); err != nil {
		return err
	}

	*e = v

	return nil
}

func (e SessionEvent) MarshalJSON() ([]byte, error) {
	type wire SessionEvent
	b, err := json.Marshal(wire(e))
	if err != nil {
		return nil, err
	}
	if e.opaqueSurface == nil && e.opaqueSources == nil {
		return b, nil
	}

	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	if e.opaqueSurface != nil {
		fields["surfaceOp"] = e.opaqueSurface
	}

	if e.opaqueSources != nil {
		fields["sourceEventSeqs"] = e.opaqueSources
	}

	return json.Marshal(fields)
}

func (e SessionEvent) Clone() SessionEvent {
	e.Data = bytes.Clone(e.Data)
	e.opaqueSurface = bytes.Clone(e.opaqueSurface)
	e.opaqueSources = bytes.Clone(e.opaqueSources)
	if e.SurfaceOp != nil {
		o := *e.SurfaceOp
		e.SurfaceOp = &o
	}

	if e.SourceEventSeqs != nil {
		e.SourceEventSeqs = append([]Sequence{}, e.SourceEventSeqs...)
	}

	return e
}

// EventPayload reads already validated event data inside a fold or consumer.
// Use DecodeSessionEvent/Validate before extracting untrusted JSON.
func EventPayload[T any](e SessionEvent) T {
	var v T
	_ = json.Unmarshal(e.Data, &v)
	return v
}

// NewSessionEvent leaves sequence allocation to Append, whose input seq is ignored.
func NewSessionEvent(kind SessionEventKind, data any) SessionEvent {
	b, err := json.Marshal(data)
	if err != nil {
		b = nil
	}

	return SessionEvent{
		Type: kind,
		Time: time.Now().UnixMilli(),
		Data: b,
	}
}

func NewTurnStarted(turn TurnID) SessionEvent { return NewSessionEvent(TurnStarted, TurnData{turn}) }

func NewTurnEnded(turn TurnID, reason TurnEndReason) SessionEvent {
	return NewSessionEvent(TurnEnded, TurnEndData{turn, reason})
}

func NewStepStarted(turn TurnID, step StepID) SessionEvent {
	return NewSessionEvent(StepStarted, StepData{turn, step})
}

func NewStepEnded(turn TurnID, step StepID) SessionEvent {
	return NewSessionEvent(StepEnded, StepData{turn, step})
}

func NewMessageEvent(kind SessionEventKind, turn TurnID, step StepID, message Message) SessionEvent {
	var data any = MessageData{
		Turn:    turn,
		Step:    step,
		Message: message,
	}
	if kind == UserMessage {
		data = message
	}

	e := NewSessionEvent(kind, data)
	e.SurfaceOp = AppendSurface()

	return e
}

func NewAssistantMessageEvent(turn TurnID, step StepID, message Message, stream []AssistantStreamRecord, usage *Usage) SessionEvent {
	e := NewSessionEvent(AssistantMessage, AssistantMessageData{
		Turn:    turn,
		Step:    step,
		Message: message,
		Stream:  stream,
		Usage:   usage,
	})
	e.SurfaceOp = AppendSurface()

	return e
}

func NewInterruptedAssistantMessageEvent(
	turn TurnID,
	step StepID,
	message Message,
	stream []AssistantStreamRecord,
	usage *Usage,
) SessionEvent {
	e := NewSessionEvent(AssistantMessage, AssistantMessageData{
		Turn:        turn,
		Step:        step,
		Message:     message,
		Stream:      stream,
		Usage:       usage,
		Interrupted: true,
	})
	e.SurfaceOp = AppendSurface()

	return e
}

func NewAttemptEvent(turn TurnID, step StepID, stream []AssistantStreamRecord) SessionEvent {
	return NewSessionEvent(AssistantAttempt, AttemptData{turn, step, stream})
}

func NewToolCallEvent(turn TurnID, step StepID, call ToolCall) SessionEvent {
	return NewSessionEvent(ToolCalled, ToolCallData{turn, step, call.ID, call.Name, call.Arguments})
}

func NewToolResultEvent(turn TurnID, step StepID, result ToolResult, callSeq Sequence) SessionEvent {
	e := NewSessionEvent(ToolResulted, ToolResultData{turn, step, result.Message, result.Error, result.Meta})
	e.SurfaceOp = AppendSurface()
	e.SourceEventSeqs = []Sequence{callSeq}
	return e
}
