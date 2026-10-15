package agentmiddleware

import (
	"context"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// StepInput is the immutable candidate input passed through the
// agent/pre-step waterfall before a step is persisted or dispatched.
type StepInput struct {
	// Kind is enter or reject; rejected candidates never admit messages.
	Kind                string
	StartsRequestSeries bool
	Prompt              prompt.Snapshot
	Index               int
	AgentID             string
	SessionID           protocol.SessionID
	InvocationID        protocol.InvocationID
	Messages            []protocol.Message
	PromptContext       prompt.Context
}

func (i StepInput) Clone() StepInput {
	i.Prompt = i.Prompt.Clone()
	i.Messages = cloneAgentMessages(i.Messages)
	i.PromptContext.Tools = cloneToolSchemas(i.PromptContext.Tools)
	return i
}

type Request struct {
	Agent        contracts.AgentRuntime
	Turn         protocol.TurnID
	Step         protocol.StepID
	Index        int
	AgentID      string
	SessionID    protocol.SessionID
	InvocationID protocol.InvocationID
	Config       protocol.CallConfig
}

func (r Request) Clone() Request {
	r.Config = r.Config.Clone()
	return r
}

type TurnStop struct {
	Agent        contracts.AgentRuntime
	Turn         protocol.TurnID
	AgentID      string
	SessionID    protocol.SessionID
	InvocationID protocol.InvocationID
	Reason       protocol.CompletionReason
	Error        *protocol.ErrorDetail
}

func (s TurnStop) Clone() TurnStop {
	if s.Error != nil {
		detail := *s.Error
		s.Error = &detail
	}

	return s
}

// Middleware contains the behavior-changing Agent hooks. Tool execution
// middleware remains owned by the Tool Runtime so schema and dispatch use the
// same prepared registry revision.
type Middleware struct {
	ID      string
	Order   int
	PreStep func(context.Context, StepInput) (StepInput, error)
	Request func(context.Context, Request) (Request, error)
	// RequestError is a waterfall. nil means stop; only retry starts another
	// attempt in the same Step. Waiting/backoff belongs to the listener.
	RequestError func(context.Context, RequestError, *RequestErrorAction) (*RequestErrorAction, error)
	Stream       func(context.Context, contracts.ChunkStream) (contracts.ChunkStream, error)
	TurnStopping func(context.Context, TurnStop) error
}

type RequestErrorAction struct{ Kind string }

type RequestError struct {
	Agent       contracts.AgentRuntime
	Turn        protocol.TurnID
	Step        protocol.StepID
	AttemptID   string
	Provider    string
	Failure     protocol.LlmFailure
	RetryPolicy *llm.ResolvedRetryPolicy
}

func (e RequestError) Clone() RequestError {
	e.Failure = e.Failure.Clone()
	e.RetryPolicy = e.RetryPolicy.Clone()
	return e
}

func cloneAgentMessages(messages []protocol.Message) []protocol.Message {
	cloned := make([]protocol.Message, len(messages))

	for i := range messages {
		cloned[i] = messages[i].Clone()
	}

	return cloned
}

func cloneToolSchemas(schemas []tools.Schema) []tools.Schema {
	cloned := make([]tools.Schema, len(schemas))

	for i := range schemas {
		cloned[i] = schemas[i].Clone()
	}

	return cloned
}
