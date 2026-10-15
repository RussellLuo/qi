package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	SourceSystem       = "system-prompt"
	SourceUser         = "user"
	SourceModel        = "model"
	SourceTool         = "tool"
)

// MessageSource retains producer-specific JSON without interpreting it.
type MessageSource struct {
	Kind        string
	Provider    string
	Model       string
	CallID      ToolCallID
	ReplayState json.RawMessage
	Extra       map[string]json.RawMessage
}

func (s MessageSource) Clone() MessageSource {
	s.ReplayState = bytes.Clone(s.ReplayState)
	if s.Extra != nil {
		extra := make(map[string]json.RawMessage, len(s.Extra))

		for k, v := range s.Extra {
			extra[k] = bytes.Clone(v)
		}

		s.Extra = extra
	}

	return s
}

func (s MessageSource) MarshalJSON() ([]byte, error) {
	fields := map[string]json.RawMessage{}

	for k, v := range s.Extra {
		fields[k] = v
	}

	put := func(k string, v any) {
		fields[k], _ = json.Marshal(v)
	}
	put("kind", s.Kind)
	if s.Provider != "" {
		put("provider", s.Provider)
	}

	if s.Model != "" {
		put("model", s.Model)
	}

	if s.CallID != "" {
		put("callId", s.CallID)
	}

	if s.ReplayState != nil {
		fields["replayState"] = s.ReplayState
	}

	return json.Marshal(fields)
}

func (s *MessageSource) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("%w: source must be an object", ErrInvalidProtocol)
	}

	decoded := MessageSource{}
	if err := json.Unmarshal(fields["kind"], &decoded.Kind); err != nil {
		return err
	}

	delete(fields, "kind")
	// User/developer sources are extension-open even when their kind happens
	// to match a core producer. Keep absent, empty or non-string fields intact;
	// the Message role validates the typed fields it actually requires.
	takeString := func(k string) string {
		var value string
		if json.Unmarshal(fields[k], &value) == nil && value != "" {
			delete(fields, k)
			return value
		}

		return ""
	}
	if decoded.Kind == SourceModel {
		decoded.Provider = takeString("provider")
		decoded.Model = takeString("model")
		if raw, ok := fields["replayState"]; ok {
			decoded.ReplayState = bytes.Clone(raw)
			delete(fields, "replayState")
		}
	}

	if decoded.Kind == SourceTool {
		decoded.CallID = ToolCallID(takeString("callId"))
	}

	if len(fields) > 0 {
		decoded.Extra = fields
	}

	*s = decoded

	return nil
}

type BlockKind string

const (
	BlockText         BlockKind = "text"
	BlockReasoning    BlockKind = "reasoning"
	BlockToolCall     BlockKind = "tool-call"
	BlockToolAddition BlockKind = "tool-addition"
	BlockToolRemoval  BlockKind = "tool-removal"
)

type ContentBlock interface {
	Kind() BlockKind
	cloneContentBlock() ContentBlock
	validateContentBlock() error
}

type TextBlock struct {
	Text string `json:"text"`
}

func (TextBlock) Kind() BlockKind { return BlockText }

func (b TextBlock) cloneContentBlock() ContentBlock { return b }

func (TextBlock) validateContentBlock() error { return nil }

type ReasoningBlock struct {
	Text string `json:"text"`
}

func (ReasoningBlock) Kind() BlockKind { return BlockReasoning }

func (b ReasoningBlock) cloneContentBlock() ContentBlock { return b }

func (ReasoningBlock) validateContentBlock() error { return nil }

type ToolCall struct {
	ID        ToolCallID `json:"id"`
	Name      string     `json:"name"`
	Arguments string     `json:"arguments"`
}

