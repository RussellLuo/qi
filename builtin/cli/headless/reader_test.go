package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/builtin/session/jsonl"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type synchronizedSink struct {
	mu sync.Mutex
	bytes.Buffer
	lines chan string
}

func (w *synchronizedSink) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.Buffer.Write(b)
	select {
	case w.lines <- string(b):
	default:
	}
	return n, err
}
func (w *synchronizedSink) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *synchronizedSink) text() string                      { w.mu.Lock(); defer w.mu.Unlock(); return w.Buffer.String() }
func parseLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}
func TestReaderPollsWithoutHintsAndDrainsFixedWatermark(t *testing.T) {
	owner, err := jsonl.NewOwner(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: resolveJSONLOwner(owner)})
	defer owner.Close()
	defer store.Close(context.Background())
	ctx, cancelContext := context.WithCancel(context.Background())
	current, err := store.Create(ctx, session.CreateRequest{ID: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &synchronizedSink{lines: make(chan string, 1000)}
	canceled := make(chan struct{}, 1)
	cancel := func() {
		select {
		case canceled <- struct{}{}:
		default:
		}
	}
	var notify func(context.Context, gordisbridge.SessionObservation) error
	stopped := false
	observe := func(fn func(context.Context, gordisbridge.SessionObservation) error) (func(), error) {
		notify = fn
		return func() { stopped = true }, nil
	}
	r, err := startReader(context.WithoutCancel(ctx), current, -1, "/fixed", sink, observe, cancel)
	if err != nil {
		t.Fatal(err)
	}
	<-sink.lines // opening session; no task events exist yet
	appendEvent := func(e protocol.SessionEvent) {
		if _, err := current.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(protocol.NewTurnStarted(1))
	// No append notification is delivered, but output must arrive while active.
	select {
	case line := <-sink.lines:
		if !strings.Contains(line, "turn_start") {
			t.Fatal(line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("polling failed during activity")
	}
	cancelContext() // settlement reads survive activity cancellation
	appendEvent(protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
	for turn := protocol.TurnID(2); turn <= 100; turn++ {
		appendEvent(protocol.NewTurnStarted(turn))
		appendEvent(protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "completed"}))
	}
	// Foreign and duplicate/stale hints are harmless; their event data is ignored.
	for i := 0; i < 200; i++ {
		notify(context.Background(), gordisbridge.SessionObservation{SessionID: "other"})
		notify(context.Background(), gordisbridge.SessionObservation{SessionID: current.ID()})
	}
	text := "terminal"
	if err = r.close(current.Watermark(), &text); err != nil {
		t.Fatal(err)
	}
	lines := parseLines(t, sink.text())
	if len(lines) != 202 || lines[len(lines)-1]["type"] != "final" || !stopped {
		t.Fatalf("lines=%d stopped=%v", len(lines), stopped)
	}
	for i := 0; i < 100; i++ {
		start, end := lines[1+2*i], lines[2+2*i]
		if start["phase"] != "turn_start" || end["phase"] != "turn_end" || start["turn"] != float64(i+1) || end["turn"] != float64(i+1) {
			t.Fatalf("out of order: %v %v", start, end)
		}
	}
	select {
	case <-canceled:
		t.Fatal("canceled successful reader")
	default:
	}
}

type brokenReadSession struct {
	session.Session
	gap     bool
	failure error
}

func (s brokenReadSession) Events(ctx context.Context, after protocol.Sequence) iter.Seq2[protocol.SessionEvent, error] {
	return func(yield func(protocol.SessionEvent, error) bool) {
		for e, err := range s.Session.Events(ctx, after) {
			if s.failure != nil {
				yield(protocol.SessionEvent{}, s.failure)
				return
			}
			if s.gap && e.Seq == 0 {
				continue
			}
			if !yield(e, err) {
				return
			}
		}
	}
}
func TestReaderCannotFinalizeUnreadDurablePrefix(t *testing.T) {
	for _, gap := range []bool{true, false} {
		t.Run(map[bool]string{true: "gap", false: "read-error"}[gap], func(t *testing.T) {
			current, _ := session.NewRuntime(session.RuntimeOptions{}).Create(context.Background(), session.CreateRequest{ID: "reader"})
			current.Append(context.Background(), protocol.NewTurnStarted(1))
			current.Append(context.Background(), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
			broken := brokenReadSession{Session: current, gap: gap}
			if !gap {
				broken.failure = errors.New("read refusal")
			}
			var out bytes.Buffer
			canceled := false
			r, err := startReader(context.Background(), broken, -1, "/fixed", &out, nil, func() { canceled = true })
			if err != nil {
				t.Fatal(err)
			}
			text := "answer"
			err = r.close(current.Watermark(), &text)
			if err == nil || !canceled || strings.Contains(out.String(), `"final"`) {
				t.Fatalf("err=%v canceled=%v out=%q", err, canceled, out.String())
			}
		})
	}
}

type failingLineSink struct {
	bytes.Buffer
	count, failAt int
	failure       error
	short         bool
}

func (s *failingLineSink) Write(b []byte) (int, error) {
	s.count++
	if s.count == s.failAt {
		if s.short {
			return len(b) - 1, nil
		}
		return 0, s.failure
	}
	return s.Buffer.Write(b)
}
func (s *failingLineSink) WriteString(text string) (int, error) { return s.Write([]byte(text)) }
func TestReaderOutputFailureCancelsAndDoesNotFinalize(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		for _, short := range []bool{false, true} {
			current, _ := session.NewRuntime(session.RuntimeOptions{}).Create(context.Background(), session.CreateRequest{ID: "reader"})
			current.Append(context.Background(), protocol.NewTurnStarted(1))
			current.Append(context.Background(), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
			failure := errors.New("broken output")
			sink := &failingLineSink{failAt: failAt, failure: failure, short: short}
			canceled := false
			r, err := startReader(context.Background(), current, -1, "/fixed", sink, nil, func() { canceled = true })
			if err == nil {
				text := "answer"
				err = r.close(current.Watermark(), &text)
			}
			want := failure
			if short {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || !canceled || strings.Contains(sink.String(), `"final"`) {
				t.Fatalf("err=%v canceled=%v out=%q", err, canceled, sink.String())
			}
		}
	}
}
