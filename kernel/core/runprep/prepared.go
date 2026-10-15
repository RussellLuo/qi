// Package runprep leases contributions for a run and binds individual LLM calls
// to that run's snapshots. Closing a run releases its calls before its snapshots.
package runprep

import (
	"context"
	"errors"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type Coordinator struct {
	Prompts    *prompt.Runtime
	Tools      *tools.Runtime
	LLM        *llm.Runtime
	Middleware *agentmiddleware.Runtime
}

// Selection limits run contributions. Nil slices select all entries; non-nil
// empty slices deliberately select none.
type Selection struct {
	Tools           []string
	PromptSections  []string
	AgentMiddleware []string
}

func (s Selection) Clone() Selection {
	return Selection{
		Tools:           cloneSelection(s.Tools),
		PromptSections:  cloneSelection(s.PromptSections),
		AgentMiddleware: cloneSelection(s.AgentMiddleware),
	}
}

// PrepareRun acquires each runtime's snapshot in order. If a later acquisition
// fails, earlier snapshots are released in reverse order before returning.
func (c Coordinator) PrepareRun(_ context.Context, selections ...Selection) (*PreparedRun, error) {
	if c.Prompts == nil || c.Tools == nil || c.LLM == nil || c.Middleware == nil {
		return nil, errors.New("prepared run requires Prompt, Tool, LLM, and Agent Middleware runtimes")
	}

	selection := Selection{}
	if len(selections) != 0 {
		selection = selections[0].Clone()
	}

	prompts, err := c.Prompts.PrepareSelected(selection.PromptSections)
	if err != nil {
		return nil, err
	}

	toolSnapshot, err := c.Tools.PrepareSelected(selection.Tools)
	if err != nil {
		prompts.Close()
		return nil, err
	}

	llmSnapshot, err := c.LLM.Prepare()
	if err != nil {
		toolSnapshot.Close()
		prompts.Close()
		return nil, err
	}

	middleware, err := c.Middleware.PrepareSelected(selection.AgentMiddleware)
	if err != nil {
		llmSnapshot.Close()
		toolSnapshot.Close()
		prompts.Close()
		return nil, err
	}

	return &PreparedRun{
		prompts:    prompts,
		tools:      toolSnapshot,
		llm:        llmSnapshot,
		middleware: middleware,
		steps:      make(map[*PreparedStep]struct{}),
	}, nil
}

func cloneSelection(values []string) []string {
	if values == nil {
		return nil
	}

	return append([]string{}, values...)
}

type Revisions struct {
	Prompt     uint64
	Tool       uint64
	Middleware uint64
	LLM        uint64
}

// PreparedRun retains contribution leases across steps, so registration changes
// affect future runs rather than replacing this run's selected implementations.
type PreparedRun struct {
	mu         sync.Mutex
	closed     bool
	prompts    *prompt.Prepared
	tools      *tools.Snapshot
	llm        *llm.Snapshot
	middleware *agentmiddleware.Snapshot
	steps      map[*PreparedStep]struct{}
}

func (r *PreparedRun) Revisions() Revisions {
	if r == nil {
		return Revisions{}
	}

	return Revisions{
		Prompt:     r.prompts.Revision(),
		Tool:       r.tools.Revision(),
		Middleware: r.middleware.Revision(),
		LLM:        r.llm.Revision(),
	}
}

func (r *PreparedRun) PreStep(ctx context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
	if r == nil || r.isClosed() {
		return agentmiddleware.StepInput{}, contracts.ErrClosed
	}

	return r.middleware.PreStep(ctx, input)
}

func (r *PreparedRun) Request(ctx context.Context, request agentmiddleware.Request) (agentmiddleware.Request, error) {
	if r == nil || r.isClosed() {
		return agentmiddleware.Request{}, contracts.ErrClosed
	}

	return r.middleware.Request(ctx, request)
}

func (r *PreparedRun) Stream(ctx context.Context, stream contracts.ChunkStream) (contracts.ChunkStream, error) {
	if r == nil || r.isClosed() {
		return nil, contracts.ErrClosed
	}

	return r.middleware.Stream(ctx, stream)
}

func (r *PreparedRun) TurnStopping(ctx context.Context, stop agentmiddleware.TurnStop) error {
	if r == nil || r.isClosed() {
		return contracts.ErrClosed
	}

	return r.middleware.TurnStopping(ctx, stop)
}

func (r *PreparedRun) RequestError(ctx context.Context, event agentmiddleware.RequestError) (*agentmiddleware.RequestErrorAction, error) {
	if r == nil || r.isClosed() {
		return nil, contracts.ErrClosed
	}

	return r.middleware.RequestError(ctx, event)
}

func (r *PreparedRun) ToolSchemas() []tools.Schema {
	if r == nil || r.isClosed() {
		return nil
	}

	return r.tools.Schemas()
}

func (r *PreparedRun) PrepareTool(call protocol.ToolCall) (*tools.PreparedCall, error) {
	return r.PrepareToolWithIdentity(call, tools.ExecutionIdentity{})
}

// PrepareToolWithIdentity binds execution attribution to one Tool call while
// retaining the run's frozen contributions and their existing lease boundary.
func (r *PreparedRun) PrepareToolWithIdentity(
	call protocol.ToolCall,
	identity tools.ExecutionIdentity,
) (*tools.PreparedCall, error) {
	return r.PrepareToolWithContext(call, tools.ExecutionContext{Identity: identity})
}

// PrepareToolWithContext carries Session cwd through the run's frozen tools.
func (r *PreparedRun) PrepareToolWithContext(call protocol.ToolCall, executionContext tools.ExecutionContext) (*tools.PreparedCall, error) {
	if r == nil || r.isClosed() {
		return nil, contracts.ErrClosed
	}

	return r.tools.PrepareWithContext(call, executionContext)
}

// AssemblePrompt precedes pre-step; resolving the adapter follows the request hook.
func (r *PreparedRun) AssemblePrompt(ctx context.Context, promptContext prompt.Context) (prompt.Snapshot, error) {
	if r == nil || r.isClosed() {
		return prompt.Snapshot{}, contracts.ErrClosed
	}

	promptContext.Tools = r.tools.Schemas()
	return r.prompts.Assemble(ctx, promptContext)
}

func (r *PreparedRun) PrepareStep(
	ctx context.Context,
	promptContext prompt.Context,
	messages []protocol.Message,
	config protocol.CallConfig,
) (*PreparedStep, error) {
	snapshot, err := r.AssemblePrompt(ctx, promptContext)
	if err != nil {
		return nil, err
	}

	return r.PrepareCall(ctx, snapshot, messages, config)
}

func (r *PreparedRun) PrepareCall(
	ctx context.Context,
	promptSnapshot prompt.Snapshot,
	messages []protocol.Message,
	config protocol.CallConfig,
) (*PreparedStep, error) {
	if r == nil {
		return nil, contracts.ErrClosed
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, contracts.ErrClosed
	}

	call, err := r.llm.PrepareCall(ctx, config)
	if err != nil {
		return nil, err
	}

	step := &PreparedStep{
		run:      r,
		prompt:   promptSnapshot.Clone(),
		messages: cloneMessages(messages),
		llm:      call,
	}
	step.request = llm.Request{
		Config:   call.Config(),
		Messages: cloneMessages(messages),
		Tools:    r.tools.Schemas(),
	}
	r.steps[step] = struct{}{}

	return step, nil
}

func (r *PreparedRun) Close() {
	if r == nil {
		return
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}

	r.closed = true
	steps := make([]*PreparedStep, 0, len(r.steps))

	for step := range r.steps {
		steps = append(steps, step)
	}

	r.mu.Unlock()

	for i := len(steps) - 1; i >= 0; i-- {
		steps[i].Close()
	}

	r.middleware.Close()
	r.llm.Close()
	r.tools.Close()
	r.prompts.Close()
}

func (r *PreparedRun) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.closed
}

