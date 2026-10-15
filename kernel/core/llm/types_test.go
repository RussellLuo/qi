package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func TestLLMRequestClonePreservesAndIsolatesMessagesAndTools(t *testing.T) {
	request := Request{
		Config: protocol.CallConfig{
			Provider: "fake",
			Model:    "model",
			Stop:     []string{"end"},
		},
		Messages: []protocol.Message{protocol.NewTextMessage("message-1", protocol.RoleUser, protocol.SourceUser, "hello")},
		Tools:    []tools.Schema{{Name: "echo", Parameters: tools.JSONSchema(`{"type":"object"}`)}},
	}
	cloned := request.Clone()
	if len(cloned.Messages) != 1 ||
		len(cloned.Tools) != 1 ||
		cloned.Messages[0].ID != "message-1" ||
		cloned.Tools[0].Name != "echo" {
		t.Fatalf("clone lost request values: %#v", cloned)
	}

	cloned.Messages[0].Content[0] = protocol.TextBlock{Text: "changed"}
	cloned.Tools[0].Parameters[0] = '['
	cloned.Config.Stop[0] = "changed"
	if request.Messages[0].Content[0].(protocol.TextBlock).Text != "hello" ||
		request.Tools[0].Parameters[0] != '{' ||
		request.Config.Stop[0] != "end" {
		t.Fatal("clone shares mutable request data")
	}
}

func TestRequestAndHistoryCloneEmptyCollections(t *testing.T) {
	for _, request := range []Request{{}, {Messages: []protocol.Message{}, Tools: []tools.Schema{}, ToolHistory: ToolHistory{Tools: []tools.Schema{}, Updates: []ToolHistoryUpdate{}}}} {
		cloned := request.Clone()
		if cloned.Messages == nil || cloned.Tools == nil || cloned.ToolHistory.Tools == nil || cloned.ToolHistory.Updates == nil {
			t.Fatal("request clone must allocate empty message, schema and update lists")
		}
		encoded, err := json.Marshal(cloned.ToolHistory)
		if err != nil || string(encoded) != `{"tools":[],"updates":[]}` {
			t.Fatalf("empty history JSON = %s, %v", encoded, err)
		}
	}
}

func TestHistoryAndModelInfoClone(t *testing.T) {
	history := ToolHistory{
		Tools:   []tools.Schema{{Name: "echo", Parameters: tools.JSONSchema(`{}`)}},
		Updates: []ToolHistoryUpdate{{MessageID: "message", Additions: []tools.Schema{{Name: "read", Parameters: tools.JSONSchema(`{}`)}}}},
	}
	encoded, err := json.Marshal(history)
	if err != nil || string(encoded) != `{"tools":[{"name":"echo","description":"","parameters":{}}],"updates":[{"messageId":"message","additions":[{"name":"read","description":"","parameters":{}}]}]}` {
		t.Fatalf("history JSON = %s, %v", encoded, err)
	}
	var decoded ToolHistory
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded, history) {
		t.Fatalf("history round trip = %+v, %v", decoded, err)
	}
	cloned := history.Clone()
	cloned.Tools[0].Parameters[0] = '['
	cloned.Updates[0].MessageID = "changed"
	cloned.Updates[0].Additions[0].Parameters[0] = '['
	if history.Tools[0].Parameters[0] != '{' || history.Updates[0].MessageID != "message" || history.Updates[0].Additions[0].Parameters[0] != '{' {
		t.Fatal("history clone shares mutable data")
	}
	if (ToolHistory{Updates: []ToolHistoryUpdate{{}}}).Clone().Updates[0].Additions == nil {
		t.Fatal("history clone must allocate empty additions")
	}
	info := ModelInfo{Ref: ModelRef{Provider: "fake", Model: "model"}, Capabilities: []string{"tools"}, Defaults: protocol.CallConfig{Stop: []string{"end"}}}
	clonedInfo := info.Clone()
	clonedInfo.Capabilities[0] = "changed"
	clonedInfo.Defaults.Stop[0] = "changed"
	if info.Capabilities[0] != "tools" || info.Defaults.Stop[0] != "end" || clonedInfo.Ref != info.Ref {
		t.Fatal("model clone changed metadata or shares defaults")
	}
}
