package headless

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/qi/builtin/session/jsonl"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// A native test Driver lets the runner see failure returns separately from
// durable facts, while still using the real Agent scheduler and attachment.
type scenarioDriver struct {
	drive func(context.Context, agentloop.Run, agentloop.EmitFunc) error
}

func (d scenarioDriver) Drive(ctx context.Context, r agentloop.Run, e agentloop.EmitFunc) error {
	return d.drive(ctx, r, e)
}

func TestReasoningUsesStderrAndKeepsAnswerAndJSONOutputClean(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", jsonMode), func(t *testing.T) {
			store := session.NewRuntime(session.RuntimeOptions{})
			outcomes := new(outcomes)
			var observed func(context.Context, contracts.AssistantStreamObservation) error
			driver := scenarioDriver{drive: func(ctx context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
				if _, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1); err != nil {
					return err
				}
				if observed != nil {
					chunk := protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Text: "Let me think.\n"}
					if err := observed(ctx, contracts.AssistantStreamObservation{Agent: run.Agent, Frame: protocol.AssistantStreamFrame{Type: "chunk", Chunk: &chunk}}); err != nil {
						return err
					}
				}
				for _, event := range []protocol.SessionEvent{
					protocol.NewTurnStarted(1),
					protocol.NewStepStarted(1, 1),
					protocol.NewAssistantMessageEvent(1, 1, protocol.NewAssistantTextMessage("answer", "fixture", "text", "Hello!"), []protocol.AssistantStreamRecord{}, nil),
					protocol.NewStepEnded(1, 1),
					protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
				} {
					if _, err := run.Attached.Session.Append(ctx, event); err != nil {
						return err
					}
				}
				return nil
			}}
			factory := newFixtureFactory(outcomes.Wrap(driver), store)
			defer factory.Close(context.Background())
			var stdout, stderr bytes.Buffer
			r := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: &stdout, Stderr: &stderr, CWD: "/test",
				Observe: func(handler func(context.Context, contracts.AssistantStreamObservation) error) (func(), error) {
					observed = handler
					return func() {}, nil
				},
			}
			result, err := r.Run(context.Background(), request{Task: "hi", JSON: jsonMode})
			if err != nil || result.ExitCode() != 0 || result.CleanupError != nil {
				t.Fatal(result, err)
			}
			if jsonMode {
				lines := parseLines(t, stdout.String())
				if final := lines[len(lines)-1]; final["type"] != "final" || final["text"] != "Hello!" || stderr.Len() != 0 {
					t.Fatal(lines, stderr.String())
				}
			} else if stdout.String() != "Hello!\n" || stderr.String() != "qi: reasoning:\nLet me think.\n" {
				t.Fatal(stdout.String(), stderr.String())
			}
		})
	}
}