// PreparedStep owns a resolved LLM call and detached prompt/request state.
// It is also tracked by its run, which closes any steps still outstanding.
type PreparedStep struct {
	mu       sync.Mutex
	closed   bool
	run      *PreparedRun
	prompt   prompt.Snapshot
	messages []protocol.Message
	llm      *llm.PreparedCall
	request  llm.Request
}

func (s *PreparedStep) Prompt() prompt.Snapshot {
	if s == nil {
		return prompt.Snapshot{}
	}

	return s.prompt.Clone()
}

func (s *PreparedStep) Messages() []protocol.Message {
	if s == nil {
		return nil
	}

	return cloneMessages(s.messages)
}

func (s *PreparedStep) LLM() *llm.PreparedCall {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	return s.llm
}

func (s *PreparedStep) ToolSchemas() []tools.Schema {
	if s == nil || s.run == nil {
		return nil
	}

	return s.run.ToolSchemas()
}

func (s *PreparedStep) Request() (llm.Request, error) {
	if s == nil {
		return llm.Request{}, contracts.ErrClosed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.llm == nil {
		return llm.Request{}, contracts.ErrClosed
	}

	return s.request.Clone(), nil
}

// FreezeRequest copies admitted Surface state; adapters cannot mutate the stored snapshot.
func (s *PreparedStep) FreezeRequest(request llm.Request) error {
	if s == nil {
		return contracts.ErrClosed
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.llm == nil {
		return contracts.ErrClosed
	}

	s.request = request.Clone()
	s.messages = cloneMessages(request.Messages)

	return nil
}

func (s *PreparedStep) Close() {
	if s == nil {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	call := s.llm
	s.llm = nil
	run := s.run
	s.mu.Unlock()
	if call != nil {
		call.Close()
	}

	if run != nil {
		run.mu.Lock()
		delete(run.steps, s)
		run.mu.Unlock()
	}
}

func cloneMessages(messages []protocol.Message) []protocol.Message {
	cloned := make([]protocol.Message, len(messages))

	for i := range messages {
		cloned[i] = messages[i].Clone()
	}

	return cloned
}
