package fstool_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type cwdModel struct{ seen *[]llm.Request }

func (p *cwdModel) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/cwd-model", Requires: []gordis.ServiceSpec{llm.RuntimeKey.Spec()}, New: func() gordis.Plugin { return &cwdModel{p.seen} }}
}
func (*cwdModel) DescribeModels() (modelcatalog.Description, error) {
	return modelcatalog.Provider("cwd", "cwd", nil, []modelcatalog.Entry{{ID: "cwd", Model: "cwd"}})
}
func (p *cwdModel) Activate(_ context.Context, s *gordis.Scope) error {
	runtime, err := s.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	description, err := p.DescribeModels()
	if err != nil {
		return err
	}
	return runtime.Bind(s).RegisterProvider(llm.AdapterRegistration{Route: "cwd", Provider: "cwd", Adapter: cwdAdapter{p.seen}}, description.Models)
}

type cwdAdapter struct{ seen *[]llm.Request }

func (a cwdAdapter) Stream(_ context.Context, request llm.Request) contracts.ChunkStream {
	*a.seen = append(*a.seen, request)
	return func(yield func(protocol.StreamChunk, error) bool) {
		toolResult := false
		for _, message := range request.Messages {
			if message.Role == protocol.RoleTool {
				toolResult = true
			}
		}
		if toolResult {
			for _, chunk := range []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText}, {Type: protocol.ChunkTextDelta, Index: 0, Text: "done"}, {Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: "done"}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}} {
				if !yield(chunk, nil) {
					return
				}
			}
			return
		}
		for i, call := range []protocol.ToolCall{{ID: "read", Name: "read_file", Arguments: `{"file_path":"value.txt"}`}, {ID: "write", Name: "write_file", Arguments: `{"file_path":"created.txt","content":"written"}`}} {
			for _, chunk := range []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: int64(i), BlockType: protocol.BlockToolCall}, {Type: protocol.ChunkToolCallDelta, Index: int64(i), ID: call.ID, Name: &call.Name, ArgumentsDelta: call.Arguments}, {Type: protocol.ChunkBlockEnd, Index: int64(i), Block: protocol.ToolCallBlock{ToolCall: call}}} {
				if !yield(chunk, nil) {
					return
				}
			}
		}
		yield(protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}}, nil)
	}
}

// Public boot -> headless Header.CWD -> AgentLoop -> prepared filesystem call.
// The core package separately verifies propagation with an ordinary domain tool.
func TestSessionCWDReachesRealFileTools(t *testing.T) {
	for _, name := range []string{"left", "right"} {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Chdir(cwd)
			if err := os.WriteFile(filepath.Join(cwd, "value.txt"), []byte(name), 0600); err != nil {
				t.Fatal(err)
			}
			seen := []llm.Request{}
			app, err := application.Compose(bundle.Extension(), application.Extension{Registrations: []registration.PluginRegistration{{Plugin: &cwdModel{&seen}, ProfileVisible: true}}})
			if err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(t.TempDir(), "profile.yaml")
			if err := os.WriteFile(profile, []byte("bundles: [base]\ninstances:\n  - {id: model, plugin: test/cwd-model}\n  - {id: files, plugin: qi/tools/fs}\n  - {id: headless, plugin: qi/cli/headless, config: {model: cwd}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostic bytes.Buffer
			code := boot.Run([]string{"--profile", profile, "--", "do file work"}, launch.Input{Stdout: &out, Stderr: &diagnostic}, app)
			if code != 0 || out.String() != "done\n" || diagnostic.Len() != 0 || len(seen) != 2 {
				t.Fatal(code, out.String(), diagnostic.String(), len(seen))
			}
			read, write := false, false
			for _, message := range seen[1].Messages {
				if message.Role != protocol.RoleTool {
					continue
				}
				if message.IsError != nil && *message.IsError {
					t.Fatal("file tool failed", message)
				}
				for _, block := range message.Content {
					if text, ok := block.(protocol.TextBlock); ok {
						read = read || strings.Contains(text.Text, "1: "+name)
						write = write || strings.Contains(text.Text, "Created file")
					}
				}
			}
			if !read || !write {
				t.Fatal("Session cwd results missing", seen[1].Messages)
			}
			if data, err := os.ReadFile(filepath.Join(cwd, "created.txt")); err != nil || string(data) != "written" {
				t.Fatal(string(data), err)
			}
		})
	}
}
