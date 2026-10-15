package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/runprep"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// RequestState tracks requests for one Agent/attachment, across all its Turns.
// Its zero value is ready for use. Hosts must not share it across attachments;
// the Session's BeginRun admission serializes its use within an attachment.
// Discard it only after that attachment's activity has drained.
type RequestState struct {
	initialized bool
	logged      bool
	generation  int
}

func requestSnapshot(ctx context.Context, current session.Session) ([]protocol.SessionEvent, session.Projection, error) {
	view, err := session.ReadSnapshot(ctx, current)
	return view.SystemEvents, view.Projection, err
}

func prepareRequestState(ctx context.Context, current session.Session, state *RequestState) (*RequestState, error) {
	if state != nil && state.initialized {
		return state, nil
	}

	_, p, err := requestSnapshot(ctx, current)
	if err != nil {
		return nil, err
	}

	if state == nil {
		state = &RequestState{}
	}

	state.generation = len(p.Surface.Replacements)
	state.initialized = true

	return state, nil
}

func requestProposal(
	ctx context.Context,
	current session.Session,
	declared protocol.CallConfig,
	continuing bool,
) (protocol.CallConfig, error) {
	_, p, err := requestSnapshot(ctx, current)
	if err != nil {
		return protocol.CallConfig{}, err
	}

	proposal := declared.Clone()
	if p.RequestHeader == nil {
		return proposal, nil
	}

	h := p.RequestHeader
	if continuing {
		proposal = h.Config.Clone()
		// Re-resolve adapter-supplied defaults for the next bound call instead
		// of treating their previous values as explicit caller overrides.
		if h.AdapterDefaults != nil {
			if h.AdapterDefaults.ReasoningEffort {
				proposal.ReasoningEffort = nil
			}

			if h.AdapterDefaults.MaxTokens {
				proposal.MaxTokens = nil
			}
		}
	} else if proposal.ReasoningEffort == nil &&
		h.Config.Provider == proposal.Provider &&
		h.Config.Model == proposal.Model &&
		(h.AdapterDefaults == nil || !h.AdapterDefaults.ReasoningEffort) {
		proposal.ReasoningEffort = h.Config.Clone().ReasoningEffort
	}

	return proposal, nil
}

func schema(s tools.Schema) protocol.ToolSchema {
	return protocol.ToolSchema{
		Name:         s.Name,
		Description:  s.Description,
		Parameters:   append(json.RawMessage(nil), s.Parameters...),
		DeferLoading: s.DeferLoading,
	}
}

func toolSchema(s protocol.ToolSchema) tools.Schema {
	return tools.Schema{
		Name:         s.Name,
		Description:  s.Description,
		Parameters:   tools.JSONSchema(append(json.RawMessage(nil), s.Parameters...)),
		DeferLoading: s.DeferLoading,
	}
}

func sameJSON(a, b any) bool {
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	if e1 != nil || e2 != nil {
		return false
	}

	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}

	return reflect.DeepEqual(l, r)
}

