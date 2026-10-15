package session_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestRuntimeConcurrentIdentityAndOwnership(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})

	var wg sync.WaitGroup
	winners := make(chan session.Session, 20)

	for range 20 {
		wg.Go(func() {
			current, err := store.Create(ctx, session.CreateRequest{ID: "shared"})
			if err == nil {
				winners <- current
			} else if !errors.Is(err, session.ErrAlreadyExists) {
				t.Error(err)
			}
		})
	}

	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("Create winners: %d", len(winners))
	}

	current := <-winners
	owners := make(chan contracts.SessionReservation, 20)

	for range 20 {
		wg.Go(func() {
			found, err := store.Get(ctx, current.ID())
			if err != nil {
				t.Error(err)
				return
			}

			if found != current {
				t.Error("Get changed Session identity")
			}

			reservation, err := found.Attach()
			if err == nil {
				owners <- reservation
			} else if !errors.Is(err, session.ErrAttached) {
				t.Error(err)
			}
		})
	}

	wg.Wait()
	if len(owners) != 1 {
		t.Fatalf("Attach winners: %d", len(owners))
	}

	reservation := <-owners
	if err := store.Delete(ctx, current.ID()); !errors.Is(err, session.ErrAttached) {
		t.Fatal(err)
	}

	release, err := reservation.BeginRun()
	if err != nil {
		t.Fatal(err)
	}

	if err := reservation.Close(); !errors.Is(err, session.ErrRunActive) {
		t.Fatal(err)
	}

	release()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	release, err = current.BeginRun()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, current.ID()); !errors.Is(err, session.ErrRunActive) {
		t.Fatal(err)
	}

	release()
	if err := store.Delete(ctx, current.ID()); err != nil {
		t.Fatal(err)
	}

	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}

	if _, err := store.Get(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	// Generated IDs avoid explicit identities, and deleted identities can be recreated.
	if _, err := store.Create(ctx, session.CreateRequest{ID: "session-1"}); err != nil {
		t.Fatal(err)
	}

	generated, err := store.Create(ctx, session.CreateRequest{})
	if err != nil || generated.ID() != "session-2" {
		t.Fatal(generated, err)
	}

	if _, err := store.Create(ctx, session.CreateRequest{ID: current.ID()}); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.Create(cancelled, session.CreateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if _, err := store.Get(cancelled, generated.ID()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	if err := store.Delete(cancelled, generated.ID()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type observer struct {
	contracts.ObservationDispatcher
	current session.Session
	events  []protocol.SessionEvent
}

func TestRuntimeReleaseChecksRuntimeOwnershipAndInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, err := store.Create(ctx, session.CreateRequest{ID: "released"})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Release(canceled, current); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	reservation, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Release(ctx, current); !errors.Is(err, session.ErrAttached) {
		t.Fatal(err)
	}

	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	end, err := current.BeginRun()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Release(ctx, current); !errors.Is(err, session.ErrRunActive) {
		t.Fatal(err)
	}

	end()
	if err := store.Release(ctx, current); err != nil {
		t.Fatal(err)
	}

	replacement, err := store.Create(ctx, session.CreateRequest{ID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Release(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale release affected replacement", err)
	}

	got, err := store.Get(ctx, replacement.ID())
	if err != nil || got != replacement {
		t.Fatal(got, err)
	}
}

func (o *observer) SessionAppended(id protocol.SessionID, event protocol.SessionEvent) {
	// Observation runs after commit and outside the Session mutex.
	if id != o.current.ID() {
		panic("wrong observation identity")
	}

	for _, err := range o.current.Events(context.Background(), -1) {
		if err != nil {
			panic(err)
		}
	}

	o.events = append(o.events, event)
}

func TestObservedRuntimePublishesCommittedEvents(t *testing.T) {
	o := new(observer)
	store := session.NewRuntime(session.RuntimeOptions{Observer: o})
	current, err := store.Create(context.Background(), session.CreateRequest{ID: "observed"})
	if err != nil {
		t.Fatal(err)
	}

	o.current = current

	for _, event := range []protocol.SessionEvent{
		protocol.NewTurnStarted(1),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	} {
		if _, err := current.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := current.Append(
		context.Background(),
		protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}),
	); !errors.Is(err, session.ErrInvalidTransition) {
		t.Fatal(err)
	}

	if len(o.events) != 2 || o.events[0].Seq != 0 || o.events[1].Seq != 1 {
		t.Fatal(o.events)
	}
}

type checkSessionStoreFactory func(*testing.T) (session.Store, func())

func checkSessionStore(t *testing.T, factory checkSessionStoreFactory) {
	t.Helper()
	t.Run("append_replay_and_copy", func(t *testing.T) {
		store, closeStore := factory(t)
		defer closeStore()

		ctx := context.Background()
		current, err := store.Create(ctx, session.CreateRequest{ID: "conformance-session"})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := store.Create(ctx, session.CreateRequest{ID: current.ID()}); !errors.Is(err, session.ErrAlreadyExists) {
			t.Fatalf("duplicate Create error = %v", err)
		}

		turn := protocol.TurnID(1)
		message := protocol.NewTextMessage("message-1", protocol.RoleUser, protocol.SourceUser, "hello")

		for _, event := range []protocol.SessionEvent{
			protocol.NewTurnStarted(turn),
			protocol.NewMessageEvent(protocol.UserMessage, turn, 0, message),
			protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "completed"}),
		} {
			if _, err := current.Append(ctx, event); err != nil {
				t.Fatal(err)
			}
		}

		message.Content[0] = protocol.TextBlock{Text: "mutated"}
		events := conformanceEvents(t, current)
		if got, want := len(events), 3; got != want {
			t.Fatalf("event count = %d, want %d", got, want)
		}

		for index, event := range events {
			if event.Seq != protocol.Sequence(index) {
				t.Fatalf("event %d sequence = %d", index, event.Seq)
			}
		}

		projected, err := current.Messages(ctx)
		if err != nil {
			t.Fatal(err)
		}

		replayed, err := session.ProjectMessages(events)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(projected, replayed) {
			t.Fatalf("Messages and replay differ:\nMessages: %#v\nReplay: %#v", projected, replayed)
		}

		if got := projected[0].Content[0].(protocol.TextBlock).Text; got != "hello" {
			t.Fatalf("stored message was mutated: %q", got)
		}

		found, err := store.Get(ctx, current.ID())
		if err != nil || found.ID() != current.ID() {
			t.Fatalf("Get = %v, %v", found, err)
		}
	})

	t.Run("run_reservation_and_delete", func(t *testing.T) {
		store, closeStore := factory(t)
		defer closeStore()

		ctx := context.Background()
		current, err := store.Create(ctx, session.CreateRequest{ID: "reservation-session"})
		if err != nil {
			t.Fatal(err)
		}

		release, err := current.BeginRun()
		if err != nil {
			t.Fatal(err)
		}

		if _, err := current.BeginRun(); !errors.Is(err, session.ErrRunActive) {
			t.Fatalf("second BeginRun error = %v", err)
		}

		release()
		release()
		if next, err := current.BeginRun(); err != nil {
			t.Fatalf("BeginRun after release: %v", err)
		} else {
			next()
		}

		if err := store.Delete(ctx, current.ID()); err != nil {
			t.Fatal(err)
		}

		if _, err := store.Get(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("Get after Delete error = %v", err)
		}
	})
}

func conformanceEvents(t *testing.T, current session.Session) []protocol.SessionEvent {
	t.Helper()
	var events []protocol.SessionEvent

	for event, err := range current.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}

		events = append(events, event.Clone())
	}

	return events
}

