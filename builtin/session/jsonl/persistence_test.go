package jsonl

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	. "github.com/RussellLuo/qi/kernel/core/session"
)

func TestEmptyStoresAllocateZeroAndUseExclusiveWatermark(t *testing.T) {
	for _, backend := range []string{"memory", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			var store Store = NewRuntime(RuntimeOptions{})
			if backend == "jsonl" {
				disk, err := newRuntimeFixture(t.TempDir(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer disk.Close()
				store = disk
			}
			current, err := store.Create(context.Background(), CreateRequest{ID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			h := current.Header()
			if h.Version != 4 || h.ID != "s" || h.IsSeeded || current.Watermark() != -1 || len(collectEvents(t, current)) != 0 {
				t.Fatalf("fresh: %+v, watermark=%d", h, current.Watermark())
			}
			seq, err := current.Append(context.Background(), protocol.NewTurnStarted(1))
			if err != nil || seq != 0 || current.Watermark() != 0 {
				t.Fatalf("first append: %d %v", seq, err)
			}
			for e, err := range current.Events(context.Background(), 0) {
				t.Fatalf("read after seq=0 returned %+v %v", e, err)
			}
			if _, err := current.Append(context.Background(), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})); err != nil {
				t.Fatal(err)
			}
			for e, err := range current.Events(context.Background(), 0) {
				if err != nil || e.Seq != 1 {
					t.Fatalf("exclusive read: %+v %v", e, err)
				}
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if seq, err := current.Append(canceled, protocol.NewTurnStarted(2)); !errors.Is(err, context.Canceled) || seq != -1 || current.Watermark() != 1 {
				t.Fatalf("canceled append: %d %v watermark=%d", seq, err, current.Watermark())
			}
			if _, err := current.Append(context.Background(), protocol.NewSessionEvent(protocol.SessionEndSeed, protocol.EndSeedData{})); err == nil {
				t.Fatal("ordinary append accepted lifecycle marker")
			}
		})
	}
}

func TestReopenEmptyClosedAndRepairedLogs(t *testing.T) {
	for _, open := range []string{"empty", "closed", "unclosed"} {
		t.Run(open, func(t *testing.T) {
			dir := t.TempDir()
			store, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Create(context.Background(), CreateRequest{ID: "s"})
			if err != nil {
				t.Fatal(err)
			}
			if open != "empty" {
				appendEvents(t, current, protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1))
			}
			if open == "closed" {
				appendEvents(t, current, protocol.NewStepEnded(1, 1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
			}
			count := len(collectEvents(t, current))
			originalHeader := current.Header()
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := openRecovered(context.Background(), reopened, "s")
			if err != nil {
				t.Fatal(err)
			}
			if restored.Header().CreatedAt != originalHeader.CreatedAt {
				t.Fatal("header changed")
			}
			facts := collectEvents(t, restored)
			suffix := facts[count:]
			if open == "unclosed" {
				if len(suffix) != 3 || suffix[0].Type != protocol.StepEnded || suffix[1].Type != protocol.TurnEnded || protocol.EventPayload[protocol.TurnEndData](suffix[1]).Reason.Kind != "interrupted" {
					t.Fatalf("repair: %+v", suffix)
				}
				suffix = suffix[2:]
			}
			if len(suffix) != 1 || suffix[0].Type != protocol.SessionEndSeed || string(suffix[0].Data) != "{}" {
				t.Fatalf("restore marker: %+v", suffix)
			}
			release, err := restored.BeginRun()
			if err != nil {
				t.Fatal(err)
			}
			release()
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			repeated, _ := openRecovered(context.Background(), again, "s")
			if len(collectEvents(t, repeated)) != len(facts) {
				t.Fatal("repeated reopen duplicated marker")
			}
		})
	}
}

func TestLegacyAndCorruptLogsStayByteIdentical(t *testing.T) {
	header := `{"version":4,"id":"s","createdAt":0,"isSeeded":false}` + "\n"
	for _, data := range []string{
		`{"apiVersion":"qi.profile/v1alpha1","sequence":1,"sessionId":"s","kind":"session/created"}` + "\n" + `{"unfinished":`,
		`{"version":3,"id":"s","createdAt":0,"isSeeded":false}` + "\n",
		header + `{"type":"future/required","seq":0,"time":0,"data":{}}` + "\n" + `{"unfinished":`,
		header + `{"type":"turn/start","seq":1,"time":0,"data":{"turn":1}}` + "\n",
	} {
		t.Run(data, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "cw.jsonl")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			runtime, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if _, err := runtime.Open(context.Background(), "s"); !errors.Is(err, ErrCorruptLog) {
				t.Fatalf("reopen: %v", err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, []byte(data)) {
				t.Fatalf("rejection altered file: %q %v", actual, err)
			}
		})
	}
}

type capturedObserver struct {
	sessions []protocol.SessionEvent
	ids      []protocol.SessionID
}

func (o *capturedObserver) SessionAppended(id protocol.SessionID, e protocol.SessionEvent) {
	o.ids = append(o.ids, id)
	o.sessions = append(o.sessions, e)
}

func (o *capturedObserver) AssistantStream(contracts.AssistantStreamObservation) {}

func (o *capturedObserver) DriverEvent(contracts.DriverEvent) {}

func TestFailedDiskWriteLeavesFactsAndProjectionUnchanged(t *testing.T) {
	observer := &capturedObserver{}
	store, err := newRuntimeFixture(t.TempDir(), observer)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	current, err := store.Create(context.Background(), CreateRequest{ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	disk := store.persistence.handles[current.ID()]
	appendEvents(t, current, protocol.NewTurnStarted(1))
	before, err := os.ReadFile(disk.path)
	if err != nil {
		t.Fatal(err)
	}
	disk.file = &faultFile{logFile: disk.file, writeErr: errors.New("write failure")}
	seq, err := current.Append(context.Background(), protocol.NewMessageEvent(protocol.UserMessage, 1, 0, protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "hello")))
	if err == nil || seq != -1 || current.Watermark() != 0 || len(observer.sessions) != 1 || observer.ids[0] != "s" {
		t.Fatalf("failed append advanced: %d %v %+v", seq, err, observer)
	}
	messages, err := current.Messages(context.Background())
	if err != nil || len(messages) != 0 {
		t.Fatalf("projection advanced: %+v %v", messages, err)
	}
	actual, err := os.ReadFile(disk.path)
	if err != nil || !bytes.Equal(before, actual) {
		t.Fatal("failed write changed file")
	}
}

func TestSnapshotAndInvalidSurfaceAppendAreAtomic(t *testing.T) {
	current, err := NewRuntime(RuntimeOptions{}).Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	e := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "hello"))
	appendEvents(t, current, e)
	e.Data[0] = '['
	e.SurfaceOp.Op = "replace"
	returned := collectEvents(t, current)
	returned[0].Data[0] = '['
	returned[0].SurfaceOp.Op = "replace"
	if ms, err := current.Messages(context.Background()); err != nil || ms[0].Content[0].(protocol.TextBlock).Text != "hello" {
		t.Fatalf("mutated log: %+v %v", ms, err)
	}
	replacement := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, protocol.NewTextMessage("u2", protocol.RoleUser, protocol.SourceUser, "summary"))
	replacement.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 0, EndSeq: 0}
	if _, err := current.Append(context.Background(), replacement); err == nil || current.Watermark() != 0 {
		t.Fatalf("invalid replace advanced watermark: %v", err)
	}
	replacement.SourceEventSeqs = []protocol.Sequence{0}
	appendEvents(t, current, replacement)
	ms, err := current.Messages(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "u2" {
		t.Fatalf("valid replace: %+v %v", ms, err)
	}
	if _, err := current.Append(context.Background(), protocol.NewSessionEvent("extension", make(chan int))); err == nil {
		t.Fatal("accepted non-JSON value")
	}
}

