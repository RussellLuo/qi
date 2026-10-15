package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func (l *Loop) turn(ctx context.Context, run Run, config Config, current session.Session, emitter *eventEmitter, turnID protocol.TurnID, errorStep *protocol.StepID) error {
	contextInbox := run.Attached.Inbox
	appendEvent := func(appendCtx context.Context, event protocol.SessionEvent) error {
		_, err := current.Append(appendCtx, event)
		return err
	}
	// Cancellation stops new model/tool work, but settlement must still record
	// the admitted turn's outcome and any completed tool results.
	settleCtx := context.WithoutCancel(ctx)
	turnStarted := false
	startTurn := func() error {
		if turnStarted {
			return nil
		}
		if err := appendEvent(settleCtx, protocol.NewTurnStarted(turnID)); err != nil {
			return err
		}

		turnStarted = true
		emitter.Emit(contracts.DriverEvent{Type: "started"})

		return nil
	}

	prepared, prepareErr := l.coordinator.PrepareRun(ctx, config.Selection)
	if prepareErr != nil {
		if err := startTurn(); err != nil {
			return errors.Join(prepareErr, err, emitter.Err())
		}

		return l.settle(settleCtx, nil, appendEvent, emitter, run, turnID, 0, prepareErr, protocol.ErrorInternal)
	}

	defer prepared.Close()

	if err := startTurn(); err != nil {
		return errors.Join(err, emitter.Err())
	}

	turnReason := protocol.ReasonCompleted
	turnConcluded := false
	state, err := prepareRequestState(ctx, current, run.Attached.RequestState)
	if err != nil {
		return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, 0, err, protocol.ErrorInternal)
	}

	// Each step claims its input, applies middleware, then executes a request
	// and its tools before deciding whether the turn needs another step.
	for stepIndex := 1; ; stepIndex++ {
		*errorStep = 0
		if stepIndex > config.MaxSteps {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, 0, ErrMaxSteps, protocol.ErrorMaxSteps)
		}

		stepInput, code, err := prepareStep(ctx, prepared, run, turnID, stepIndex)
		if err != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, 0, err, code)
		}
		if stepInput.Kind == "reject" {
			return l.blocked(settleCtx, prepared, appendEvent, emitter, run, turnID)
		}
		stepID := protocol.StepID(stepIndex)

		if (stepIndex == 1 || turnConcluded) && len(stepInput.Messages) == 0 {
			if err := appendEvent(settleCtx, protocol.NewTurnEnded(turnID, protocol.TurnEndReason{Kind: string(turnReason)})); err != nil {
				return err
			}

			emitter.Emit(contracts.DriverEvent{Type: "ended", Reason: protocol.TurnEndReason{Kind: string(turnReason)}})
			return emitter.Err()
		}

		if err := appendEvent(ctx, protocol.NewStepStarted(turnID, stepID)); err != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, 0, err, protocol.ErrorInternal)
		}

		*errorStep = stepID
		accumulated, uncommitted, err := l.requestAttempts(ctx, settleCtx, current, prepared, state, run, config, turnID, stepID, stepInput, emitter)
		if uncommitted {
			return reportUncommitted(emitter, run, err)
		}
		if err != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, stepID, err, protocol.ErrorStream)
		}

		calls := toolCalls(accumulated.Message)
		if accumulated.Finish.Kind == protocol.FinishMaxTokens {
			turnReason = protocol.ReasonMaxTokens
			calls = nil
		}

		concluded, uncommitted, err := executeTools(ctx, settleCtx, current, prepared, run, turnID, stepID, calls)
		if uncommitted {
			return reportUncommitted(emitter, run, err)
		}
		if err != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, stepID, err, protocol.ErrorTool)
		}

		if ctx.Err() != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, stepID, context.Cause(ctx), protocol.ErrorCancelled)
		}

		turnConcluded = concluded
		if err := appendEvent(settleCtx, protocol.NewStepEnded(turnID, stepID)); err != nil {
			return reportUncommitted(emitter, run, err)
		}
		if !concluded {
			continue
		}

		pending, err := contextInbox.Pending(ctx)
		if err != nil {
			return l.settle(settleCtx, prepared, appendEvent, emitter, run, turnID, 0, err, protocol.ErrorInternal)
		}
		if len(pending[contracts.NextStep]) > 0 {
			continue
		}

		if err := prepared.TurnStopping(run.activityContext, agentmiddleware.TurnStop{
			Agent:        run.Agent,
			Turn:         turnID,
			AgentID:      run.Agent.SessionID().String(),
			SessionID:    run.Attached.Session.ID(),
			InvocationID: run.InvocationID,
			Reason:       turnReason,
		}); err != nil {
			return l.settle(
				settleCtx,
				nil,
				appendEvent,
				emitter,
				run,
				turnID,
				0,
				fmt.Errorf("agent/turn-stopping: %w", err),
				protocol.ErrorInternal,
			)
		}
		if err := ctx.Err(); err != nil {
			return l.settle(settleCtx, nil, appendEvent, emitter, run, turnID, 0, err, protocol.ErrorCancelled)
		}
		// A serial listener may await work and steer before returning.
		pending, err = contextInbox.Pending(ctx)
		if err != nil {
			return l.settle(settleCtx, nil, appendEvent, emitter, run, turnID, 0, err, protocol.ErrorInternal)
		}
		if len(pending[contracts.NextStep]) > 0 {
			continue
		}

		if err := appendEvent(settleCtx, protocol.NewTurnEnded(turnID, protocol.TurnEndReason{Kind: string(turnReason)})); err != nil {
			return reportUncommitted(emitter, run, err)
		}

		emitter.Emit(contracts.DriverEvent{Type: "ended", Reason: protocol.TurnEndReason{Kind: string(turnReason)}})

		return emitter.Err()
	}
}
