// Package contract defines the tool Provider API without a dependency on RPC.
package contract

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis-rpc/cmd/rpcgen -type Provider -key Key -version Version -package bindings -out ../bindings/provider.go

const ID = "qi/tools/provider"
const Version = "1"

type Provider interface {
	ListTools(context.Context) ([]Tool, error)
	CallTool(context.Context, CallRequest) (CallResponse, error)
}

var Key = gordis.NewKey[Provider](ID)

type FailureMode string

const (
	FailureResult FailureMode = "result"
	FailureAbort  FailureMode = "abort"
)

type Tool struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Parameters    json.RawMessage `json:"parameters"`
	Output        json.RawMessage `json:"output"`
	TimeoutMillis int64           `json:"timeoutMillis"`
	FailureMode   FailureMode     `json:"failureMode"`
}

// CallRequest uses the Runtime call identity; the RPC session fixes generation.
type CallRequest struct {
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	ToolCallID string          `json:"toolCallId"`
}

type CallResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *CallError      `json:"error,omitempty"`
}

type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error  `json:"-"`
}

// NewCallError attaches a local cause, which never crosses the wire.
func NewCallError(code, message string, cause error) *CallError {
	return &CallError{Code: code, Message: message, cause: cause}
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("toolprocess: %s: %s", e.Code, e.Message)
}

func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
