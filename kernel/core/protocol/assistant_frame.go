package protocol

import (
	"encoding/json"
	"fmt"
)

// AssistantStreamFrame is local attempt framing, distinct from model block
// indexes and Session seq. Only committed outcomes carry a settlement seq.
type AssistantStreamFrame struct {
	Type      string
	AttemptID string
	Revision  int64
	Turn      TurnID
	Step      StepID
	Index     int64
	Time      int64
	Chunk     *StreamChunk
	Outcome   *AssistantStreamOutcome
}

type AssistantStreamOutcome struct {
	Kind      string
	EventType SessionEventKind
	Seq       *Sequence
}

func (o AssistantStreamOutcome) Validate() error {
	if o.Kind == "abandoned" && o.EventType == "" && o.Seq == nil {
		return nil
	}
	if o.Kind == "committed" &&
		(o.EventType == AssistantMessage || o.EventType == AssistantAttempt) &&
		o.Seq != nil &&
		safeIndex(int64(*o.Seq)) {
		return nil
	}

	return fmt.Errorf("%w: invalid assistant stream outcome", ErrInvalidProtocol)
}

func (o AssistantStreamOutcome) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}

	w := map[string]any{"kind": o.Kind}
	if o.Kind == "committed" {
		w["eventType"], w["seq"] = o.EventType, *o.Seq
	}

	return json.Marshal(w)
}

func (o *AssistantStreamOutcome) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	var w struct {
		Kind      string           `json:"kind"`
		EventType SessionEventKind `json:"eventType"`
		Seq       *Sequence        `json:"seq"`
	}
	required := []string{"kind"}
	if tag.Kind == "committed" {
		required = append(required, "eventType", "seq")
	}

	if err := strictObject(data, &w, required, nil); err != nil {
		return err
	}

	v := AssistantStreamOutcome{w.Kind, w.EventType, w.Seq}
	if err := v.Validate(); err != nil {
		return err
	}

	*o = v

	return nil
}

func (f AssistantStreamFrame) Validate() error {
	if f.AttemptID == "" || !safeIndex(f.Revision) || f.Revision == 0 {
		return fmt.Errorf("%w: frame requires attemptId and positive revision", ErrInvalidProtocol)
	}

	switch f.Type {
	case "start":
		if f.Turn <= 0 ||
			f.Step <= 0 ||
			!safeIndex(int64(f.Turn)) ||
			!safeIndex(int64(f.Step)) ||
			f.Chunk != nil ||
			f.Outcome != nil ||
			f.Index != 0 ||
			f.Time != 0 {
			break
		}
		return nil
	case "chunk":
		if f.Turn != 0 || f.Step != 0 || !safeIndex(f.Index) || !safeInteger(f.Time) || f.Chunk == nil || f.Outcome != nil {
			break
		}
		return f.Chunk.Validate()
	case "end":
		if f.Turn != 0 || f.Step != 0 || !safeIndex(f.Index) || f.Time != 0 || f.Chunk != nil || f.Outcome == nil {
			break
		}
		return f.Outcome.Validate()
	}

	return fmt.Errorf("%w: invalid assistant stream frame", ErrInvalidProtocol)
}

func (f AssistantStreamFrame) MarshalJSON() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}

	w := map[string]any{
		"type":      f.Type,
		"attemptId": f.AttemptID,
		"revision":  f.Revision,
	}

	switch f.Type {
	case "start":
		w["turn"], w["step"] = f.Turn, f.Step
	case "chunk":
		w["index"], w["time"], w["chunk"] = f.Index, f.Time, f.Chunk
	case "end":
		w["index"], w["outcome"] = f.Index, f.Outcome
	}

	return json.Marshal(w)
}

func (f *AssistantStreamFrame) UnmarshalJSON(data []byte) error {
	var w struct {
		Type      string                  `json:"type"`
		AttemptID string                  `json:"attemptId"`
		Revision  int64                   `json:"revision"`
		Turn      TurnID                  `json:"turn"`
		Step      StepID                  `json:"step"`
		Index     int64                   `json:"index"`
		Time      int64                   `json:"time"`
		Chunk     *StreamChunk            `json:"chunk"`
		Outcome   *AssistantStreamOutcome `json:"outcome"`
	}
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	required := []string{"type", "attemptId", "revision"}

	switch tag.Type {
	case "start":
		required = append(required, "turn", "step")
	case "chunk":
		required = append(required, "index", "time", "chunk")
	case "end":
		required = append(required, "index", "outcome")
	default:
		return fmt.Errorf("%w: unknown frame type", ErrInvalidProtocol)
	}

	if err := strictObject(data, &w, required, nil); err != nil {
		return err
	}

	v := AssistantStreamFrame{w.Type, w.AttemptID, w.Revision, w.Turn, w.Step, w.Index, w.Time, w.Chunk, w.Outcome}
	if err := v.Validate(); err != nil {
		return err
	}

	*f = v

	return nil
}

// Clone detaches protocol data while local observers retain the Agent reference.
func (f AssistantStreamFrame) Clone() AssistantStreamFrame {
	if f.Chunk != nil {
		v, err := f.Chunk.Snapshot()
		if err != nil {
			panic(err)
		}

		f.Chunk = &v
	}

	if f.Outcome != nil {
		v := *f.Outcome
		if v.Seq != nil {
			seq := *v.Seq
			v.Seq = &seq
		}

		f.Outcome = &v
	}

	return f
}
