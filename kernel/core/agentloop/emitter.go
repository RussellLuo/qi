package agentloop

import (
	"context"
	"errors"
	"log"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type eventEmitter struct {
	run      Run
	emit     EmitFunc
	observer contracts.ObservationDispatcher
	err      error
}

func newEmitter(run Run, emit EmitFunc, observer contracts.ObservationDispatcher) *eventEmitter {
	return &eventEmitter{
		run:      run,
		emit:     emit,
		observer: observer,
	}
}

func (e *eventEmitter) Emit(event contracts.DriverEvent) {
	event.InvocationID = e.run.InvocationID
	event.SessionID = e.run.Attached.Session.ID()
	if e.observer != nil {
		e.observe("driver/event", func() { e.observer.DriverEvent(event.Clone()) })
	}

	if e.err == nil {
		e.err = e.emit(event.Clone())
	}
}

// Read the committed object, including the store-assigned seq and time.
// Observation errors do not undo persistence or interrupt settlement.
type driverSession struct {
	session.Session
	emitter *eventEmitter
}

func (s driverSession) Snapshot(ctx context.Context) (session.Snapshot, error) {
	return session.ReadSnapshot(ctx, s.Session)
}

func (s driverSession) Append(ctx context.Context, event protocol.SessionEvent) (protocol.Sequence, error) {
	seq, err := s.Session.Append(ctx, event)
	if err != nil {
		return seq, err
	}

	for stored, readErr := range s.Session.Events(context.WithoutCancel(ctx), seq-1) {
		if readErr != nil {
			s.emitter.err = errors.Join(s.emitter.err, readErr)
			break
		}

		if stored.Seq == seq {
			s.emitter.Emit(contracts.DriverEvent{Type: "session", Session: &stored})
			break
		}
	}

	return seq, nil
}

func (e *eventEmitter) Err() error { return e.err }

type scheduledSession struct {
	session.Session
	writer contracts.InboxScheduler
}

func (s scheduledSession) Snapshot(ctx context.Context) (session.Snapshot, error) {
	return session.ReadSnapshot(ctx, s.Session)
}

func (s scheduledSession) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	return s.writer.Append(ctx, e)
}

func (e *eventEmitter) observe(name string, callback func()) {
	if err := observation.Call(name, callback); err != nil {
		log.Print(err)
		if observer, ok := e.run.Agent.(contracts.AgentErrorObserver); ok {
			if nested := observation.Call("agent/observer-error", func() {
				observer.AgentError(contracts.AgentErrorObservation{Agent: e.run.Agent, InstanceID: e.run.Agent.InstanceID(), Error: err})
			}); nested != nil {
				log.Print(nested)
			}
		}
	}
}
