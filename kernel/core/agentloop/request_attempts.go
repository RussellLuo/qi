package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// Each attempt has a fresh bound call and live/durable stream. Pre-step and
// Inbox claim happen outside this loop; only the first request admits its batch.
func (l *Loop) requestAttempts(
	ctx, settleCtx context.Context,
	current session.Session,
	prepared *runprep.PreparedRun,
	state *RequestState,
	run Run,
	config Config,
	turn protocol.TurnID,
	step protocol.StepID,
	input agentmiddleware.StepInput,
	emitter *eventEmitter,
) (llm.Accumulated, bool, error) {
	first := true

	for {
		proposal, err := requestProposal(ctx, current, config.LLM, step > 1 || !first)
		if err != nil {
			return llm.Accumulated{}, false, err
		}

		requestInput, err := prepared.Request(ctx, agentmiddleware.Request{
			Agent:        run.Agent,
			Turn:         turn,
			Step:         step,
			Index:        int(step),
			AgentID:      run.Agent.SessionID().String(),
			SessionID:    run.Attached.Session.ID(),
			InvocationID: run.InvocationID,
			Config:       proposal,
		})
		if err != nil {
			return llm.Accumulated{}, false, fmt.Errorf("agent/request: %w", err)
		}
		if err := validateRequest(requestInput, run, turn, int(step)); err != nil {
			return llm.Accumulated{}, false, err
		}

		preparedStep, err := prepared.PrepareCall(ctx, input.Prompt, input.Messages, requestInput.Config)
		if err != nil {
			return llm.Accumulated{}, false, fmt.Errorf("prepare call %d: %w", step, err)
		}
		if err := ctx.Err(); err != nil {
			preparedStep.Close()
			return llm.Accumulated{}, false, err
		}

		batch := input.Messages
		if !first {
			batch = nil
		}

		request, err := admitRequest(
			ctx,
			current,
			preparedStep,
			state,
			run,
			turn,
			step,
			input.Prompt.Text,
			batch,
			first && input.StartsRequestSeries,
		)
		if err != nil {
			preparedStep.Close()
			return llm.Accumulated{}, false, err
		}

		first = false
		retryPolicy := preparedStep.LLM().RetryPolicy()

		// Failed attempts have no Message in the Surface; the successful
		// Message keeps the Step identity while live attempts use their own counter.
		accumulator, err := llm.NewAccumulator(protocol.MessageID(fmt.Sprintf("%s/assistant-%d", run.InvocationID, step)), protocol.MessageSource{
			Kind:     protocol.SourceModel,
			Provider: request.Config.Provider,
			Model:    request.Config.Model,
		})
		if err != nil {
			preparedStep.Close()
			return llm.Accumulated{}, false, err
		}

		attempt := newAssistantAttempt(run.Agent, turn, step, accumulator, emitter)
		attempt.start()
		stream := preparedStep.LLM().Stream(ctx, request)
		stream, streamErr := prepared.Stream(ctx, stream)
		if streamErr == nil {
			streamErr = attempt.consume(stream)
		}

		preparedStep.Close()
		if ctx.Err() != nil {
			streamErr = errors.Join(streamErr, ctx.Err())
		}

		var accumulated llm.Accumulated
		if streamErr == nil {
			accumulated, streamErr = accumulator.Finish()
		}

		// Settle every attempt before consulting retry middleware. Failed
		// attempts retain stream records; success publishes an assistant Message.
		settlement := protocol.NewAttemptEvent(turn, step, attempt.records)
		if streamErr == nil {
			settlement = protocol.NewAssistantMessageEvent(turn, step, accumulated.Message, attempt.records, accumulated.Usage)
		} else if ctx.Err() != nil {
			if prefix := accumulator.Attempt(); prefix != nil {
				settlement = protocol.NewInterruptedAssistantMessageEvent(turn, step, *prefix, attempt.records, accumulator.Usage())
			}
		}

		if err := attempt.settle(settleCtx, current, settlement); err != nil {
			return llm.Accumulated{}, true, errors.Join(streamErr, err)
		}
		if streamErr == nil {
			return accumulated, false, nil
		}
		if err := ctx.Err(); err != nil {
			return llm.Accumulated{}, false, errors.Join(streamErr, err)
		}

		var failure *llm.StreamFailure
		if errors.As(streamErr, &failure) && failure.Reason.Failure != nil {
			action, hookErr := prepared.RequestError(ctx, agentmiddleware.RequestError{
				Agent:       run.Agent,
				Turn:        turn,
				Step:        step,
				AttemptID:   attempt.id,
				Provider:    request.Config.Provider,
				Failure:     failure.Reason.Failure.Clone(),
				RetryPolicy: retryPolicy,
			})
			if hookErr != nil {
				return llm.Accumulated{}, false, fmt.Errorf("agent/request-error: %w", hookErr)
			}
			if action != nil && action.Kind == "retry" {
				continue
			}
		}

		return llm.Accumulated{}, false, fmt.Errorf("LLM step %d: %w", step, streamErr)
	}
}
