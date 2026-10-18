// Package process runs ordinary JSON Tool plugins in trusted go-plugin/gRPC processes.
// Provider bridges tool descriptions and JSON calls; gordis-rpc owns process
// sessions and lifecycle coordination. Plugins require tools.RuntimeKey and may
// require explicitly declared Host imports, but provide no services themselves.
// Tools depend on arguments and business config, without Session cwd or Agent/Turn
// identity. Successful results contain a JSON Value and one matching TextBlock;
// rich results are rejected.
package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
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
	MaxToolTimeout      = 30 * time.Second
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
)

type FailureMode = contract.FailureMode

const (
	FailureResult = contract.FailureResult
	FailureAbort  = contract.FailureAbort
)

type Tool = contract.Tool
type CallRequest = contract.CallRequest
type CallResponse = contract.CallResponse

const (
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
		request.ToolCallID == "" {
		return errors.New("toolprocess: call requires tool and tool-call identity")
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
		end := MaxErrorMessage
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end]
	}
	return message
}
