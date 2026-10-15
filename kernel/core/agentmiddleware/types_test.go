package agentmiddleware

import (
	"testing"

	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func TestStepInputClone(t *testing.T) {
	input := StepInput{
		Kind: "enter", AgentID: "agent", SessionID: "session", InvocationID: "invocation",
		Prompt:        prompt.Snapshot{SectionIDs: []string{"section"}, Diagnostics: []prompt.Diagnostic{{Bytes: 5}}},
		Messages:      []protocol.Message{protocol.NewTextMessage("message", protocol.RoleUser, protocol.SourceUser, "hello")},
		PromptContext: prompt.Context{Tools: []tools.Schema{{Name: "echo", Parameters: tools.JSONSchema(`{}`)}}},
	}
	cloned := input.Clone()
	cloned.Prompt.SectionIDs[0] = "changed"
	cloned.Prompt.Diagnostics[0].Bytes = 0
	cloned.Messages[0].Content[0] = protocol.TextBlock{Text: "changed"}
	cloned.PromptContext.Tools[0].Parameters[0] = '['
	if input.Prompt.SectionIDs[0] != "section" || input.Prompt.Diagnostics[0].Bytes != 5 || input.Messages[0].Content[0].(protocol.TextBlock).Text != "hello" || input.PromptContext.Tools[0].Parameters[0] != '{' {
		t.Fatal("step clone shares mutable data")
	}
	if cloned.Kind != input.Kind || cloned.AgentID != input.AgentID || cloned.SessionID != input.SessionID || cloned.InvocationID != input.InvocationID {
		t.Fatal("step clone lost attribution")
	}
	for _, input := range []StepInput{{}, {Messages: []protocol.Message{}, PromptContext: prompt.Context{Tools: []tools.Schema{}}}} {
		cloned := input.Clone()
		if cloned.Messages == nil || cloned.PromptContext.Tools == nil {
			t.Fatal("step clone must allocate empty message and schema lists")
		}
	}
}

func TestRequestStopAndErrorClone(t *testing.T) {
	request := Request{Config: protocol.CallConfig{Stop: []string{"end"}}}
	clonedRequest := request.Clone()
	clonedRequest.Config.Stop[0] = "changed"
	if request.Config.Stop[0] != "end" {
		t.Fatal("request clone shares config")
	}
	stop := TurnStop{Error: &protocol.ErrorDetail{Message: "failed"}}
	clonedStop := stop.Clone()
	clonedStop.Error.Message = "changed"
	if stop.Error.Message != "failed" || (TurnStop{}).Clone().Error != nil {
		t.Fatal("stop clone changed error")
	}
	status, retries := 429, int64(2)
	failure := RequestError{Failure: protocol.LlmFailure{Status: &status}, RetryPolicy: &llm.ResolvedRetryPolicy{MaxRetries: &retries, RetryableCodes: []string{"RATE_LIMIT"}}}
	clonedFailure := failure.Clone()
	*clonedFailure.Failure.Status = 500
	*clonedFailure.RetryPolicy.MaxRetries = 3
	clonedFailure.RetryPolicy.RetryableCodes[0] = "changed"
	if status != 429 || retries != 2 || failure.RetryPolicy.RetryableCodes[0] != "RATE_LIMIT" || (RequestError{}).Clone().RetryPolicy != nil {
		t.Fatal("request error clone shares failure or retry policy")
	}
}
