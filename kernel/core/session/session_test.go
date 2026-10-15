package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestLiveSessionEventLogIsAppendOnlyAndReplayable(t *testing.T) {
	ctx := context.Background()
	store := NewRuntime(RuntimeOptions{})
	created, err := store.Create(ctx, CreateRequest{ID: "session-replay"})
	if err != nil {
		t.Fatal(err)
	}

	turnID := protocol.TurnID(1)
	stepID := protocol.StepID(1)
	appendEvents(t, created,
		protocol.NewTurnStarted(turnID),
		protocol.NewMessageEvent(
			protocol.UserMessage,
			turnID,
			0,
			protocol.NewTextMessage("message-user", protocol.RoleUser, protocol.SourceUser, "hello"),
		),
		protocol.NewStepStarted(turnID, stepID),
		protocol.NewAssistantMessageEvent(
			turnID,
			stepID,
			protocol.NewToolCallMessage("assistant-call", protocol.ToolCall{
				ID:        "call-1",
				Name:      "echo",
				Arguments: `{"text":"hello"}`,
			}, protocol.MessageSource{
				Kind:     protocol.SourceModel,
				Provider: "test",
				Model:    "test",
			}),
			[]protocol.AssistantStreamRecord{},
			nil,
		),
		protocol.NewToolCallEvent(turnID, stepID, protocol.ToolCall{
			ID:        "call-1",
			Name:      "echo",
			Arguments: string(`{"text":"hello"}`),
		}),
		protocol.NewToolResultEvent(
			turnID,
			stepID,
			protocol.ToolResult{
				Message: protocol.NewToolResultMessage(
					"result-message",
					"call-1",
					[]protocol.ContentBlock{protocol.TextBlock{Text: `{"text":"hello"}`}},
					false,
				),
			},
			4,
		),
		protocol.NewAssistantMessageEvent(
			turnID,
			stepID,
			protocol.NewAssistantTextMessage("message-assistant", "test", "test", "done"),
			[]protocol.AssistantStreamRecord{},
			nil,
		),
		protocol.NewStepEnded(turnID, stepID),
		protocol.NewTurnEnded(turnID, protocol.TurnEndReason{Kind: "completed"}),
	)

	events := collectEvents(t, created)
	if got, want := len(events), 9; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}

	for i, event := range events {
		if got, want := event.Seq, protocol.Sequence(i); got != want {
			t.Fatalf("event %d sequence = %d, want %d", i, got, want)
		}
	}

	messages, err := created.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}

	replayed, err := ProjectMessages(events)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(messages, replayed) {
		t.Fatalf("replayed messages differ:\nactual: %#v\nreplay: %#v", messages, replayed)
	}

	if roles := messageRoles(messages); !reflect.DeepEqual(roles, []protocol.Role{
		protocol.RoleUser, protocol.RoleAssistant, protocol.RoleTool, protocol.RoleAssistant,
	}) {
		t.Fatalf("message roles = %v", roles)
	}

	if messages[2].Source.Kind != protocol.SourceTool {
		t.Fatalf("tool result source = %q", messages[2].Source)
	}

	if _, ok := messages[2].Content[0].(protocol.TextBlock); !ok {
		t.Fatalf("tool result block = %T", messages[2].Content[0])
	}
}

func TestAssistantAttemptIsFactButNotSuccessfulMessage(t *testing.T) {
	ctx := context.Background()
	created, err := NewRuntime(RuntimeOptions{}).Create(ctx, CreateRequest{ID: "session-failed"})
	if err != nil {
		t.Fatal(err)
	}

	detail := protocol.NewErrorDetail(protocol.ErrorStream, errors.New("stream interrupted"))
	appendEvents(t, created,
		protocol.NewTurnStarted(1),
		protocol.NewMessageEvent(
			protocol.UserMessage,
			1,
			0,
			protocol.NewTextMessage("message-user", protocol.RoleUser, protocol.SourceUser, "hello"),
		),
		protocol.NewStepStarted(1, 1),
		protocol.NewAttemptEvent(1, 1, []protocol.AssistantStreamRecord{}),
		protocol.NewStepEnded(1, 1),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "error", Error: &protocol.LlmFailure{Code: "UNKNOWN", Message: detail.Message}}),
	)
	messages, err := created.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got := len(messages); got != 1 {
		t.Fatalf("projected message count = %d, want only the user message", got)
	}

	events := collectEvents(t, created)
	if events[3].Type != protocol.AssistantAttempt || protocol.EventPayload[protocol.AttemptData](events[3]).Stream == nil {
		t.Fatalf("attempt event was not retained: %+v", events[3])
	}
}

func TestLiveSessionRejectsInvalidSequenceTerminalAndJSON(t *testing.T) {
	ctx := context.Background()
	created, err := NewRuntime(RuntimeOptions{}).Create(ctx, CreateRequest{ID: "session-invalid"})
	if err != nil {
		t.Fatal(err)
	}

	badSequence := protocol.NewTurnStarted(1)
	badSequence.Seq = 99
	if _, err := ProjectMessages([]protocol.SessionEvent{badSequence}); !errors.Is(err, ErrInvalidSequence) {
		t.Fatalf("bad persisted sequence: %v", err)
	}

	appendEvents(t, created,
		protocol.NewTurnStarted(1),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	)
	if _, err := created.Append(ctx, protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("duplicate terminal error = %v", err)
	}

	unknown := protocol.SessionEvent{
		Ignorable: true,
		Type:      "extension/future",
		Data:      json.RawMessage(`{"broken":`),
	}
	if _, err := created.Append(ctx, unknown); !errors.Is(err, protocol.ErrInvalidProtocol) {
		t.Fatalf("non-JSON event error = %v", err)
	}

	unknown.Data = json.RawMessage(`{"retained":true}`)
	if _, err := created.Append(ctx, unknown); err != nil {
		t.Fatalf("append unknown event: %v", err)
	}

	if _, err := created.Append(ctx, protocol.SessionEvent{
		Type: "extension/future",
		Data: json.RawMessage(`{}`),
	}); !errors.Is(err, protocol.ErrInvalidProtocol) {
		t.Fatalf("unknown version error = %v", err)
	}
}

