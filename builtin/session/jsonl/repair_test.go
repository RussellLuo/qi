package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	. "github.com/RussellLuo/qi/kernel/core/session"
)

func assertRepairJSON(t *testing.T, expected, actual []protocol.SessionEvent) {
	t.Helper()
	a, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("repair differs:\n%s\n%s", a, b)
	}
}

func writePrefix(t *testing.T, dir string, header protocol.SessionHeader, events []protocol.SessionEvent, tail []byte) string {
	t.Helper()
	path := (&Backend{dir: dir}).path(header.ID)
	b, err := protocol.EncodeSessionHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	data := append(b, '\n')
	for _, e := range events {
		b, err := protocol.EncodeSessionEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
		data = append(data, '\n')
	}
	data = append(data, tail...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCutEveryExecutionAndRepairBoundary(t *testing.T) {
	header, events, expected := repairFixture(t, "repair-mixed-assistant-order")
	full := append(cloneEvents(events), expected...)
	for n := 0; n <= len(full); n++ {
		prefix := full[:n]
		// Includes turn/start, step/start, assistant settlement, tool/call,
		// each tool/result, step/end and turn/end; also partial physical tails.
		t.Run(stringBoundary(prefix), func(t *testing.T) {
			dir := t.TempDir()
			path := writePrefix(t, dir, header, prefix, []byte(`{"type":"assistant/message"`))
			closers, err := InterruptedTurnClosers(prefix)
			if err != nil {
				t.Fatal(err)
			}
			store, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			current, err := openRecovered(context.Background(), store, header.ID)
			if err != nil {
				t.Fatal(err)
			}
			facts := collectEvents(t, current)
			assertRepairJSON(t, prefix, facts[:n])
			assertRepairJSON(t, closers, facts[n:len(facts)-1])
			if facts[len(facts)-1].Type != protocol.SessionEndSeed {
				t.Fatal("marker did not follow repair")
			}
			if current.LastTurn() != func() protocol.TurnID {
				if n >= 2 {
					return 1
				}
				return 0
			}() {
				t.Fatal("lost Turn cursor")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			if _, err := openRecovered(context.Background(), again, header.ID); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("repeat recovery mutated balanced prefix/marker")
			}
		})
	}
}

func stringBoundary(events []protocol.SessionEvent) string {
	if len(events) == 0 {
		return "header"
	}
	e := events[len(events)-1]
	return string(e.Type) + "/" + fmt.Sprint(e.Seq)
}

func TestRecoveryWriteFailureConvergesFromCommittedPrefix(t *testing.T) {
	header, events, expected := repairFixture(t, "repair-mixed-assistant-order")
	for failAt := 0; failAt <= len(expected); failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			dir := t.TempDir()
			store, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Create(context.Background(), CreateRequest{ID: header.ID, Header: &header})
			if err != nil {
				t.Fatal(err)
			}
			appendEvents(t, current, events...)
			disk := store.persistence.handles[current.ID()]
			disk.file = &faultFile{logFile: disk.file, writeErr: errors.New("recovery write"), failWriteAt: failAt + 1}
			_, err = current.Attach()
			if err == nil {
				t.Fatal("recovery concealed failed durable write")
			}
			if got := len(collectEvents(t, current)) - len(events); got != failAt {
				t.Fatalf("committed %d, want %d", got, failAt)
			}
			_ = store.Close()
			reopened, err := newRuntimeFixture(dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restored, err := openRecovered(context.Background(), reopened, header.ID)
			if err != nil {
				t.Fatal(err)
			}
			facts := collectEvents(t, restored)
			assertRepairJSON(t, expected, facts[len(events):len(facts)-1])
			if facts[len(facts)-1].Type != protocol.SessionEndSeed {
				t.Fatal("no marker")
			}
		})
	}
}

