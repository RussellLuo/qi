package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/RussellLuo/qi/builtin/tools/process/internal/contract"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func invalidResponse(message string) *CallError {
	return &CallError{Code: CodeHostValidation, Message: message}
}
func describeTools(ds []tools.DefinitionInfo) ([]Tool, error) {
	out := make([]Tool, len(ds))
	for i, d := range ds {
		out[i] = Tool{Name: d.Name, Description: d.Description, Parameters: d.Parameters.Clone(), Output: d.Output.Clone(), TimeoutNanos: int64(d.Timeout), FailureMode: FailureMode(d.FailureMode), DeferLoading: d.DeferLoading}
	}
	return out, ValidateTools(out)
}
func encodeExecution(e tools.Execution) CallRequest {
	return CallRequest{AgentID: e.Identity.AgentID, SessionID: string(e.Identity.SessionID), Turn: int64(e.Identity.Turn), Step: int64(e.Identity.Step), CWD: e.CWD, Tool: e.Call.Name, ToolCallID: string(e.Call.ID), CallArguments: e.Call.Arguments, Arguments: append([]byte(nil), e.Arguments...)}
}
func decodeExecution(e CallRequest) tools.Execution {
	return tools.Execution{Identity: tools.ExecutionIdentity{AgentID: e.AgentID, SessionID: protocol.SessionID(e.SessionID), Turn: protocol.TurnID(e.Turn), Step: protocol.StepID(e.Step)}, CWD: e.CWD, Call: protocol.ToolCall{ID: protocol.ToolCallID(e.ToolCallID), Name: e.Tool, Arguments: e.CallArguments}, Arguments: append([]byte(nil), e.Arguments...)}
}
func encodeResult(r tools.ExecutionResult) (contract.Result, error) {
	out := contract.Result{IsError: r.IsError, Value: r.Value, Meta: r.Meta, ConcludesTurn: r.ConcludesTurn}
	if r.Error != nil {
		out.Failure = &contract.Failure{Message: r.Error.Message}
		if r.Error.Info != nil {
			i := r.Error.Info
			out.Failure.Info = &contract.FailureInfo{Name: i.Name, Code: i.Code, Reason: i.Reason}
		}
	}
	var err error
	// nil is a distinct invalid business result; Host Runtime applies its policy.
	if r.Content != nil {
		out.Content, err = protocol.EncodeContent(r.Content)
		if err != nil {
			return out, err
		}
	}
	if r.AdditionalContexts != nil {
		out.Contexts = make([][]byte, len(r.AdditionalContexts))
		for i, m := range r.AdditionalContexts {
			out.Contexts[i], err = json.Marshal(m)
			if err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
func decodeResult(r contract.Result) (tools.ExecutionResult, error) {
	out := tools.ExecutionResult{IsError: r.IsError, Value: r.Value, Meta: r.Meta, ConcludesTurn: r.ConcludesTurn}
	if r.Failure != nil {
		out.Error = &tools.Failure{Message: r.Failure.Message}
		if r.Failure.Info != nil {
			i := r.Failure.Info
			out.Error.Info = &protocol.ToolFailureInfo{Name: i.Name, Code: i.Code, Reason: i.Reason}
		}
	}
	var err error
	if r.Content != nil {
		out.Content, err = protocol.DecodeContent(r.Content)
		if err != nil {
			return out, err
		}
	}
	if r.Contexts != nil {
		out.AdditionalContexts = make([]protocol.Message, len(r.Contexts))
		for i, b := range r.Contexts {
			if err = json.Unmarshal(b, &out.AdditionalContexts[i]); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func decodeStrict(raw json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("toolprocess: trailing JSON data")
	}
	return nil
}