func TestDirectFailuresRequireOwnDurableSettlement(t *testing.T) {
	failure := errors.New("original append/driver failure")
	for _, mode := range []string{"no-turn", "unclosed", "fake-ended", "completed-error", "settled-error", "settled-aborted", "empty-success", "multi-turn"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", mode, jsonMode), func(t *testing.T) {
				store := session.NewRuntime(session.RuntimeOptions{})
				outcomes := new(outcomes)
				driver := scenarioDriver{drive: func(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
					current, _ := store.Get(ctx, run.Attached.Session.ID())
					appendEvent := func(e protocol.SessionEvent) {
						if _, err := current.Append(ctx, e); err != nil {
							t.Error(err)
						}
					}
					run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1)
					if mode == "no-turn" {
						return failure
					}
					if mode == "fake-ended" {
						emit(contracts.DriverEvent{Type: "ended", Reason: protocol.TurnEndReason{Kind: "completed"}})
						return failure
					}
					appendEvent(protocol.NewTurnStarted(1))
					if mode == "unclosed" {
						return failure
					}
					reason := protocol.TurnEndReason{Kind: "completed"}
					if mode == "settled-error" {
						reason = protocol.TurnEndReason{Kind: "error", Error: &protocol.LlmFailure{Code: "fixture_failed", Message: "failed"}}
					}
					if mode == "settled-aborted" {
						reason = protocol.TurnEndReason{Kind: "aborted", Reason: &protocol.AgentCancelCause{Kind: "parent"}}
					}
					appendEvent(protocol.NewTurnEnded(1, reason))
					if mode == "multi-turn" {
						appendEvent(protocol.NewTurnStarted(2))
						appendEvent(protocol.NewStepStarted(2, 1))
						appendEvent(protocol.NewAssistantMessageEvent(2, 1, protocol.NewAssistantTextMessage("m", "fake", "echo", "last text"), []protocol.AssistantStreamRecord{}, nil))
						appendEvent(protocol.NewStepEnded(2, 1))
						appendEvent(protocol.NewTurnEnded(2, protocol.TurnEndReason{Kind: "completed"}))
					}
					if mode == "empty-success" || mode == "multi-turn" {
						return nil
					}
					return failure
				}}
				factory := newFixtureFactory(outcomes.Wrap(driver), store)
				defer factory.Close(context.Background())
				var out, errout bytes.Buffer
				runner := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: &out, Stderr: &errout, CWD: "/test"}
				result, err := runner.Run(context.Background(), request{Task: "task", JSON: jsonMode})
				output := out.String()
				if jsonMode {
					lines := parseLines(t, output)
					if len(lines) == 0 || lines[0]["type"] != "session" {
						t.Fatal("missing session")
					}
					if err == nil {
						if lines[len(lines)-1]["type"] != "final" || lines[len(lines)-1]["text"] != result.Text {
							t.Fatalf("final=%v", lines)
						}
					} else {
						for _, line := range lines {
							if line["type"] == "final" {
								t.Fatal("unsettled final")
							}
						}
					}
					output = result.Text + "\n"
					if err != nil {
						output = ""
					}
				}

				switch mode {
				case "settled-error", "settled-aborted":
					if err != nil || result.ExitCode() != 1 || output != "\n" {
						t.Fatalf("settled=%+v err=%v stdout=%q", result, err, out.String())
					}
				case "empty-success":
					if err != nil || result.ExitCode() != 0 || output != "\n" {
						t.Fatalf("empty=%+v err=%v", result, err)
					}
				case "multi-turn":
					if err != nil || result.Text != "last text" || result.ExitCode() != 0 {
						t.Fatalf("multi=%+v err=%v", result, err)
					}
				default:
					if !errors.Is(err, failure) || output != "" {
						t.Fatalf("failure=%v stdout=%q", err, out.String())
					}
				}
			})
		}
	}
}
func TestSummaryIgnoresHistoricalAndUncommittedText(t *testing.T) {
	store := session.NewRuntime(session.RuntimeOptions{})
	current, _ := store.Create(context.Background(), session.CreateRequest{ID: "summary"})
	appendEvent := func(e protocol.SessionEvent) {
		if _, err := current.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(protocol.NewTurnStarted(1))
	appendEvent(protocol.NewStepStarted(1, 1))
	appendEvent(protocol.NewAssistantMessageEvent(1, 1, protocol.NewAssistantTextMessage("old", "fake", "echo", "old answer"), []protocol.AssistantStreamRecord{}, nil))
	appendEvent(protocol.NewStepEnded(1, 1))
	appendEvent(protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
	after := current.Watermark()
	appendEvent(protocol.NewTurnStarted(2))
	appendEvent(protocol.NewStepStarted(2, 1))
	record, _ := protocol.NewAssistantStreamRecord(time.Now().UnixMilli(), protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: 0, Text: "abandoned"})
	appendEvent(protocol.NewAttemptEvent(2, 1, []protocol.AssistantStreamRecord{record}))
	appendEvent(protocol.NewStepEnded(2, 1))
	appendEvent(protocol.NewTurnEnded(2, protocol.TurnEndReason{Kind: "completed"}))
	text, reason, err := summarize(context.Background(), current, after, current.Watermark())
	if err != nil || text != "" || reason.Kind != "completed" {
		t.Fatalf("summary %q %+v %v", text, reason, err)
	}
}

type releaseCheckStore struct {
	*session.Runtime
	release func(context.Context) error
}

func (s *releaseCheckStore) Release(ctx context.Context, current session.Session) error {
	return errors.Join(s.Runtime.Release(ctx, current), s.release(ctx))
}

func TestMissingCommittedCompletionRetainsCleanupFailure(t *testing.T) {
	var order []string
	failure := errors.New("dispose failed")
	store := &releaseCheckStore{Runtime: session.NewRuntime(session.RuntimeOptions{}), release: func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("cleanup received canceled context")
		}
		order = append(order, "dispose")
		return failure
	}}
	outcomes := new(outcomes)
	registry := agent.NewRegistry()
	factory, err := agent.NewFactory(agent.FactoryOptions{Registry: registry, Store: store, Driver: outcomes.Wrap(scenarioDriver{drive: func(ctx context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
		_, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1)
		return err
	}})})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close(context.Background())
	r := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: io.Discard, Stderr: io.Discard, CWD: "/test", BeginShutdown: func(code int) {
		if code != 1 {
			t.Error("missing committed completion selected success", code)
		}
		order = append(order, "begin")
	}}
	result, err := r.Run(context.Background(), request{Task: "hello"})
	if err != nil || result.ExitCode() != 1 || result.Reason != nil || !errors.Is(result.CleanupError, failure) || (len(order) < 2 || order[0] != "begin" || order[len(order)-1] != "dispose") {
		t.Fatal(result, err, order)
	}
}

