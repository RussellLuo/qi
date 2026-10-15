// Package agentloop drives turns through prompt preparation, LLM attempts, tool
// execution, and Session settlement. Agent admission lives in package agent.
package agentloop

import (
	"context"
	"errors"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
)

const DefaultMaxSteps = 8

var ErrMaxSteps = errors.New("agent loop reached max steps")

type Config struct {
	MaxSteps  int
	LLM       protocol.CallConfig
	Selection runprep.Selection
}

type AttachedRun struct {
	Session      session.Session
	Reservation  contracts.SessionReservation
	Inbox        contracts.InboxScheduler
	RequestState *RequestState
}

type Run struct {
	activityContext context.Context
	Agent           contracts.AgentRuntime
	Attached        AttachedRun
	InvocationID    protocol.InvocationID
	Config          Config
}

type EmitFunc func(contracts.DriverEvent) error

type Driver interface {
	Drive(context.Context, Run, EmitFunc) error
}

type Loop struct {
	coordinator runprep.Coordinator
	observer    contracts.ObservationDispatcher
}

func New(coordinator runprep.Coordinator, observers ...contracts.ObservationDispatcher) *Loop {
	loop := &Loop{
		coordinator: coordinator,
	}
	if len(observers) > 0 {
		loop.observer = observers[0]
	}

	return loop
}

func (l *Loop) Drive(ctx context.Context, run Run, emit EmitFunc) (driveErr error) {
	run.activityContext = ctx
	if l == nil {
		return errors.New("agent loop is nil")
	}
	if run.Agent == nil || run.Attached.Session == nil || run.Attached.Reservation == nil || run.Attached.Inbox == nil || run.Attached.RequestState == nil {
		return errors.New("agent run requires Agent, Session, attachment, Inbox and RequestState")
	}
	if run.InvocationID == "" || run.Agent.SessionID() != run.Attached.Session.ID() || run.Attached.Reservation.SessionID() != run.Attached.Session.ID() {
		return errors.New("agent run has inconsistent attached identity")
	}
	if emit == nil {
		return errors.New("agent run requires an event sink")
	}

	config, err := normalizeConfig(run.Config)
	if err != nil {
		return err
	}

	current := run.Attached.Session
	releaseReservation, err := current.BeginRunOwned(run.Attached.Reservation)
	if err != nil {
		return err
	}
	defer releaseReservation()
	current = scheduledSession{Session: current, writer: run.Attached.Inbox}

	turnID := current.LastTurn() + 1
	var errorStep protocol.StepID
	defer func() {
		if driveErr == nil {
			return
		}

		e := contracts.AgentErrorObservation{
			InstanceID: run.Agent.InstanceID(),
			Agent:      run.Agent,
			Turn:       turnID,
			Step:       errorStep,
			Error:      driveErr,
		}
		if observer, ok := run.Agent.(contracts.AgentErrorObserver); ok {
			observer.AgentError(e)
		} else if observer, ok := l.observer.(contracts.AgentErrorObserver); ok {
			observer.AgentError(e)
		}
	}()

	emitter := newEmitter(run, emit, l.observer)
	current = driverSession{Session: current, emitter: emitter}
	return l.turn(ctx, run, config, current, emitter, turnID, &errorStep)
}
