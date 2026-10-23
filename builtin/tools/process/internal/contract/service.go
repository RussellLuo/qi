// Package contract defines the tool Provider API without a dependency on RPC.
package contract

import (
	"context"

	"github.com/RussellLuo/gordis"
)

//go:generate go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Provider -key Key -version Version -package bindings -out ../bindings/provider.go

const ID = "qi/tools/provider"
const Version = "2"

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

// Byte slices preserve JSON bytes, including whitespace and large integers.
type Tool struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Parameters   []byte      `json:"parameters"`
	Output       []byte      `json:"output"`
	TimeoutNanos int64       `json:"timeoutNanos"`
	FailureMode  FailureMode `json:"failureMode"`
	DeferLoading bool        `json:"deferLoading"`
}

type CallRequest struct {
	AgentID       string `json:"agentId"`
	SessionID     string `json:"sessionId"`
	Turn          int64  `json:"turn"`
	Step          int64  `json:"step"`
	CWD           string `json:"cwd"`
	Tool          string `json:"tool"`
	ToolCallID    string `json:"toolCallId"`
	CallArguments string `json:"callArguments"`
	Arguments     []byte `json:"arguments"`
}

type CallResponse struct {
	Result *Result    `json:"result,omitempty"`
	Error  *CallError `json:"error,omitempty"`
}

type Result struct {
	IsError       bool     `json:"isError"`
	Value         []byte   `json:"value"`
	Failure       *Failure `json:"failure"`
	Content       []byte   `json:"content"`
	Meta          []byte   `json:"meta"`
	Contexts      [][]byte `json:"contexts"`
	ConcludesTurn bool     `json:"concludesTurn"`
}
type Failure struct {
	Message string       `json:"message"`
	Info    *FailureInfo `json:"info"`
}
type FailureInfo struct {
	Name   string  `json:"name"`
	Code   string  `json:"code"`
	Reason *string `json:"reason"`
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
	// Keep the business message identical when Host Runtime turns a raw error
	// into its failure result. Code remains available through errors.As.
	return e.Message
}

func (e *CallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}
