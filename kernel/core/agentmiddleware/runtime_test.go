package agentmiddleware

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestSnapshotFreezesOrderedAgentWaterfalls(t *testing.T) {
	runtime := NewRuntime()
	owned := newTestOwner("middleware", 1)
	var order []string

	for _, item := range []struct {
		id    string
		order int
	}{{"later", 20}, {"first", 10}} {
		item := item
		if _, err := runtime.Register(owned, Middleware{
			ID:    item.id,
			Order: item.order,
			PreStep: func(_ context.Context, input StepInput) (StepInput, error) {
				order = append(order, item.id)
				return input, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	if _, err := prepared.PreStep(context.Background(), StepInput{Index: 1}); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(order, []string{"first", "later"}) {
		t.Fatalf("middleware order = %v", order)
	}

	if _, err := runtime.Register(newTestOwner("late", 2), Middleware{
		ID: "late",
		PreStep: func(_ context.Context, input StepInput) (StepInput, error) {
			order = append(order, "late")
			return input, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	order = nil
	if _, err := prepared.PreStep(context.Background(), StepInput{}); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(order, []string{"first", "later"}) {
		t.Fatalf("prepared snapshot observed late registration: %v", order)
	}
}

func TestStreamAndTurnStoppingStages(t *testing.T) {
	runtime := NewRuntime()
	owned := newTestOwner("middleware", 1)
	stopped := false
	if _, err := runtime.Register(owned, Middleware{
		ID: "stream",
		Stream: func(_ context.Context, upstream contracts.ChunkStream) (contracts.ChunkStream, error) {
			return func(yield func(protocol.StreamChunk, error) bool) {
				for chunk, err := range upstream {
					if chunk.Type == protocol.ChunkTextDelta {
						chunk.Text = "wrapped:" + chunk.Text
					}

					if !yield(chunk, err) {
						return
					}
				}
			}, nil
		},
		TurnStopping: func(context.Context, TurnStop) error {
			stopped = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	stream, err := prepared.Stream(context.Background(), func(yield func(protocol.StreamChunk, error) bool) {
		yield(protocol.StreamChunk{
			Type:  protocol.ChunkTextDelta,
			Index: 0,
			Text:  "hello",
		}, nil)
	})
	if err != nil {
		t.Fatal(err)
	}

	for chunk, err := range stream {
		if err != nil || chunk.Text != "wrapped:hello" {
			t.Fatalf("stream chunk/error = %+v/%v", chunk, err)
		}
	}

	if err := prepared.TurnStopping(context.Background(), TurnStop{Reason: protocol.ReasonCompleted}); err != nil {
		t.Fatal(err)
	}

	if !stopped {
		t.Fatal("turn-stopping middleware was not called")
	}
}

// testOwner is a deterministic ContributionOwner for runtime and conformance
// tests. Stop rejects new acquisitions, waits for admitted work, and then runs
// cleanup callbacks in reverse registration order.
type testOwner struct {
	mu         sync.Mutex
	identity   contracts.OwnerIdentity
	stopping   bool
	stopped    bool
	active     int
	cleanups   []func()
	activeZero chan struct{}
}

func newTestOwner(id string, generation uint64) *testOwner {
	return &testOwner{identity: contracts.OwnerIdentity{ID: id, Generation: generation}}
}

func (o *testOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *testOwner) Acquire() (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return nil, contracts.ErrOwnerUnavailable
	}

	o.active++
	var once sync.Once

	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.active--
			if o.active == 0 && o.activeZero != nil {
				close(o.activeZero)
				o.activeZero = nil
			}

			o.mu.Unlock()
		})
	}, nil
}

func (o *testOwner) DeferCleanup(_ string, cleanup func()) error {
	if cleanup == nil {
		return errors.New("cleanup is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return contracts.ErrOwnerUnavailable
	}

	o.cleanups = append(o.cleanups, cleanup)

	return nil
}

func (o *testOwner) Stop(ctx context.Context) error {
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	if !o.stopping {
		o.stopping = true
	}

	wait := o.activeZero
	if o.active > 0 && wait == nil {
		wait = make(chan struct{})
		o.activeZero = wait
	}

	o.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	cleanups := o.cleanups
	o.cleanups = nil
	o.stopped = true
	o.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	return nil
}

func TestRequestErrorWaterfallFreezeAndDefaultStop(t *testing.T) {
	r := NewRuntime()
	defer r.Close()

	owned := newTestOwner("hooks", 1)
	order := []string{}
	if _, err := r.Register(owned, Middleware{
		ID:    "retry",
		Order: 1,
		RequestError: func(_ context.Context, e RequestError, action *RequestErrorAction) (*RequestErrorAction, error) {
			order = append(order, "retry")
			if action != nil {
				t.Fatal(action)
			}

			return &RequestErrorAction{Kind: "retry"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Register(owned, Middleware{
		ID:    "stop",
		Order: 2,
		RequestError: func(_ context.Context, _ RequestError, action *RequestErrorAction) (*RequestErrorAction, error) {
			order = append(order, "stop")
			if action == nil || action.Kind != "retry" {
				t.Fatal(action)
			}

			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	s, err := r.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if _, err := r.Register(owned, Middleware{
		ID:    "late",
		Order: 3,
		RequestError: func(context.Context, RequestError, *RequestErrorAction) (*RequestErrorAction, error) {
			t.Fatal("late listener entered prepared generation")
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	action, err := s.RequestError(context.Background(), RequestError{})
	if err != nil || action != nil || !reflect.DeepEqual(order, []string{"retry", "stop"}) {
		t.Fatal(action, err, order)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.RequestError(ctx, RequestError{}); err != context.Canceled || len(order) != 2 {
		t.Fatal("canceled waterfall invoked listeners", err, order)
	}
}

func TestRequestErrorRejectsUnknownDecision(t *testing.T) {
	r := NewRuntime()
	defer r.Close()

	if _, err := r.Register(newTestOwner("invalid", 1), Middleware{
		ID: "invalid",
		RequestError: func(context.Context, RequestError, *RequestErrorAction) (*RequestErrorAction, error) {
			return &RequestErrorAction{Kind: "continue"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	s, err := r.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if _, err := s.RequestError(context.Background(), RequestError{}); err == nil {
		t.Fatal("unknown retry semantics accepted")
	}
}
