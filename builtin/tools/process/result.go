package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func invalidResponse(message string) *CallError {
	return &CallError{Code: CodeHostValidation, Message: message}
}
func describeTools(descriptions []tools.DefinitionInfo) ([]Tool, error) {
	var definitions []Tool
	for _, d := range descriptions {
		if d.DeferLoading || d.Timeout < 0 || d.Timeout > MaxToolTimeout || d.Timeout%time.Millisecond != 0 {
			return nil, errors.New("toolprocess: unsupported deferred Tool or timeout")
		}
		definitions = append(definitions, Tool{
			Name: d.Name, Description: d.Description, Parameters: json.RawMessage(d.Parameters), Output: json.RawMessage(d.Output),
			TimeoutMillis: d.Timeout.Milliseconds(), FailureMode: FailureMode(d.FailureMode),
		})
	}
	return definitions, ValidateTools(definitions)
}

func wireResult(ctx context.Context, result tools.ExecutionResult, executeErr error) (CallResponse, error) {
	if result.Meta != nil || len(result.AdditionalContexts) != 0 || result.ConcludesTurn || len(result.Content) != 1 {
		return CallResponse{}, invalidResponse("unsupported Tool result")
	}
	text, ok := result.Content[0].(protocol.TextBlock)
	if !ok {
		return CallResponse{}, invalidResponse("unsupported Tool content")
	}
	if result.IsError {
		if result.Error == nil || text.Text != "Error: "+result.Error.Message {
			return CallResponse{}, invalidResponse("unsupported Tool failure")
		}
		code := ""
		if result.Error.Info != nil {
			code = result.Error.Info.Code
		}
		switch code {
		case "ABORTED_BEFORE_DISPATCH":
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return CallResponse{}, &CallError{Code: CodeTimeout, Message: "Tool call timed out"}
			}
			return CallResponse{}, &CallError{Code: CodeCancelled, Message: "Tool call cancelled"}
		case "cancelled":
			return CallResponse{}, &CallError{Code: CodeCancelled, Message: "Tool call cancelled"}
		case "timeout":
			return CallResponse{}, &CallError{Code: CodeTimeout, Message: "Tool call timed out"}
		case "invalid_result", "invalid_arguments":
			return CallResponse{}, invalidResponse("invalid Tool value")
		case "panic":
			return CallResponse{}, &CallError{Code: CodeTransport, Message: "Tool execution panicked"}
		}
		// Native errors can contain arguments or credentials. Preserve the
		// Provider/1 business-error category with a bounded safe message.
		return CallResponse{Error: NewToolError("tool execution failed")}, nil
	}
	if executeErr != nil || text.Text != string(result.Value) || validateJSONDocument(result.Value, MaxResultBytes, "result") != nil {
		return CallResponse{}, invalidResponse("invalid Tool result")
	}
	return CallResponse{Result: append(json.RawMessage(nil), result.Value...)}, nil
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
