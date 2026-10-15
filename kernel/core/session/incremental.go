package session

import (
	"fmt"
	"reflect"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// projectionState belongs to one Session incarnation and is used only under
// its mutex. Fold remains an independent, pure reference for durable recovery.
// Messages align with Surface.Nodes; nil represents an empty message rewrite.
type projectionState struct {
	p         Projection
	messages  []*protocol.Message
	declared  map[string]protocol.ToolSchema
	available map[string]bool
	active    []protocol.ToolSchema
	baseline  bool
}

func newProjectionState(events []protocol.SessionEvent) (*projectionState, error) {
	s := &projectionState{
		p: Projection{
			Surface:     Surface{Nodes: []protocol.Sequence{}, Replacements: []SurfaceReplacement{}},
			ToolHistory: ToolHistory{Tools: []protocol.ToolSchema{}, Updates: []ToolHistoryUpdate{}},
			Inbox:       map[string][]protocol.Message{"next-turn": {}, "next-step": {}},
		},
		declared:  map[string]protocol.ToolSchema{},
		available: map[string]bool{},
		active:    []protocol.ToolSchema{},
	}
	for n, event := range events {
		publish, err := s.prepare(events[:n], event)
		if err != nil {
			return nil, err
		}
		publish()
	}
	return s, nil
}

// prepare checks the candidate against committed state without modifying it.
// The returned mutation is infallible and may run only after durable commit.
// Ordinary append never copies or scans the old log. Replacement may scan the
// current Surface, and Inbox splice checks only the pending queues.
func (s *projectionState) prepare(events []protocol.SessionEvent, e protocol.SessionEvent) (func(), error) {
	if e.Seq != protocol.Sequence(len(events)) {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrInvalidSequence, e.Seq, len(events))
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	update := func() {}
	if !e.IsKnown() {
		return update, nil
	}

	switch e.Type {
	case protocol.RequestHeader:
		d := protocol.EventPayload[protocol.RequestHeaderData](e)
		h := d.Header
		active := h.Tools
		if active == nil {
			active = []protocol.ToolSchema{}
		}
		redeclared := false
		for _, tool := range active {
			if old, ok := s.declared[tool.Name]; ok && !jsonEqual(old, tool) {
				redeclared = true
			}
		}
		reset := !s.baseline || d.Reason == "series" || d.StartsSeries || redeclared
		update = func() {
			s.p.RequestHeader = &h
			s.active = active
			if reset {
				s.baseline = true
				s.declared = map[string]protocol.ToolSchema{}
				s.available = map[string]bool{}
				for _, tool := range active {
					s.declared[tool.Name] = tool
					s.available[tool.Name] = true
				}
				s.p.ToolHistory = ToolHistory{Tools: active, Updates: []ToolHistoryUpdate{}}
			}
		}
	case protocol.RequestContextEvent:
		d := protocol.EventPayload[protocol.RequestContext](e)
		update = func() { s.p.RequestContext = &d }
	case protocol.DeveloperMessage:
		d := protocol.EventPayload[protocol.MessageData](e)
		additions := []protocol.ToolSchema{}
		removals := []string{}
		if d.HeaderSeq != nil {
			ref := events[*d.HeaderSeq]
			if ref.Type != protocol.RequestHeader {
				return nil, fmt.Errorf("%w: headerSeq is not request/header", protocol.ErrInvalidProtocol)
			}
			definitions := protocol.EventPayload[protocol.RequestHeaderData](ref).Header.Tools
			for _, block := range d.Message.Content {
				if add, ok := block.(protocol.ToolAdditionBlock); ok {
					matches := []protocol.ToolSchema{}
					for _, tool := range definitions {
						if tool.Name == add.ToolName {
							matches = append(matches, tool)
						}
					}
					if len(matches) != 1 {
						return nil, fmt.Errorf("%w: tool addition has no unique historical definition", protocol.ErrInvalidProtocol)
					}
					additions = append(additions, matches[0])
				}
			}
		}
		for _, block := range d.Message.Content {
			if remove, ok := block.(protocol.ToolRemovalBlock); ok {
				removals = append(removals, remove.ToolName)
			}
		}
		update = func() {
			for _, tool := range additions {
				s.declared[tool.Name] = tool
				s.available[tool.Name] = true
			}
			for _, name := range removals {
				delete(s.available, name)
			}
			s.p.ToolHistory.Updates = append(s.p.ToolHistory.Updates, ToolHistoryUpdate{d.Message.ID, additions})
		}
	case protocol.InboxSpliced:
		d := protocol.EventPayload[protocol.InboxSpliceData](e)
		queue := s.p.Inbox[d.Target]
		count := int64(0)
		if d.RemovedCount != nil {
			count = *d.RemovedCount
		}
		if d.Start > int64(len(queue)) || count > int64(len(queue))-d.Start {
			return nil, fmt.Errorf("%w: inbox span out of range", protocol.ErrInvalidProtocol)
		}
		next := append([]protocol.Message{}, queue[:d.Start]...)
		next = append(next, d.Inserted...)
		next = append(next, queue[d.Start+count:]...)
		ids := map[protocol.MessageID]bool{}
		for target, messages := range s.p.Inbox {
			if target == d.Target {
				messages = next
			}
			for _, message := range messages {
				if ids[message.ID] {
					return nil, fmt.Errorf("%w: duplicate inbox message id", protocol.ErrInvalidProtocol)
				}
				ids[message.ID] = true
			}
		}
		update = func() { s.p.Inbox[d.Target] = next }
	}

	if !e.IsSurface() {
		return update, nil
	}
	message := DeriveEventMessage(e)
	if e.SurfaceOp.Op == "append" {
		return func() {
			update()
			s.p.Surface.Nodes = append(s.p.Surface.Nodes, e.Seq)
			s.messages = append(s.messages, message)
		}, nil
	}

	start, end := -1, -1
	for n, seq := range s.p.Surface.Nodes {
		if seq == e.SurfaceOp.StartSeq {
			start = n
		}
		if seq == e.SurfaceOp.EndSeq {
			end = n
		}
	}
	if start < 0 || end < start {
		return nil, fmt.Errorf("%w: replace range is not in current surface", protocol.ErrInvalidProtocol)
	}
	shadowed := append([]protocol.Sequence{}, s.p.Surface.Nodes[start:end+1]...)
	refs := map[protocol.Sequence]bool{}
	for _, seq := range e.SourceEventSeqs {
		refs[seq] = true
	}
	for _, seq := range shadowed {
		if !refs[seq] {
			return nil, fmt.Errorf("%w: missing shadowed source node %d", protocol.ErrInvalidProtocol, seq)
		}
	}
	if start == 0 && events[s.p.Surface.Nodes[0]].Type == protocol.SystemMessage &&
		(e.Type != protocol.SystemMessage || len(shadowed) != 1) {
		return nil, fmt.Errorf("%w: system head must be rewritten alone as system/message", protocol.ErrInvalidProtocol)
	}
	if e.Type == protocol.ToolResulted {
		if len(shadowed) != 1 || events[shadowed[0]].Type != protocol.ToolResulted {
			return nil, fmt.Errorf("%w: tool result rewrite must target one tool result", protocol.ErrInvalidProtocol)
		}
		if !reflect.DeepEqual(toolResultIdentity(events[shadowed[0]].Data), toolResultIdentity(e.Data)) {
			return nil, fmt.Errorf("%w: tool result rewrite may change only content", protocol.ErrInvalidProtocol)
		}
	}
	return func() {
		update()
		nodes := append([]protocol.Sequence{}, s.p.Surface.Nodes[:start]...)
		nodes = append(nodes, e.Seq)
		nodes = append(nodes, s.p.Surface.Nodes[end+1:]...)
		messages := append([]*protocol.Message{}, s.messages[:start]...)
		messages = append(messages, message)
		messages = append(messages, s.messages[end+1:]...)
		s.p.Surface.Nodes = nodes
		s.messages = messages
		s.p.Surface.Replacements = append(s.p.Surface.Replacements,
			SurfaceReplacement{e.Seq, e.SurfaceOp.StartSeq, e.SurfaceOp.EndSeq, shadowed})
	}, nil
}

func cloneMessages(messages []protocol.Message) []protocol.Message {
	cloned := make([]protocol.Message, len(messages))
	for n, message := range messages {
		cloned[n] = message.Clone()
	}
	return cloned
}

func cloneInbox(inbox map[string][]protocol.Message) map[string][]protocol.Message {
	cloned := make(map[string][]protocol.Message, len(inbox))
	for target, messages := range inbox {
		cloned[target] = cloneMessages(messages)
	}
	return cloned
}

func (s *projectionState) messageSnapshot() []protocol.Message {
	messages := make([]protocol.Message, 0, len(s.messages))
	for _, message := range s.messages {
		if message != nil {
			messages = append(messages, message.Clone())
		}
	}
	return messages
}

func cloneSchemas(tools []protocol.ToolSchema) []protocol.ToolSchema {
	if tools == nil {
		return nil
	}
	cloned := make([]protocol.ToolSchema, len(tools))
	for n, tool := range tools {
		tool.Parameters = append([]byte(nil), tool.Parameters...)
		cloned[n] = tool
	}
	return cloned
}

func (s *projectionState) snapshot() Projection {
	p := s.p
	p.Messages = s.messageSnapshot()
	p.Surface.Nodes = append([]protocol.Sequence{}, p.Surface.Nodes...)
	p.Surface.Replacements = make([]SurfaceReplacement, len(s.p.Surface.Replacements))
	for n, replacement := range s.p.Surface.Replacements {
		replacement.ShadowedSeqs = append([]protocol.Sequence{}, replacement.ShadowedSeqs...)
		p.Surface.Replacements[n] = replacement
	}
	if p.RequestHeader != nil {
		h := *p.RequestHeader
		h.Config = h.Config.Clone()
		h.Tools = cloneSchemas(h.Tools)
		if h.AdapterDefaults != nil {
			defaults := *h.AdapterDefaults
			h.AdapterDefaults = &defaults
		}
		p.RequestHeader = &h
	}
	if p.RequestContext != nil {
		c := *p.RequestContext
		if c.ContextWindow != nil {
			window := *c.ContextWindow
			c.ContextWindow = &window
		}
		if c.SystemPromptUpdate != nil {
			mode := *c.SystemPromptUpdate
			c.SystemPromptUpdate = &mode
		}
		p.RequestContext = &c
	}
	history := s.p.ToolHistory
	// Fold's final consolidation is a read-time view, not a new baseline. Keep
	// the accumulated history intact so a later tool delta can complete it.
	if len(s.active) != len(s.available) {
		history = ToolHistory{Tools: s.active, Updates: []ToolHistoryUpdate{}}
	} else {
		for _, tool := range s.active {
			if !s.available[tool.Name] {
				history = ToolHistory{Tools: s.active, Updates: []ToolHistoryUpdate{}}
				break
			}
		}
	}
	p.ToolHistory.Tools = cloneSchemas(history.Tools)
	p.ToolHistory.Updates = make([]ToolHistoryUpdate, len(history.Updates))
	for n, update := range history.Updates {
		update.Additions = cloneSchemas(update.Additions)
		p.ToolHistory.Updates[n] = update
	}
	p.Inbox = cloneInbox(s.p.Inbox)
	return p
}
