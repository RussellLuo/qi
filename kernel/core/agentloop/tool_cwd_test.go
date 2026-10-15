package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Exercise the actual Session -> Loop -> PreparedRun -> Tool Snapshot chain.
// Filesystem cwd is the Session's header, not the process or another Session.
func TestSessionCWDReachesPreparedCalls(t *testing.T) {
	adapter := &scriptedAdapter{Version: "cwd", Scripts: []scriptedScript{}}
	for range 2 {
		adapter.Scripts = append(adapter.Scripts,
			scriptedScript{Chunks: toolChunks(
				protocol.ToolCall{ID: "read", Name: "read_file", Arguments: `{"file_path":"value.txt"}`},
				protocol.ToolCall{ID: "write", Name: "write_file", Arguments: `{"file_path":"created.txt","content":"written"}`},
			)}, scriptedScript{Chunks: textChunks("done")})
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)
	seen := make(map[string]string)
	definitions := []tools.Definition{}
	for _, name := range []string{"read_file", "write_file"} {
		definitions = append(definitions, tools.Definition{Name: name, Description: "Observe prepared call context", Parameters: tools.JSONSchema(`{"type":"object"}`), Output: tools.JSONSchema(`{"type":"object"}`), Execute: tools.JSONTool(func(_ context.Context, e tools.Execution) (json.RawMessage, error) {
			seen[name+"/"+string(e.Identity.SessionID)] = e.CWD
			return json.RawMessage(`{}`), nil
		})})
	}
	if _, err := h.loop.coordinator.Tools.RegisterBatch(h.owner, definitions); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"left", "right"} {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, "value.txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		id := protocol.SessionID(name)
		header, err := session.CreateHeader(id, nil)
		if err != nil {
			t.Fatal(err)
		}
		header.CWD = &cwd
		h.current, err = h.store.Create(context.Background(), session.CreateRequest{ID: id, Header: &header})
		if err != nil {
			t.Fatal(err)
		}
		h.agent = &testAgentRuntime{sessionID: id}
		events, err := h.drive(context.Background(), Config{MaxSteps: 4, LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range []string{"read_file", "write_file"} {
			if seen[tool+"/"+name] != cwd {
				t.Fatal("prepared call lost Session cwd", seen)
			}
		}
		assertTerminalConsistency(t, h.current, events, protocol.ReasonCompleted)
		if err := h.reservation.Close(); err != nil {
			t.Fatal(err)
		}
		h.reservation = nil
	}
}
