// Package observation supplies bounded local subscription queues, not an event
// bus. Producers never call listeners or wait for a consumer.
package observation

import (
	"context"
	"iter"
	"sync"
	"sync/atomic"
)

const Capacity = 256

type Subscription[T any] struct {
	ctx      context.Context
	mu       sync.Mutex
	queue    chan T
	done     chan struct{}
	closed   bool
	finished bool
	dropped  atomic.Uint64
	remove   func()
	stop     func() bool
}

func New[T any](ctx context.Context, remove func()) *Subscription[T] {
	s := &Subscription[T]{ctx: ctx, queue: make(chan T, Capacity), done: make(chan struct{}), remove: remove}
	return s
}

// Watch is called after registration; cancellation cannot leave a stale entry.
func (s *Subscription[T]) Watch() {
	s.mu.Lock()
	s.stop = context.AfterFunc(s.ctx, s.Close)
	if s.closed {
		s.stop()
	}
	s.mu.Unlock()
}

func (s *Subscription[T]) Push(value T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.finished {
		return
	}
	select {
	case s.queue <- value:
	default:
		s.dropped.Add(1)
	}
}

// Finish preserves queued facts and ends iteration after their delivery.
func (s *Subscription[T]) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finished {
		s.finished = true
		close(s.queue)
	}
}

// Drop accounts for bounded prepublication observations that could not be retained.
func (s *Subscription[T]) Drop(count uint64) { s.dropped.Add(count) }

func (s *Subscription[T]) Dropped() uint64 { return s.dropped.Load() }

func (s *Subscription[T]) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if s.remove != nil {
		s.remove()
	}
}

func (s *Subscription[T]) Events() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		defer s.Close()
		for {
			if err := s.ctx.Err(); err != nil {
				var zero T
				yield(zero, err)
				return
			}
			select {
			case value, ok := <-s.queue:
				if !ok || !yield(value, nil) {
					return
				}
			case <-s.done:
				return
			case <-s.ctx.Done():
				var zero T
				yield(zero, s.ctx.Err())
				return
			}
		}
	}
}