func admitRequest(
	ctx context.Context,
	current session.Session,
	prepared *runprep.PreparedStep,
	state *RequestState,
	run Run,
	turn protocol.TurnID,
	step protocol.StepID,
	prompt string,
	batch []protocol.Message,
	startsSeries bool,
) (llm.Request, error) {
	if err := ctx.Err(); err != nil {
		return llm.Request{}, err
	}

	events, p, err := requestSnapshot(ctx, current)
	if err != nil {
		return llm.Request{}, err
	}

	call := prepared.LLM()
	header := protocol.EpochHeader{Config: call.Config(), AdapterDefaults: call.AdapterDefaults()}

	for _, t := range prepared.ToolSchemas() {
		header.Tools = append(header.Tools, schema(t))
	}

	// A replacement starts a fresh request baseline. Models without tool-update
	// support also need consolidation when their visible tool set changes.
	toolsChanged := p.RequestHeader != nil && !sameJSON(p.RequestHeader.Tools, header.Tools)
	consolidate := startsSeries ||
		state.generation != len(p.Surface.Replacements) ||
		(!call.Model().ToolUpdate && toolsChanged)
	if err := projectSystem(
		ctx,
		current,
		events,
		run,
		turn,
		step,
		prompt,
		call.Model().SystemPromptUpdate == "in-history",
		consolidate,
	); err != nil {
		return llm.Request{}, err
	}

	for _, message := range batch {
		if message.Role != protocol.RoleUser {
			return llm.Request{}, errors.New("pre-step batch must contain user messages")
		}
		if _, err := current.Append(ctx, protocol.NewMessageEvent(protocol.UserMessage, turn, step, message)); err != nil {
			return llm.Request{}, err
		}
	}

	_, p, err = requestSnapshot(ctx, current)
	if err != nil {
		return llm.Request{}, err
	}

	// Refresh after committing system/user facts: their Surface operations may
	// have changed the generation that selects the next header reason.
	startsSeries = startsSeries || state.generation != len(p.Surface.Replacements)
	baseline := p.RequestHeader
	reason := ""
	if !state.logged {
		if baseline == nil {
			reason = "initial"
		} else {
			reason = "resume"
		}
	} else if baseline == nil || !protocol.HeaderEquals(*baseline, header) {
		reason = "change"
	} else if startsSeries {
		reason = "series"
	}

	var headerSeq *protocol.Sequence
	if reason != "" {
		seq, err := current.Append(ctx, protocol.NewSessionEvent(protocol.RequestHeader, protocol.RequestHeaderData{
			Header:       header,
			Reason:       reason,
			StartsSeries: startsSeries && reason != "series",
		}))
		if err != nil {
			return llm.Request{}, err
		}

		headerSeq = &seq
		state.logged = true
	}

	// Tool additions refer to the committed header that defines their schemas;
	// removals need only names. A series boundary alone does not change tools.
	if baseline != nil && headerSeq != nil && reason != "series" {
		previous, active := map[string]bool{}, map[string]bool{}

		for _, t := range baseline.Tools {
			previous[t.Name] = true
		}

		for _, t := range header.Tools {
			active[t.Name] = true
		}

		content := []protocol.ContentBlock{}
		additions := false

		for _, t := range header.Tools {
			if !previous[t.Name] {
				content = append(content, protocol.ToolAdditionBlock{ToolName: t.Name})
				additions = true
			}
		}

		for _, t := range baseline.Tools {
			if !active[t.Name] {
				content = append(content, protocol.ToolRemovalBlock{ToolName: t.Name})
			}
		}

		if len(content) > 0 {
			message := protocol.Message{
				ID:      protocol.MessageID(fmt.Sprintf("%s/tools-%d", run.InvocationID, step)),
				Role:    protocol.RoleDeveloper,
				Source:  protocol.MessageSource{Kind: "tool-registry"},
				Content: content,
			}
			data := protocol.MessageData{
				Turn:    turn,
				Step:    step,
				Message: message,
			}
			if additions {
				data.HeaderSeq = headerSeq
			}

			event := protocol.NewSessionEvent(protocol.DeveloperMessage, data)
			event.SurfaceOp = protocol.AppendSurface()
			if _, err := current.Append(ctx, event); err != nil {
				return llm.Request{}, err
			}
		}
	}

	state.generation = len(p.Surface.Replacements)
	requestContext := call.Context()
	if p.RequestContext == nil || !sameJSON(*p.RequestContext, requestContext) {
		if _, err := current.Append(ctx, protocol.NewSessionEvent(protocol.RequestContextEvent, requestContext)); err != nil {
			return llm.Request{}, err
		}
	}

	if err := ctx.Err(); err != nil {
		return llm.Request{}, err
	}

	_, p, err = requestSnapshot(ctx, current)
	if err != nil {
		return llm.Request{}, err
	}
	// Build exclusively from committed facts and the bound adapter's context.
	request := llm.Request{
		Config:      p.RequestHeader.Config.Clone(),
		Messages:    p.Messages,
		SessionID:   current.ID(),
		ToolHistory: llm.ToolHistory{Tools: []tools.Schema{}, Updates: []llm.ToolHistoryUpdate{}},
	}

	for _, t := range p.RequestHeader.Tools {
		request.Tools = append(request.Tools, toolSchema(t))
	}

	for _, t := range p.ToolHistory.Tools {
		request.ToolHistory.Tools = append(request.ToolHistory.Tools, toolSchema(t))
	}

	for _, u := range p.ToolHistory.Updates {
		update := llm.ToolHistoryUpdate{MessageID: u.MessageID, Additions: []tools.Schema{}}

		for _, t := range u.Additions {
			update.Additions = append(update.Additions, toolSchema(t))
		}

		request.ToolHistory.Updates = append(request.ToolHistory.Updates, update)
	}

	if err := prepared.FreezeRequest(request); err != nil {
		return llm.Request{}, err
	}

	return prepared.Request()
}

