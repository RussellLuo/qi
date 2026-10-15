package agentloop

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// executeTools records every committed proposal and drains started work before
// returning. The bool distinguishes a storage refusal from a settled tool error.
func executeTools(ctx, settleCtx context.Context, current session.Session, prepared *runprep.PreparedRun, run Run, turnID protocol.TurnID, stepID protocol.StepID, calls []protocol.ToolCall) (concluded, uncommitted bool, err error) {
	concluded = len(calls) == 0

	for callIndex, call := range calls {
		callSeq, err := current.Append(settleCtx, protocol.NewToolCallEvent(turnID, stepID, call))
		if err != nil {
			return false, true, err
		}

		// Cancellation stops dispatch, but every committed proposal still gets
		// an ordered call/result pair. Started work returns only after drain.
		executionResult := tools.AbortedBeforeDispatchResult()
		var executeErr error
		if ctx.Err() == nil {
			executionContext := tools.ExecutionContext{Identity: tools.ExecutionIdentity{
				AgentID:   run.Agent.SessionID().String(),
				SessionID: run.Attached.Session.ID(),
				Turn:      turnID,
				Step:      stepID,
			}}
			if cwd := current.Header().CWD; cwd != nil {
				executionContext.CWD = *cwd
			}
			executionResult, executeErr = executeTool(ctx, prepared, call, executionContext)
		}

		resultMessageID := protocol.MessageID(fmt.Sprintf("%s/tool-result-%d-%d", run.InvocationID, stepID, callIndex+1))
		result := protocol.ToolResult{
			Message: protocol.NewToolResultMessage(resultMessageID, call.ID, executionResult.Content, executionResult.IsError),
			Meta:    executionResult.Meta,
		}
		if executionResult.Error != nil {
			result.Error = executionResult.Error.Info
		}

		if err := appendSessionEvent(settleCtx, current, protocol.NewToolResultEvent(turnID, stepID, result, callSeq)); err != nil {
			return false, true, err
		}
		if err := run.Attached.Inbox.EnqueueContexts(settleCtx, executionResult.AdditionalContexts); err != nil {
			return false, true, err
		}

		concluded = concluded || (!executionResult.IsError && executionResult.ConcludesTurn)
		if executeErr != nil && ctx.Err() == nil {
			return false, false, fmt.Errorf("tool %q: %w", call.Name, executeErr)
		}
	}

	return concluded, false, nil
}

func appendSessionEvent(ctx context.Context, current session.Session, event protocol.SessionEvent) error {
	_, err := current.Append(ctx, event)
	return err
}

func toolCalls(message protocol.Message) []protocol.ToolCall {
	var calls []protocol.ToolCall

	for _, block := range message.Content {
		if value, ok := block.(protocol.ToolCallBlock); ok {
			calls = append(calls, value.ToolCall.Clone())
		}
	}

	return calls
}

func executeTool(
	ctx context.Context,
	prepared *runprep.PreparedRun,
	call protocol.ToolCall,
	executionContext tools.ExecutionContext,
) (tools.ExecutionResult, error) {
	preparedCall, err := prepared.PrepareToolWithContext(call, executionContext)
	if err != nil {
		if errors.Is(err, tools.ErrNotFound) {
			return tools.FailureResult("not_found", fmt.Errorf("tool %q is not available", call.Name)), nil
		}

		return tools.FailureResult("prepare_failed", err), err
	}

	defer preparedCall.Close()

	result, err := preparedCall.Execute(ctx)
	if err != nil && result.Content == nil {
		return tools.FailureResult("execution_failed", err), err
	}

	return result, err
}
