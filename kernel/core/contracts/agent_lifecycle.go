package contracts

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// AgentControl is a transport-neutral activity port. Admission and observation
// contexts never own activity cancellation.
type AgentControl interface {
	AgentRuntime
	CancelActivity(protocol.AgentCancelCause, ...CancelOptions) error
	Send(context.Context, protocol.Message, InboxTarget, bool) error
	Followup(context.Context, protocol.Message) error
	Steer(context.Context, protocol.Message) error
	Inject(context.Context, protocol.Message) error
	Pending(context.Context) (InboxState, error)
	Splice(context.Context, InboxTarget, int, int, []protocol.Message) ([]protocol.Message, error)
	WhenIdle(context.Context) error
	Status() AgentStatus
	Disposed() bool
}

type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"
	AgentRunning AgentStatus = "running"
)

// SessionReservation is held for an attached Agent's entire lifetime.
type SessionReservation interface {
	SessionID() protocol.SessionID
	CreationSource() string
	BeginRun() (func(), error)
	Close() error
}

type AgentSnapshot struct {
	SessionID  protocol.SessionID
	InstanceID string
	Status     AgentStatus
	Disposed   bool
}

type AgentCreated struct {
	Snapshot AgentSnapshot
	Agent    AgentControl
	Source   string
}

type AgentStatusObservation struct {
	InstanceID string
	Agent      AgentControl
	Status     AgentStatus
}

type AgentDisposed struct {
	Agent    AgentControl
	Snapshot AgentSnapshot
	Source   string
	Err      error
}

func ValidateCancelCause(cause protocol.AgentCancelCause) error {
	switch cause.Kind {
	case "user", "parent", "disposed":
		if cause.Reason == "" {
			return nil
		}
	case "hook":
		return nil
	}

	return fmt.Errorf("invalid activity cancel cause: %+v", cause)
}

type ActivityCancellation struct{ Cause protocol.AgentCancelCause }

func (e ActivityCancellation) Error() string {
	return "agent activity canceled: " + e.Cause.Kind + " " + e.Cause.Reason
}

func (ActivityCancellation) Unwrap() error { return context.Canceled }

func CancellationCause(err error) protocol.AgentCancelCause {
	var canceled ActivityCancellation
	if errors.As(err, &canceled) {
		return canceled.Cause
	}

	return protocol.AgentCancelCause{Kind: "parent"}
}

// InboxTarget determines the next boundary at which input is claimed.
type InboxTarget = string

const (
	NextTurn InboxTarget = "next-turn"
	NextStep InboxTarget = "next-step"
)

type InboxState = map[string][]protocol.Message
type CancelOptions struct{ KeepInbox bool }

// InboxSession is the neutral durable input/write port supplied by a Driver.
type InboxSession interface {
	Append(context.Context, protocol.SessionEvent) (protocol.Sequence, error)
	Events(context.Context, protocol.Sequence) iter.Seq2[protocol.SessionEvent, error]
}

// InboxScheduler serializes Loop appends with input, claim and cancel mutations.
type InboxScheduler interface {
	Append(context.Context, protocol.SessionEvent) (protocol.Sequence, error)
	Claim(context.Context, InboxTarget, protocol.TurnID) ([]protocol.Message, error)
	Pending(context.Context) (InboxState, error)
	// EnqueueContexts persists tool-provided next-step input without waking work.
	EnqueueContexts(context.Context, []protocol.Message) error
}

// AgentErrorObservation carries the original local error, never ErrorDetail JSON.
type AgentErrorObservation struct {
	InstanceID string
	Agent      AgentRuntime
	Turn       protocol.TurnID
	Step       protocol.StepID
	Error      error
}

type AgentErrorObserver interface{ AgentError(AgentErrorObservation) }

type AgentInboxObservation struct {
	InstanceID string
	Agent      AgentControl
	Kind       string
	Message    protocol.Message
	Turn       protocol.TurnID
}

func (o AgentInboxObservation) Clone() AgentInboxObservation {
	o.Message = o.Message.Clone()
	return o
}

// Notifications are fact-only and must return without waiting for listeners.
type AgentInboxObserver interface{ Inbox(AgentInboxObservation) }
