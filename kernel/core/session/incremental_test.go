package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func assertIncrementalProjection(t *testing.T, current *LiveSession) {
	t.Helper()
	view, err := current.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, err := Fold(collectEvents(t, current))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Projection, want) {
		t.Fatalf("incremental projection differs from Fold:\ngot: %#v\nwant: %#v", view.Projection, want)
	}
	messages, err := current.Messages(context.Background())
	if err != nil || !reflect.DeepEqual(messages, want.Messages) {
		t.Fatalf("Messages differs: %v", err)
	}
	inbox, err := NewInbox(current).Pending(context.Background())
	if err != nil || !reflect.DeepEqual(inbox, want.Inbox) {
		t.Fatalf("Inbox differs: %v", err)
	}
}

// Exercise every prefix, including invalid candidates, against an independent
// Fold, without mutating the committed projection on rejection.
func TestIncrementalProjectionMatchesPrefixes(t *testing.T) {
	cases := projectionCases()
	for _, fixture := range cases {
		if fixture.Operation != "session" && fixture.Operation != "inbox" {
			continue
		}
		t.Run(fixture.ID, func(t *testing.T) {
			var input struct {
				Events []json.RawMessage `json:"events"`
			}
			if err := json.Unmarshal(fixture.Input, &input); err != nil {
				t.Fatal(err)
			}
			state, err := newProjectionState(nil)
			if err != nil {
				t.Fatal(err)
			}
			events := []protocol.SessionEvent{}
			for _, raw := range input.Events {
				event, err := protocol.DecodeSessionEvent(raw)
				if err != nil {
					break // Both paths use the same strict wire decoder.
				}
				want, foldErr := Fold(append(cloneEvents(events), event))
				before := state.snapshot()
				publish, incrementalErr := state.prepare(events, event)
				if (foldErr == nil) != (incrementalErr == nil) {
					t.Fatalf("seq %d: Fold=%v, incremental=%v", event.Seq, foldErr, incrementalErr)
				}
				if !reflect.DeepEqual(before, state.snapshot()) {
					t.Fatal("prepare mutated committed state")
				}
				if foldErr != nil {
					break
				}
				publish()
				events = append(events, event)
				if !reflect.DeepEqual(state.snapshot(), want) {
					t.Fatalf("seq %d: incremental result differs from Fold", event.Seq)
				}
			}
		})
	}
}

func newIncrementalSession(t *testing.T, commit func([]byte) error) *LiveSession {
	t.Helper()
	header, err := CreateHeader("incremental", nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Restore(header, nil, nil, commit)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestIncrementalCommitFailureAndRetry(t *testing.T) {
	fail := false
	current := newIncrementalSession(t, func([]byte) error {
		if fail {
			return errors.New("disk unavailable")
		}
		return nil
	})
	ctx := context.Background()
	tool := protocol.ToolSchema{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}
	h := protocol.EpochHeader{
		Config: protocol.CallConfig{Provider: "test", Model: "test"},
		Tools:  []protocol.ToolSchema{tool},
	}
	seq := protocol.Sequence(2)
	developer := protocol.NewSessionEvent(protocol.DeveloperMessage, protocol.MessageData{
		Turn: 1, Step: 1, HeaderSeq: &seq,
		Message: protocol.Message{
			ID:      "tools",
			Role:    protocol.RoleDeveloper,
			Source:  protocol.MessageSource{Kind: "tool-registry"},
			Content: []protocol.ContentBlock{protocol.ToolAdditionBlock{ToolName: "echo"}},
		},
	})
	developer.SurfaceOp = protocol.AppendSurface()
	events := []protocol.SessionEvent{
		protocol.NewTurnStarted(1),
		protocol.NewStepStarted(1, 1),
		protocol.NewSessionEvent(protocol.RequestHeader, protocol.RequestHeaderData{Header: h, Reason: "initial"}),
		protocol.NewSessionEvent(protocol.RequestContextEvent, protocol.RequestContext{Provider: "test", Model: "test"}),
		developer,
		protocol.NewMessageEvent(
			protocol.SystemMessage,
			1,
			1,
			protocol.NewTextMessage("system", protocol.RoleSystem, protocol.SourceSystem, "prompt"),
		),
		protocol.NewMessageEvent(protocol.UserMessage, 1, 1, inboxMessage("user")),
		protocol.NewSessionEvent(
			protocol.InboxSpliced,
			protocol.InboxSpliceData{
				Target:   contracts.NextTurn,
				Start:    0,
				Inserted: []protocol.Message{inboxMessage("queued")},
			},
		),
		protocol.NewAssistantMessageEvent(
			1,
			1,
			protocol.NewAssistantTextMessage("answer", "test", "test", "answer"),
			[]protocol.AssistantStreamRecord{},
			nil,
		),
		protocol.NewStepEnded(1, 1),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	}
	for _, event := range events {
		before, _ := current.Snapshot(ctx)
		watermark, lastTurn := current.Watermark(), current.LastTurn()
		fail = true
		if seq, err := current.Append(ctx, event); err == nil || seq != -1 {
			t.Fatalf("failed commit accepted %s: %d %v", event.Type, seq, err)
		}
		after, _ := current.Snapshot(ctx)
		if !reflect.DeepEqual(before, after) || current.Watermark() != watermark || current.LastTurn() != lastTurn {
			t.Fatalf("failed %s polluted state", event.Type)
		}
		fail = false
		appendEvents(t, current, event)
		assertIncrementalProjection(t, current)
	}
	// A successful replacement after a failed commit must shadow exactly once.
	rewrite := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, inboxMessage("rewritten"))
	rewrite.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 6, EndSeq: 6}
	rewrite.SourceEventSeqs = []protocol.Sequence{6}
	fail = true
	before, _ := current.Snapshot(ctx)
	if _, err := current.Append(ctx, rewrite); err == nil {
		t.Fatal("replacement committed")
	}
	after, _ := current.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed replacement changed cache")
	}
	fail = false
	appendEvents(t, current, rewrite)
	assertIncrementalProjection(t, current)
}

