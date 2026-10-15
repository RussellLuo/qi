package jsonl_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/qi/builtin/session/jsonl"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func persistentRuntime(t *testing.T, dir string, check func(protocol.SessionHeader) error) (*session.Runtime, *jsonl.Owner) {
	t.Helper()
	o, err := jsonl.NewOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolveOwner(o), HeaderCheck: check})
	t.Cleanup(func() {
		if err := r.Close(context.Background()); err != nil {
			t.Error(err)
			return
		}
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, o
}

func persistentFactory(t *testing.T, r *session.Runtime, initialize agent.Initializer) agent.Factory {
	t.Helper()
	prompts, toolRuntime, llmRuntime, middleware := prompt.NewRuntime(), tools.NewRuntime(), llm.NewRuntime(), agentmiddleware.NewRuntime()
	owner := newTestOwner("persistent-factory", 1)
	adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: textChunks("reply")}, {Chunks: textChunks("reply")}, {Chunks: textChunks("reply")}}}
	if _, err := llmRuntime.RegisterAdapter(owner, llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: adapter}); err != nil {
		t.Fatal(err)
	}
	driver := agentloop.New(runprep.Coordinator{Prompts: prompts, Tools: toolRuntime, LLM: llmRuntime, Middleware: middleware})
	f, err := agent.NewFactory(agent.FactoryOptions{Registry: agent.NewRegistry(), Store: r, Driver: driver, Initialize: initialize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(context.Background()); err != nil {
			t.Error(err)
		}
		middleware.Close()
		llmRuntime.Close()
		toolRuntime.Close()
		prompts.Close()
		if err := owner.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return f
}

func submitPersistent(t *testing.T, h agent.Handle, text string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, events, err := h.Agent().Submit(ctx, agent.Input{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range events {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Agent().WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
	facts := collect(t, h.Session())
	last := facts[len(facts)-1]
	if last.Type != protocol.TurnEnded || protocol.EventPayload[protocol.TurnEndData](last).Reason.Kind != "completed" {
		t.Fatal("turn not durably settled", last)
	}
}

func persistentConfig() agent.AgentOptions {
	return agent.AgentOptions{Config: agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}}}
}

func TestRuntimeJSONLFactoryCreateDisposeResume(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, _ := persistentRuntime(t, dir, nil)
	f := persistentFactory(t, r, nil)
	h, err := f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}, Agent: persistentConfig()})
	if err != nil {
		t.Fatal(err)
	}
	submitPersistent(t, h, "first")
	facts := collect(t, h.Session())
	if _, err := f.Resume(ctx, agent.ResumeOptions{SessionID: "history", Agent: persistentConfig()}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal("live Resume borrowed instance", err)
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "history"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	before, err := os.ReadFile(onlyLog(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	h, err = f.Resume(ctx, agent.ResumeOptions{SessionID: "history", Agent: persistentConfig()})
	if err != nil {
		t.Fatal(err)
	}
	restored := collect(t, h.Session())
	if len(restored) != len(facts)+1 || restored[len(facts)].Type != protocol.SessionEndSeed {
		t.Fatal("recovery marker", restored)
	}
	submitPersistent(t, h, "second")
	messages, err := h.Session().Messages(ctx)
	if err != nil || len(messages) != 4 {
		t.Fatal("lost dialog on same-process Resume", len(messages), err)
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(onlyLog(t, dir))
	if err != nil || !bytes.HasPrefix(after, before) {
		t.Fatal("Resume changed committed prefix", err)
	}
}

type persistenceInitializer func(context.Context, agent.Initialization, agent.CleanupRegistrar) error

func (f persistenceInitializer) Initialize(ctx context.Context, i agent.Initialization, r agent.CleanupRegistrar) error {
	return f(ctx, i, r)
}

func TestRuntimeJSONLFactoryFailedTransactionsKeepCorrectHistory(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("initialization")
	for _, operation := range []string{"create", "resume", "attach"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			r, _ := persistentRuntime(t, dir, nil)
			var current session.Session
			if operation != "create" {
				var err error
				current, err = r.Create(ctx, session.CreateRequest{ID: "history"})
				if err != nil {
					t.Fatal(err)
				}
				appendCompletedTurn(t, current, 1, "first", "kept")
				if operation == "resume" {
					if err := r.Release(ctx, current); err != nil {
						t.Fatal(err)
					}
				}
			}
			f := persistentFactory(t, r, persistenceInitializer(func(context.Context, agent.Initialization, agent.CleanupRegistrar) error { return cause }))
			var err error
			switch operation {
			case "create":
				_, err = f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}, Agent: persistentConfig()})
			case "resume":
				_, err = f.Resume(ctx, agent.ResumeOptions{SessionID: "history", Agent: persistentConfig()})
			case "attach":
				_, err = f.Attach(ctx, agent.AttachOptions{Session: current, Agent: persistentConfig()})
			}
			if !errors.Is(err, cause) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			logs := 0
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".jsonl") {
					logs++
				}
			}
			if operation == "create" {
				if logs != 0 {
					t.Fatal("failed Create retained history")
				}
				next, err := r.Create(ctx, session.CreateRequest{ID: "history"})
				if err != nil {
					t.Fatal("creation rollback left physical ownership", err)
				}
				if err := r.RollbackCreate(ctx, next); err != nil {
					t.Fatal(err)
				}
			} else {
				if logs != 1 {
					t.Fatal("deleted existing history")
				}
				if operation == "resume" {
					current, err = r.Open(ctx, "history")
					if err != nil {
						t.Fatal("failed Resume leaked file handle", err)
					}
				} else if got, err := r.Get(ctx, "history"); err != nil || got != current {
					t.Fatal("borrowed Attach released instance", err)
				}
				messages, err := current.Messages(ctx)
				if err != nil || len(messages) != 1 {
					t.Fatal("existing messages lost", err)
				}
			}
		})
	}
}

