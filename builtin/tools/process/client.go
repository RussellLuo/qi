package process

import (
	"context"
	"encoding/json"
	"errors"

	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/contract"
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

func (client *Client) Call(ctx context.Context, request CallRequest) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, client.mapError(ctx, err)
	}
	if err := ValidateCall(request); err != nil {
		return nil, invalidResponse("invalid Tool call")
	}
	select {
	case client.pending <- struct{}{}:
		defer func() { <-client.pending }()
	default:
		return nil, &CallError{Code: CodeBackpressure, Message: "Tool process is at capacity"}
	}
	response, err := client.remote.CallTool(ctx, request)
	if err != nil {
		return nil, client.mapError(ctx, err)
	}
	if response.Error != nil {
		if len(response.Result) != 0 || len(response.Error.Message) > MaxErrorMessage {
			return nil, invalidResponse("invalid error response")
		}
		switch response.Error.Code {
		case CodeToolError, CodeHostValidation, CodeTransport, CodeBackpressure, CodeCancelled, CodeTimeout:
		default:
			return nil, invalidResponse("unknown error category")
		}
		if response.Error.Code == CodeCancelled {
			response.Error = contract.NewCallError(response.Error.Code, response.Error.Message, context.Canceled)
		}
		if response.Error.Code == CodeTimeout {
			response.Error = contract.NewCallError(response.Error.Code, response.Error.Message, context.DeadlineExceeded)
		}
		return nil, response.Error
	}
	if validateJSONDocument(response.Result, MaxResultBytes, "result") != nil {
		return nil, invalidResponse("invalid result")
	}
	return response.Result, nil
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