func TestAttachRecoversExclusivelyAndFailedRepairBlocksAdmission(t *testing.T) {
	header, events, expected := repairFixture(t, "repair-mixed-assistant-order")
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "jsonl"}[persistent], func(t *testing.T) {
			var store Store = NewRuntime(RuntimeOptions{})
			if persistent {
				disk, err := newRuntimeFixture(t.TempDir(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer disk.Close()
				store = disk
			}
			current, err := store.Create(context.Background(), CreateRequest{ID: header.ID, Header: &header})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			release, err := lease.BeginRun()
			if err != nil {
				t.Fatal(err)
			}
			appendEvents(t, current, events...)
			if _, err := current.Attach(); !errors.Is(err, ErrAttached) {
				t.Fatalf("live owner recovered: %v", err)
			}
			if len(collectEvents(t, current)) != len(events) {
				t.Fatal("active prefix repaired")
			}
			release()
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if persistent {
				disk := store.(*runtimeFixture).persistence.handles[current.ID()]
				file := disk.file
				disk.file = &faultFile{logFile: file, writeErr: errors.New("repair write")}
				if _, err := current.Attach(); err == nil {
					t.Fatal("failed repair acquired owner")
				}
				if _, err := current.Append(context.Background(), protocol.NewStepEnded(1, 1)); !errors.Is(err, ErrUnclosedLog) {
					t.Fatalf("write during failed recovery: %v", err)
				}
				if _, err := current.BeginRun(); !errors.Is(err, ErrUnclosedLog) {
					t.Fatalf("run during failed recovery: %v", err)
				}
				disk.file = file
			}
			next, err := current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			if next.CreationSource() != "resume" {
				t.Fatal("not resume")
			}
			facts := collectEvents(t, current)
			assertRepairJSON(t, expected, facts[len(events):len(facts)-1])
			if err := next.Close(); err != nil {
				t.Fatal(err)
			}
			third, err := current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			defer third.Close()
			if len(collectEvents(t, current)) != len(facts) {
				t.Fatal("reattach duplicated repair")
			}
		})
	}
}

func TestWriterLockChild(t *testing.T) {
	dir := os.Getenv("JSONL_LOCK_CHILD_DIR")
	if dir == "" {
		return
	}
	store, err := newRuntimeFixture(dir, nil)
	if errors.Is(err, ErrStoreLocked) {
		return
	}
	if store != nil {
		_ = store.Close()
	}
	t.Fatalf("another process admitted writer: %v", err)
}

func TestStoreWriterLockPrecedesRecovery(t *testing.T) {
	dir := t.TempDir()
	header, events, _ := repairFixture(t, "repair-tool-not-started")
	store, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Write the crash prefix while the owner has no loaded Session. A second
	// constructor must refuse before truncating its tail or adding any closer.
	path := writePrefix(t, dir, header, events, []byte("partial"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newRuntimeFixture(filepath.Join(dir, "."), nil); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("same-process writer=%v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWriterLockChild$")
	cmd.Env = append(os.Environ(), "JSONL_LOCK_CHILD_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, output)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("losing writer changed crash prefix")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newRuntimeFixture(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := openRecovered(context.Background(), reopened, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Watermark() <= protocol.Sequence(len(events)-1) {
		t.Fatal("next owner did not recover")
	}
}

type recoveryObserver struct {
	capturedObserver
	check func(protocol.SessionID, protocol.SessionEvent)
}

func (o *recoveryObserver) SessionAppended(id protocol.SessionID, event protocol.SessionEvent) {
	if o.check != nil {
		o.check(id, event)
	}
	o.capturedObserver.SessionAppended(id, event)
}

func TestRepairObservationsFollowCommittedFactsOutsideSessionMutex(t *testing.T) {
	header, events, expected := repairFixture(t, "repair-mixed-assistant-order")
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprint(persistent), func(t *testing.T) {
			observer := &recoveryObserver{}
			var store Store = NewRuntime(RuntimeOptions{Observer: observer})
			dir := t.TempDir()
			if persistent {
				disk, err := newRuntimeFixture(dir, observer)
				if err != nil {
					t.Fatal(err)
				}
				defer disk.Close()
				store = disk
			}
			current, err := store.Create(context.Background(), CreateRequest{ID: header.ID, Header: &header})
			if err != nil {
				t.Fatal(err)
			}
			appendEvents(t, current, events...)
			observer.sessions = nil
			observer.check = func(id protocol.SessionID, e protocol.SessionEvent) {
				if id != current.ID() || current.Watermark() < e.Seq {
					t.Fatal("observation preceded commit")
				}
				persisted := collectEvents(t, current)[e.Seq]
				assertRepairJSON(t, []protocol.SessionEvent{persisted}, []protocol.SessionEvent{e})
			}
			lease, err := current.Attach()
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			assertRepairJSON(t, expected, observer.sessions[:len(expected)])
			if len(observer.sessions) != len(expected)+1 || observer.sessions[len(expected)].Type != protocol.SessionEndSeed {
				t.Fatal("missing committed repair observations")
			}
		})
	}
	// Construction and Open publish nothing; Attach publishes only the repair suffix.
	dir := t.TempDir()
	writePrefix(t, dir, header, events, nil)
	observer := &capturedObserver{}
	store, err := newRuntimeFixture(dir, observer)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if len(observer.sessions) != 0 {
		t.Fatal("construction published facts")
	}
	current, err := store.Open(context.Background(), header.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.sessions) != 0 {
		t.Fatal("Open published repair")
	}
	lease, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertRepairJSON(t, expected, observer.sessions[:len(expected)])
	if len(observer.sessions) != len(expected)+1 {
		t.Fatal("Attach observation count", len(observer.sessions))
	}
}

