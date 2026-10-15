package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
)

const SessionFormatVersion = 4

type SessionHeader struct {
	Version         int        `json:"version"`
	ID              SessionID  `json:"id"`
	CreatedAt       int64      `json:"createdAt"`
	IsSeeded        bool       `json:"isSeeded"`
	CWD             *string    `json:"cwd,omitempty"`
	ParentSession   *SessionID `json:"parentSession,omitempty"`
	Origin          *string    `json:"origin,omitempty"`
	DelegationDepth *int64     `json:"delegationDepth,omitempty"`
	AgentPreset     *string    `json:"agentPreset,omitempty"`
}

func (h SessionHeader) Validate() error {
	if h.Version != SessionFormatVersion {
		return fmt.Errorf("%w: session header version must be 4", ErrUnsupportedVersion)
	}
	if h.ID == "" ||
		!safeIndex(h.CreatedAt) ||
		(h.CWD != nil && !filepath.IsAbs(*h.CWD)) ||
		(h.Origin != nil && *h.Origin != "subagent") ||
		(h.DelegationDepth != nil && !safeIndex(*h.DelegationDepth)) {
		return fmt.Errorf("%w: invalid session header", ErrInvalidProtocol)
	}

	return nil
}

func (h *SessionHeader) UnmarshalJSON(b []byte) error {
	type wire SessionHeader
	var w wire
	if err := sessionObject(
		b,
		&w,
		[]string{"version", "id", "createdAt", "isSeeded"},
		[]string{"cwd", "parentSession", "origin", "delegationDepth", "agentPreset"},
	); err != nil {
		return err
	}

	v := SessionHeader(w)
	if err := v.Validate(); err != nil {
		return err
	}

	*h = v

	return nil
}

func (h SessionHeader) Clone() SessionHeader {
	b, _ := json.Marshal(h)
	var v SessionHeader
	_ = json.Unmarshal(b, &v)
	return v
}