func TestInboxInsertWithoutRemovedCountPreservesPending(t *testing.T) {
	events := []protocol.SessionEvent{}
	for _, id := range []protocol.MessageID{"a", "b"} {
		e := protocol.NewSessionEvent(protocol.InboxSpliced, protocol.InboxSpliceData{Target: "next-turn", Start: 0, Inserted: []protocol.Message{protocol.NewTextMessage(id, protocol.RoleUser, protocol.SourceUser, string(id))}})
		e.Seq = protocol.Sequence(len(events))
		events = append(events, e)
	}
	p, err := Fold(events)
	if err != nil || len(p.Inbox["next-turn"]) != 2 || p.Inbox["next-turn"][0].ID != "b" {
		t.Fatalf("inbox insertion: %+v %v", p.Inbox, err)
	}

}

func TestPendingInboxSurvivesJSONLReopen(t *testing.T) {
	dir := t.TempDir()
	disk, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := disk.Create(context.Background(), CreateRequest{ID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, current, protocol.NewSessionEvent(protocol.InboxSpliced, protocol.InboxSpliceData{Target: "next-step", Start: 0, Inserted: []protocol.Message{protocol.NewTextMessage("pending", protocol.RoleUser, protocol.SourceUser, "later")}}))
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	current, err = openRecovered(context.Background(), restored, "s")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Fold(collectEvents(t, current))
	if err != nil || len(p.Inbox["next-step"]) != 1 || p.Inbox["next-step"][0].ID != "pending" || len(p.Messages) != 0 {
		t.Fatalf("pending replay: %+v %v", p, err)
	}
}

func TestSurfaceProtectsSystemHeadAndToolResultIdentity(t *testing.T) {
	sys := protocol.NewMessageEvent(protocol.SystemMessage, 1, 1, protocol.NewTextMessage("sys", protocol.RoleSystem, protocol.SourceSystem, "prompt"))
	sys.Seq = 0
	replacement := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "summary"))
	replacement.Seq = 1
	replacement.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 0, EndSeq: 0}
	replacement.SourceEventSeqs = []protocol.Sequence{0}
	if _, err := Fold([]protocol.SessionEvent{sys, replacement}); err == nil {
		t.Fatal("replaced system head with user message")
	}
	call := protocol.NewToolCallEvent(1, 1, protocol.ToolCall{ID: "c", Name: "echo", Arguments: "{}"})
	call.Seq = 0
	original := protocol.NewToolResultEvent(1, 1, protocol.ToolResult{Message: protocol.NewToolResultMessage("r", "c", []protocol.ContentBlock{protocol.TextBlock{Text: "old"}}, false)}, 0)
	original.Seq = 1
	rewrite := protocol.NewToolResultEvent(1, 1, protocol.ToolResult{Message: protocol.NewToolResultMessage("r", "c", []protocol.ContentBlock{protocol.TextBlock{Text: "new"}}, false)}, 0)
	rewrite.Seq = 2
	rewrite.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 1, EndSeq: 1}
	rewrite.SourceEventSeqs = []protocol.Sequence{1}
	if _, err := Fold([]protocol.SessionEvent{call, original, rewrite}); err != nil {
		t.Fatalf("content-only rewrite: %v", err)
	}
	changed := protocol.EventPayload[protocol.ToolResultData](rewrite)
	changed.Message.ID = "changed"
	rewrite = protocol.NewSessionEvent(protocol.ToolResulted, changed)
	rewrite.Seq = 2
	rewrite.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 1, EndSeq: 1}
	rewrite.SourceEventSeqs = []protocol.Sequence{1}
	if _, err := Fold([]protocol.SessionEvent{call, original, rewrite}); err == nil {
		t.Fatal("accepted changed tool result identity")
	}
}

