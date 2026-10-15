package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

type StreamChunkType string

const (
	ChunkBlockStart     StreamChunkType = "block-start"
	ChunkTextDelta      StreamChunkType = "text-delta"
	ChunkReasoningDelta StreamChunkType = "reasoning-delta"
	ChunkToolCallDelta  StreamChunkType = "tool-call-delta"
	ChunkBlockEnd       StreamChunkType = "block-end"
	ChunkUsage          StreamChunkType = "usage"
	ChunkFinish         StreamChunkType = "finish"
)

type FinishKind string

const (
	FinishStop      FinishKind = "stop"
	FinishToolCalls FinishKind = "tool-calls"
	FinishMaxTokens FinishKind = "max-tokens"
	FinishAborted   FinishKind = "aborted"
	FinishError     FinishKind = "error"
)

type LlmFailure struct {
	Message              string   `json:"message"`
	Code                 string   `json:"code"`
	Status               *int     `json:"status,omitempty"`
	ProviderRetryAfterMs *float64 `json:"providerRetryAfterMs,omitempty"`
	RequestID            *string  `json:"requestId,omitempty"`
	OffloadImages        *int     `json:"offloadImages,omitempty"`
}

type FinishReason struct {
	Kind    FinishKind  `json:"kind"`
	Failure *LlmFailure `json:"failure,omitempty"`
}

func (f LlmFailure) Clone() LlmFailure {
	if f.Status != nil {
		v := *f.Status
		f.Status = &v
	}

	if f.ProviderRetryAfterMs != nil {
		v := *f.ProviderRetryAfterMs
		f.ProviderRetryAfterMs = &v
	}

	if f.RequestID != nil {
		v := *f.RequestID
		f.RequestID = &v
	}

	if f.OffloadImages != nil {
		v := *f.OffloadImages
		f.OffloadImages = &v
	}

	return f
}

func (r FinishReason) Validate() error {
	switch r.Kind {
	case FinishStop, FinishToolCalls, FinishMaxTokens:
		if r.Failure != nil {
			return fmt.Errorf("%w: unexpected finish failure", ErrInvalidProtocol)
		}
	case FinishError, FinishAborted:
		if r.Failure == nil || r.Failure.Message == "" || r.Failure.Code == "" {
			return fmt.Errorf("%w: finish requires failure message/code", ErrInvalidProtocol)
		}
	default:
		return fmt.Errorf("%w: unknown finish kind %q", ErrInvalidProtocol, r.Kind)
	}

	return nil
}

func (r *FinishReason) UnmarshalJSON(data []byte) error {
	var w struct {
		Kind    FinishKind  `json:"kind"`
		Failure *LlmFailure `json:"failure,omitempty"`
	}
	if err := strictObject(data, &w, []string{"kind"}, []string{"failure"}); err != nil {
		return err
	}

	v := FinishReason{w.Kind, w.Failure}
	if err := v.Validate(); err != nil {
		return err
	}

	*r = v

	return nil
}

func (f *LlmFailure) UnmarshalJSON(data []byte) error {
	type wire LlmFailure
	var w wire
	if err := strictObject(
		data,
		&w,
		[]string{"message", "code"},
		[]string{"status", "providerRetryAfterMs", "requestId", "offloadImages"},
	); err != nil {
		return err
	}

	*f = LlmFailure(w)

	return nil
}

