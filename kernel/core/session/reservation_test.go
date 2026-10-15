package session

import (
	"context"
	"errors"

	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
)

func TestReservationIsStoreScopedAndRejectsStaleOwners(t *testing.T) {

	var store Store = NewRuntime(RuntimeOptions{})

	current, err := store.Create(context.Background(), CreateRequest{ID: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan contracts.SessionReservation, 20)
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			found, err := store.Get(context.Background(), current.ID())
			if err != nil {
				t.Error(err)
				return
			}
			lease, err := found.Attach()
			if err == nil {
				results <- lease
			} else if !errors.Is(err, ErrAttached) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("attachment winners=%d", len(results))
	}
	reservation := <-results
	if err := store.Delete(context.Background(), current.ID()); !errors.Is(err, ErrAttached) {
		t.Fatalf("deleted attached Session: %v", err)
	}
	foreignStore := NewRuntime(RuntimeOptions{})
	foreign, err := foreignStore.Create(context.Background(), CreateRequest{ID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.BeginRunOwned(reservation); err == nil {
		t.Fatal("foreign store used reservation with matching id")
	}
	release, err := reservation.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); !errors.Is(err, ErrRunActive) {
		t.Fatalf("detached active Drive: %v", err)
	}
	if _, err := reservation.BeginRun(); !errors.Is(err, ErrRunActive) {
		t.Fatalf("parallel Drive=%v", err)
	}
	release()
	release()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reservation.BeginRun(); err == nil {
		t.Fatal("stale owner admitted a Drive")
	}
	if _, err := current.Attach(); !errors.Is(err, ErrAttached) {
		t.Fatal("stale Close detached replacement")
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), current.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("deleted Session admitted an owner: %v", err)
	}

}
