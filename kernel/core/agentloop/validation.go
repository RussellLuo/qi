package agentloop

import (
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func normalizeConfig(config Config) (Config, error) {
	if config.MaxSteps == 0 {
		config.MaxSteps = DefaultMaxSteps
	}

	if config.MaxSteps < 1 {
		return Config{}, errors.New("maxSteps must be positive")
	}
	if config.LLM.Provider == "" || config.LLM.Model == "" {
		return Config{}, errors.New("agent LLM config requires provider and model")
	}

	config.LLM = config.LLM.Clone()
	config.Selection = config.Selection.Clone()

	return config, nil
}

func validateStepInput(input agentmiddleware.StepInput, run Run, step int) error {
	if input.Kind != "enter" && input.Kind != "reject" {
		return errors.New("invalid pre-step decision")
	}
	if input.Index != step ||
		input.AgentID != run.Agent.SessionID().String() ||
		input.SessionID != run.Attached.Session.ID() ||
		input.InvocationID != run.InvocationID {
		return errors.New("agent/pre-step changed run identity")
	}
	if input.PromptContext.AgentID != run.Agent.SessionID().String() || input.PromptContext.SessionID != run.Attached.Session.ID() {
		return errors.New("agent/pre-step changed prompt identity")
	}

	for _, message := range input.Messages {
		if message.Role != protocol.RoleUser {
			return errors.New("agent/pre-step batch requires user messages")
		}
		if err := message.Validate(); err != nil {
			return fmt.Errorf("agent/pre-step returned invalid message: %w", err)
		}
	}

	return nil
}

func validateRequest(request agentmiddleware.Request, run Run, turn protocol.TurnID, step int) error {
	if request.Agent != run.Agent ||
		request.Turn != turn ||
		request.Step != protocol.StepID(step) ||
		request.Index != step ||
		request.AgentID != run.Agent.SessionID().String() ||
		request.SessionID != run.Attached.Session.ID() ||
		request.InvocationID != run.InvocationID {
		return errors.New("agent/request changed run identity")
	}
	if request.Config.Provider == "" || request.Config.Model == "" {
		return errors.New("agent/request returned an incomplete LLM config")
	}

	return nil
}

func messageByID(messages []protocol.Message, id protocol.MessageID) (protocol.Message, bool) {
	for _, message := range messages {
		if message.ID == id {
			return message.Clone(), true
		}
	}

	return protocol.Message{}, false
}
