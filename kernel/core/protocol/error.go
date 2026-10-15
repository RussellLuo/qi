package protocol

import (
	"context"
	"errors"
	"fmt"
)

type ErrorCode string

const (
	ErrorInvalidArgument ErrorCode = "invalid_argument"
	ErrorNotFound        ErrorCode = "not_found"
	ErrorConflict        ErrorCode = "conflict"
	ErrorCancelled       ErrorCode = "cancelled"
	ErrorStream          ErrorCode = "stream_error"
	ErrorTool            ErrorCode = "tool_error"
	ErrorMaxSteps        ErrorCode = "max_steps"
	ErrorInternal        ErrorCode = "internal"
)

type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func NewErrorDetail(code ErrorCode, err error) *ErrorDetail {
	if err == nil {
		return nil
	}
	if code == "" {
		code = ErrorInternal
	}

	return &ErrorDetail{Code: code, Message: err.Error()}
}

func (e *ErrorDetail) Validate() error {
	if e == nil || e.Code == "" || e.Message == "" {
		return fmt.Errorf("%w: error detail requires code and message", ErrInvalidProtocol)
	}

	return nil
}

func ErrorCodeFor(err error, fallback ErrorCode) ErrorCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorCancelled
	}
	if fallback == "" {
		return ErrorInternal
	}

	return fallback
}
