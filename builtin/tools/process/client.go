package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	gordisprocess "github.com/RussellLuo/gordis/process"
)

type diagnosticsProvider interface {
	Snapshot() gordisprocess.Diagnostics
}

type Client struct {
	caller gordisprocess.Caller
}

func NewClient(caller gordisprocess.Caller) (*Client, error) {
	if caller == nil {
		return nil, errors.New("toolprocess: nil process caller")
	}
	return &Client{caller: caller}, nil
}

func (c *Client) Describe(ctx context.Context) (Metadata, error) {
	var metadata Metadata
	if err := c.caller.Call(ctx, MethodDescribe, nil, &metadata); err != nil {
		return Metadata{}, c.mapCallError(ctx, err)
	}
	if err := ValidateMetadata(metadata); err != nil {
		return Metadata{}, &CallError{Code: CodeHostValidation, Message: "invalid package metadata", cause: err}
	}
	metadata.Capabilities = append([]string(nil), metadata.Capabilities...)
	return metadata, nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var definitions []Tool
	if err := c.caller.Call(ctx, MethodListTools, nil, &definitions); err != nil {
		return nil, c.mapCallError(ctx, err)
	}
	if err := ValidateTools(definitions); err != nil {
		return nil, &CallError{Code: CodeHostValidation, Message: "invalid Tool definitions", cause: err}
	}
	return cloneTools(definitions), nil
}

func (c *Client) Call(ctx context.Context, request CallRequest) (json.RawMessage, error) {
	if err := ValidateCall(request); err != nil {
		return nil, &CallError{Code: CodeHostValidation, Message: "invalid Tool call", cause: err}
	}
	request.Arguments = append(json.RawMessage(nil), request.Arguments...)
	var response CallResponse
	if err := c.caller.Call(ctx, MethodCall, request, &response); err != nil {
		return nil, c.mapCallError(ctx, err)
	}
	if response.Error != nil {
		if len(response.Result) != 0 || response.Error.Code != CodeToolError || len(response.Error.Message) > MaxErrorMessage {
			return nil, &CallError{Code: CodeHostValidation, Message: "invalid Tool error response"}
		}
		return nil, &CallError{Code: CodeToolError, Message: boundedMessage(response.Error.Message, "tool execution failed")}
	}
	if err := validateJSONDocument(response.Result, MaxResultBytes, "result"); err != nil {
		return nil, &CallError{Code: CodeHostValidation, Message: "invalid Tool result", cause: err}
	}
	return append(json.RawMessage(nil), response.Result...), nil
}

func (c *Client) mapCallError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return &CallError{Code: CodeCancelled, Message: "Tool call cancelled", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &CallError{Code: CodeTimeout, Message: "Tool call timed out", cause: context.DeadlineExceeded}
	}
	if errors.Is(err, gordisprocess.ErrBackpressure) {
		return &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity", cause: err}
	}
	var rpc *gordisprocess.RPCError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case CodeCancelled:
			return &CallError{Code: CodeCancelled, Message: "Tool call cancelled", cause: context.Canceled}
		case CodeTimeout:
			return &CallError{Code: CodeTimeout, Message: "Tool call timed out", cause: context.DeadlineExceeded}
		case CodeBackpressure:
			return &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity", cause: err}
		case CodeHostValidation:
			return &CallError{Code: CodeHostValidation, Message: "Tool process returned an invalid response", cause: err}
		}
	}
	if provider, ok := c.caller.(diagnosticsProvider); ok && errors.Is(err, gordisprocess.ErrClosed) {
		deadline := time.Now().Add(50 * time.Millisecond)
		for {
			diagnostics := provider.Snapshot()
			if diagnostics.Reaped {
				if strings.Contains(diagnostics.Error, "exited") || strings.Contains(diagnostics.Error, "read: EOF") {
					return &CallError{Code: CodeProcessExit, Message: "Tool process exited", cause: err}
				}
				break
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	return &CallError{Code: CodeTransport, Message: "Tool process transport failed", cause: err}
}

func cloneTools(definitions []Tool) []Tool {
	cloned := make([]Tool, len(definitions))
	for i, definition := range definitions {
		cloned[i] = definition
		cloned[i].Parameters = append(json.RawMessage(nil), definition.Parameters...)
		cloned[i].Output = append(json.RawMessage(nil), definition.Output...)
	}
	return cloned
}