type CallConfig struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	ReasoningEffort *string  `json:"reasoningEffort,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxTokens       *int64   `json:"maxTokens,omitempty"`
	Stop            []string `json:"stop,omitempty"`
}

type ToolSchema struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Parameters   json.RawMessage `json:"parameters"`
	DeferLoading bool            `json:"deferLoading,omitempty"`
}

type AdapterDefaults struct {
	ReasoningEffort bool `json:"reasoningEffort,omitempty"`
	MaxTokens       bool `json:"maxTokens,omitempty"`
}

type EpochHeader struct {
	Config          CallConfig       `json:"config"`
	AdapterDefaults *AdapterDefaults `json:"adapterDefaults,omitempty"`
	Tools           []ToolSchema     `json:"tools,omitempty"`
}

type RequestHeaderData struct {
	Header       EpochHeader `json:"header"`
	Reason       string      `json:"reason"`
	StartsSeries bool        `json:"startsSeries,omitempty"`
}

type RequestContext struct {
	Provider           string  `json:"provider"`
	Model              string  `json:"model"`
	ContextWindow      *int64  `json:"contextWindow,omitempty"`
	SystemPromptUpdate *string `json:"systemPromptUpdate,omitempty"`
}

func validatePayload(b []byte, target any, required, optional []string) error {
	return sessionObject(b, target, required, optional)
}

func positive(n int64) bool { return n > 0 && safeIndex(n) }

func invalid(what string) error { return fmt.Errorf("%w: %s", ErrInvalidProtocol, what) }

func (e SessionEvent) IsSurface() bool {
	switch e.Type {
	case SystemMessage, DeveloperMessage, UserMessage, AssistantMessage, ToolResulted:
		return true
	}

	return false
}

func (e SessionEvent) IsKnown() bool {
	switch e.Type {
	case TurnStarted,
		TurnEnded,
		StepStarted,
		StepEnded,
		SystemMessage,
		DeveloperMessage,
		UserMessage,
		AssistantMessage,
		AssistantAttempt,
		ToolCalled,
		ToolResulted,
		RequestHeader,
		RequestContextEvent,
		InboxSpliced,
		SessionEndSeed:
		return true
	}

	return false
}

func (e SessionEvent) Validate() error {
	if e.Type == "" || !safeIndex(int64(e.Seq)) || !safeInteger(e.Time) || !json.Valid(e.Data) {
		return invalid("event requires type, safe seq/time and JSON data")
	}
	if !e.IsKnown() {
		if !e.Ignorable {
			return invalid("unknown required event: " + string(e.Type))
		}

		return nil
	}

	if e.IsSurface() {
		if e.SurfaceOp == nil {
			return invalid("message event requires surfaceOp")
		}

		o := e.SurfaceOp
		if o.Op == "append" {
			if o.StartSeq != 0 || o.EndSeq != 0 {
				return invalid("append with replacement fields")
			}
		} else if o.Op != "replace" ||
			!safeIndex(int64(o.StartSeq)) ||
			!safeIndex(int64(o.EndSeq)) ||
			o.StartSeq >= e.Seq ||
			o.EndSeq >= e.Seq {
			return invalid("invalid surface replace")
		}

		if e.SourceEventSeqs != nil {
			if e.Type == AssistantMessage || len(e.SourceEventSeqs) == 0 {
				return invalid("invalid sourceEventSeqs")
			}

			seen := map[Sequence]bool{}

			for _, seq := range e.SourceEventSeqs {
				if !safeIndex(int64(seq)) || seq >= e.Seq || seen[seq] {
					return invalid("sourceEventSeqs must be distinct earlier positions")
				}

				seen[seq] = true
			}
		}
	} else if e.SurfaceOp != nil || e.SourceEventSeqs != nil {
		return invalid("log-only event carries surface metadata")
	}

	step := func(turn TurnID, step StepID) error {
		if !positive(int64(turn)) || !positive(int64(step)) {
			return invalid("invalid turn/step")
		}

		return nil
	}
	records := func(rs []AssistantStreamRecord) error {
		if rs == nil {
			return invalid("stream must be an array")
		}

		for _, r := range rs {
			if err := r.Validate(); err != nil {
				return err
			}
		}

		return nil
	}

	switch e.Type {
	case TurnStarted:
		var d TurnData
		if err := validatePayload(e.Data, &d, []string{"turn"}, nil); err != nil {
			return err
		}
		if !positive(int64(d.Turn)) {
			return invalid("invalid turn")
		}
	case TurnEnded:
		var d TurnEndData
		if err := validatePayload(e.Data, &d, []string{"turn", "reason"}, nil); err != nil {
			return err
		}
		if !positive(int64(d.Turn)) {
			return invalid("invalid turn")
		}
		return validateTurnReason(d.Reason, e.Data)
	case StepStarted, StepEnded:
		var d StepData
		if err := validatePayload(e.Data, &d, []string{"turn", "step"}, nil); err != nil {
			return err
		}
		return step(d.Turn, d.Step)
	case UserMessage:
		var m Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			return err
		}
		if m.Role != RoleUser {
			return invalid("user/message requires user role")
		}
	case SystemMessage, DeveloperMessage:
		var d MessageData
		optional := []string{}
		if e.Type == DeveloperMessage {
			optional = []string{"headerSeq"}
		}
		if err := validatePayload(e.Data, &d, []string{"turn", "step", "message"}, optional); err != nil {
			return err
		}
		if err := step(d.Turn, d.Step); err != nil {
			return err
		}
		role := RoleSystem
		if e.Type == DeveloperMessage {
			role = RoleDeveloper
		}
		if d.Message.Role != role {
			return invalid("wrong message role")
		}
		additions := false
		for _, b := range d.Message.Content {
			if b.Kind() == BlockToolAddition {
				additions = true
			}
		}
		if additions != (d.HeaderSeq != nil) || (d.HeaderSeq != nil && (!safeIndex(int64(*d.HeaderSeq)) || *d.HeaderSeq >= e.Seq)) {
			return invalid("headerSeq required exactly for additions and must be earlier")
		}
	case AssistantMessage:
		var d AssistantMessageData
		if err := validatePayload(e.Data, &d, []string{"turn", "step", "message", "stream"}, []string{"usage", "interrupted"}); err != nil {
			return err
		}
		if err := step(d.Turn, d.Step); err != nil {
			return err
		}
		if d.Message.Role != RoleAssistant {
			return invalid("wrong assistant role")
		}
		if err := records(d.Stream); err != nil {
			return err
		}
		if d.Usage != nil {
			if err := d.Usage.Validate(); err != nil {
				return err
			}
		}
		if err := trueOnly(e.Data, "interrupted"); err != nil {
			return err
		}
	case AssistantAttempt:
		var d AttemptData
		if err := validatePayload(e.Data, &d, []string{"turn", "step", "stream"}, nil); err != nil {
			return err
		}
		if err := step(d.Turn, d.Step); err != nil {
			return err
		}
		return records(d.Stream)
	case ToolCalled:
		var d ToolCallData
		if err := validatePayload(e.Data, &d, []string{"turn", "step", "callId", "name", "arguments"}, nil); err != nil {
			return err
		}
		if err := step(d.Turn, d.Step); err != nil {
			return err
		}
		return (ToolCall{
			ID:        d.CallID,
			Name:      d.Name,
			Arguments: d.Arguments,
		}).Validate()
	case ToolResulted:
		var d ToolResultData
		if err := validatePayload(e.Data, &d, []string{"turn", "step", "message"}, []string{"error", "meta"}); err != nil {
			return err
		}
		if err := step(d.Turn, d.Step); err != nil {
			return err
		}
		if d.Error != nil {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(e.Data, &fields)
			var info ToolFailureInfo
			if err := sessionObject(fields["error"], &info, []string{"name", "code"}, []string{"reason"}); err != nil {
				return err
			}
		}
		return (ToolResult{
			Message: d.Message,
			Error:   d.Error,
			Meta:    d.Meta,
		}).Validate()
	case RequestHeader:
		var d RequestHeaderData
		if err := validatePayload(e.Data, &d, []string{"header", "reason"}, []string{"startsSeries"}); err != nil {
			return err
		}
		switch d.Reason {
		case "initial", "resume", "change", "series":
		default:
			return invalid("invalid header reason")
		}
		if err := trueOnly(e.Data, "startsSeries"); err != nil {
			return err
		}
		return validateHeader(d.Header, e.Data)
	case RequestContextEvent:
		var d RequestContext
		if err := validatePayload(e.Data, &d, []string{"provider", "model"}, []string{"contextWindow", "systemPromptUpdate"}); err != nil {
			return err
		}
		if d.Provider == "" ||
			d.Model == "" ||
			(d.ContextWindow != nil && !positive(*d.ContextWindow)) ||
			(d.SystemPromptUpdate != nil && *d.SystemPromptUpdate != "in-history") {
			return invalid("invalid request context")
		}
	case InboxSpliced:
		var d InboxSpliceData
		if err := validatePayload(e.Data, &d, []string{"target", "start", "inserted"}, []string{"removedCount", "outcome"}); err != nil {
			return err
		}
		if (d.Target != "next-turn" && d.Target != "next-step") ||
			!safeIndex(d.Start) ||
			(d.RemovedCount != nil && !safeIndex(*d.RemovedCount)) ||
			d.Inserted == nil ||
			(d.Outcome != "" && d.Outcome != "canceled") {
			return invalid("invalid inbox splice")
		}
		for _, m := range d.Inserted {
			if m.Role != RoleUser {
				return invalid("inbox requires user messages")
			}
		}
	case SessionEndSeed:
		var d EndSeedData
		if err := validatePayload(e.Data, &d, nil, []string{"inherited"}); err != nil {
			return err
		}
		return trueOnly(e.Data, "inherited")
	}

	return nil
}

func trueOnly(b []byte, key string) error {
	var f map[string]json.RawMessage
	_ = json.Unmarshal(b, &f)
	if raw, ok := f[key]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return invalid(key + " must be true")
	}

	return nil
}

func validateTurnReason(r TurnEndReason, b []byte) error {
	var outer struct {
		Reason json.RawMessage `json:"reason"`
	}
	_ = json.Unmarshal(b, &outer)
	required := []string{"kind"}

	switch r.Kind {
	case "completed", "blocked", "max-tokens", "interrupted", "forked":
	case "error":
		required = append(required, "error")
	case "aborted":
		required = append(required, "reason")
	default:
		return invalid("unknown turn reason")
	}

	var v TurnEndReason
	if err := sessionObject(outer.Reason, &v, required, nil); err != nil {
		return err
	}
	if r.Kind == "error" && (r.Error == nil || r.Error.Message == "" || r.Error.Code == "") {
		return invalid("turn error requires LlmFailure")
	}
	if r.Kind == "aborted" {
		if r.Reason == nil {
			return invalid("aborted requires cause")
		}

		required = []string{"kind"}

		switch r.Reason.Kind {
		case "user", "parent", "disposed", "legacy":
		case "hook":
			required = append(required, "reason")
		default:
			return invalid("unknown cancel cause")
		}

		var f struct {
			Reason json.RawMessage `json:"reason"`
		}
		_ = json.Unmarshal(outer.Reason, &f)
		var c AgentCancelCause

		return sessionObject(f.Reason, &c, required, nil)
	}

	return nil
}

func validateHeader(h EpochHeader, b []byte) error {
	var outer struct {
		Header json.RawMessage `json:"header"`
	}
	_ = json.Unmarshal(b, &outer)
	var f map[string]json.RawMessage
	if err := sessionObject(outer.Header, &f, []string{"config"}, []string{"adapterDefaults", "tools"}); err != nil {
		return err
	}

	var config CallConfig
	if err := sessionObject(
		f["config"],
		&config,
		[]string{"provider", "model"},
		[]string{"reasoningEffort", "temperature", "maxTokens", "stop"},
	); err != nil {
		return err
	}
	if raw, ok := f["config"]; ok {
		var configFields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &configFields)
		if stop, ok := configFields["stop"]; ok {
			var members []json.RawMessage
			if err := json.Unmarshal(stop, &members); err != nil {
				return err
			}

			for _, member := range members {
				if len(bytes.TrimSpace(member)) == 0 || bytes.TrimSpace(member)[0] != '"' {
					return invalid("stop members must be strings")
				}
			}
		}
	}

	if h.Config.Provider == "" ||
		h.Config.Model == "" ||
		(h.Config.Temperature != nil && (math.IsNaN(*h.Config.Temperature) || math.IsInf(*h.Config.Temperature, 0))) ||
		(h.Config.MaxTokens != nil && !positive(*h.Config.MaxTokens)) {
		return invalid("invalid call config")
	}
	if raw, ok := f["adapterDefaults"]; ok {
		var defaults AdapterDefaults
		if err := sessionObject(raw, &defaults, nil, []string{"reasoningEffort", "maxTokens"}); err != nil {
			return err
		}
		if !defaults.ReasoningEffort && !defaults.MaxTokens {
			return invalid("empty adapterDefaults")
		}
		if err := trueOnly(raw, "reasoningEffort"); err != nil {
			return err
		}
		if err := trueOnly(raw, "maxTokens"); err != nil {
			return err
		}
	}

	if raw, ok := f["tools"]; ok {
		var tools []json.RawMessage
		_ = json.Unmarshal(raw, &tools)
		if len(tools) == 0 {
			return invalid("empty tools must be omitted")
		}

		for _, raw := range tools {
			var t ToolSchema
			if err := sessionObject(raw, &t, []string{"name", "description", "parameters"}, []string{"deferLoading"}); err != nil {
				return err
			}

			var p map[string]json.RawMessage
			if t.Name == "" || json.Unmarshal(t.Parameters, &p) != nil || p == nil {
				return invalid("invalid tool definition")
			}
			if err := trueOnly(raw, "deferLoading"); err != nil {
				return err
			}
		}
	}

	return nil
}

// Session records permit opaque JSON null data/meta, while numeric positions
// reject negative zero as well as fractional, negative and unsafe values.
func sessionObject(b []byte, target any, required, optional []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if fields == nil {
		return invalid("expected object")
	}

	allowed := map[string]bool{}

	for _, key := range required {
		allowed[key] = true
		if fields[key] == nil {
			return invalid("missing " + key)
		}
	}

	for _, key := range optional {
		allowed[key] = true
	}

	for key, value := range fields {
		raw := bytes.TrimSpace(value)
		if !allowed[key] ||
			(bytes.Equal(raw, []byte("null")) && key != "data" && key != "meta" && key != "surfaceOp" && key != "sourceEventSeqs") {
			return invalid("invalid field " + key)
		}

		switch key {
		case "seq", "startSeq", "endSeq", "headerSeq", "start", "removedCount", "turn", "step":
			if bytes.Equal(raw, []byte("-0")) {
				return invalid("negative zero position")
			}
		}
	}

	return json.Unmarshal(b, target)
}

func (c CallConfig) MarshalJSON() ([]byte, error) {
	type wire CallConfig
	b, err := json.Marshal(wire(c))
	if err != nil || c.Stop == nil {
		return b, err
	}

	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	fields["stop"], err = json.Marshal(c.Stop)
	if err != nil {
		return nil, err
	}

	return json.Marshal(fields)
}