func TestHeaderCheckPrecedesRecoveryUnderWriterLock(t *testing.T) {
	dir := t.TempDir()
	header, events, _ := repairFixture(t, "repair-tool-not-started")
	header.CWD = nil
	path := writePrefix(t, dir, header, events, []byte("partial"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("entry rejects historical header")
	checked := false
	runtime, backend := newRuntimeBackend(t, dir, RuntimeOptions{HeaderCheck: func(got protocol.SessionHeader) error {
		checked = true
		if got.ID != header.ID || got.CWD != nil {
			t.Fatalf("header=%+v", got)
		}
		if other, err := newRuntimeFixture(dir, nil); !errors.Is(err, ErrStoreLocked) {
			if other != nil {
				other.Close()
			}
			t.Fatalf("header check ran without exclusive ownership: %v", err)
		}
		return rejected
	}})
	_, err = runtime.Open(context.Background(), header.ID)
	if !checked || !errors.Is(err, rejected) {
		t.Fatalf("checked=%t err=%v", checked, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejection changed prefix: %v", err)
	}
	// Rejection releases the file handle, while the backend retains its directory lock.
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := openRecovered(context.Background(), store, header.ID); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(path)
	if err != nil || bytes.Equal(before, after) {
		t.Fatalf("normal recovery did not repair prefix: %v", err)
	}
}

func TestProjectionSliceDoesNotRelaxWriterAdmission(t *testing.T) {
	// A developer message without Turn/Step opens is valid for projection,
	// but cannot be admitted as an executable log.
	header, err := CreateHeader("projection-slice", nil)
	if err != nil {
		t.Fatal(err)
	}
	event := protocol.NewMessageEvent(protocol.DeveloperMessage, 1, 1,
		protocol.NewTextMessage("context", protocol.RoleDeveloper, "plugin-context", "context"))
	event.Seq = 0
	events := []protocol.SessionEvent{event}
	if _, err := Fold(events); err != nil {
		t.Fatal(err)
	}
	closers, err := InterruptedTurnClosers(events)
	if err != nil || len(closers) != 0 {
		t.Fatalf("balanced projection repair: %v %v", closers, err)
	}
	dir := t.TempDir()
	writePrefix(t, dir, header, events, nil)
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Open(context.Background(), header.ID); err == nil {
		t.Fatal("projection slice admitted as executable log")
	} else if !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
	// The recovery kernel still rejects malformed execution boundary order.
	invalid := []protocol.SessionEvent{protocol.NewTurnStarted(1), protocol.NewTurnStarted(2)}
	invalid[0].Seq = 0
	invalid[1].Seq = 1
	if _, err := InterruptedTurnClosers(invalid); !errors.Is(err, ErrInvalidTransition) {
		t.Fatal(err)
	}
}

func TestPendingJSONLReopenAndClaim(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(ctx, CreateRequest{ID: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	inbox := NewInbox(current)
	for _, item := range []struct{ target, id string }{{contracts.NextTurn, "turn-one"}, {contracts.NextStep, "step-one"}, {contracts.NextTurn, "turn-two"}} {
		if _, err := inbox.Splice(ctx, item.target, 999, 0, []protocol.Message{inboxMessage(item.id)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := openRecovered(ctx, reopened, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	pending := NewInbox(restored)
	p, err := pending.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inboxIDs(p[contracts.NextTurn]), []string{"turn-one", "turn-two"}) || !reflect.DeepEqual(inboxIDs(p[contracts.NextStep]), []string{"step-one"}) {
		t.Fatal(p)
	}
	claimed, err := pending.Claim(ctx, contracts.NextTurn, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inboxIDs(claimed), []string{"step-one", "turn-one"}) {
		t.Fatal(claimed)
	}
}