type rejectStore struct {
	session.ResourceStore
	kind protocol.SessionEventKind
	err  error
}
type rejectSession struct {
	session.Session
	kind protocol.SessionEventKind
	err  error
}

func (s rejectStore) Get(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	current, err := s.ResourceStore.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return rejectSession{current, s.kind, s.err}, nil
}
func (s rejectStore) Create(ctx context.Context, r session.CreateRequest) (session.Session, error) {
	current, err := s.ResourceStore.Create(ctx, r)
	if err != nil {
		return nil, err
	}
	return rejectSession{current, s.kind, s.err}, nil
}
func (s rejectStore) Release(ctx context.Context, current session.Session) error {
	return s.ResourceStore.Release(ctx, current.(rejectSession).Session)
}
func (s rejectStore) RollbackCreate(ctx context.Context, current session.Session) error {
	return s.ResourceStore.RollbackCreate(ctx, current.(rejectSession).Session)
}
func (s rejectSession) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	if e.Type == s.kind {
		return -1, s.err
	}
	return s.Session.Append(ctx, e)
}
func TestDefaultLoopAppendRefusalCannotBecomeSuccess(t *testing.T) {
	for _, kind := range []protocol.SessionEventKind{protocol.InboxSpliced, protocol.TurnStarted, protocol.AssistantMessage, protocol.TurnEnded} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", kind, jsonMode), func(t *testing.T) {
				owner, err := jsonl.NewOwner(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
				failure := errors.New("injected append refusal")
				runtime := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolveJSONLOwner(owner)})
				defer runtime.Close(context.Background())
				store := rejectStore{runtime, kind, failure}
				prompts, toolRuntime, llmRuntime, middleware := prompt.NewRuntime(), tools.NewRuntime(), llm.NewRuntime(), agentmiddleware.NewRuntime()
				defer prompts.Close()
				defer toolRuntime.Close()
				defer llmRuntime.Close()
				defer middleware.Close()
				if _, err := llmRuntime.RegisterAdapter(newContributionOwner("cli-test", 1), llm.AdapterRegistration{Route: "chat", Provider: "fake", Adapter: echoAdapter{}}); err != nil {
					t.Fatal(err)
				}
				outcomes := new(outcomes)
				loop := agentloop.New(runprep.Coordinator{Prompts: prompts, Tools: toolRuntime, LLM: llmRuntime, Middleware: middleware})
				factory := newFixtureFactory(outcomes.Wrap(loop), store)
				defer factory.Close(context.Background())
				var stdout, stderr bytes.Buffer
				runner := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: &stdout, Stderr: &stderr, CWD: "/test", Config: agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fake", Model: "echo"}}}}
				result, err := runner.Run(context.Background(), request{Task: "hello", JSON: jsonMode})
				if !errors.Is(err, failure) || (!jsonMode && stdout.Len() != 0) || strings.Contains(stdout.String(), `"type":"final"`) {
					t.Fatalf("unsettled result=%+v err=%v stdout=%q", result, err, stdout.String())
				}
			})
		}
	}
}

