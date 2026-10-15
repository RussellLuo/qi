package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/qi/kernel/core/contracts"
)

type output struct {
	event contracts.DriverEvent
	err   error
}

// Ordinary previews are bounded. Ended and the Driver return error each have
// one independent slot; observation cancellation never cancels the activity.
type subscription struct {
	mu         sync.Mutex
	queue      chan output
	terminal   *output
	finalErr   error
	detached   bool
	finished   bool
	dropped    atomic.Uint64
	canceled   chan struct{}
	cancelOnce sync.Once
	stop       func() bool
}

var ErrDuplicateEnded = errors.New("Driver emitted duplicate ended events")

func newSubscription(ctx context.Context) *subscription {
	s := &subscription{queue: make(chan output, 256), canceled: make(chan struct{})}
	s.stop = context.AfterFunc(ctx, s.detach)
	return s
}
func (s *subscription) detach() {
	s.mu.Lock()
	s.detached = true
	s.mu.Unlock()
	s.cancelOnce.Do(func() { close(s.canceled) })
}
func (s *subscription) emit(event contracts.DriverEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return ErrClosed
	}
	if event.Type == "ended" {
		if s.terminal != nil {
			s.finalErr = ErrDuplicateEnded
			return ErrDuplicateEnded
		}
		item := output{event: event.Clone()}
		s.terminal = &item
		return nil
	}
	if s.detached {
		return nil
	}
	select {
	case s.queue <- output{event: event.Clone()}:
	default:
		s.dropped.Add(1)
	}
	return nil
}
func (s *subscription) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	s.finalErr = errors.Join(s.finalErr, err)
	close(s.queue)
}
func (s *subscription) stream(yield func(contracts.DriverEvent, error) bool) {
	defer s.detach()
	defer s.stop()
	for {
		select {
		case <-s.canceled:
			return
		case item, ok := <-s.queue:
			if !ok {
				s.mu.Lock()
				terminal, err := s.terminal, s.finalErr
				s.mu.Unlock()
				if terminal != nil && !yield(terminal.event.Clone(), nil) {
					return
				}
				if err != nil {
					yield(contracts.DriverEvent{}, err)
				}
				return
			}
			if !yield(item.event.Clone(), item.err) {
				return
			}
		}
	}
}