func repairFixture(t *testing.T, id string) (protocol.SessionHeader, []protocol.SessionEvent, []protocol.SessionEvent) {
	t.Helper()
	header := protocol.SessionHeader{Version: protocol.SessionFormatVersion, ID: "s1", CreatedAt: 1000}
	events := []protocol.SessionEvent{
		{Type: "agent/inbox/spliced", Seq: 0, Time: 1001, Data: json.RawMessage(`{"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}`)},
		{Type: "turn/start", Seq: 1, Time: 1002, Data: json.RawMessage(`{"turn":1}`)},
		{Type: "agent/inbox/spliced", Seq: 2, Time: 1003, Data: json.RawMessage(`{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}`)},
		{Type: "step/start", Seq: 3, Time: 1004, Data: json.RawMessage(`{"turn":1,"step":1}`)},
		{Type: "system/message", Seq: 4, Time: 1005, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}}`), SurfaceOp: protocol.AppendSurface()},
		{Type: "user/message", Seq: 5, Time: 1006, Data: json.RawMessage(`{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}`), SurfaceOp: protocol.AppendSurface()},
		{Type: "request/header", Seq: 6, Time: 1007, Data: json.RawMessage(`{"header":{"config":{"provider":"p","model":"m"},"tools":[{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]},"reason":"initial"}`)},
		{Type: "request/context", Seq: 7, Time: 1007, Data: json.RawMessage(`{"provider":"p","model":"m"}`)},
	}
	var repair []protocol.SessionEvent
	switch id {
	case "repair-mixed-assistant-order":
		events = append(events, []protocol.SessionEvent{
			{Type: "assistant/message", Seq: 8, Time: 1008, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"a-tool","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},{"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}]},"stream":[{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}},{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}]}`), SurfaceOp: protocol.AppendSurface()},
			{Type: "tool/call", Seq: 9, Time: 1009, Data: json.RawMessage(`{"turn":1,"step":1,"callId":"c2","name":"echo","arguments":"{broken"}`)},
		}...)
		repair = []protocol.SessionEvent{
			{Type: "tool/result", Seq: 10, Time: 1009, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"interrupted-tool-result-c1-10","role":"tool","toolCallId":"c1","isError":true,"source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]},"error":{"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}}`), SurfaceOp: protocol.AppendSurface()},
			{Type: "tool/result", Seq: 11, Time: 1009, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"interrupted-tool-result-c2-11","role":"tool","toolCallId":"c2","isError":true,"source":{"kind":"tool","callId":"c2"},"content":[{"type":"text","text":"The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."}]},"error":{"name":"ToolOutcomeUnknownError","code":"TOOL_OUTCOME_UNKNOWN"}}`), SurfaceOp: protocol.AppendSurface(), SourceEventSeqs: []protocol.Sequence{9}},
			{Type: "step/end", Seq: 12, Time: 1009, Data: json.RawMessage(`{"turn":1,"step":1}`)},
			{Type: "turn/end", Seq: 13, Time: 1009, Data: json.RawMessage(`{"turn":1,"reason":{"kind":"interrupted"}}`)},
		}
	case "repair-tool-not-started":
		events = append(events, []protocol.SessionEvent{
			{Type: "assistant/message", Seq: 8, Time: 1008, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"a-tool","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]},"stream":[{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}]}`), SurfaceOp: protocol.AppendSurface()},
		}...)
		repair = []protocol.SessionEvent{
			{Type: "tool/result", Seq: 9, Time: 1008, Data: json.RawMessage(`{"turn":1,"step":1,"message":{"id":"interrupted-tool-result-c1-9","role":"tool","toolCallId":"c1","isError":true,"source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]},"error":{"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}}`), SurfaceOp: protocol.AppendSurface()},
			{Type: "step/end", Seq: 10, Time: 1008, Data: json.RawMessage(`{"turn":1,"step":1}`)},
			{Type: "turn/end", Seq: 11, Time: 1008, Data: json.RawMessage(`{"turn":1,"reason":{"kind":"interrupted"}}`)},
		}
	default:
		t.Fatal("unknown repair case", id)
	}
	return header, events, repair
}