type Usage struct {
	InputTokens      uint64  `json:"inputTokens"`
	OutputTokens     uint64  `json:"outputTokens"`
	TotalTokens      *uint64 `json:"totalTokens,omitempty"`
	CacheReadTokens  *uint64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *uint64 `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  *uint64 `json:"reasoningTokens,omitempty"`
}

func (u Usage) Validate() error {
	for _, n := range []*uint64{
		&u.InputTokens,
		&u.OutputTokens,
		u.TotalTokens,
		u.CacheReadTokens,
		u.CacheWriteTokens,
		u.ReasoningTokens,
	} {
		if n != nil && *n > uint64(MaxSafeInteger) {
			return fmt.Errorf("%w: usage tokens must be safe integers", ErrInvalidProtocol)
		}
	}

	return nil
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	type wire Usage
	var w wire
	if err := strictObject(
		data,
		&w,
		[]string{"inputTokens", "outputTokens"},
		[]string{"totalTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens"},
	); err != nil {
		return err
	}

	v := Usage(w)
	if err := v.Validate(); err != nil {
		return err
	}

	*u = v

	return nil
}

// ReplayEnvelope is snapshotted through JSON at every durable boundary. Keeping
// unknown values here makes unsupported Go values fail instead of becoming {}.
type ReplayEnvelope struct {
	Response any   `json:"response"`
	Blocks   []any `json:"blocks,omitempty"`
}

func (r ReplayEnvelope) MarshalJSON() ([]byte, error) {
	// Marshal first to reject cycles and non-finite/unsupported values before
	// walking. Opaque structs such as Go errors must not silently become {}.
	if _, err := json.Marshal(r.Response); err != nil {
		return nil, err
	}
	if _, err := json.Marshal(r.Blocks); err != nil {
		return nil, err
	}
	if err := validateReplayValue(reflect.ValueOf(r.Response)); err != nil {
		return nil, err
	}
	if err := validateReplayValue(reflect.ValueOf(r.Blocks)); err != nil {
		return nil, err
	}

	w := map[string]any{"response": r.Response}
	if r.Blocks != nil {
		w["blocks"] = r.Blocks
	}

	return json.Marshal(w)
}

func validateReplayValue(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == reflect.TypeFor[json.RawMessage]() {
		return nil
	}

	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return validateReplayValue(value.Elem())
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("replay objects require string keys")
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := validateReplayValue(iter.Value()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateReplayValue(value.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Bool,
		reflect.String,
		reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64,
		reflect.Float32,
		reflect.Float64:
	default:
		return fmt.Errorf("replay value %s is not a JSON value", value.Type())
	}

	return nil
}

func (r *ReplayEnvelope) UnmarshalJSON(data []byte) error {
	type wire ReplayEnvelope
	var w wire
	// response may be JSON null; blocks, when present, must be an array.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil || fields["response"] == nil || len(fields) > 2 {
		return fmt.Errorf("%w: invalid replay envelope", ErrInvalidProtocol)
	}

	for k := range fields {
		if k != "response" && k != "blocks" {
			return fmt.Errorf("%w: replay field %q", ErrInvalidProtocol, k)
		}
	}

	if raw, ok := fields["blocks"]; ok && (len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[') {
		return fmt.Errorf("%w: replay blocks must be array", ErrInvalidProtocol)
	}

	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&w); err != nil {
		return err
	}

	*r = ReplayEnvelope(w)

	return nil
}

type StreamChunk struct {
	Type           StreamChunkType
	Index          int64
	BlockType      BlockKind
	Text           string
	ID             ToolCallID
	Name           *string
	ArgumentsDelta string
	Block          ContentBlock
	Usage          *Usage
	Reason         FinishReason
	ReplayState    *ReplayEnvelope
}

const MaxSafeInteger int64 = 9007199254740991

func safeInteger(v int64) bool { return v >= -MaxSafeInteger && v <= MaxSafeInteger }

func safeIndex(v int64) bool { return v >= 0 && safeInteger(v) }

func (c StreamChunk) Validate() error {
	if c.Type != ChunkUsage && c.Type != ChunkFinish && !safeIndex(c.Index) {
		return fmt.Errorf("%w: block index must be non-negative safe integer", ErrInvalidProtocol)
	}

	switch c.Type {
	case ChunkBlockStart:
		switch c.BlockType {
		case BlockText, BlockReasoning, BlockToolCall, BlockToolAddition, BlockToolRemoval:
		default:
			return fmt.Errorf("%w: unknown block type", ErrInvalidProtocol)
		}
	case ChunkTextDelta, ChunkReasoningDelta, ChunkToolCallDelta:
	case ChunkBlockEnd:
		if c.Block == nil {
			return fmt.Errorf("%w: block-end requires block", ErrInvalidProtocol)
		}
		if err := c.Block.validateContentBlock(); err != nil {
			return err
		}
	case ChunkUsage:
		if c.Usage == nil {
			return fmt.Errorf("%w: usage required", ErrInvalidProtocol)
		}
		if err := c.Usage.Validate(); err != nil {
			return err
		}
	case ChunkFinish:
		if err := c.Reason.Validate(); err != nil {
			return err
		}
		if c.ReplayState != nil {
			if _, err := json.Marshal(c.ReplayState); err != nil {
				return fmt.Errorf("%w: replay state: %v", ErrInvalidProtocol, err)
			}
		}
	default:
		return fmt.Errorf("%w: unknown chunk type %q", ErrInvalidProtocol, c.Type)
	}

	if (c.BlockType != "" && c.Type != ChunkBlockStart) ||
		(c.Text != "" && c.Type != ChunkTextDelta && c.Type != ChunkReasoningDelta) ||
		((c.ID != "" || c.Name != nil || c.ArgumentsDelta != "") && c.Type != ChunkToolCallDelta) ||
		(c.Block != nil && c.Type != ChunkBlockEnd) ||
		(c.Usage != nil && c.Type != ChunkUsage) ||
		((c.Reason.Kind != "" || c.Reason.Failure != nil || c.ReplayState != nil) && c.Type != ChunkFinish) ||
		((c.Type == ChunkFinish || c.Type == ChunkUsage) && c.Index != 0) {
		return fmt.Errorf("%w: conflicting chunk payload", ErrInvalidProtocol)
	}

	return nil
}

func (c StreamChunk) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	w := map[string]any{"type": c.Type}

	switch c.Type {
	case ChunkBlockStart:
		w["index"] = c.Index
		w["blockType"] = c.BlockType
	case ChunkTextDelta, ChunkReasoningDelta:
		w["index"] = c.Index
		w["text"] = c.Text
	case ChunkToolCallDelta:
		w["index"] = c.Index
		w["id"] = c.ID
		w["argumentsDelta"] = c.ArgumentsDelta
		if c.Name != nil {
			w["name"] = *c.Name
		}
	case ChunkBlockEnd:
		b, err := EncodeContent([]ContentBlock{c.Block})
		if err != nil {
			return nil, err
		}
		var blocks []json.RawMessage
		_ = json.Unmarshal(b, &blocks)
		w["index"] = c.Index
		w["block"] = blocks[0]
	case ChunkUsage:
		w["usage"] = c.Usage
	case ChunkFinish:
		w["reason"] = c.Reason
		if c.ReplayState != nil {
			w["replayState"] = c.ReplayState
		}
	}

	return json.Marshal(w)
}

func (c *StreamChunk) UnmarshalJSON(data []byte) error {
	var w struct {
		Type           StreamChunkType `json:"type"`
		Index          int64           `json:"index"`
		BlockType      BlockKind       `json:"blockType"`
		Text           string          `json:"text"`
		ID             ToolCallID      `json:"id"`
		Name           *string         `json:"name"`
		ArgumentsDelta string          `json:"argumentsDelta"`
		Block          json.RawMessage `json:"block"`
		Usage          *Usage          `json:"usage"`
		Reason         FinishReason    `json:"reason"`
		ReplayState    *ReplayEnvelope `json:"replayState"`
	}
	var tag struct {
		Type StreamChunkType `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	required := []string{"type"}
	var optional []string

	switch tag.Type {
	case ChunkBlockStart:
		required = append(required, "index", "blockType")
	case ChunkTextDelta, ChunkReasoningDelta:
		required = append(required, "index", "text")
	case ChunkToolCallDelta:
		required = append(required, "index", "id", "argumentsDelta")
		optional = []string{"name"}
	case ChunkBlockEnd:
		required = append(required, "index", "block")
	case ChunkUsage:
		required = append(required, "usage")
	case ChunkFinish:
		required = append(required, "reason")
		optional = []string{"replayState"}
	default:
		return fmt.Errorf("%w: unknown chunk type %q", ErrInvalidProtocol, tag.Type)
	}

	if err := strictObject(data, &w, required, optional); err != nil {
		return err
	}

	v := StreamChunk{
		Type:           w.Type,
		Index:          w.Index,
		BlockType:      w.BlockType,
		Text:           w.Text,
		ID:             w.ID,
		Name:           w.Name,
		ArgumentsDelta: w.ArgumentsDelta,
		Usage:          w.Usage,
		Reason:         w.Reason,
		ReplayState:    w.ReplayState,
	}
	if w.Block != nil {
		blocks, err := DecodeContent(append(append([]byte{'['}, w.Block...), ']'))
		if err != nil {
			return err
		}

		v.Block = blocks[0]
	}

	if err := v.Validate(); err != nil {
		return err
	}

	*c = v

	return nil
}

// Snapshot rejects non-JSON replay values and detaches all nested mutable data.
func (c StreamChunk) Snapshot() (StreamChunk, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return StreamChunk{}, err
	}

	var v StreamChunk
	err = json.Unmarshal(data, &v)

	return v, err
}

func strictObject(data []byte, target any, required, optional []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("%w: expected object", ErrInvalidProtocol)
	}
	if bytes.Equal(bytes.TrimSpace(fields["index"]), []byte("-0")) {
		return fmt.Errorf("%w: negative zero index", ErrInvalidProtocol)
	}

	allowed := map[string]bool{}

	for _, k := range required {
		allowed[k] = true
		if fields[k] == nil {
			return fmt.Errorf("%w: missing %s", ErrInvalidProtocol, k)
		}
	}

	for _, k := range optional {
		allowed[k] = true
	}

	for k, v := range fields {
		if !allowed[k] || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("%w: invalid field %s", ErrInvalidProtocol, k)
		}
	}

	return json.Unmarshal(data, target)
}
