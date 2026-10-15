package headless

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

const maxFailures = 64

type driveFailure struct {
	session        protocol.SessionID
	after, through protocol.Sequence
	err            error
}

// outcomes records synchronous driver returns before Agent idle. Only failures
// need retention; overflow fails closed instead of hiding earlier failures.
type outcomes struct {
	mu        sync.Mutex
	completed uint64
	failures  []driveFailure
	overflow  bool
}
type outcomeDriver struct {
	inner    agentloop.Driver
	outcomes *outcomes
}

func (o *outcomes) Wrap(inner agentloop.Driver) agentloop.Driver {
	return &outcomeDriver{inner: inner, outcomes: o}
}
func (d *outcomeDriver) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	current := run.Attached.Session
	after := current.Watermark()
	// Delegate exactly the original activity, callback and error.
	err := d.inner.Drive(ctx, run, emit)
	through := current.Watermark()
	recordErr := err
	d.outcomes.mu.Lock()
	d.outcomes.completed++
	if recordErr != nil {
		if len(d.outcomes.failures) < maxFailures {
			d.outcomes.failures = append(d.outcomes.failures, driveFailure{run.Attached.Session.ID(), after, through, recordErr})
		} else {
			d.outcomes.overflow = true
		}
	}
	d.outcomes.mu.Unlock()
	return err
}
func (o *outcomes) Mark() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures = nil
	o.overflow = false
	return o.completed
}

func (o *outcomes) Check(ctx context.Context, current session.Session, mark uint64) error {
	o.mu.Lock()
	count := o.completed
	failures := append([]driveFailure(nil), o.failures...)
	overflow := o.overflow
	o.mu.Unlock()
	if count == mark {
		return errors.New("headless activity ended without a driver completion")
	}
	if overflow {
		return errors.New("headless driver failure summary overflow")
	}
	for _, failure := range failures {
		if failure.session != current.ID() {
			return errors.New("headless outcome includes another Session")
		}
		// The error's own durable interval must actually close with error/aborted.
		// A later successful Turn, historical result or local ended notice cannot
		// cover an append failure in this Drive.
		var turn protocol.TurnID
		var step protocol.StepID
		var reason *protocol.TurnEndReason
		for event, err := range current.Events(ctx, failure.after) {
			if err != nil {
				return err
			}
			if event.Seq > failure.through {
				break
			}
			switch event.Type {
			case protocol.TurnStarted:
				turn = protocol.EventPayload[protocol.TurnData](event).Turn
			case protocol.StepStarted:
				step = protocol.EventPayload[protocol.StepData](event).Step
			case protocol.StepEnded:
				step = 0
			case protocol.TurnEnded:
				data := protocol.EventPayload[protocol.TurnEndData](event)
				reason = &data.Reason
				turn = 0
			}
		}
		if turn != 0 || step != 0 || reason == nil || (reason.Kind != "error" && reason.Kind != "aborted") {
			return fmt.Errorf("headless driver failed without durable settlement: %w", failure.err)
		}
	}
	return nil
}