func TestIncrementalRejectsInvalidReferencesWithoutMutation(t *testing.T) {
	current := newIncrementalSession(t, nil)
	call := protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{}`}
	header := protocol.EpochHeader{
		Config: protocol.CallConfig{Provider: "test", Model: "test"},
		Tools:  []protocol.ToolSchema{{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	result := protocol.ToolResult{
		Message: protocol.NewToolResultMessage(
			"result",
			"call",
			[]protocol.ContentBlock{protocol.TextBlock{Text: "ok"}},
			false,
		),
	}
	appendEvents(t, current,
		protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1),
		protocol.NewMessageEvent(
			protocol.SystemMessage,
			1,
			1,
			protocol.NewTextMessage("system", protocol.RoleSystem, protocol.SourceSystem, "prompt"),
		),
		protocol.NewMessageEvent(protocol.UserMessage, 1, 1, inboxMessage("user")),
		protocol.NewToolCallEvent(1, 1, call),
		protocol.NewToolResultEvent(1, 1, result, 4),
		protocol.NewSessionEvent(protocol.RequestHeader, protocol.RequestHeaderData{Header: header, Reason: "initial"}),
		protocol.NewSessionEvent(
			protocol.InboxSpliced,
			protocol.InboxSpliceData{
				Target:   contracts.NextTurn,
				Start:    0,
				Inserted: []protocol.Message{inboxMessage("pending")},
			},
		),
	)
	replace := func(start, end protocol.Sequence, sources []protocol.Sequence) protocol.SessionEvent {
		event := protocol.NewMessageEvent(protocol.UserMessage, 1, 1, inboxMessage("replacement"))
		event.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: start, EndSeq: end}
		event.SourceEventSeqs = sources
		return event
	}
	developer := func(ref protocol.Sequence, tool string) protocol.SessionEvent {
		event := protocol.NewSessionEvent(protocol.DeveloperMessage, protocol.MessageData{
			Turn: 1, Step: 1, HeaderSeq: &ref,
			Message: protocol.Message{
				ID:      "delta",
				Role:    protocol.RoleDeveloper,
				Source:  protocol.MessageSource{Kind: "tool-registry"},
				Content: []protocol.ContentBlock{protocol.ToolAdditionBlock{ToolName: tool}},
			},
		})
		event.SurfaceOp = protocol.AppendSurface()
		return event
	}
	badResult := protocol.NewToolResultEvent(1, 1, result, 4)
	badResult.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 5, EndSeq: 5}
	badResult.SourceEventSeqs = []protocol.Sequence{5}
	data := protocol.EventPayload[protocol.ToolResultData](badResult)
	data.Message.ID = "changed-identity"
	badResult.Data, _ = json.Marshal(data)
	badTarget := badResult.Clone()
	badTarget.SurfaceOp.StartSeq, badTarget.SurfaceOp.EndSeq = 3, 3
	badTarget.SourceEventSeqs = []protocol.Sequence{3}
	stagedDelta := developer(6, "echo")
	stagedDelta.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: 3, EndSeq: 3}
	// Missing shadowed source is discovered after tool declarations are parsed.
	for name, event := range map[string]protocol.SessionEvent{
		"non-surface range": replace(4, 4, []protocol.Sequence{4}),
		"reversed range":    replace(5, 3, []protocol.Sequence{3, 5}),
		"missing shadow":    replace(3, 5, []protocol.Sequence{3}),
		"system head":       replace(2, 2, []protocol.Sequence{2}),
		"tool identity":     badResult,
		"tool target":       badTarget,
		"header type":       developer(3, "echo"),
		"undefined tool":    developer(6, "missing"),
		"staged delta":      stagedDelta,
		"inbox span": protocol.NewSessionEvent(protocol.InboxSpliced, protocol.InboxSpliceData{
			Target: contracts.NextTurn, Start: 2, Inserted: []protocol.Message{},
		}),
		"inbox duplicate": protocol.NewSessionEvent(protocol.InboxSpliced, protocol.InboxSpliceData{
			Target: contracts.NextStep, Start: 0, Inserted: []protocol.Message{inboxMessage("pending")},
		}),
		"execution bracket": protocol.NewStepEnded(1, 2),
	} {
		t.Run(name, func(t *testing.T) {
			before, _ := current.Snapshot(context.Background())
			watermark := current.Watermark()
			if _, err := current.Append(context.Background(), event); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			after, _ := current.Snapshot(context.Background())
			if current.Watermark() != watermark || !reflect.DeepEqual(before, after) {
				t.Fatal("rejection mutated committed state")
			}
			assertIncrementalProjection(t, current)
		})
	}
	// Valid content-only tool rewrite and repeated surface replacement succeed.
	data.Message.ID = "result"
	data.Message.Content = []protocol.ContentBlock{protocol.TextBlock{Text: "rewritten"}}
	badResult.Data, _ = json.Marshal(data)
	appendEvents(t, current, badResult, developer(6, "echo"), replace(3, 3, []protocol.Sequence{3}))
	assertIncrementalProjection(t, current)
	rewrite := replace(current.Watermark(), current.Watermark(), []protocol.Sequence{current.Watermark()})
	appendEvents(t, current, rewrite)
	assertIncrementalProjection(t, current)
	view, _ := current.Snapshot(context.Background())
	view.Projection.Surface.Replacements[0].ShadowedSeqs[0] = 999
	view.Projection.ToolHistory.Updates[0].Additions[0].Parameters[0] = 'x'
	view.Projection.Messages[0].Content[0] = protocol.TextBlock{Text: "changed"}
	assertIncrementalProjection(t, current)
}

func TestIncrementalRestoreRepairCommittedPrefix(t *testing.T) {
	current := newIncrementalSession(t, nil)
	call := protocol.ToolCall{ID: "call", Name: "echo", Arguments: `{}`}
	appendEvents(t, current,
		protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1),
		protocol.NewAssistantMessageEvent(
			1,
			1,
			protocol.NewToolCallMessage("call-message", call, protocol.MessageSource{Kind: protocol.SourceModel, Provider: "test", Model: "test"}),
			[]protocol.AssistantStreamRecord{},
			nil,
		),
		protocol.NewToolCallEvent(1, 1, call),
	)
	events := collectEvents(t, current)
	commits := 0
	reopened, err := Restore(current.Header(), events, nil, func([]byte) error {
		commits++
		if commits == 2 {
			return errors.New("second recovery commit fails")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Caller changes to the loaded prefix cannot affect cache or durable facts.
	events[0].Data[0] = 'x'
	assertIncrementalProjection(t, reopened)
	if _, err := reopened.Attach(); err == nil || reopened.Watermark() != 4 {
		t.Fatalf("repair did not retain only the committed tool result: %v", err)
	}
	assertIncrementalProjection(t, reopened)
	lease, err := reopened.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertIncrementalProjection(t, reopened)
	end := reopened.Watermark()
	lease, err = reopened.Attach()
	if err != nil || reopened.Watermark() != end {
		t.Fatal("repair is not idempotent", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, reopened, protocol.NewTurnStarted(2), protocol.NewTurnEnded(2, protocol.TurnEndReason{Kind: "completed"}))
	assertIncrementalProjection(t, reopened)
}

type replayOnlySession struct{ Session }

func TestIncrementalSnapshotCopiesAndFallback(t *testing.T) {
	current := newIncrementalSession(t, nil)
	window := int64(8192)
	mode := "in-history"
	max := int64(128)
	appendEvents(t, current,
		protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1),
		protocol.NewMessageEvent(protocol.SystemMessage, 1, 1, protocol.NewTextMessage("system", protocol.RoleSystem, protocol.SourceSystem, "")),
		protocol.NewSessionEvent(protocol.RequestHeader, protocol.RequestHeaderData{Reason: "initial", Header: protocol.EpochHeader{
			Config:          protocol.CallConfig{Provider: "test", Model: "test", MaxTokens: &max, Stop: []string{"stop"}},
			AdapterDefaults: &protocol.AdapterDefaults{MaxTokens: true},
			Tools:           []protocol.ToolSchema{{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
		}}),
		protocol.NewSessionEvent(
			protocol.RequestContextEvent,
			protocol.RequestContext{Provider: "test", Model: "test", ContextWindow: &window, SystemPromptUpdate: &mode},
		),
		protocol.NewSessionEvent(
			protocol.InboxSpliced,
			protocol.InboxSpliceData{
				Target:   contracts.NextStep,
				Start:    0,
				Inserted: []protocol.Message{inboxMessage("pending")},
			},
		),
		protocol.NewStepEnded(1, 1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	)
	ctx := context.Background()
	want, _ := ReadSnapshot(ctx, replayOnlySession{current})
	view, err := ReadSnapshot(ctx, current)
	if err != nil || !reflect.DeepEqual(view, want) || len(view.SystemEvents) != 1 {
		t.Fatal("cached and fallback snapshots differ", err)
	}
	view.Projection.Surface.Nodes[0] = 999
	view.Projection.RequestHeader.Config.Stop[0] = "changed"
	*view.Projection.RequestHeader.Config.MaxTokens = 1
	view.Projection.RequestHeader.AdapterDefaults.MaxTokens = false
	view.Projection.RequestHeader.Tools[0].Parameters[0] = 'x'
	view.Projection.ToolHistory.Tools[0].Parameters[0] = 'x'
	*view.Projection.RequestContext.ContextWindow = 1
	*view.Projection.RequestContext.SystemPromptUpdate = "changed"
	view.Projection.Inbox[contracts.NextStep][0].Content[0] = protocol.TextBlock{Text: "changed"}
	view.SystemEvents[0].Data[0] = 'x'
	next, _ := current.Snapshot(ctx)
	if !reflect.DeepEqual(next, want) {
		t.Fatal("caller mutation polluted snapshot")
	}
	assertIncrementalProjection(t, current)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ReadSnapshot(canceled, current); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := current.Close(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Memory and persistent Session read-after-close semantics stay unchanged.
	if _, err := current.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	persistent := newIncrementalSession(t, func([]byte) error { return nil })
	_ = persistent.Close(func() error { return nil })
	if _, err := persistent.Snapshot(ctx); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := persistent.PendingInbox(ctx); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
}

// Fail immediately if an online consumer accidentally falls back to Events.
type noReplaySession struct{ *LiveSession }

func (s noReplaySession) Events(context.Context, protocol.Sequence) iter.Seq2[protocol.SessionEvent, error] {
	panic("online cached reads replayed the log")
}

func TestIncrementalOnlineReadsAndConcurrentAppend(t *testing.T) {
	current := newIncrementalSession(t, nil)
	online := noReplaySession{current}
	ctx := context.Background()
	var readers sync.WaitGroup
	for n := 0; n < 4; n++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for n := 0; n < 100; n++ {
				if _, err := ReadSnapshot(ctx, online); err != nil {
					t.Error(err)
				}
				if _, err := online.Messages(ctx); err != nil {
					t.Error(err)
				}
				if _, err := NewInbox(online).Pending(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	for n := 1; n <= 100; n++ {
		turn := protocol.TurnID(n)
		appendEvents(t, current, protocol.NewTurnStarted(turn),
			protocol.NewMessageEvent(protocol.UserMessage, turn, 0, inboxMessage(fmt.Sprintf("user-%d", n))),
			protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "completed"}))
	}
	readers.Wait()
	assertIncrementalProjection(t, current)
}
