package session

import (
	"context"
	"iter"
	"log"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// SessionSubscription delivers accepted facts from every writer. Subscribe
// registers before returning; Events does not replay history. Queues hold 256
// events and drop new notifications on overflow. Read Events(after) to repair
// gaps, deduplicate by seq, and use Watermark to finish a catch-up pass.
// Cancellation or Close only removes this observation.
type SessionSubscription interface {
	Events() iter.Seq2[protocol.SessionEvent, error]
	Dropped() uint64
	Close()
}

func (s *LiveSession) Subscribe(ctx context.Context) (SessionSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, contracts.ErrClosed
	}
	if s.subscribers == nil {
		s.subscribers = make(map[*observation.Subscription[protocol.SessionEvent]]struct{})
	}
	var sub *observation.Subscription[protocol.SessionEvent]
	sub = observation.New[protocol.SessionEvent](ctx, func() {
		s.mu.Lock()
		delete(s.subscribers, sub)
		s.mu.Unlock()
	})
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()
	sub.Watch()
	return sub, nil
}

// Called after the Session mutex is released, while notifications serializes
// producers. Enqueue clones independently; no callback runs under either lock.
func (s *LiveSession) publish(event protocol.SessionEvent) {
	s.mu.Lock()
	subs := make([]*observation.Subscription[protocol.SessionEvent], 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.Push(event.Clone())
	}
	if s.observer != nil {
		if err := observation.Call("session/appended", func() { s.observer.SessionAppended(s.id, event.Clone()) }); err != nil {
			log.Print(err)
		}
	}
}

func (s *LiveSession) finishSubscriptionsLocked() {
	for sub := range s.subscribers {
		sub.Finish()
	}
	s.subscribers = nil
}
