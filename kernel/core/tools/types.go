package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type JSONSchema json.RawMessage

func (s JSONSchema) Clone() JSONSchema { return append(JSONSchema(nil), s...) }

func (s JSONSchema) MarshalJSON() ([]byte, error) { return json.RawMessage(s).MarshalJSON() }

func (s *JSONSchema) UnmarshalJSON(b []byte) error {
	if !json.Valid(b) {
		return errors.New("invalid JSON schema")
	}

	*s = append((*s)[:0], b...)
	return nil
}

type FailureMode string

const (
	// ToolFailureResult makes validation and execution failures visible to the
	// model as content and Failure and allows the loop to continue.
	ToolFailureResult FailureMode = "result"
	// ToolFailureAbort returns the same ExecutionResult plus an error so
	// the caller can persist the fact and then terminate the run.
	ToolFailureAbort FailureMode = "abort"
)

// ExecutionIdentity attributes a call to its active execution. Turn and Step
// are Session-local counters, not global IDs. Direct calls outside an AgentLoop
// may leave this value empty. It carries no runtime handles or durable metadata.
type ExecutionIdentity struct {
	AgentID   string
	SessionID protocol.SessionID
	Turn      protocol.TurnID
	Step      protocol.StepID
}

// ExecutionContext captures per-call attribution and the optional absolute
// Session working directory. CWD is a resolution base, not an access boundary.
type ExecutionContext struct {
	Identity ExecutionIdentity
	CWD      string
}

type Execution struct {
	Identity  ExecutionIdentity
	CWD       string
	Call      protocol.ToolCall
	Arguments json.RawMessage
}

func (e Execution) Clone() Execution {
	e.Call = e.Call.Clone()
	e.Arguments = append(json.RawMessage(nil), e.Arguments...)
	return e
}

// Failure describes an execution failure; only Info is persisted as event error.
type Failure struct {
	Message string
	Info    *protocol.ToolFailureInfo
}

type ExecutionResult struct {
	IsError            bool
	Value              json.RawMessage
	Error              *Failure
	Content            []protocol.ContentBlock
	Meta               json.RawMessage
	AdditionalContexts []protocol.Message
	ConcludesTurn      bool
}

func (r ExecutionResult) Clone() ExecutionResult {
	r.Value = append(json.RawMessage(nil), r.Value...)
	r.Meta = append(json.RawMessage(nil), r.Meta...)
	r.Content = protocol.CloneContent(r.Content)
	if r.Error != nil {
		v := *r.Error
		if v.Info != nil {
			info := v.Info.Clone()
			v.Info = &info
		}

		r.Error = &v
	}

	if r.AdditionalContexts != nil {
		messages := make([]protocol.Message, len(r.AdditionalContexts))

		for i, m := range r.AdditionalContexts {
			messages[i] = m.Clone()
		}

		r.AdditionalContexts = messages
	}

	return r
}

func (r ExecutionResult) Validate() error {
	if r.Content == nil {
		return errors.New("tool result requires content array")
	}
	if _, err := protocol.EncodeContent(r.Content); err != nil {
		return err
	}
	if r.IsError {
		if r.Error == nil || r.Error.Message == "" || r.Value != nil || r.ConcludesTurn {
			return errors.New("invalid tool failure result")
		}
		if r.Error.Info != nil && (r.Error.Info.Name == "" || r.Error.Info.Code == "") {
			return errors.New("invalid tool failure info")
		}
	} else if r.Error != nil || !json.Valid(r.Value) {
		return errors.New("tool success requires JSON value and no error")
	}

	if r.Meta != nil && !json.Valid(r.Meta) {
		return errors.New("tool meta is not JSON")
	}

	for _, m := range r.AdditionalContexts {
		if err := m.Validate(); err != nil {
			return err
		}
		if m.Role != protocol.RoleUser {
			return errors.New("additional context requires user role")
		}
	}

	return nil
}

type ExecuteFunc func(context.Context, Execution) (ExecutionResult, error)

// JSONTool explicitly adapts a value-only implementation to model-facing text.
// ToolPackage/1 and existing JSON tools retain their own value/schema contract.
func JSONTool(invoke func(context.Context, Execution) (json.RawMessage, error)) ExecuteFunc {
	return func(ctx context.Context, e Execution) (ExecutionResult, error) {
		value, err := invoke(ctx, e)
		if err != nil {
			return ExecutionResult{}, err
		}

		return ExecutionResult{
			Value:   append(json.RawMessage(nil), value...),
			Content: []protocol.ContentBlock{protocol.TextBlock{Text: string(value)}},
		}, nil
	}
}

func FailureResult(code string, cause error) ExecutionResult {
	return ExecutionResult{
		IsError: true,
		Error: &Failure{
			Message: cause.Error(),
			Info:    &protocol.ToolFailureInfo{Name: "ToolExecutionError", Code: code},
		},
		Content: []protocol.ContentBlock{protocol.TextBlock{Text: fmt.Sprintf("Error: %s", cause)}},
	}
}

type Definition struct {
	DeferLoading bool
	Name         string
	Description  string
	Parameters   JSONSchema
	Output       JSONSchema
	Timeout      time.Duration
	FailureMode  FailureMode
	Execute      ExecuteFunc
}

// DefinitionInfo describes a compiled Tool without exposing execution handles.
// Schemas are independent copies owned by the caller.
type DefinitionInfo struct {
	Name         string
	Description  string
	Parameters   JSONSchema
	Output       JSONSchema
	Timeout      time.Duration
	FailureMode  FailureMode
	DeferLoading bool
}

type Schema struct {
	DeferLoading bool       `json:"deferLoading,omitempty"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Parameters   JSONSchema `json:"parameters"`
}

func (s Schema) Clone() Schema {
	s.Parameters = s.Parameters.Clone()
	return s
}

type ExecutePlan struct {
	Execution Execution
	Invoke    ExecuteFunc
}

type Middleware struct {
	ID    string
	Order int
	// Pre and Execute may transform arguments or wrap dispatch. Their returned
	// execution must retain Identity, CWD and the ToolCall ID/Name.
	Pre     func(context.Context, Execution) (Execution, error)
	Execute func(context.Context, ExecutePlan) (ExecutePlan, error)
	Post    func(context.Context, Execution, ExecutionResult) (ExecutionResult, error)
}
