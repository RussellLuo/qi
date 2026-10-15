package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestSchemaJSONAndClone(t *testing.T) {
	schema := Schema{Name: "echo", Description: "Echo", Parameters: JSONSchema(`{"type":"object"}`)}
	for _, deferred := range []bool{false, true} {
		schema.DeferLoading = deferred
		want := `{"name":"echo","description":"Echo","parameters":{"type":"object"}}`
		if deferred {
			want = `{"deferLoading":true,"name":"echo","description":"Echo","parameters":{"type":"object"}}`
		}
		encoded, err := json.Marshal(schema)
		if err != nil || string(encoded) != want {
			t.Fatalf("schema JSON = %s, %v; want %s", encoded, err, want)
		}
		var decoded Schema
		if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, schema) {
			t.Fatalf("schema round trip = %+v, %v", decoded, err)
		}
	}
	cloned := schema.Clone()
	cloned.Parameters[0] = '['
	if schema.Parameters[0] != '{' {
		t.Fatal("schema clone shares parameters")
	}
	for _, empty := range []JSONSchema{nil, {}} {
		if empty.Clone() != nil {
			t.Fatal("empty schema clone must be nil")
		}
	}
	raw := JSONSchema(`{}`)
	if err := raw.UnmarshalJSON([]byte(`invalid`)); err == nil || string(raw) != `{}` {
		t.Fatal("invalid JSON changed schema")
	}
	data := []byte(`{"type":"string"}`)
	if err := raw.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	data[0] = '['
	if raw[0] != '{' {
		t.Fatal("schema decoder retained input buffer")
	}
}

func TestExecutionAndResultClone(t *testing.T) {
	execution := Execution{
		Identity: ExecutionIdentity{AgentID: "agent", SessionID: "session", Turn: 1, Step: 2},
		CWD:      "/work", Call: protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{}`}, Arguments: json.RawMessage(`{}`),
	}
	clonedExecution := execution.Clone()
	if !reflect.DeepEqual(execution, clonedExecution) {
		t.Fatal("execution clone lost attribution")
	}
	clonedExecution.Arguments[0] = '['
	if execution.Arguments[0] != '{' {
		t.Fatal("execution clone shares arguments")
	}
	reason := "reason"
	result := FailureResult("FAILED", errors.New("failed"))
	result.Error.Info.Reason = &reason
	result.Meta = json.RawMessage(`{}`)
	result.AdditionalContexts = []protocol.Message{protocol.NewTextMessage("context", protocol.RoleUser, protocol.SourceUser, "context")}
	cloned := result.Clone()
	if !reflect.DeepEqual(result, cloned) || cloned.Validate() != nil {
		t.Fatal("result clone lost values")
	}
	cloned.Error.Message = "changed"
	*cloned.Error.Info.Reason = "changed"
	cloned.Content[0] = protocol.TextBlock{Text: "changed"}
	cloned.Meta[0] = '['
	cloned.AdditionalContexts[0].Content[0] = protocol.TextBlock{Text: "changed"}
	if result.Error.Message != "failed" || reason != "reason" || result.Meta[0] != '{' ||
		result.Content[0].(protocol.TextBlock).Text != "Error: failed" ||
		result.AdditionalContexts[0].Content[0].(protocol.TextBlock).Text != "context" {
		t.Fatal("result clone shares mutable data")
	}
	for _, contexts := range [][]protocol.Message{nil, {}} {
		cloned := (ExecutionResult{AdditionalContexts: contexts}).Clone()
		if (cloned.AdditionalContexts == nil) != (contexts == nil) {
			t.Fatal("result clone changed nil/empty additional contexts")
		}
	}
}

func TestJSONToolAndFailureResults(t *testing.T) {
	value := json.RawMessage(`{"ok":true}`)
	invoke := JSONTool(func(context.Context, Execution) (json.RawMessage, error) { return value, nil })
	result, err := invoke(context.Background(), Execution{})
	if err != nil || result.Validate() != nil || result.Content[0].(protocol.TextBlock).Text != string(value) {
		t.Fatalf("JSON tool result = %+v, %v", result, err)
	}
	value[0] = '['
	if result.Value[0] != '{' {
		t.Fatal("JSON tool result shares returned value")
	}
	failed := errors.New("invoke failed")
	invoke = JSONTool(func(context.Context, Execution) (json.RawMessage, error) { return nil, failed })
	result, err = invoke(context.Background(), Execution{})
	if !errors.Is(err, failed) || !reflect.DeepEqual(result, ExecutionResult{}) {
		t.Fatal("JSON tool changed error or returned a partial result")
	}
	for _, result := range []ExecutionResult{FailureResult("FAILED", failed), AbortedBeforeDispatchResult()} {
		if err := result.Validate(); err != nil || !result.IsError || result.Value != nil {
			t.Fatalf("failure result = %+v, %v", result, err)
		}
	}
	if aborted := AbortedBeforeDispatchResult(); aborted.Error.Info.Name != "AbortError" || aborted.Error.Info.Code != "ABORTED_BEFORE_DISPATCH" {
		t.Fatal("cancellation failure changed")
	}
	for _, result := range []ExecutionResult{
		{}, {Value: json.RawMessage(`null`)},
		{IsError: true, Content: []protocol.ContentBlock{}},
		{Value: json.RawMessage(`null`), Content: []protocol.ContentBlock{}, Meta: json.RawMessage(`invalid`)},
	} {
		if result.Validate() == nil {
			t.Fatalf("accepted invalid result: %+v", result)
		}
	}
}
