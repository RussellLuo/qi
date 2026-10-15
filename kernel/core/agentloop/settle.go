package agentloop

import (
	"context"
	"errors"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
)

func (l *Loop) settle(
	ctx context.Context,
	prepared *runprep.PreparedRun,
	appendEvent func(context.Context, protocol.SessionEvent) error,
	emitter *eventEmitter,
	run Run,
	turnID protocol.TurnID,
	stepID protocol.StepID,
	cause error,
	fallback protocol.ErrorCode,
) error {
	cause = errors.Join(context.Cause(run.activityContext), cause)
	reason := completionReason(cause, fallback)
	code := protocol.ErrorCodeFor(cause, fallback)
	detail := protocol.NewErrorDetail(code, cause)
	var settleErr error
	if stepID != 0 {
		if err := appendEvent(ctx, protocol.NewStepEnded(turnID, stepID)); err != nil {
			return reportUncommitted(emitter, run, errors.Join(cause, err))
		}
	}

	if prepared != nil {
		settleErr = errors.Join(settleErr, prepared.TurnStopping(run.activityContext, agentmiddleware.TurnStop{
			Agent:        run.Agent,
			Turn:         turnID,
			AgentID:      run.Agent.SessionID().String(),
			SessionID:    run.Attached.Session.ID(),
			InvocationID: run.InvocationID,
			Reason:       reason,
			Error:        detail,
		}))
	}
	// A listener can cancel while awaiting. Re-read the first activity cause.
	cause = errors.Join(context.Cause(run.activityContext), cause)
	reason = completionReason(cause, fallback)
	detail = protocol.NewErrorDetail(protocol.ErrorCodeFor(cause, fallback), cause)
	if err := appendEvent(ctx, protocol.NewTurnEnded(turnID, durableTurnReason(cause, reason))); err != nil {
		return reportUncommitted(emitter, run, errors.Join(cause, settleErr, err))
	}

	emitter.Emit(contracts.DriverEvent{
		Type:      "ended",
		Reason:    durableTurnReason(cause, reason),
		Err:       cause,
		ErrorCode: protocol.ErrorCodeFor(cause, fallback),
	})

	return errors.Join(cause, settleErr, emitter.Err())
}

func completionReason(err error, fallback protocol.ErrorCode) protocol.CompletionReason {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || fallback == protocol.ErrorCancelled {
		return protocol.ReasonCancelled
	}
	if errors.Is(err, ErrMaxSteps) || fallback == protocol.ErrorMaxSteps {
		return protocol.ReasonMaxSteps
	}

	return protocol.ReasonFailed
}

// Keep the outer coarse completion view out of the durable Turn payload.
func durableTurnReason(cause error, reason protocol.CompletionReason) protocol.TurnEndReason {
	switch reason {
	case protocol.ReasonCancelled:
		return protocol.TurnEndReason{Kind: "aborted", Reason: cancelReason(cause)}
	case protocol.ReasonMaxSteps:
		return protocol.TurnEndReason{Kind: "blocked"}
	}

	failure := &protocol.LlmFailure{Code: "UNKNOWN", Message: cause.Error()}
	var streamFailure *llm.StreamFailure
	if errors.As(cause, &streamFailure) && streamFailure.Reason.Failure != nil {
		v := streamFailure.Reason.Failure.Clone()
		failure = &v
	} else {
		var structured interface{ LlmFailure() protocol.LlmFailure }
		if errors.As(cause, &structured) {
			v := structured.LlmFailure().Clone()
			failure = &v
		}
	}

	return protocol.TurnEndReason{Kind: "error", Error: failure}
}

func cancelReason(err error) *protocol.AgentCancelCause {
	cause := contracts.CancellationCause(err)
	return &cause
}
