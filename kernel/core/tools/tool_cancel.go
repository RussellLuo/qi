package tools

import (
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// AbortedBeforeDispatchResult records a known live cancellation without dispatch.
func AbortedBeforeDispatchResult() ExecutionResult {
	return ExecutionResult{
		IsError: true,
		Content: []protocol.ContentBlock{protocol.TextBlock{Text: "Error: tool call aborted before dispatch"}},
		Error: &Failure{
			Message: "tool call aborted before dispatch",
			Info:    &protocol.ToolFailureInfo{Name: "AbortError", Code: "ABORTED_BEFORE_DISPATCH"},
		},
	}
}