func TestMemoryReleaseIdempotentAndRejectsForeignInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	current, err := store.Create(ctx, session.CreateRequest{ID: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := session.Restore(current.Header(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, foreign); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("foreign instance released resource", err)
	}
	for range 2 {
		if err := store.Release(ctx, current); err != nil {
			t.Fatal("same release not idempotent", err)
		}
	}
	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("released instance remained open", err)
	}
	if err := store.Release(ctx, foreign); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("unknown closed ID treated as own release", err)
	}
	replacement, err := store.Create(ctx, session.CreateRequest{ID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale release accepted replacement", err)
	}
	if got, err := store.Get(ctx, replacement.ID()); err != nil || got != replacement {
		t.Fatal(err)
	}
	other := session.NewRuntime(session.RuntimeOptions{})
	if err := other.Release(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("foreign Store accepted credential", err)
	}
}

func TestRollbackCreateChecksAttachmentAndExactInstance(t *testing.T) {
	ctx := context.Background()
	store := session.NewRuntime(session.RuntimeOptions{})
	old, err := store.Create(ctx, session.CreateRequest{ID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := old.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackCreate(ctx, old); !errors.Is(err, session.ErrAttached) {
		t.Fatal("rollback deleted attached instance", err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackCreate(ctx, old); err != nil {
		t.Fatal(err)
	}
	next, err := store.Create(ctx, session.CreateRequest{ID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RollbackCreate(ctx, old); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("old credential affected replacement", err)
	}
	if got, err := store.Get(ctx, next.ID()); err != nil || got != next {
		t.Fatal("replacement lost", got, err)
	}
}