// This controlled backend tests runner capability selection. It retains real
// Session facts in process; it supplies no evidence about JSONL physical I/O.
type ownedHistory struct {
	*session.Runtime
	header                protocol.SessionHeader
	history               []protocol.SessionEvent
	opened                session.Session
	releases, opens, gets int
	releaseErr            error
}

func (s *ownedHistory) Get(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	s.gets++
	if s.opened != nil && s.opened.ID() == id {
		return s.opened, nil
	}
	return s.Runtime.Get(ctx, id)
}
func (s *ownedHistory) Release(ctx context.Context, current session.Session) error {
	s.header = current.Header()
	s.history = nil
	for e, err := range current.Events(ctx, -1) {
		if err != nil {
			return err
		}
		s.history = append(s.history, e.Clone())
	}
	var err error
	if s.opened == current {
		err = current.(*session.LiveSession).Release(func() error { return nil })
		if err == nil {
			s.opened = nil
		}
	} else {
		err = s.Runtime.Release(ctx, current)
	}
	if err == nil {
		s.releases++
	}
	return errors.Join(err, s.releaseErr)
}
func (s *ownedHistory) Open(ctx context.Context, id protocol.SessionID) (session.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.opened != nil {
		return nil, session.ErrAttached
	}
	if s.header.ID != id {
		return nil, session.ErrNotFound
	}
	current, err := session.Restore(s.header, s.history, nil, nil)
	if err != nil {
		return nil, err
	}
	s.opened = current
	s.opens++
	return current, nil
}

func TestOwnedCreateResumeAndCleanupResult(t *testing.T) {
	for _, releaseFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "release-error"}[releaseFailure], func(t *testing.T) {
			ctx := context.Background()
			store := &ownedHistory{Runtime: session.NewRuntime(session.RuntimeOptions{})}
			failure := errors.New("release failure")
			if releaseFailure {
				store.releaseErr = failure
			}
			prompts, llmRuntime, toolsRuntime, middleware := prompt.NewRuntime(), llm.NewRuntime(), tools.NewRuntime(), agentmiddleware.NewRuntime()
			defer prompts.Close()
			defer llmRuntime.Close()
			defer toolsRuntime.Close()
			defer middleware.Close()
			chunks := []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText}, {Type: protocol.ChunkTextDelta, Index: 0, Text: "answer"}, {Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: "answer"}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}
			adapter := &scriptedAdapter{Scripts: []scriptedScript{{Chunks: chunks}, {Chunks: chunks}}}
			if _, err := llmRuntime.RegisterAdapter(newContributionOwner("owned-runner", 1), llm.AdapterRegistration{Route: "chat", Provider: "fixture", Adapter: adapter}); err != nil {
				t.Fatal(err)
			}
			outcomes := new(outcomes)
			registry := agent.NewRegistry()
			factory, err := agent.NewFactory(agent.FactoryOptions{Registry: registry, Store: store, Driver: outcomes.Wrap(agentloop.New(runprep.Coordinator{Prompts: prompts, LLM: llmRuntime, Tools: toolsRuntime, Middleware: middleware}))})
			if err != nil {
				t.Fatal(err)
			}
			defer factory.Close(ctx)
			var stdout, stderr bytes.Buffer
			runner := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: &stdout, Stderr: &stderr, CWD: "/owned", Config: agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fixture", Model: "text"}}}}
			first, err := runner.Run(ctx, request{Task: "first"})
			if err != nil || first.ExitCode() != 0 || first.Text != "answer" || store.releases != 1 || store.gets != 0 || len(registry.Snapshot()) != 0 {
				t.Fatal(first, err, store.releases, store.gets, registry.Snapshot())
			}
			if errors.Is(first.CleanupError, failure) != releaseFailure {
				t.Fatal("cleanup outcome", first.CleanupError)
			}
			if _, err := store.Runtime.Get(ctx, first.SessionID); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("owned instance retained", err)
			}
			facts := len(store.history)
			stdout.Reset()
			second, err := runner.Run(ctx, request{Task: "second", Resume: true, SessionID: string(first.SessionID), JSON: true})
			if err != nil || second.ExitCode() != 0 || second.SessionID != first.SessionID || store.opens != 1 || store.gets != 0 || store.releases != 2 || store.opened != nil || len(registry.Snapshot()) != 0 {
				t.Fatal(second, err, store.opens, store.gets, store.releases)
			}
			if second.FirstSeq < protocol.Sequence(facts-1) || len(store.history) <= facts || len(adapter.Seen) != 2 {
				t.Fatal("history not resumed", second, len(store.history), len(adapter.Seen))
			}
			if errors.Is(second.CleanupError, failure) != releaseFailure {
				t.Fatal(second.CleanupError)
			}
			if !bytes.Contains(stdout.Bytes(), []byte("answer")) {
				t.Fatal(stdout.String())
			}
			// A rejected adoption must still dispose an already acquired owned Handle.
			otherCWD := "/other"
			store.header.CWD = &otherCWD
			rejected, err := runner.Run(ctx, request{Task: "rejected", Resume: true, SessionID: string(first.SessionID)})
			if err == nil || store.releases != 3 || store.opened != nil || len(registry.Snapshot()) != 0 || len(adapter.Seen) != 2 {
				t.Fatal(rejected, err, store.releases)
			}
		})
	}
}

