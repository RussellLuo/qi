package process

import (
	"context"
	"encoding/json"
	"errors"

	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/contract"
	"github.com/RussellLuo/qi/kernel/core/tools"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Client struct {
	loader  interface{ Exited() bool }
	remote  Provider
	pending chan struct{}
}

func NewClient(session *rpc.Session) (*Client, error) {
	return newClient(session, (Limits{}).defaults().MaxPending)
}

func newClient(session *rpc.Session, maxPending int) (*Client, error) {
	if session == nil {
		return nil, errors.New("toolprocess: nil session")
	}
	remote, err := rpc.ExportClient(session, ProviderKey)
	if err != nil {
		return nil, err
	}
	return &Client{
		loader:  session,
		remote:  remote,
		pending: make(chan struct{}, maxPending),
	}, nil
}

func (client *Client) ListTools(ctx context.Context) ([]Tool, error) {
	out, err := client.remote.ListTools(ctx)
	if err != nil {
		return nil, client.mapError(ctx, err)
	}
	if err = ValidateTools(out); err != nil {
		return nil, invalidResponse("invalid Tool definitions")
	}
	return out, nil
}

// Call preserves the complete raw result and structured call error. Host Runtime
// alone validates the result and applies the definition's failure policy.
func (client *Client) Call(ctx context.Context, request CallRequest) (tools.ExecutionResult, error) {
	empty := tools.ExecutionResult{}
	if err := ctx.Err(); err != nil {
		return empty, client.mapError(ctx, err)
	}
	if ValidateCall(request) != nil {
		return empty, invalidResponse("invalid Tool call")
	}
	select {
	case client.pending <- struct{}{}:
		defer func() { <-client.pending }()
	default:
		return empty, &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity"}
	}
	response, err := client.remote.CallTool(ctx, request)
	if err != nil {
		return empty, client.mapError(ctx, err)
	}
	raw, err := json.Marshal(response)
	if err != nil || len(raw) > MaxResultBytes {
		return empty, invalidResponse("oversized Tool response")
	}
	result := empty
	if response.Result != nil {
		result, err = decodeResult(*response.Result)
		if err != nil {
			return empty, invalidResponse("invalid canonical Tool content")
		}
	} else if response.Error == nil {
		return empty, invalidResponse("missing Tool result")
	}
	if response.Error != nil {
		e := response.Error
		if len(e.Message) > MaxErrorMessage || e.Message == "" {
			return empty, invalidResponse("invalid Tool error")
		}
		switch e.Code {
		case CodeToolError, CodeHostValidation, CodeTransport, CodeBackpressure, CodeCancelled, CodeTimeout, CodePanic, CodeRetired:
		default:
			return empty, invalidResponse("unknown error category")
		}
		if e.Code == CodeCancelled {
			e = contract.NewCallError(e.Code, e.Message, context.Canceled)
		}
		if e.Code == CodeTimeout {
			e = contract.NewCallError(e.Code, e.Message, context.DeadlineExceeded)
		}
		return result, e
	}
	return result, nil
}

func (client *Client) mapError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return contract.NewCallError(CodeCancelled, "Tool call cancelled", context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return contract.NewCallError(CodeTimeout, "Tool call timed out", context.DeadlineExceeded)
	}
	if client.loader != nil && client.loader.Exited() {
		return &CallError{Code: CodeProcessExit, Message: "Tool process exited"}
	}
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		return contract.NewCallError(CodeCancelled, "Tool call cancelled", context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return contract.NewCallError(CodeTimeout, "Tool call timed out", context.DeadlineExceeded)
	}
	if status.Code(err) == codes.DeadlineExceeded {
		return contract.NewCallError(CodeTimeout, "Tool call timed out", context.DeadlineExceeded)
	}
	if status.Code(err) == codes.ResourceExhausted {
		return &CallError{Code: CodeHostValidation, Message: "oversized message"}
	}
	return &CallError{Code: CodeTransport, Message: "Tool process unavailable"}
}