func (c *ToolCall) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID        *ToolCallID `json:"id"`
		Name      *string     `json:"name"`
		Arguments *string     `json:"arguments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProtocol, err)
	}
	if wire.ID == nil || wire.Name == nil || wire.Arguments == nil {
		return fmt.Errorf("%w: tool call requires id, name and raw arguments string", ErrInvalidProtocol)
	}

	decoded := ToolCall{
		ID:        *wire.ID,
		Name:      *wire.Name,
		Arguments: *wire.Arguments,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}

	*c = decoded

	return nil
}

func (c ToolCall) Clone() ToolCall { return c }

func (c ToolCall) Validate() error {
	if c.ID == "" || c.Name == "" {
		return fmt.Errorf("%w: tool call requires id and name", ErrInvalidProtocol)
	}

	return nil
}

type ToolCallBlock struct{ ToolCall }

func (ToolCallBlock) Kind() BlockKind { return BlockToolCall }

func (b ToolCallBlock) cloneContentBlock() ContentBlock { return b }

func (b ToolCallBlock) validateContentBlock() error { return b.ToolCall.Validate() }

type ToolAdditionBlock struct {
	ToolName string `json:"toolName"`
}

func (ToolAdditionBlock) Kind() BlockKind { return BlockToolAddition }

func (b ToolAdditionBlock) cloneContentBlock() ContentBlock { return b }

func (b ToolAdditionBlock) validateContentBlock() error { return validateToolName(b.ToolName) }

type ToolRemovalBlock struct {
	ToolName string `json:"toolName"`
}

func (ToolRemovalBlock) Kind() BlockKind { return BlockToolRemoval }

func (b ToolRemovalBlock) cloneContentBlock() ContentBlock { return b }

func (b ToolRemovalBlock) validateContentBlock() error { return validateToolName(b.ToolName) }

func validateToolName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: tool change requires toolName", ErrInvalidProtocol)
	}

	return nil
}

type Message struct {
	ID         MessageID      `json:"id"`
	Role       Role           `json:"role"`
	Source     MessageSource  `json:"source"`
	Content    []ContentBlock `json:"-"`
	ToolCallID ToolCallID     `json:"toolCallId,omitempty"`
	IsError    *bool          `json:"isError,omitempty"`
}

func NewTextMessage(id MessageID, role Role, source string, text string) Message {
	content := []ContentBlock{TextBlock{Text: text}}
	if role == RoleSystem && text == "" {
		content = []ContentBlock{}
	}

	return Message{
		ID:      id,
		Role:    role,
		Source:  MessageSource{Kind: source},
		Content: content,
	}
}

func NewAssistantTextMessage(id MessageID, provider, model, text string) Message {
	return Message{
		ID:   id,
		Role: RoleAssistant,
		Source: MessageSource{
			Kind:     SourceModel,
			Provider: provider,
			Model:    model,
		},
		Content: []ContentBlock{TextBlock{Text: text}},
	}
}

func NewToolCallMessage(id MessageID, call ToolCall, source MessageSource) Message {
	return Message{
		ID:      id,
		Role:    RoleAssistant,
		Source:  source.Clone(),
		Content: []ContentBlock{ToolCallBlock{ToolCall: call}},
	}
}

func NewToolResultMessage(id MessageID, callID ToolCallID, content []ContentBlock, isError bool) Message {
	return Message{
		ID:         id,
		Role:       RoleTool,
		Source:     MessageSource{Kind: SourceTool, CallID: callID},
		ToolCallID: callID,
		IsError:    &isError,
		Content:    CloneContent(content),
	}
}

func CloneContent(content []ContentBlock) []ContentBlock {
	if content == nil {
		return nil
	}

	cloned := make([]ContentBlock, len(content))

	for i, b := range content {
		if b != nil {
			cloned[i] = b.cloneContentBlock()
		}
	}

	return cloned
}

func (m Message) Clone() Message {
	m.Source = m.Source.Clone()
	m.Content = CloneContent(m.Content)
	if m.IsError != nil {
		v := *m.IsError
		m.IsError = &v
	}

	return m
}

func (m Message) Validate() error {
	if m.ID == "" || m.Content == nil || m.Source.Kind == "" {
		return fmt.Errorf("%w: message requires id, source kind and content array", ErrInvalidProtocol)
	}

	switch m.Role {
	case RoleSystem:
		if m.Source.Kind != SourceSystem {
			return fmt.Errorf("%w: system source", ErrInvalidProtocol)
		}
	case RoleAssistant:
		if m.Source.Kind != SourceModel || m.Source.Provider == "" || m.Source.Model == "" {
			return fmt.Errorf("%w: model source requires provider and model", ErrInvalidProtocol)
		}
	case RoleTool:
		if m.Source.Kind != SourceTool || m.ToolCallID == "" || m.Source.CallID != m.ToolCallID {
			return fmt.Errorf("%w: tool source must match toolCallId", ErrInvalidProtocol)
		}
	case RoleUser, RoleDeveloper:
	default:
		return fmt.Errorf("%w: unknown role %q", ErrInvalidProtocol, m.Role)
	}

	if m.Role != RoleTool && (m.ToolCallID != "" || m.IsError != nil) {
		return fmt.Errorf("%w: unexpected tool message fields", ErrInvalidProtocol)
	}
	if _, err := json.Marshal(m.Source); err != nil {
		return fmt.Errorf("%w: source: %v", ErrInvalidProtocol, err)
	}

	for _, b := range m.Content {
		if b == nil {
			return fmt.Errorf("%w: nil block", ErrInvalidProtocol)
		}
		if err := b.validateContentBlock(); err != nil {
			return err
		}
	}

	return nil
}

func EncodeContent(content []ContentBlock) ([]byte, error) {
	blocks := make([]map[string]any, 0, len(content))

	for _, b := range content {
		if b == nil {
			return nil, fmt.Errorf("%w: nil block", ErrInvalidProtocol)
		}
		if err := b.validateContentBlock(); err != nil {
			return nil, err
		}

		wire := map[string]any{"type": b.Kind()}

		switch v := b.(type) {
		case TextBlock:
			wire["text"] = v.Text
		case ReasoningBlock:
			wire["text"] = v.Text
		case ToolCallBlock:
			wire["id"] = v.ID
			wire["name"] = v.Name
			wire["arguments"] = v.Arguments
		case ToolAdditionBlock:
			wire["toolName"] = v.ToolName
		case ToolRemovalBlock:
			wire["toolName"] = v.ToolName
		default:
			return nil, fmt.Errorf("%w: unsupported block %T", ErrInvalidProtocol, b)
		}

		blocks = append(blocks, wire)
	}

	return json.Marshal(blocks)
}

func DecodeContent(data []byte) ([]ContentBlock, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: content must be array", ErrInvalidProtocol)
	}

	blocks := make([]ContentBlock, 0, len(raw))

	for _, r := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(r, &fields); err != nil {
			return nil, err
		}

		var kind BlockKind
		if err := json.Unmarshal(fields["type"], &kind); err != nil {
			return nil, err
		}

		takeString := func(k string) (string, error) {
			raw := bytes.TrimSpace(fields[k])
			if len(raw) == 0 || raw[0] != '"' {
				return "", fmt.Errorf("%w: %s must be a string", ErrInvalidProtocol, k)
			}

			var text string
			err := json.Unmarshal(raw, &text)

			return text, err
		}
		var b ContentBlock
		allowed := map[string]bool{"type": true}

		switch kind {
		case BlockText, BlockReasoning:
			text, err := takeString("text")
			if err != nil {
				return nil, err
			}
			allowed["text"] = true
			if kind == BlockText {
				b = TextBlock{Text: text}
			} else {
				b = ReasoningBlock{Text: text}
			}
		case BlockToolCall:
			id, err := takeString("id")
			if err != nil {
				return nil, err
			}
			name, err := takeString("name")
			if err != nil {
				return nil, err
			}
			args, err := takeString("arguments")
			if err != nil {
				return nil, err
			}
			allowed["id"] = true
			allowed["name"] = true
			allowed["arguments"] = true
			b = ToolCallBlock{
				ToolCall: ToolCall{
					ID:        ToolCallID(id),
					Name:      name,
					Arguments: args,
				},
			}
		case BlockToolAddition, BlockToolRemoval:
			name, err := takeString("toolName")
			if err != nil {
				return nil, err
			}
			allowed["toolName"] = true
			if kind == BlockToolAddition {
				b = ToolAdditionBlock{ToolName: name}
			} else {
				b = ToolRemovalBlock{ToolName: name}
			}
		default:
			return nil, fmt.Errorf("%w: unknown block type %q", ErrInvalidProtocol, kind)
		}

		for k := range fields {
			if !allowed[k] {
				return nil, fmt.Errorf("%w: unexpected block field %q", ErrInvalidProtocol, k)
			}
		}

		if err := b.validateContentBlock(); err != nil {
			return nil, err
		}

		blocks = append(blocks, b)
	}

	return blocks, nil
}

type messageEnvelope struct {
	ID         MessageID       `json:"id"`
	Role       Role            `json:"role"`
	Source     MessageSource   `json:"source"`
	Content    json.RawMessage `json:"content"`
	ToolCallID ToolCallID      `json:"toolCallId,omitempty"`
	IsError    *bool           `json:"isError,omitempty"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	content, err := EncodeContent(m.Content)
	if err != nil {
		return nil, err
	}

	return json.Marshal(messageEnvelope{m.ID, m.Role, m.Source, content, m.ToolCallID, m.IsError})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProtocol, err)
	}
	if raw, ok := fields["isError"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%w: isError must be boolean", ErrInvalidProtocol)
	}

	var w messageEnvelope
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProtocol, err)
	}

	content, err := DecodeContent(w.Content)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProtocol, err)
	}

	decoded := Message{
		ID:         w.ID,
		Role:       w.Role,
		Source:     w.Source,
		Content:    content,
		ToolCallID: w.ToolCallID,
		IsError:    w.IsError,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}

	*m = decoded

	return nil
}

