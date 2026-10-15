package agent

import (
	"context"
	"errors"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
)

func (a *loopAgent) work() {
	defer a.cleanup()

	for {
		select {
		case <-a.inbox:
			a.run()
		case <-a.ctx.Done():
			// An admitted wake owns a boundary even if shutdown cleared its input.
			select {
			case <-a.inbox:
				a.run()
			default:
			}
			return
		}
	}
}

func (a *loopAgent) run() {
	for {
		// Select pending input and capture configuration under the admission
		// gate. Driver callbacks run after the gate has been released.
		a.transitions.Lock()
		pending, err := a.inputInbox.Pending(context.Background())
		invocation := a.newInvocation()
		a.activeOutput = nil
		a.errorReported.Store(false)
		if err == nil {
			if q := pending[contracts.NextTurn]; len(q) > 0 {
				if request := a.pendingOutputs[q[0].ID]; request != nil {
					invocation = request.invocation
					a.activeOutput = request
					delete(a.pendingOutputs, q[0].ID)
				}
			}
		}

		a.mu.Lock()
		runCtx, cancel := a.activityCtx, a.activeStop
		if runCtx == nil {
			runCtx, cancel = context.WithCancelCause(a.ctx)
		}

		a.active = invocation
		a.activeStop = cancel
		a.activityCtx = runCtx
		config := cloneConfig(a.config)
		requestState := a.requestState
		a.mu.Unlock()
		a.transitions.Unlock()
		run := agentloop.Run{
			Agent:        a,
			Attached:     agentloop.AttachedRun{Session: a.session, Reservation: a.reservation, Inbox: a, RequestState: requestState},
			InvocationID: invocation,
			Config:       config.Loop,
		}

		blocked := false
		if err == nil {
			err = a.driver.Drive(runCtx, run, func(event contracts.DriverEvent) error {
				a.transitions.Lock()
				defer a.transitions.Unlock()

				if event.InvocationID != invocation || event.SessionID != a.sessionID {
					return errors.New("Driver observation belongs to another invocation or Session")
				}
				if event.Stream != nil {
					a.AssistantStream(contracts.AssistantStreamObservation{Agent: a, Frame: event.Stream.Clone()})
				}

				if event.Type == "ended" && event.Reason.Kind == "blocked" {
					blocked = true
				}

				if request := a.activeOutput; request != nil {

					return request.output.emit(event)
				}

				return nil
			})
		}

		// A Driver may fail before it can persist or emit an ending fact.
		// The native Loop reports with turn/step; wrappers and custom Drivers
		// still receive an independent original-error notification here.
		if err != nil && !a.errorReported.Load() {
			a.AgentError(contracts.AgentErrorObservation{Agent: a, Error: err})
		}
		// Settle the subscription before deciding whether pending input starts
		// another activity. An explicit wake can replay input after cancellation.
		a.transitions.Lock()
		canceled := runCtx.Err() != nil
		cancel(nil)
		if a.activeOutput != nil {
			a.activeOutput.output.finish(err)
		}

		a.activeOutput = nil
		pending, pendingErr := a.inputInbox.Pending(context.Background())
		a.mu.Lock()
		if a.disposed {
			a.cleanupErr = errors.Join(a.cleanupErr, settlementFailure(err))
		}
		replay := a.wakeRequested
		a.wakeRequested = false
		more := pendingErr == nil && (len(pending[contracts.NextTurn]) > 0 || len(pending[contracts.NextStep]) > 0)
		continueWork := !a.disposed && more && ((err == nil && !blocked && !canceled) || replay)
		a.active = ""
		a.activeStop = nil
		a.activityCtx = nil
		if !continueWork {
			a.status = StatusIdle
		}

		idle := a.idle
		a.mu.Unlock()
		if !continueWork {
			a.notifyStatus(StatusIdle)
			close(idle)
		}

		a.transitions.Unlock()
		if !continueWork {
			return
		}
	}
}

// Disposal's requested cancellation is expected. Joined settlement failures
// must still reach the common Dispose result, including after cancellation.
func settlementFailure(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var failure error
		for _, part := range joined.Unwrap() {
			failure = errors.Join(failure, settlementFailure(part))
		}
		return failure
	}
	if errors.Is(err, context.Canceled) {
		if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			return settlementFailure(wrapped.Unwrap())
		}
		return nil
	}
	return err
}
