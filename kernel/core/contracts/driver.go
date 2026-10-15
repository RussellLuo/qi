package contracts

import "github.com/RussellLuo/qi/kernel/core/protocol"

// DriverEvent is a local observation port, not a second wire protocol. Session
// and Stream carry canonical facts; an ended event also reports an uncommitted
// failure when storage could not accept a closer. Delivery never owns activity.
type DriverEvent struct {
	Type         string // started, session, stream, ended
	SessionID    protocol.SessionID
	InvocationID protocol.InvocationID
	Session      *protocol.SessionEvent
	Stream       *protocol.AssistantStreamFrame
	Reason       protocol.TurnEndReason
	Err          error
	ErrorCode    protocol.ErrorCode // local diagnostic fallback, not persisted
}

func (e DriverEvent) Clone() DriverEvent {
	if e.Session != nil {
		v := e.Session.Clone()
		e.Session = &v
	}

	if e.Stream != nil {
		v := e.Stream.Clone()
		e.Stream = &v
	}

	if e.Reason.Error != nil {
		v := e.Reason.Error.Clone()
		e.Reason.Error = &v
	}

	if e.Reason.Reason != nil {
		v := *e.Reason.Reason
		e.Reason.Reason = &v
	}

	return e
}