func TestLiveSessionCopiesEventsAndSerializesRuns(t *testing.T) {
	ctx := context.Background()
	created, err := NewRuntime(RuntimeOptions{}).Create(ctx, CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}

	message := protocol.NewTextMessage("message-1", protocol.RoleUser, protocol.SourceUser, "hello")
	event := protocol.NewMessageEvent(protocol.UserMessage, 1, 0, message)
	appendEvents(t, created, protocol.NewTurnStarted(1), event)
	message.Content[0] = protocol.TextBlock{Text: "mutated"}
	events := collectEvents(t, created)
	stored := protocol.EventPayload[protocol.Message](events[1]).Content[0].(protocol.TextBlock)
	if stored.Text != "hello" {
		t.Fatalf("stored text = %q", stored.Text)
	}

	appendEvents(t, created, protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
	release, err := created.BeginRun()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := created.BeginRun(); !errors.Is(err, ErrRunActive) {
		t.Fatalf("second BeginRun error = %v", err)
	}

	release()
	if _, err := created.BeginRun(); err != nil {
		t.Fatalf("BeginRun after release: %v", err)
	}
}

func appendEvents(t *testing.T, target Session, events ...protocol.SessionEvent) {
	t.Helper()

	for _, event := range events {
		if _, err := target.Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
	}
}

func collectEvents(t *testing.T, target Session) []protocol.SessionEvent {
	t.Helper()
	var events []protocol.SessionEvent

	for event, err := range target.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}

		events = append(events, event)
	}

	return events
}

func messageRoles(messages []protocol.Message) []protocol.Role {
	roles := make([]protocol.Role, len(messages))

	for i, message := range messages {
		roles[i] = message.Role
	}

	return roles
}

// Setup uses Restore so append cost is measured against a fixed committed
// prefix, independently of the cost of constructing that prefix.
func benchmarkSession(b *testing.B, turns int) *LiveSession {
	b.Helper()
	events := make([]protocol.SessionEvent, 0, turns*3)
	for n := 1; n <= turns; n++ {
		turn := protocol.TurnID(n)
		events = append(events,
			protocol.NewTurnStarted(turn),
			protocol.NewMessageEvent(protocol.UserMessage, turn, 0,
				protocol.NewTextMessage(protocol.MessageID(fmt.Sprintf("user-%d", n)), protocol.RoleUser, protocol.SourceUser, "hello")),
			protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "completed"}),
		)
	}
	for n := range events {
		events[n].Seq = protocol.Sequence(n)
	}
	header, err := CreateHeader("benchmark", nil)
	if err != nil {
		b.Fatal(err)
	}
	current, err := Restore(header, events, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	return current
}

func BenchmarkSessionLongLog(b *testing.B) {
	ctx := context.Background()
	for _, turns := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			b.Run("AppendMessage", func(b *testing.B) {
				current := benchmarkSession(b, turns)
				length := len(current.events)
				event := protocol.NewMessageEvent(protocol.UserMessage, 0, 0,
					protocol.NewTextMessage("next-user", protocol.RoleUser, protocol.SourceUser, "hello"))
				current.events = append(current.events, protocol.SessionEvent{})[:length]
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					if _, err := current.Append(ctx, event); err != nil {
						b.Fatal(err)
					}
					// Reset only the appended node; execution admission and tool/
					// Inbox state are unaffected by this user-message candidate.
					current.events = current.events[:length]
					current.projection.p.Surface.Nodes = current.projection.p.Surface.Nodes[:turns]
					current.projection.messages[turns] = nil
					current.projection.messages = current.projection.messages[:turns]
				}
			})
			b.Run("AppendFact", func(b *testing.B) {
				current := benchmarkSession(b, turns)
				length := len(current.events)
				event := protocol.SessionEvent{Type: "extension/metadata", Ignorable: true, Data: []byte(`{"value":1}`)}
				// Reserve the log slot once; resetting a projection-neutral fact
				// keeps the history length fixed throughout this microbenchmark.
				current.events = append(current.events, protocol.SessionEvent{})[:length]
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					if _, err := current.Append(ctx, event); err != nil {
						b.Fatal(err)
					}
					current.events = current.events[:length]
				}
			})
			b.Run("Messages", func(b *testing.B) {
				current := benchmarkSession(b, turns)
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					if _, err := current.Messages(ctx); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Inbox", func(b *testing.B) {
				inbox := NewInbox(benchmarkSession(b, turns))
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					if _, err := inbox.Pending(ctx); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func TestSessionPhysicalReleaseFailureCanRetryThenIsIdempotent(t *testing.T) {
	header, err := CreateHeader("resource", nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Restore(header, nil, nil, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("physical release")
	count := 0
	if err := current.Release(func() error { count++; return cause }); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("failed release reopened admission", err)
	}
	for range 2 {
		if err := current.Release(func() error { count++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := current.Close(func() error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("release did not retry once or repeated successful cleanup", count)
	}
}