func testRuntime(t *testing.T, dir string) (*Runtime, *outcomes) {
	t.Helper()
	profile, err := fixtureProfile()
	if err != nil {
		t.Fatal(err)
	}
	profile.Instances[1].Config = nil
	profile.Instances = append(profile.Instances, profileapi.InstanceSpec{ID: "persistence", Plugin: "qi/session/persistence-jsonl", Config: map[string]any{"directory": dir}})
	outcomes := new(outcomes)
	r, err := Start(context.Background(), profile, outcomes.Wrap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r, outcomes
}
func testRunner(r *Runtime, o *outcomes, out, errout *bytes.Buffer) runner {
	return runner{Sessions: r.Sessions, Factory: r.Factory, outcomes: o, Observe: r.Observe, CWD: "/test/headless", Stdout: out, Stderr: errout, Config: r.Config}
}

type failingDispose struct {
	agent.Factory
	before func()
	err    error
}
type failingHandle struct {
	agent.Handle
	before func()
	err    error
}

func (h failingHandle) Dispose(ctx context.Context) error {
	h.before()
	return errors.Join(h.Handle.Dispose(ctx), h.err)
}
func (f failingDispose) Create(ctx context.Context, o agent.CreateOptions) (agent.Handle, error) {
	h, err := f.Factory.Create(ctx, o)
	if err != nil {
		return nil, err
	}
	return failingHandle{h, f.before, f.err}, nil
}
func TestCleanupFailureKeepsConfirmedTaskOutcome(t *testing.T) {
	runtime, outcomes := testRuntime(t, t.TempDir())
	var out, errout bytes.Buffer
	runner := testRunner(runtime, outcomes, &out, &errout)
	selected := -1
	runner.BeginShutdown = func(code int) {
		if selected == -1 {
			selected = code
		}
	}
	failure := errors.New("detach failure")
	runner.Factory = failingDispose{runtime.Factory, func() {
		if selected != 0 || !strings.Contains(out.String(), `"type":"final"`) {
			t.Errorf("disposal before confirmed output: code=%d out=%q", selected, out.String())
		}
	}, failure}
	result, err := runner.Run(context.Background(), request{Task: "hello", JSON: true})
	if err != nil || result.ExitCode() != 0 || !errors.Is(result.CleanupError, failure) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestRunAndPersistentResumeOwnOnlyCurrentInterval(t *testing.T) {
	dir := t.TempDir()
	runtime, outcomes := testRuntime(t, dir)
	var out, errout bytes.Buffer
	runner := testRunner(runtime, outcomes, &out, &errout)
	first, err := runner.Run(context.Background(), request{Task: "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^session-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(string(first.SessionID)) {
		t.Fatalf("not UUID v4: %s", first.SessionID)
	}
	if first.Text != "hello\n" || first.ExitCode() != 0 || out.String() != "hello\n\n" || errout.Len() != 0 {
		t.Fatalf("first=%+v stdout=%q stderr=%q", first, out.String(), errout.String())
	}
	current, err := runtime.Sessions.(session.StoreOpener).Open(context.Background(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Header().CWD == nil || *current.Header().CWD != runner.CWD {
		t.Fatal("cwd missing")
	}
	if len(runtime.Agents.Snapshot()) != 0 {
		t.Fatal("runner did not detach Agent")
	}
	if err := runtime.Sessions.(session.ResourceStore).Release(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime, outcomes = testRuntime(t, dir)
	out.Reset()
	runner = testRunner(runtime, outcomes, &out, &errout)
	second, err := runner.Run(context.Background(), request{Task: "again", SessionID: string(first.SessionID), Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionID != first.SessionID || second.Text != "again" || second.FirstSeq < first.Through || out.String() != "again\n" {
		t.Fatalf("resume=%+v stdout=%q", second, out.String())
	}
	current, _ = runtime.Sessions.(session.StoreOpener).Open(context.Background(), first.SessionID)
	if current.LastTurn() != 2 {
		t.Fatalf("turns=%d", current.LastTurn())
	}
	if err := runtime.Sessions.(session.ResourceStore).Release(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, err = runner.Run(context.Background(), request{Task: "x", SessionID: "unknown", Resume: true}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unknown=%v", err)
	}
	if _, err = runner.Run(context.Background(), request{Task: "x", SessionID: " " + string(first.SessionID) + " ", Resume: true}); err == nil {
		t.Fatal("trimmed id")
	}
	if out.Len() != 0 {
		t.Fatal("resume rejection wrote answer")
	}
}
func TestResumeRejectsUnsupportedOwnership(t *testing.T) {
	runtime, outcomes := testRuntime(t, t.TempDir())
	cwd := "/test/headless"
	other := "/elsewhere"
	preset := "preset"
	origin := "subagent"
	parent := protocol.SessionID("parent")
	for _, c := range []struct {
		name   string
		change func(*protocol.SessionHeader)
		event  *protocol.SessionEvent
		match  string
	}{
		{name: "no-cwd", change: func(h *protocol.SessionHeader) { h.CWD = nil }, match: "no working directory"},
		{name: "cwd", change: func(h *protocol.SessionHeader) { h.CWD = &other }, match: "was recorded"},
		{name: "preset", change: func(h *protocol.SessionHeader) { h.AgentPreset = &preset }, match: "agent preset"},
		{name: "origin", change: func(h *protocol.SessionHeader) { h.Origin = &origin }, match: "subagent"},
		{name: "parent", change: func(h *protocol.SessionHeader) { h.ParentSession = &parent }, match: "forked"},
		{name: "preset-event", event: &protocol.SessionEvent{Type: "agent-preset/selected", Ignorable: true, Data: []byte(`{"agentPreset":"selected"}`)}, match: "agent preset"},
		{name: "malformed-event", event: &protocol.SessionEvent{Type: "agent-preset/selected", Ignorable: true, Data: []byte(`{"agentPreset":null}`)}, match: "malformed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := protocol.SessionID(c.name)
			h := protocol.SessionHeader{Version: 4, ID: id, CreatedAt: 1, CWD: &cwd}
			if c.change != nil {
				c.change(&h)
			}
			current, err := runtime.Sessions.Create(context.Background(), session.CreateRequest{ID: id, Header: &h})
			if err != nil {
				t.Fatal(err)
			}
			if c.event != nil {
				if _, err = current.Append(context.Background(), *c.event); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtime.Sessions.(session.ResourceStore).Release(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			runner := testRunner(runtime, outcomes, &out, &errout)
			if _, err = runner.Run(context.Background(), request{Task: "x", SessionID: string(id), Resume: true}); err == nil || !strings.Contains(err.Error(), c.match) {
				t.Fatalf("err=%v want=%s", err, c.match)
			}
			if out.Len() != 0 || len(runtime.Agents.Snapshot()) != 0 {
				t.Fatal("rejected run admitted activity")
			}
		})
	}
	// A compatible live Agent still belongs to its existing owner.
	h := protocol.SessionHeader{Version: 4, ID: "live", CreatedAt: 1, CWD: &cwd}
	current, _ := runtime.Sessions.Create(context.Background(), session.CreateRequest{ID: h.ID, Header: &h})
	handle, err := runtime.Factory.Attach(context.Background(), agent.AttachOptions{Session: current, Agent: agent.AgentOptions{Config: runtime.Config}})
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	runner := testRunner(runtime, outcomes, &out, &errout)
	if _, err = runner.Run(context.Background(), request{Task: "x", SessionID: "live", Resume: true}); err == nil || !strings.Contains(err.Error(), "live") {
		t.Fatalf("live=%v", err)
	}
	handle.Dispose(context.Background())
}
func TestRepairPrecedesIntervalAndDoesNotCountOldFailure(t *testing.T) {
	runtime, outcomes := testRuntime(t, t.TempDir())
	cwd := "/test/headless"
	h := protocol.SessionHeader{Version: 4, ID: "repair", CreatedAt: 1, CWD: &cwd}
	current, err := runtime.Sessions.Create(context.Background(), session.CreateRequest{ID: h.ID, Header: &h})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []protocol.SessionEvent{protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1)} {
		if _, err = current.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	old := current.Watermark()
	if err := runtime.Sessions.(session.ResourceStore).Release(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	runner := testRunner(runtime, outcomes, &out, &errout)
	result, err := runner.Run(context.Background(), request{Task: "fresh", SessionID: "repair", Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.FirstSeq <= old || result.ExitCode() != 0 || result.Text != "fresh" || errout.Len() != 0 {
		t.Fatalf("repair result=%+v stderr=%q", result, errout.String())
	}
}

func minimalRuntime(t *testing.T, name, directory string) (*Runtime, *outcomes) {
	t.Helper()
	profile, err := fixtureProfile()
	if err != nil {
		t.Fatal(err)
	}
	for i := range profile.Instances {
		if profile.Instances[i].ID == "session" && name == "minimal-jsonl.yaml" {
			profile.Instances[i].Config = nil
		}
	}
	if name == "minimal-jsonl.yaml" {
		profile.Instances = append(profile.Instances, profileapi.InstanceSpec{ID: "persistence", Plugin: "qi/session/persistence-jsonl", Config: map[string]any{"directory": directory}})
	}
	// Readiness must not depend on the file's declaration order.
	for i, j := 0, len(profile.Instances)-1; i < j; i, j = i+1, j-1 {
		profile.Instances[i], profile.Instances[j] = profile.Instances[j], profile.Instances[i]
	}
	outcomes := new(outcomes)
	runtime, err := Start(context.Background(), profile, outcomes.Wrap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return runtime, outcomes
}

func TestMinimalProfilesRunWithoutPromptOrToolContributions(t *testing.T) {
	for _, name := range []string{"minimal.yaml", "minimal-jsonl.yaml"} {
		t.Run(name, func(t *testing.T) {
			runtime, outcomes := minimalRuntime(t, name, t.TempDir())
			for _, jsonMode := range []bool{false, true} {
				var out, errout bytes.Buffer
				runner := testRunner(runtime, outcomes, &out, &errout)
				runner.ObserveEvents = runtime.ObserveEvents
				result, err := runner.Run(context.Background(), request{Task: "hello", JSON: jsonMode})
				if err != nil || result.CleanupError != nil || result.ExitCode() != 0 || result.Text != "hello" || errout.Len() != 0 {
					t.Fatalf("result=%+v err=%v stderr=%q", result, err, errout.String())
				}
				if jsonMode {
					lines := parseLines(t, out.String())
					if lines[0]["type"] != "session" || lines[len(lines)-1]["type"] != "final" || lines[len(lines)-1]["text"] != result.Text {
						t.Fatalf("JSON did not project the committed result: %v", lines)
					}
					for _, line := range lines {
						if line["type"] == "tool_call" || line["type"] == "tool_result" {
							t.Fatalf("minimal profile executed a tool: %v", line)
						}
					}
				} else if out.String() != result.Text+"\n" {
					t.Fatalf("stdout=%q", out.String())
				}
				if len(runtime.Agents.Snapshot()) != 0 {
					t.Fatal("completed minimal run did not detach its Agent")
				}
			}
		})
	}
}

func TestResumeUsesOpenWithoutPersistencePreflight(t *testing.T) {
	runtime, outcomes := minimalRuntime(t, "minimal.yaml", "")
	var out, errout bytes.Buffer
	runner := testRunner(runtime, outcomes, &out, &errout)
	first, err := runner.Run(context.Background(), request{Task: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Sessions.Get(context.Background(), first.SessionID); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("owned runtime resource retained", err)
	}
	for _, store := range []session.Store{runtime.Sessions} {
		for _, id := range []string{string(first.SessionID), "unknown"} {
			for _, jsonMode := range []bool{false, true} {
				out.Reset()
				runner.Sessions = store
				_, err := runner.Run(context.Background(), request{Task: "again", SessionID: id, Resume: true, JSON: jsonMode})
				if !errors.Is(err, session.ErrResumeUnsupported) || !strings.Contains(err.Error(), "cannot resume history") {
					t.Fatalf("resume=%v", err)
				}
				if out.Len() != 0 || len(runtime.Agents.Snapshot()) != 0 {
					t.Fatal("rejected resume admitted activity or wrote an answer")
				}
			}
		}
	}
}

type shortOutput struct{}

func (shortOutput) Write(raw []byte) (int, error) { return len(raw) - 1, nil }

// Output failure and disposal failure are runner contracts. Exercise both
// without constructing a Host, Profile, provider plugin or launch consumer.
func TestOutputFailurePreservesCleanupError(t *testing.T) {
	outputFailure := errors.New("output failed")
	cleanupFailure := errors.New("dispose failed")
	for _, tc := range []struct {
		name                  string
		writer                io.Writer
		outputErr, cleanupErr error
	}{
		{"writer", failedWriter{outputFailure}, outputFailure, nil},
		{"short", shortOutput{}, io.ErrShortWrite, nil},
		{"joined", failedWriter{outputFailure}, outputFailure, cleanupFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			released := 0
			store := &releaseCheckStore{Runtime: session.NewRuntime(session.RuntimeOptions{}), release: func(context.Context) error {
				released++
				return tc.cleanupErr
			}}
			t.Cleanup(func() { _ = store.Close(context.Background()) })
			outcomes := new(outcomes)
			driver := scenarioDriver{drive: func(ctx context.Context, run agentloop.Run, _ agentloop.EmitFunc) error {
				if _, err := run.Attached.Inbox.Claim(ctx, contracts.NextTurn, 1); err != nil {
					return err
				}
				for _, event := range []protocol.SessionEvent{
					protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1),
					protocol.NewAssistantMessageEvent(1, 1, protocol.NewAssistantTextMessage("answer", "fixture", "text", "answer"), []protocol.AssistantStreamRecord{}, nil),
					protocol.NewStepEnded(1, 1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
				} {
					if _, err := run.Attached.Session.Append(ctx, event); err != nil {
						return err
					}
				}
				return nil
			}}
			factory := newFixtureFactory(outcomes.Wrap(driver), store)
			t.Cleanup(func() { _ = factory.Close(context.Background()) })
			selected := -1
			runner := runner{Sessions: store, Factory: factory, outcomes: outcomes, Stdout: tc.writer, Stderr: io.Discard, CWD: "/test", BeginShutdown: func(code int) { selected = code }}
			result, err := runner.Run(context.Background(), request{Task: "hello"})
			if !errors.Is(err, tc.outputErr) || !errors.Is(result.CleanupError, tc.cleanupErr) || released != 1 || selected != 1 {
				t.Fatal(result, err, released, selected)
			}
		})
	}
}
