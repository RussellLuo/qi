package agent

import (
	"context"
	"iter"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
)

type AgentSubscription interface {
	Events() iter.Seq2[AgentNotification, error]
	Dropped() uint64
	Close()
}
type AssistantSubscription interface {
	Events() iter.Seq2[contracts.AssistantStreamObservation, error]
	Dropped() uint64
	Close()
}

// AgentNotification has exactly one branch. InstanceID distinguishes replacement
// attachments with the same SessionID; these notifications are not replayable.
type AgentNotification struct {
	InstanceID string
	Created    *contracts.AgentCreated
	Disposed   *contracts.AgentDisposed
	Status     *contracts.AgentStatusObservation
	Inbox      *contracts.AgentInboxObservation
	Error      *contracts.AgentErrorObservation
}

func (e AgentNotification) Clone() AgentNotification {
	if e.Created != nil {
		v := *e.Created
		e.Created = &v
	}
	if e.Disposed != nil {
		v := *e.Disposed
		e.Disposed = &v
	}
	if e.Status != nil {
		v := *e.Status
		e.Status = &v
	}
	if e.Inbox != nil {
		v := e.Inbox.Clone()
		e.Inbox = &v
	}
	if e.Error != nil {
		v := *e.Error
		e.Error = &v
	}
	return e
}
func (e AgentNotification) kind() string {
	switch {
	case e.Created != nil:
		return "created"
	case e.Disposed != nil:
		return "disposed"
	case e.Status != nil:
		return "status"
	case e.Inbox != nil:
		return "inbox"
	default:
		return "error"
	}
}
func (e AgentNotification) runtime() contracts.AgentRuntime {
	switch {
	case e.Created != nil:
		return e.Created.Agent
	case e.Disposed != nil:
		return e.Disposed.Agent
	case e.Status != nil:
		return e.Status.Agent
	case e.Inbox != nil:
		return e.Inbox.Agent
	default:
		return e.Error.Agent
	}
}
func (e AgentNotification) call(h ObserverHandlers) {
	switch {
	case e.Created != nil:
		if h.Created != nil {
			h.Created(*e.Created)
		}
	case e.Disposed != nil:
		if h.Disposed != nil {
			h.Disposed(*e.Disposed)
		}
	case e.Status != nil:
		if h.Status != nil {
			h.Status(*e.Status)
		}
	case e.Inbox != nil:
		if h.Inbox != nil {
			h.Inbox(e.Inbox.Clone())
		}
	case e.Error != nil:
		if h.Error != nil {
			h.Error(*e.Error)
		}
	}
}
func (e AgentNotification) handles(h ObserverHandlers) bool {
	switch {
	case e.Created != nil:
		return h.Created != nil
	case e.Disposed != nil:
		return h.Disposed != nil
	case e.Status != nil:
		return h.Status != nil
	case e.Inbox != nil:
		return h.Inbox != nil
	default:
		return h.Error != nil
	}
}
func (e AgentNotification) observe(h AgentObserver) {
	switch {
	case e.Created != nil:
		h.Created(*e.Created)
	case e.Disposed != nil:
		h.Disposed(*e.Disposed)
	case e.Status != nil:
		h.Status(*e.Status)
	case e.Inbox != nil:
		h.Inbox(e.Inbox.Clone())
	case e.Error != nil:
		h.AgentError(*e.Error)
	}
}

// Local queues use 256 slots, drop new on overflow and count loss. They register
// synchronously and never own cancellation. Subscribe then query State/Pending;
// Session facts remain the authority after notification or preview gaps.
func (a *loopAgent) Subscribe(ctx context.Context) (AgentSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	if a.notifications == nil {
		a.notifications = make(map[*observation.Subscription[AgentNotification]]struct{})
	}
	var sub *observation.Subscription[AgentNotification]
	sub = observation.New[AgentNotification](ctx, func() { a.mu.Lock(); delete(a.notifications, sub); a.mu.Unlock() })
	a.notifications[sub] = struct{}{}
	a.mu.Unlock()
	sub.Watch()
	return sub, nil
}
func (a *loopAgent) SubscribeAssistant(ctx context.Context) (AssistantSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	if a.streams == nil {
		a.streams = make(map[*observation.Subscription[contracts.AssistantStreamObservation]]struct{})
	}
	var sub *observation.Subscription[contracts.AssistantStreamObservation]
	sub = observation.New[contracts.AssistantStreamObservation](ctx, func() { a.mu.Lock(); delete(a.streams, sub); a.mu.Unlock() })
	a.streams[sub] = struct{}{}
	a.mu.Unlock()
	sub.Watch()
	return sub, nil
}
func (a *loopAgent) notify(e AgentNotification) {
	a.mu.Lock()
	if !a.ready {
		if e.Inbox != nil && !a.disposed {
			if len(a.initialNotifications) < 256 {
				a.initialNotifications = append(a.initialNotifications, e.Clone())
			} else {
				a.initialDropped++
			}
		}
		a.mu.Unlock()
		return
	}
	for sub := range a.notifications {
		sub.Push(e.Clone())
	}
	a.mu.Unlock()
	if a.observer != nil {
		e.observe(a.observer)
	}
}
func (a *loopAgent) AssistantStream(e contracts.AssistantStreamObservation) {
	e.InstanceID = a.InstanceID()
	a.mu.Lock()
	defer a.mu.Unlock()
	for sub := range a.streams {
		sub.Push(e.Clone())
	}
}
func (a *loopAgent) snapshot() contracts.AgentSnapshot {
	instance := a.InstanceID()
	a.mu.Lock()
	defer a.mu.Unlock()
	return contracts.AgentSnapshot{SessionID: a.sessionID, InstanceID: instance, Status: a.status, Disposed: a.disposed}
}
func (a *loopAgent) finishObservations(e contracts.AgentDisposed) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for sub := range a.notifications {
		sub.Push(AgentNotification{InstanceID: e.Snapshot.InstanceID, Disposed: &e})
		sub.Finish()
	}
	for sub := range a.streams {
		sub.Finish()
	}
	a.notifications = nil
	a.streams = nil
}

func (a *loopAgent) notifyDiagnostic(e contracts.AgentErrorObservation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for sub := range a.notifications {
		sub.Push(AgentNotification{InstanceID: e.InstanceID, Error: &e})
	}
}