// The subprocess runs the current Go test binary; no Go compiler, provider,
// HTTP server or in-process retained state participates in child recovery.
func TestRuntimeJSONLProcessChild(t *testing.T) {
	mode := os.Getenv("QI_JSONL_PROCESS_MODE")
	if mode == "" {
		return
	}
	dir := os.Getenv("QI_JSONL_PROCESS_DIR")
	if mode == "lock" {
		p, err := jsonl.NewPersistence(dir)
		if p != nil {
			_ = p.Close()
		}
		if !errors.Is(err, jsonl.ErrStoreLocked) {
			t.Fatal("child acquired held writer lock", err)
		}
		return
	}
	ctx := context.Background()
	r, _ := persistentRuntime(t, dir, nil)
	f := persistentFactory(t, r, nil)
	var h agent.Handle
	var err error
	if mode == "create" || mode == "crash" {
		h, err = f.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "history"}, Agent: persistentConfig()})
	} else {
		h, err = f.Resume(ctx, agent.ResumeOptions{SessionID: "history", Agent: persistentConfig()})
	}
	if err != nil {
		t.Fatal(err)
	}
	text := "first"
	if mode == "resume" {
		messages, err := h.Session().Messages(ctx)
		if err != nil || len(messages) != 2 {
			t.Fatal("restart lost first turn", err)
		}
		text = "second"
	}
	submitPersistent(t, h, text)
	if mode == "crash" {
		// Skip every Handle/Runtime/backend cleanup to prove kernel lock release
		// and durability of a settled turn across abrupt process exit.
		os.Exit(23)
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
}

func runJSONLChild(t *testing.T, dir, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeJSONLProcessChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "QI_JSONL_PROCESS_DIR="+dir, "QI_JSONL_PROCESS_MODE="+mode)
	out, err := cmd.CombinedOutput()
	if mode == "crash" {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 {
			t.Fatalf("crash child: %v\n%s", err, out)
		}
	} else if err != nil {
		t.Fatalf("%s child: %v\n%s", mode, err, out)
	}
}

