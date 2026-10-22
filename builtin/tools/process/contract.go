// Package process bridges activation-time Tool contributions over a versioned
// gordis-rpc contract. Host Runtime owns execution policy; Child calls only the
// original business Execute under its owner lease. Host and Child must be rebuilt
// together when the wire contract changes.
package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/RussellLuo/qi/builtin/tools/process/internal/contract"
)

const (
	ContractID = contract.ID

	MaxTools            = 32
	MaxNameBytes        = 128
	MaxDescriptionBytes = 2048
	MaxSchemaBytes      = 64 * 1024
	MaxArgumentsBytes   = 128 * 1024
	MaxResultBytes      = 128 * 1024
	MaxErrorMessage     = 1024
)

type FailureMode = contract.FailureMode

const (
	FailureResult = contract.FailureResult
	FailureAbort  = contract.FailureAbort
)

type Tool = contract.Tool
type CallRequest = contract.CallRequest
type CallResponse = contract.CallResponse
type Result = contract.Result
type Failure = contract.Failure
type FailureInfo = contract.FailureInfo

const (
	CodePanic          = "panic"
	CodeRetired        = "retired"
	CodeToolError      = "tool_error"
	CodeHostValidation = "host_validation"
	CodeTransport      = "transport"
	CodeProcessExit    = "process_exit"
	CodeBackpressure   = "backpressure"
	CodeCancelled      = "cancelled"
	CodeTimeout        = "timeout"
)

type CallError = contract.CallError

func NewToolError(message string) *CallError {
	return &CallError{Code: CodeToolError, Message: boundedMessage(message, "tool execution failed")}
}

func ValidateTools(definitions []Tool) error {
	if len(definitions) == 0 || len(definitions) > MaxTools {
		return fmt.Errorf("toolprocess: tool count must be between 1 and %d", MaxTools)
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name == "" || strings.Contains(definition.Name, "/") || len(definition.Name) > MaxNameBytes {
			return errors.New("toolprocess: invalid tool name")
		}
		if _, ok := seen[definition.Name]; ok {
			return fmt.Errorf("toolprocess: duplicate tool name %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		if len(definition.Description) > MaxDescriptionBytes {
			return fmt.Errorf("toolprocess: invalid description for %q", definition.Name)
		}
		if err := validateJSONDocument(definition.Parameters, MaxSchemaBytes, "parameter schema"); err != nil {
			return fmt.Errorf("toolprocess: tool %q: %w", definition.Name, err)
		}
		if err := validateJSONDocument(definition.Output, MaxSchemaBytes, "output schema"); err != nil {
			return fmt.Errorf("toolprocess: tool %q: %w", definition.Name, err)
		}
		if definition.FailureMode != FailureResult && definition.FailureMode != FailureAbort {
			return fmt.Errorf("toolprocess: invalid failure mode for %q", definition.Name)
		}
	}
	return nil
}

func ValidateCall(request CallRequest) error {
	if request.Tool == "" || strings.Contains(request.Tool, "/") || len(request.Tool) > MaxNameBytes ||
		request.ToolCallID == "" {
		return errors.New("toolprocess: call requires tool and tool-call identity")
	}
	if request.CWD != "" && !filepath.IsAbs(request.CWD) {
		return errors.New("toolprocess: cwd must be absolute")
	}
	if len(request.CallArguments) > MaxArgumentsBytes {
		return errors.New("toolprocess: oversized original arguments")
	}
	// JSON/schema validation belongs exclusively to Host Runtime. Middleware may
	// retain non-JSON original Call.Arguments while supplying valid Arguments.
	if len(request.Arguments) > MaxArgumentsBytes {
		return errors.New("toolprocess: oversized arguments")
	}
	return nil
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
		end := MaxErrorMessage
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end]
	}
	return message
}
