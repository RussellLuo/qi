package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
)

type AgentObserver interface {
	Created(contracts.AgentCreated)
	Status(contracts.AgentStatusObservation)
	Inbox(contracts.AgentInboxObservation)
	AgentError(contracts.AgentErrorObservation)
	Disposed(contracts.AgentDisposed)
}
type ObserverHandlers struct {
	ID       string
	Order    int
	Created  func(contracts.AgentCreated)
	Status   func(contracts.AgentStatusObservation)
	Inbox    func(contracts.AgentInboxObservation)
	Error    func(contracts.AgentErrorObservation)
	Disposed func(contracts.AgentDisposed)
}
type AgentEvents interface {
	AgentObserver
	Register(contracts.ContributionOwner, ObserverHandlers) (func(), error)
	Dropped() uint64
	Close(context.Context) error
}

var ErrObservationCapacity = errors.New("agent lifecycle observation capacity exhausted")

// agentEvents serializes passive callbacks. Each admitted lifecycle reserves
// two records until Disposed is delivered; up to 256 lifecycles and 256 ordinary
// facts are retained. Full lifecycle capacity rejects creation, never disposal.
// Callbacks run outside resource cleanup. Close belongs to the event owner and
// must not be awaited from its own callback; Dispose/WhenIdle may be awaited.
type agentEvents struct {
	entries  *registry.Registry[ObserverHandlers]
	mirror   AgentObserver
	mu       sync.Mutex
	queue    []eventDelivery
	ordinary int
	reserved int
	capacity int
	closing  bool
	wake     chan struct{}
	done     chan struct{}
	dropped  atomic.Uint64
}
type eventDelivery struct {
	event  AgentNotification
	ticket *lifecycleTicket
}
type lifecycleTicket struct {
	events    *agentEvents
	published bool
	finished  bool
}

func NewAgentEvents(mirrors ...AgentObserver) AgentEvents {
	r := &agentEvents{entries: registry.New[ObserverHandlers](nil), capacity: 256, wake: make(chan struct{}, 1), done: make(chan struct{})}
	if len(mirrors) > 0 {
		r.mirror = mirrors[0]
	}
	go r.work()
	return r
}
func (r *agentEvents) Register(owner contracts.ContributionOwner, h ObserverHandlers) (func(), error) {
	if h.ID == "" || (h.Created == nil && h.Status == nil && h.Inbox == nil && h.Error == nil && h.Disposed == nil) {
		return nil, errors.New("observer requires id and handler")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, ErrClosed
	}
	return r.entries.Register(owner, h.ID, h.Order, h)
}
func (r *agentEvents) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *agentEvents) reserve() (*lifecycleTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, ErrClosed
	}
	if r.reserved >= r.capacity {
		return nil, ErrObservationCapacity
	}
	r.reserved++
	return &lifecycleTicket{events: r}, nil
}
func (t *lifecycleTicket) cancel() {
	r := t.events
	r.mu.Lock()
	defer r.mu.Unlock()
	if !t.published && !t.finished {
		t.finished = true
		r.reserved--
		r.signal()
	}
}
func (t *lifecycleTicket) created(event contracts.AgentCreated) {
	r := t.events
	r.mu.Lock()
	defer r.mu.Unlock()
	t.published = true
	r.queue = append(r.queue, eventDelivery{event: AgentNotification{InstanceID: event.Snapshot.InstanceID, Created: &event}, ticket: t})
	r.signal()
}
func (t *lifecycleTicket) disposed(event contracts.AgentDisposed) {
	r := t.events
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.finished {
		return
	}
	t.finished = true
	r.queue = append(r.queue, eventDelivery{event: AgentNotification{InstanceID: event.Snapshot.InstanceID, Disposed: &event}, ticket: t})
	r.signal()
}
func (r *agentEvents) enqueue(event AgentNotification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.ordinary >= 256 {
		r.dropped.Add(1)
		return
	}
	r.ordinary++
	r.queue = append(r.queue, eventDelivery{event: event.Clone()})
	r.signal()
}
func (r *agentEvents) Created(e contracts.AgentCreated) {
	r.enqueue(AgentNotification{InstanceID: e.Snapshot.InstanceID, Created: &e})
}
func (r *agentEvents) Status(e contracts.AgentStatusObservation) {
	r.enqueue(AgentNotification{InstanceID: e.InstanceID, Status: &e})
}
func (r *agentEvents) Inbox(e contracts.AgentInboxObservation) {
	r.enqueue(AgentNotification{InstanceID: e.InstanceID, Inbox: &e})
}
func (r *agentEvents) AgentError(e contracts.AgentErrorObservation) {
	r.enqueue(AgentNotification{InstanceID: e.InstanceID, Error: &e})
}
func (r *agentEvents) Disposed(e contracts.AgentDisposed) {
	r.enqueue(AgentNotification{InstanceID: e.Snapshot.InstanceID, Disposed: &e})
}
func (r *agentEvents) Dropped() uint64 { return r.dropped.Load() }
func (r *agentEvents) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closing = true
	r.signal()
	r.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *agentEvents) work() {
	defer close(r.done)
	defer r.entries.Close()
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			finished := r.closing && r.reserved == 0
			r.mu.Unlock()
			if finished {
				return
			}
			<-r.wake
			continue
		}
		item := r.queue[0]
		r.queue[0] = eventDelivery{}
		r.queue = r.queue[1:]
		if item.ticket == nil {
			r.ordinary--
		}
		r.mu.Unlock()
		r.deliver(item.event)
		if item.ticket != nil && item.event.Disposed != nil {
			r.mu.Lock()
			r.reserved--
			r.mu.Unlock()
		}
	}
}
func (r *agentEvents) deliver(event AgentNotification) {
	var failures []error
	func() {
		snapshot, err := r.entries.PrepareWhere(func(item registry.Item[ObserverHandlers]) bool { return event.handles(item.Value) })
		if err != nil {
			return
		}
		defer snapshot.Close()
		for _, item := range snapshot.Items() {
			if err := callLifecycleObserver(event.kind(), fmt.Sprintf("observer %q", item.Identity.Key), func() { event.call(item.Value) }); err != nil {
				failures = append(failures, err)
			}
		}
	}()
	if r.mirror != nil {
		if err := callLifecycleObserver(event.kind(), "observer mirror", func() { event.observe(r.mirror) }); err != nil {
			failures = append(failures, err)
		}
	}
	for _, err := range failures {
		log.Print(err)
		if event.Error == nil {
			diagnostic := contracts.AgentErrorObservation{Agent: event.runtime(), InstanceID: event.InstanceID, Error: err}
			if a, ok := event.runtime().(*loopAgent); ok {
				a.notifyDiagnostic(diagnostic)
			}
			r.deliver(AgentNotification{InstanceID: event.InstanceID, Error: &diagnostic})
		}
	}
}

func callLifecycleObserver(event, identity string, call func()) (err error) {
	return observation.Call(identity+" handling "+event, call)
}
