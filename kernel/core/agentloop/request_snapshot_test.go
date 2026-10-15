package agentloop

import (
	"context"
	"fmt"
	"iter"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type cachedOnlySession struct{ *session.LiveSession }

func (s cachedOnlySession) Events(context.Context, protocol.Sequence) iter.Seq2[protocol.SessionEvent, error] {
	panic("request replayed the full log")
}

type replayRequestSession struct{ session.Session }

func TestRequestSnapshotWrapperForwarding(t *testing.T) {
	ctx := context.Background()
	header, err := session.CreateHeader("snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := session.Restore(header, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []protocol.SessionEvent{
		protocol.NewTurnStarted(1), protocol.NewStepStarted(1, 1),
		protocol.NewMessageEvent(protocol.SystemMessage, 1, 1, protocol.NewTextMessage("prompt", protocol.RoleSystem, protocol.SourceSystem, "")),
		protocol.NewStepEnded(1, 1), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	} {
		if _, err := current.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	want, err := session.ReadSnapshot(ctx, replayRequestSession{current})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := scheduledSession{Session: driverSession{Session: cachedOnlySession{current}}}
	events, p, err := requestSnapshot(ctx, wrapped)
	if err != nil || !reflect.DeepEqual(events, want.SystemEvents) || !reflect.DeepEqual(p, want.Projection) {
		t.Fatal("wrapper lost the committed cached view", err)
	}
	if _, err := prepareRequestState(ctx, wrapped, &RequestState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := requestProposal(ctx, wrapped, Config{}.LLM, false); err != nil {
		t.Fatal(err)
	}
	// Wrapping an arbitrary Session still retains the original Fold fallback.
	events, p, err = requestSnapshot(ctx, scheduledSession{Session: driverSession{Session: replayRequestSession{current}}})
	if err != nil || !reflect.DeepEqual(events, want.SystemEvents) || !reflect.DeepEqual(p, want.Projection) {
		t.Fatal("wrapper lost fallback semantics", err)
	}
}

func BenchmarkRequestSnapshotLongLog(b *testing.B) {
	ctx := context.Background()
	for _, turns := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			events := []protocol.SessionEvent{}
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
			header, err := session.CreateHeader("benchmark", nil)
			if err != nil {
				b.Fatal(err)
			}
			current, err := session.Restore(header, events, nil, nil)
			if err != nil {
				b.Fatal(err)
			}
			for _, path := range []string{"FoldFallback", "CachedWrappers"} {
				b.Run(path, func(b *testing.B) {
					var target session.Session = replayRequestSession{current}
					if path == "CachedWrappers" {
						target = current
					}
					target = scheduledSession{Session: driverSession{Session: target}}
					b.ReportAllocs()
					b.ResetTimer()
					for n := 0; n < b.N; n++ {
						if _, _, err := requestSnapshot(ctx, target); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
