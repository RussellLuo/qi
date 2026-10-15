package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func inboxMessage(id string) protocol.Message {
	return protocol.NewTextMessage(protocol.MessageID(id), protocol.RoleUser, protocol.SourceUser, id)
}

func inboxIDs(q []protocol.Message) []string {
	ids := []string{}

	for _, m := range q {
		ids = append(ids, string(m.ID))
	}

	return ids
}

func TestInboxNormalizeDeduplicateAndClaim(t *testing.T) {
	ctx := context.Background()
	current, err := NewRuntime(RuntimeOptions{}).Create(ctx, CreateRequest{ID: "inbox"})
	if err != nil {
		t.Fatal(err)
	}

	inbox := NewInbox(current)
	splice := func(target string, start, count int, inserted ...protocol.Message) []protocol.Message {
		t.Helper()
		removed, err := inbox.Splice(ctx, target, start, count, inserted, true)
		if err != nil {
			t.Fatal(err)
		}

		return removed
	}
	splice(contracts.NextTurn, 999, -1, inboxMessage("a"), inboxMessage("b"))
	splice(contracts.NextTurn, -1, 0, inboxMessage("c"))
	if got := inboxIDs(splice(contracts.NextTurn, -999, 1)); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatal(got)
	}

	splice(contracts.NextStep, 0, 0, inboxMessage("d"))
	before := current.Watermark()
	if _, err := inbox.Splice(ctx, contracts.NextStep, 99, 99, nil, true); err != nil || current.Watermark() != before {
		t.Fatal("no-op committed", err)
	}

	if _, err := inbox.Splice(ctx, contracts.NextStep, 99, 0, []protocol.Message{inboxMessage("b")}, true); err == nil || current.Watermark() != before {
		t.Fatal("cross-queue duplicate accepted")
	}

	if _, err := inbox.Splice(ctx, contracts.NextStep, 99, 0, []protocol.Message{inboxMessage("x"), inboxMessage("x")}, true); err == nil {
		t.Fatal("insert duplicates accepted")
	}
	// Replacing the removed identity is legal; duplicates remaining elsewhere are not.
	splice(contracts.NextTurn, 0, 1, inboxMessage("c"))
	claimed, err := inbox.Claim(ctx, contracts.NextTurn, 1)
	if err != nil {
		t.Fatal(err)
	}

	if got := inboxIDs(claimed); !reflect.DeepEqual(got, []string{"d", "c"}) {
		t.Fatal(got)
	}

	state, err := inbox.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(inboxIDs(state[contracts.NextTurn]), []string{"b"}) || len(state[contracts.NextStep]) != 0 {
		t.Fatal(state)
	}

	for event, err := range current.Events(ctx, -1) {
		if err != nil {
			t.Fatal(err)
		}

		d := protocol.EventPayload[protocol.InboxSpliceData](event)
		if d.RemovedCount != nil && *d.RemovedCount == 0 {
			t.Fatal("serialized zero removedCount")
		}

		if event.Seq >= 5 && d.Outcome != "" {
			t.Fatal("claim marked canceled", event)
		}
	}
}

type failingInboxSession struct {
	contracts.InboxSession
	fail bool
}

func (s *failingInboxSession) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	if s.fail {
		return -1, errors.New("disk unavailable")
	}

	return s.InboxSession.Append(ctx, e)
}

func TestInboxFailedAppendDoesNotChangeProjection(t *testing.T) {
	ctx := context.Background()
	current, _ := NewRuntime(RuntimeOptions{}).Create(ctx, CreateRequest{ID: "failure"})
	writer := &failingInboxSession{InboxSession: current}
	inbox := NewInbox(writer)
	if _, err := inbox.Splice(ctx, contracts.NextStep, 0, 0, []protocol.Message{inboxMessage("one")}, false); err != nil {
		t.Fatal(err)
	}

	writer.fail = true
	if _, err := inbox.Splice(ctx, contracts.NextTurn, 0, 0, []protocol.Message{inboxMessage("two")}, false); err == nil {
		t.Fatal("insertion passed")
	}

	if _, err := inbox.Claim(ctx, contracts.NextStep, 1); err == nil {
		t.Fatal("claim passed")
	}

	notified := false
	if err := inbox.Clear(ctx, func(protocol.Message) {
		notified = true
	}); err == nil || notified {
		t.Fatal("discard notification without commit")
	}

	p, err := inbox.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if current.Watermark() != 0 ||
		!reflect.DeepEqual(inboxIDs(p[contracts.NextStep]), []string{"one"}) ||
		len(p[contracts.NextTurn]) != 0 {
		t.Fatal(p)
	}
}
