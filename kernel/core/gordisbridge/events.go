package gordisbridge

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	// SessionAppendedTopic carries committed facts with their Session identity.
	SessionAppendedTopic     = events.NewTopic[SessionObservation]("qi.session.appended/2")
	DriverEventTopic         = events.NewTopic[contracts.DriverEvent]("agent/driver")
	AgentCreatedTopic        = events.NewTopic[contracts.AgentCreated]("agent/created")
	AgentStatusTopic         = events.NewTopic[contracts.AgentStatusObservation]("agent/status")
	AgentInboxInsertedTopic  = events.NewTopic[contracts.AgentInboxObservation]("agent/inbox/inserted")
	AgentInboxClaimedTopic   = events.NewTopic[contracts.AgentInboxObservation]("agent/inbox/claimed")
	AgentInboxDiscardedTopic = events.NewTopic[contracts.AgentInboxObservation]("agent/inbox/discarded")
	AgentDisposedTopic       = events.NewTopic[contracts.AgentDisposed]("agent/disposed")
	AgentErrorTopic          = events.NewTopic[contracts.AgentErrorObservation]("agent/error")
	AssistantStreamTopic     = events.NewTopic[contracts.AssistantStreamObservation]("agent/assistant-stream")
)

type observation struct {
	created  *contracts.AgentCreated
	disposed *contracts.AgentDisposed
	error    *contracts.AgentErrorObservation
	session  *SessionObservation
	driver   *contracts.DriverEvent
	stream   *contracts.AssistantStreamObservation
	status   *contracts.AgentStatusObservation
	inbox    *contracts.AgentInboxObservation
}

// EventDispatcher adapts fact-only qi observations to the Gordis
// EventBus. Producers never wait for observers: a generation-owned worker
// drains a bounded queue, and overload is visible through Dropped.
type EventDispatcher struct {
	binding events.Binding
	mu      sync.Mutex
	closed  bool
	queue   chan observation
	dropped atomic.Uint64
}

func NewEventDispatcher(scope *gordis.Scope, capacity int) (*EventDispatcher, error) {
	if scope == nil {
		return nil, gordis.ErrClosed
	}
	if capacity < 1 {
		capacity = 256
	}

	dispatcher := &EventDispatcher{
		binding: events.Bind(scope),
		queue:   make(chan observation, capacity),
	}
	if err := scope.OnStop("close qi observation admission", func(context.Context) error {
		dispatcher.mu.Lock()
		dispatcher.closed = true
		dispatcher.mu.Unlock()
		return nil
	}); err != nil {
		return nil, err
	}
	if err := scope.AfterReady("dispatch qi observations", func(ctx context.Context) error {
		binding := events.Bind(scope)

		for {
			select {
			case item := <-dispatcher.queue:
				if item.created != nil {
					_ = binding.Emit(ctx, AgentCreatedTopic, *item.created)
				}
				if item.disposed != nil {
					_ = binding.Emit(ctx, AgentDisposedTopic, *item.disposed)
				}
				if item.error != nil {
					_ = binding.Emit(ctx, AgentErrorTopic, *item.error)
				}
				if item.session != nil {
					_ = binding.Publish(ctx, SessionAppendedTopic, item.session.Clone())
				}
				if item.driver != nil {
					_ = binding.Publish(ctx, DriverEventTopic, item.driver.Clone())
				}
				if item.stream != nil {
					_ = binding.Publish(ctx, AssistantStreamTopic, item.stream.Clone())
				}
				if item.inbox != nil {
					topic := AgentInboxInsertedTopic
					if item.inbox.Kind == "claimed" {
						topic = AgentInboxClaimedTopic
					}

					if item.inbox.Kind == "discarded" {
						topic = AgentInboxDiscardedTopic
					}

					_ = binding.Emit(ctx, topic, item.inbox.Clone())
				}
				if item.status != nil {
					_ = binding.Emit(ctx, AgentStatusTopic, *item.status)
				}
			case <-ctx.Done():
				return nil
			}
		}
	}); err != nil {
		return nil, err
	}

	return dispatcher, nil
}

func (d *EventDispatcher) SessionAppended(id protocol.SessionID, event protocol.SessionEvent) {
	cloned := SessionObservation{SessionID: id, Event: event.Clone()}
	d.enqueue(observation{session: &cloned})
}

func (d *EventDispatcher) DriverEvent(event contracts.DriverEvent) {
	cloned := event.Clone()
	d.enqueue(observation{driver: &cloned})
}

func (d *EventDispatcher) AssistantStream(event contracts.AssistantStreamObservation) {
	cloned := event.Clone()
	d.enqueue(observation{stream: &cloned})
}

func (d *EventDispatcher) Dropped() uint64 {
	if d == nil {
		return 0
	}

	return d.dropped.Load()
}

func (d *EventDispatcher) enqueue(item observation) {
	if d == nil {
		return
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}

	select {
	case d.queue <- item:
	default:
		d.dropped.Add(1)
	}

	d.mu.Unlock()
}

// SessionObservation carries the session identity at the application boundary.
type SessionObservation struct {
	SessionID protocol.SessionID    `json:"sessionId"`
	Event     protocol.SessionEvent `json:"event"`
}

func (s SessionObservation) Clone() SessionObservation {
	s.Event = s.Event.Clone()
	return s
}

// Lifecycle facts are passive snapshots. Native AgentEvents owns guaranteed
// internal pairing; the Host EventBus may drop and counts overload.
func (d *EventDispatcher) Created(event contracts.AgentCreated) {
	d.enqueue(observation{created: &event})
}

func (d *EventDispatcher) Status(event contracts.AgentStatusObservation) {
	d.enqueue(observation{status: &event})
}

func (d *EventDispatcher) Disposed(event contracts.AgentDisposed) {
	d.enqueue(observation{disposed: &event})
}

func (d *EventDispatcher) Inbox(event contracts.AgentInboxObservation) {
	cloned := event.Clone()
	d.enqueue(observation{inbox: &cloned})
}

func (d *EventDispatcher) AgentError(event contracts.AgentErrorObservation) {
	d.enqueue(observation{error: &event})
}