// ToolResult holds only persistent result facts. Execution-local value stays in contracts.
type ToolFailureInfo struct {
	Name   string  `json:"name"`
	Code   string  `json:"code"`
	Reason *string `json:"reason,omitempty"`
}

func (info ToolFailureInfo) Clone() ToolFailureInfo {
	if info.Reason != nil {
		reason := *info.Reason
		info.Reason = &reason
	}

	return info
}

type ToolResult struct {
	Message Message          `json:"message"`
	Error   *ToolFailureInfo `json:"error,omitempty"`
	Meta    json.RawMessage  `json:"meta,omitempty"`
}

func (r ToolResult) Clone() ToolResult {
	r.Message = r.Message.Clone()
	r.Meta = bytes.Clone(r.Meta)
	if r.Error != nil {
		v := r.Error.Clone()
		r.Error = &v
	}

	return r
}

func (r ToolResult) Validate() error {
	if err := r.Message.Validate(); err != nil {
		return err
	}
	if r.Message.Role != RoleTool {
		return fmt.Errorf("%w: result requires tool role", ErrInvalidProtocol)
	}
	if r.Error != nil && (r.Message.IsError == nil || !*r.Message.IsError || r.Error.Name == "" || r.Error.Code == "") {
		return fmt.Errorf("%w: invalid tool failure info", ErrInvalidProtocol)
	}
	if r.Meta != nil && !json.Valid(r.Meta) {
		return fmt.Errorf("%w: invalid tool meta", ErrInvalidProtocol)
	}

	return nil
}