func TestRuntimeJSONLIndependentProcessRestart(t *testing.T) {
	for _, mode := range []string{"create", "crash"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			runJSONLChild(t, dir, mode)
			before, err := os.ReadFile(onlyLog(t, dir))
			if err != nil {
				t.Fatal(err)
			}
			runJSONLChild(t, dir, "resume")
			after, err := os.ReadFile(onlyLog(t, dir))
			if err != nil || !bytes.HasPrefix(after, before) {
				t.Fatal("restart changed committed bytes", err)
			}
			r, p := persistentRuntime(t, dir, nil)
			runJSONLChild(t, dir, "lock")
			current, err := r.Open(context.Background(), "history")
			if err != nil {
				t.Fatal(err)
			}
			messages, err := current.Messages(context.Background())
			if err != nil || len(messages) != 4 {
				t.Fatal("cross-process history did not retain both turns", len(messages), err)
			}
			if err := r.Release(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJSONLRuntimeFactsRemainProtocolEncoded(t *testing.T) {
	dir := t.TempDir()
	r, _ := persistentRuntime(t, dir, nil)
	h, err := r.Create(context.Background(), session.CreateRequest{ID: "format"})
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedTurn(t, h, 1, "u", "format")
	data, err := os.ReadFile(onlyLog(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if _, err := protocol.DecodeSessionHeader(lines[0]); err != nil {
		t.Fatal(err)
	}
	facts := collect(t, h)
	if len(lines) != len(facts)+1 {
		t.Fatal("extra persistence records")
	}
	for i, line := range lines[1:] {
		encoded, err := protocol.EncodeSessionEvent(facts[i])
		if err != nil || !bytes.Equal(line, encoded) {
			t.Fatal("persistence changed protocol framing", err)
		}
	}
}

func TestRuntimeJSONLRestartsAndRepairsInterruptedTail(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(ctx, session.CreateRequest{ID: "persistent"})
	if err != nil {
		t.Fatal(err)
	}
	appendCompletedTurn(t, current, 1, "message-1", "before restart")
	want := collect(t, current)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := onlyLog(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	committedSize := info.Size()
	committedPrefix, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"apiVersion":"qi.profile/v1alpha1"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := openRecovered(ctx, reopened, "persistent")
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, restored)
	if len(got) != len(want)+1 {
		t.Fatalf("restored events = %d, want %d", len(got), len(want)+1)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := protocol.EncodeSessionEvent(got[len(want)])
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != committedSize+int64(len(marker))+1 {
		t.Fatalf("size with restore marker = %d, original %d", info.Size(), committedSize)
	}
	restoredBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredBytes[:committedSize], committedPrefix) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatal("restore changed committed prefix")
	}
	appendCompletedTurn(t, restored, 2, "message-2", "after restart")
	if messages, err := restored.Messages(ctx); err != nil || len(messages) != 2 {
		t.Fatalf("messages after restart = %d, %v", len(messages), err)
	}
}

func TestRuntimeJSONLRejectsCommittedCorruption(t *testing.T) {
	dir := t.TempDir()
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), session.CreateRequest{ID: "corrupt"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(onlyLog(t, dir), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.Open(context.Background(), "corrupt")
	if !errors.Is(err, jsonl.ErrCorruptLog) {
		t.Fatalf("reopen error = %v, want ErrCorruptLog", err)
	}
}

func appendCompletedTurn(t *testing.T, current session.Session, turn protocol.TurnID, message protocol.MessageID, text string) {
	t.Helper()
	for _, event := range []protocol.SessionEvent{
		protocol.NewTurnStarted(turn),
		protocol.NewMessageEvent(protocol.UserMessage, turn, 0, protocol.NewTextMessage(message, protocol.RoleUser, protocol.SourceUser, text)),
		protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "completed"}),
	} {
		if _, err := current.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func collect(t *testing.T, current session.Session) []protocol.SessionEvent {
	t.Helper()
	var result []protocol.SessionEvent
	for event, err := range current.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, event)
	}
	return result
}

func onlyLog(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("JSONL files = %v", matches)
	}
	return matches[0]
}
