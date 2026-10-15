package agentloop

import (
	"context"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
)

// prepareStep claims once and applies Prompt/PreStep before a Step is admitted.
func prepareStep(ctx context.Context, prepared *runprep.PreparedRun, run Run, turnID protocol.TurnID, stepIndex int) (agentmiddleware.StepInput, protocol.ErrorCode, error) {
	target := contracts.NextStep
	if stepIndex == 1 {
		target = contracts.NextTurn
	}
	messages, err := run.Attached.Inbox.Claim(ctx, target, turnID)
	if err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorInternal, err
	}

	promptContext := prompt.Context{
		AgentID:   run.Agent.SessionID().String(),
		SessionID: run.Attached.Session.ID(),
		Tools:     prepared.ToolSchemas(),
	}
	promptSnapshot, err := prepared.AssemblePrompt(ctx, promptContext)
	if err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorInternal, err
	}
	if err := ctx.Err(); err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorCancelled, err
	}

	stepInput, err := prepared.PreStep(ctx, agentmiddleware.StepInput{
		Kind:          "enter",
		Prompt:        promptSnapshot,
		Index:         stepIndex,
		AgentID:       run.Agent.SessionID().String(),
		SessionID:     run.Attached.Session.ID(),
		InvocationID:  run.InvocationID,
		Messages:      messages,
		PromptContext: promptContext,
	})
	if err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorInternal, fmt.Errorf("agent/pre-step: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorCancelled, err
	}
	if stepInput.Kind == "reject" {
		return stepInput, "", nil
	}
	if err := validateStepInput(stepInput, run, stepIndex); err != nil {
		return agentmiddleware.StepInput{}, protocol.ErrorInternal, err
	}

	return stepInput, "", nil
}
