package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type AssistantStreamRecordType string

const (
	RecordChunk           AssistantStreamRecordType = "chunk"
	RecordTextChunks      AssistantStreamRecordType = "text-chunks"
	RecordReasoningChunks AssistantStreamRecordType = "reasoning-chunks"
	RecordToolCallChunks  AssistantStreamRecordType = "tool-call-chunks"
)

type TimedStreamChunk struct {
	Time  int64       `json:"time"`
	Chunk StreamChunk `json:"chunk"`
}

type AssistantStreamRecord struct {
	Type  AssistantStreamRecordType
	Time  int64
	Chunk *StreamChunk
	Time0 int64
	Index int64
	DT    []int64
	Texts []string
	ID    ToolCallID
	Name  *string
	Args  []string
}

func (r AssistantStreamRecord) Validate() error {
	if r.Type == RecordChunk {
		if !safeInteger(r.Time) || r.Chunk == nil {
			return fmt.Errorf("%w: raw record requires safe integer time and chunk", ErrInvalidProtocol)
		}
		if r.Time0 != 0 || r.Index != 0 || r.DT != nil || r.Texts != nil || r.ID != "" || r.Name != nil || r.Args != nil {
			return fmt.Errorf("%w: conflicting raw record", ErrInvalidProtocol)
		}

		return r.Chunk.Validate()
	}

	if r.Time != 0 || r.Chunk != nil {
		return fmt.Errorf("%w: conflicting packed record", ErrInvalidProtocol)
	}

	var members int

	switch r.Type {
	case RecordTextChunks, RecordReasoningChunks:
		members = len(r.Texts)
		if r.ID != "" || r.Name != nil || r.Args != nil {
			return fmt.Errorf("%w: conflicting text run", ErrInvalidProtocol)
		}
	case RecordToolCallChunks:
		members = len(r.Args)
		if r.Texts != nil || r.ID == "" || (r.Name != nil && *r.Name == "") {
			return fmt.Errorf("%w: invalid tool run", ErrInvalidProtocol)
		}
	default:
		return fmt.Errorf("%w: unknown record type %q", ErrInvalidProtocol, r.Type)
	}

	if !safeInteger(r.Time0) || !safeIndex(r.Index) || r.DT == nil || members == 0 || len(r.DT) != members-1 {
		return fmt.Errorf("%w: run requires safe time/index, non-empty fragments and matching dt", ErrInvalidProtocol)
	}

	time := r.Time0

	for _, gap := range r.DT {
		if !safeInteger(gap) {
			return fmt.Errorf("%w: dt must be safe integer", ErrInvalidProtocol)
		}

		time += gap
		if !safeInteger(time) {
			return fmt.Errorf("%w: reconstructed time must be safe integer", ErrInvalidProtocol)
		}
	}

	return nil
}

func (r AssistantStreamRecord) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}

	w := map[string]any{"type": r.Type}
	if r.Type == RecordChunk {
		w["time"] = r.Time
		w["chunk"] = r.Chunk
	} else {
		w["time0"] = r.Time0
		w["index"] = r.Index
		w["dt"] = r.DT
		if r.Type == RecordToolCallChunks {
			w["id"] = r.ID
			w["args"] = r.Args
			if r.Name != nil {
				w["name"] = *r.Name
			}
		} else {
			w["texts"] = r.Texts
		}
	}

	return json.Marshal(w)
}

func (r *AssistantStreamRecord) UnmarshalJSON(data []byte) error {
	var w struct {
		Type  AssistantStreamRecordType `json:"type"`
		Time  int64                     `json:"time"`
		Chunk *StreamChunk              `json:"chunk"`
		Time0 int64                     `json:"time0"`
		Index int64                     `json:"index"`
		DT    []int64                   `json:"dt"`
		Texts []string                  `json:"texts"`
		ID    ToolCallID                `json:"id"`
		Name  *string                   `json:"name"`
		Args  []string                  `json:"args"`
	}
	var tag struct {
		Type AssistantStreamRecordType `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	required := []string{"type"}
	var optional []string

	switch tag.Type {
	case RecordChunk:
		required = append(required, "time", "chunk")
	case RecordTextChunks, RecordReasoningChunks:
		required = append(required, "time0", "index", "dt", "texts")
	case RecordToolCallChunks:
		required = append(required, "time0", "index", "dt", "id", "args")
		optional = []string{"name"}
	default:
		return fmt.Errorf("%w: unknown record type", ErrInvalidProtocol)
	}

	if err := strictObject(data, &w, required, optional); err != nil {
		return err
	}
	if w.Type != RecordChunk {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}

		for _, key := range []string{"dt", "texts", "args"} {
			raw, ok := fields[key]
			if !ok {
				continue
			}

			var members []json.RawMessage
			if err := json.Unmarshal(raw, &members); err != nil {
				return err
			}

			for _, member := range members {
				member = bytes.TrimSpace(member)
				if bytes.Equal(member, []byte("null")) || ((key == "texts" || key == "args") && (len(member) == 0 || member[0] != '"')) {
					return fmt.Errorf("%w: invalid %s member", ErrInvalidProtocol, key)
				}
			}
		}
	}

	v := AssistantStreamRecord{
		Type:  w.Type,
		Time:  w.Time,
		Chunk: w.Chunk,
		Time0: w.Time0,
		Index: w.Index,
		DT:    w.DT,
		Texts: w.Texts,
		ID:    w.ID,
		Name:  w.Name,
		Args:  w.Args,
	}
	if err := v.Validate(); err != nil {
		return err
	}

	*r = v

	return nil
}

// NewAssistantStreamRecord writes an uncompressed, detached durable record.
func NewAssistantStreamRecord(time int64, chunk StreamChunk) (AssistantStreamRecord, error) {
	snapshot, err := chunk.Snapshot()
	if err != nil {
		return AssistantStreamRecord{}, err
	}

	r := AssistantStreamRecord{
		Type:  RecordChunk,
		Time:  time,
		Chunk: &snapshot,
	}

	return r, r.Validate()
}

// ExpandAssistantStream preserves every fragment boundary, optional name and
// adjacent timestamp gap, including clock rollback.
func ExpandAssistantStream(records []AssistantStreamRecord) ([]TimedStreamChunk, error) {
	expanded := make([]TimedStreamChunk, 0)

	for _, r := range records {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		if r.Type == RecordChunk {
			chunk, err := r.Chunk.Snapshot()
			if err != nil {
				return nil, err
			}

			expanded = append(expanded, TimedStreamChunk{Time: r.Time, Chunk: chunk})
			continue
		}

		fragments := r.Texts
		if r.Type == RecordToolCallChunks {
			fragments = r.Args
		}

		time := r.Time0

		for i, text := range fragments {
			if i > 0 {
				time += r.DT[i-1]
			}

			chunk := StreamChunk{Index: r.Index, Text: text}

			switch r.Type {
			case RecordTextChunks:
				chunk.Type = ChunkTextDelta
			case RecordReasoningChunks:
				chunk.Type = ChunkReasoningDelta
			case RecordToolCallChunks:
				chunk.Type = ChunkToolCallDelta
				chunk.Text = ""
				chunk.ID = r.ID
				chunk.ArgumentsDelta = text
				if r.Name != nil {
					name := *r.Name
					chunk.Name = &name
				}
			}

			expanded = append(expanded, TimedStreamChunk{Time: time, Chunk: chunk})
		}
	}

	return expanded, nil
}
