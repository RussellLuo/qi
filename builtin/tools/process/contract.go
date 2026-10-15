// Package process runs ordinary JSON Tool plugins in trusted stdio processes.
// It reuses ToolPackage/1 without transporting Go services or Scope objects.
// Plugins must require only tools.RuntimeKey and provide no services. Choosing
// this adapter declares that Tools depend only on arguments and business config,
// without Session cwd or Agent/Turn identity. Successful results must contain
// a JSON Value and one TextBlock with the same bytes; rich results are rejected.
package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	ContractID      = "qi.tool-package/1"
	HostAPIVersion  = 1
	CapabilityTools = "tools"

	MethodDescribe  = "toolpackage.describe"
	MethodListTools = "toolpackage.list-tools"
	MethodCall      = "toolpackage.call"

	MaxTools            = 32
	MaxNameBytes        = 128
	MaxDescriptionBytes = 2048
	MaxSchemaBytes      = 64 * 1024
	MaxArgumentsBytes   = 128 * 1024
	MaxResultBytes      = 128 * 1024
	MaxErrorMessage     = 1024
	MaxToolTimeout      = 30 * time.Second
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type FailureMode string

const (
	FailureResult FailureMode = "result"
	FailureAbort  FailureMode = "abort"
)

type Metadata struct {
	ID           string   `json:"id"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

type Tool struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Parameters    json.RawMessage `json:"parameters"`
	Output        json.RawMessage `json:"output"`
	TimeoutMillis int64           `json:"timeoutMillis"`
	FailureMode   FailureMode     `json:"failureMode"`
}

type Invocation struct {
	RequestID    string `json:"requestId"`
	ToolCallID   string `json:"toolCallId"`
	InvocationID string `json:"invocationId"`
}

type CallRequest struct {
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
	Invocation Invocation      `json:"invocation"`
}

type CallResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *CallError      `json:"error,omitempty"`
}

const (
	CodeToolError      = "tool_error"
	CodeHostValidation = "host_validation"
	CodeTransport      = "transport"
	CodeProcessExit    = "process_exit"
	CodeBackpressure   = "backpressure"
	CodeCancelled      = "cancelled"
	CodeTimeout        = "timeout"
)

type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
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

func NewToolError(message string) *CallError {
	return &CallError{Code: CodeToolError, Message: boundedMessage(message, "tool execution failed")}
}

func ValidateMetadata(metadata Metadata) error {
	if !identifierPattern.MatchString(metadata.ID) || !versionPattern.MatchString(metadata.Version) {
		return errors.New("toolprocess: invalid package metadata identity")
	}
	if len(metadata.Capabilities) != 1 || metadata.Capabilities[0] != CapabilityTools {
		return errors.New("toolprocess: unsupported package capabilities")
	}
	return nil
}

func ValidateTools(definitions []Tool) error {
	if len(definitions) == 0 || len(definitions) > MaxTools {
		return fmt.Errorf("toolprocess: tool count must be between 1 and %d", MaxTools)
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if !identifierPattern.MatchString(definition.Name) || len(definition.Name) > MaxNameBytes {
			return errors.New("toolprocess: invalid tool name")
		}
		if _, ok := seen[definition.Name]; ok {
			return fmt.Errorf("toolprocess: duplicate tool name %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		if definition.Description == "" || len(definition.Description) > MaxDescriptionBytes {
			return fmt.Errorf("toolprocess: invalid description for %q", definition.Name)
		}
		if err := validateJSONDocument(definition.Parameters, MaxSchemaBytes, "parameter schema"); err != nil {
			return fmt.Errorf("toolprocess: tool %q: %w", definition.Name, err)
		}
		if err := validateJSONDocument(definition.Output, MaxSchemaBytes, "output schema"); err != nil {
			return fmt.Errorf("toolprocess: tool %q: %w", definition.Name, err)
		}
		if definition.TimeoutMillis < 0 || definition.TimeoutMillis > MaxToolTimeout.Milliseconds() {
			return fmt.Errorf("toolprocess: invalid timeout for %q", definition.Name)
		}
		if definition.FailureMode != FailureResult && definition.FailureMode != FailureAbort {
			return fmt.Errorf("toolprocess: invalid failure mode for %q", definition.Name)
		}
	}
	return nil
}

func ValidateCall(request CallRequest) error {
	if !identifierPattern.MatchString(request.Tool) ||
		request.Invocation.RequestID == "" || request.Invocation.ToolCallID == "" || request.Invocation.InvocationID == "" {
		return errors.New("toolprocess: call requires tool and invocation identities")
	}
	return validateJSONDocument(request.Arguments, MaxArgumentsBytes, "arguments")
}

func validateJSONDocument(raw json.RawMessage, limit int, label string) error {
	if len(raw) == 0 || len(raw) > limit || !json.Valid(raw) {
		return fmt.Errorf("invalid or oversized %s", label)
	}
	return nil
}

func boundedMessage(message, fallback string) string {
	if message == "" {
		message = fallback
	}
	if len(message) > MaxErrorMessage {
		message = message[:MaxErrorMessage]
	}
	return message
}