func projectSystem(
	ctx context.Context,
	current session.Session,
	events []protocol.SessionEvent,
	run Run,
	turn protocol.TurnID,
	step protocol.StepID,
	rendered string,
	inHistory, startsSeries bool,
) error {
	type node struct {
		seq  protocol.Sequence
		text string
	}
	nodes := []node{}

	for _, e := range events {
		seq := e.Seq
		if e.Type != protocol.SystemMessage {
			continue
		}

		message := protocol.EventPayload[protocol.MessageData](e).Message
		text := ""

		for _, block := range message.Content {
			if b, ok := block.(protocol.TextBlock); ok {
				text += b.Text
			}
		}

		nodes = append(nodes, node{seq, text})
	}

	commit := func(text string, replace *protocol.Sequence) error {
		message := protocol.NewTextMessage(
			protocol.MessageID(fmt.Sprintf("%s/system-%d", run.InvocationID, current.Watermark()+1)),
			protocol.RoleSystem,
			protocol.SourceSystem,
			text,
		)
		event := protocol.NewMessageEvent(protocol.SystemMessage, turn, step, message)
		if replace != nil {
			event.SurfaceOp = &protocol.SurfaceOp{
				Op:       "replace",
				StartSeq: *replace,
				EndSeq:   *replace,
			}
			event.SourceEventSeqs = []protocol.Sequence{*replace}
		}

		_, err := current.Append(ctx, event)

		return err
	}
	if len(nodes) == 0 {
		return commit(rendered, nil)
	}

	// Collapse into the first system message at a new baseline. Later system
	// messages remain durable facts but are replaced with empty Surface text.
	if !inHistory || startsSeries || rendered == "" {
		for _, n := range nodes[1:] {
			if n.text != "" {
				seq := n.seq
				if err := commit("", &seq); err != nil {
					return err
				}
			}
		}

		if nodes[0].text != rendered {
			seq := nodes[0].seq
			return commit(rendered, &seq)
		}

		return nil
	}

	// Within an in-history series, append only a changed prompt so prior
	// system updates remain visible to the model.
	latest := nodes[0].text

	for _, n := range nodes {
		if n.text != "" {
			latest = n.text
		}
	}

	if latest != rendered {
		return commit(rendered, nil)
	}

	return nil
}

func (l *Loop) blocked(
	ctx context.Context,
	prepared *runprep.PreparedRun,
	appendEvent func(context.Context, protocol.SessionEvent) error,
	emitter *eventEmitter,
	run Run,
	turn protocol.TurnID,
) error {
	hookErr := prepared.TurnStopping(run.activityContext, agentmiddleware.TurnStop{
		Agent:        run.Agent,
		Turn:         turn,
		AgentID:      run.Agent.SessionID().String(),
		SessionID:    run.Attached.Session.ID(),
		InvocationID: run.InvocationID,
		Reason:       protocol.ReasonBlocked,
	})
	hookErr = errors.Join(context.Cause(run.activityContext), hookErr)
	if hookErr != nil {
		return l.settle(ctx, nil, appendEvent, emitter, run, turn, 0, hookErr, protocol.ErrorInternal)
	}
	if err := appendEvent(ctx, protocol.NewTurnEnded(turn, protocol.TurnEndReason{Kind: "blocked"})); err != nil {
		return errors.Join(err, emitter.Err())
	}

	emitter.Emit(contracts.DriverEvent{Type: "ended", Reason: protocol.TurnEndReason{Kind: "blocked"}})

	return emitter.Err()
}
