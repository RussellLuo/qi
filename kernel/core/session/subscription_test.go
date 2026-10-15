package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestSubscriptionCutOverflowCatchupAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := NewRuntime(RuntimeOptions{})
	current, _ := store.Create(ctx, CreateRequest{ID: "one"})
	other, _ := store.Create(ctx, CreateRequest{ID: "two"})
	// Subscribe is effective before iterator consumption or initial watermark.
	sub, err := current.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	second, _ := current.Subscribe(ctx)
	defer second.Close()
	otherSub, _ := other.Subscribe(ctx)
	defer otherSub.Close()
	initial := current.Watermark()
	if initial != -1 {
		t.Fatal(initial)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				e := protocol.SessionEvent{Type: "plugin/fact", Time: int64(worker*100 + i), Ignorable: true, Data: []byte(fmt.Sprintf(`{"worker":%d,"index":%d}`, worker, i))}
				if _, err := current.Append(ctx, e); err != nil {
					t.Error(err)
				}
				for j := range e.Data {
					e.Data[j] = 'x'
				} // queues and log own independent bytes
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	if sub.Dropped() != 144 || second.Dropped() != 144 || otherSub.Dropped() != 0 {
		t.Fatal("overflow/cursor isolation", sub.Dropped(), second.Dropped(), otherSub.Dropped())
	}
	watermark := current.Watermark()
	if watermark != 399 || other.Watermark() != -1 {
		t.Fatal(watermark)
	}
	var notified []protocol.SessionEvent
	for e, err := range sub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		notified = append(notified, e)
		if len(notified) == 256 {
			break
		}
	}
	for i, e := range notified {
		if e.Seq != protocol.Sequence(i) {
			t.Fatal("notification order", e.Seq)
		}
	}
	// Loss only removes wakeups. A final fixed watermark recovers all 400 facts.
	all := collectEvents(t, current)
	recovered := append([]protocol.SessionEvent{}, notified...)
	for e, err := range current.Events(ctx, notified[len(notified)-1].Seq) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq > watermark {
			break
		}
		recovered = append(recovered, e)
	}
	if !reflect.DeepEqual(recovered, all) {
		t.Fatal("gap catchup did not recover committed prefix")
	}
	for e, err := range second.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e, all[0]) {
			t.Fatal("clone leaked")
		}
		e.Data[0] = 'x'
		break
	}
	if !reflect.DeepEqual(collectEvents(t, current), all) {
		t.Fatal("observer mutated durable log")
	}
	// Read a snapshot after registering, then deduplicate overlapping notifications.
	cut, _ := current.Subscribe(ctx)
	defer cut.Close()
	cursor := current.Watermark()
	_, _ = current.Append(ctx, protocol.SessionEvent{Type: "plugin/cut", Ignorable: true, Data: []byte(`{"after":true}`)})
	count := 0
	for e, err := range current.Events(ctx, cursor) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		cursor = e.Seq
	}
	for e, err := range cut.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Seq > cursor {
			count++
		}
		break
	}
	if count != 1 {
		t.Fatal("subscription cut duplicated or lost append", count)
	}
	// Early iterator exit and canceled context only remove observation.
	canceledCtx, stop := context.WithCancel(ctx)
	canceledSub, _ := current.Subscribe(canceledCtx)
	stop()
	for _, err := range canceledSub.Events() {
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if _, err := current.Append(ctx, protocol.SessionEvent{Type: "plugin/still-live", Ignorable: true, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionRejectsFalseCommitsAndObservesRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header, _ := CreateHeader("recovery", nil)
	failure := errors.New("storage unavailable")
	fail := false
	current, err := Restore(header, nil, nil, func([]byte) error {
		if fail {
			return failure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := current.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	appendEvents(t, current, protocol.NewSessionEvent(protocol.TurnStarted, protocol.TurnData{Turn: 1}), protocol.NewSessionEvent(protocol.StepStarted, protocol.StepData{Turn: 1, Step: 1}))
	before := current.Watermark()
	fail = true
	if seq, err := current.Append(ctx, protocol.NewSessionEvent(protocol.StepEnded, protocol.StepData{Turn: 1, Step: 1})); seq != -1 || !errors.Is(err, failure) {
		t.Fatal(seq, err)
	}
	if current.Watermark() != before {
		t.Fatal("failed commit advanced seq")
	}
	fail = false
	reservation, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	_ = reservation.Close()
	appendEvents(t, current, protocol.NewSessionEvent(protocol.TurnStarted, protocol.TurnData{Turn: 2}), protocol.NewSessionEvent(protocol.StepStarted, protocol.StepData{Turn: 2, Step: 1}))
	// Developer messages are legal independent Session writers, preserving metadata.
	m := protocol.NewTextMessage("developer", protocol.RoleDeveloper, protocol.SourceSystem, "instructions")
	e := protocol.NewSessionEvent(protocol.DeveloperMessage, protocol.MessageData{Turn: 2, Step: 1, Message: m})
	e.SurfaceOp = protocol.AppendSurface()
	appendEvents(t, current, e, protocol.NewSessionEvent(protocol.StepEnded, protocol.StepData{Turn: 2, Step: 1}), protocol.NewSessionEvent(protocol.TurnEnded, protocol.TurnEndData{Turn: 2, Reason: protocol.TurnEndReason{Kind: "completed"}}))
	all := collectEvents(t, current)
	count := 0
	seen := map[protocol.SessionEventKind]bool{}
	for e, err := range sub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e, all[count]) {
			t.Fatal("unaccepted or corrupt observation", e)
		}
		seen[e.Type] = true
		count++
		if count == len(all) {
			break
		}
	}
	if !seen[protocol.SessionEndSeed] || !seen[protocol.DeveloperMessage] || sub.Dropped() != 0 {
		t.Fatal("recovery/non-Driver coverage", seen)
	}
	if _, err := current.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	if err := current.Release(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := current.Subscribe(ctx); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal("closed subscription", err)
	}
}
