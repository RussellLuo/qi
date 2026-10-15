package session

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type SurfaceReplacement struct {
	Seq          protocol.Sequence   `json:"seq"`
	Start        protocol.Sequence   `json:"start"`
	End          protocol.Sequence   `json:"end"`
	ShadowedSeqs []protocol.Sequence `json:"shadowedSeqs"`
}

// Surface identifies the log events currently visible to model requests.
// Replacements shadow earlier nodes without deleting the underlying history.
type Surface struct {
	Nodes        []protocol.Sequence  `json:"nodes"`
	Replacements []SurfaceReplacement `json:"replacements"`
}

type ToolHistoryUpdate struct {
	MessageID protocol.MessageID    `json:"messageId"`
	Additions []protocol.ToolSchema `json:"additions"`
}

type ToolHistory struct {
	Tools   []protocol.ToolSchema `json:"tools"`
	Updates []ToolHistoryUpdate   `json:"updates"`
}

// Projection is the request-facing view reconstructed from the event log,
// including effective messages, model/tool declarations, and pending input.
type Projection struct {
	Surface        Surface
	Messages       []protocol.Message
	RequestHeader  *protocol.EpochHeader
	RequestContext *protocol.RequestContext
	ToolHistory    ToolHistory
	Inbox          map[string][]protocol.Message
}

// Fold validates complete core event JSON and surface references. Turn/Step
// execution brackets are checked separately by the store admission state.
func Fold(events []protocol.SessionEvent) (Projection, error) {
	p := Projection{
		Surface:     Surface{Nodes: []protocol.Sequence{}, Replacements: []SurfaceReplacement{}},
		Messages:    []protocol.Message{},
		ToolHistory: ToolHistory{Tools: []protocol.ToolSchema{}, Updates: []ToolHistoryUpdate{}},
		Inbox:       map[string][]protocol.Message{"next-turn": {}, "next-step": {}},
	}
	declared := map[string]protocol.ToolSchema{}
	available := map[string]bool{}
	active := []protocol.ToolSchema{}
	baseline := false

	for i, e := range events {
		if e.Seq != protocol.Sequence(i) {
			return Projection{}, fmt.Errorf("%w: got %d, want %d", ErrInvalidSequence, e.Seq, i)
		}
		if err := e.Validate(); err != nil {
			return Projection{}, err
		}
		if !e.IsKnown() {
			continue
		}

		switch e.Type {
		case protocol.RequestHeader:
			d := protocol.EventPayload[protocol.RequestHeaderData](e)
			h := d.Header
			p.RequestHeader = &h
			active = h.Tools
			if active == nil {
				active = []protocol.ToolSchema{}
			}
			redeclared := false
			for _, t := range active {
				if old, ok := declared[t.Name]; ok && !jsonEqual(old, t) {
					redeclared = true
				}
			}
			if !baseline || d.Reason == "series" || d.StartsSeries || redeclared {
				baseline = true
				declared = map[string]protocol.ToolSchema{}
				available = map[string]bool{}

				for _, t := range active {
					declared[t.Name] = t
					available[t.Name] = true
				}

				p.ToolHistory = ToolHistory{Tools: active, Updates: []ToolHistoryUpdate{}}
			}
		case protocol.RequestContextEvent:
			d := protocol.EventPayload[protocol.RequestContext](e)
			p.RequestContext = &d
		case protocol.DeveloperMessage:
			d := protocol.EventPayload[protocol.MessageData](e)
			additions := []protocol.ToolSchema{}
			if d.HeaderSeq != nil {
				ref := events[*d.HeaderSeq]
				if ref.Type != protocol.RequestHeader {
					return Projection{}, fmt.Errorf("%w: headerSeq is not request/header", protocol.ErrInvalidProtocol)
				}

				definitions := protocol.EventPayload[protocol.RequestHeaderData](ref).Header.Tools

				for _, b := range d.Message.Content {
					if add, ok := b.(protocol.ToolAdditionBlock); ok {
						matches := []protocol.ToolSchema{}

						for _, t := range definitions {
							if t.Name == add.ToolName {
								matches = append(matches, t)
							}
						}

						if len(matches) != 1 {
							return Projection{}, fmt.Errorf("%w: tool addition has no unique historical definition", protocol.ErrInvalidProtocol)
						}

						additions = append(additions, matches[0])
						declared[add.ToolName] = matches[0]
						available[add.ToolName] = true
					}
				}
			}
			for _, b := range d.Message.Content {
				if remove, ok := b.(protocol.ToolRemovalBlock); ok {
					delete(available, remove.ToolName)
				}
			}
			p.ToolHistory.Updates = append(p.ToolHistory.Updates, ToolHistoryUpdate{d.Message.ID, additions})
		case protocol.InboxSpliced:
			d := protocol.EventPayload[protocol.InboxSpliceData](e)
			queue := p.Inbox[d.Target]
			count := int64(0)
			if d.RemovedCount != nil {
				count = *d.RemovedCount
			}
			if d.Start > int64(len(queue)) || count > int64(len(queue))-d.Start {
				return Projection{}, fmt.Errorf("%w: inbox span out of range", protocol.ErrInvalidProtocol)
			}
			next := append([]protocol.Message{}, queue[:d.Start]...)
			next = append(next, d.Inserted...)
			next = append(next, queue[d.Start+count:]...)
			ids := map[protocol.MessageID]bool{}
			for target, qs := range p.Inbox {
				if target == d.Target {
					qs = next
				}

				for _, m := range qs {
					if ids[m.ID] {
						return Projection{}, fmt.Errorf("%w: duplicate inbox message id", protocol.ErrInvalidProtocol)
					}

					ids[m.ID] = true
				}
			}
			p.Inbox[d.Target] = next
		}

		if !e.IsSurface() {
			continue
		}

		if e.SurfaceOp.Op == "append" {
			p.Surface.Nodes = append(p.Surface.Nodes, e.Seq)
			continue
		}

		start, end := -1, -1

		for j, n := range p.Surface.Nodes {
			if n == e.SurfaceOp.StartSeq {
				start = j
			}

			if n == e.SurfaceOp.EndSeq {
				end = j
			}
		}

		if start < 0 || end < start {
			return Projection{}, fmt.Errorf("%w: replace range is not in current surface", protocol.ErrInvalidProtocol)
		}

		shadowed := append([]protocol.Sequence{}, p.Surface.Nodes[start:end+1]...)
		refs := map[protocol.Sequence]bool{}

		for _, n := range e.SourceEventSeqs {
			refs[n] = true
		}

		for _, n := range shadowed {
			if !refs[n] {
				return Projection{}, fmt.Errorf("%w: missing shadowed source node %d", protocol.ErrInvalidProtocol, n)
			}
		}

		if start == 0 &&
			events[p.Surface.Nodes[0]].Type == protocol.SystemMessage &&
			(e.Type != protocol.SystemMessage || len(shadowed) != 1) {
			return Projection{}, fmt.Errorf("%w: system head must be rewritten alone as system/message", protocol.ErrInvalidProtocol)
		}
		if e.Type == protocol.ToolResulted {
			if len(shadowed) != 1 || events[shadowed[0]].Type != protocol.ToolResulted {
				return Projection{}, fmt.Errorf("%w: tool result rewrite must target one tool result", protocol.ErrInvalidProtocol)
			}
			// Compare the durable JSON with only message.content masked. A
			// typed Message with nil content cannot be marshaled as a valid message.
			if !reflect.DeepEqual(toolResultIdentity(events[shadowed[0]].Data), toolResultIdentity(e.Data)) {
				return Projection{}, fmt.Errorf("%w: tool result rewrite may change only content", protocol.ErrInvalidProtocol)
			}
		}

		nodes := append([]protocol.Sequence{}, p.Surface.Nodes[:start]...)
		nodes = append(nodes, e.Seq)
		nodes = append(nodes, p.Surface.Nodes[end+1:]...)
		p.Surface.Nodes = nodes
		p.Surface.Replacements = append(p.Surface.Replacements, SurfaceReplacement{e.Seq, e.SurfaceOp.StartSeq, e.SurfaceOp.EndSeq, shadowed})
	}

	for _, n := range p.Surface.Nodes {
		if m := DeriveEventMessage(events[n]); m != nil {
			p.Messages = append(p.Messages, m.Clone())
		}
	}

	if len(active) != len(available) {
		p.ToolHistory = ToolHistory{Tools: active, Updates: []ToolHistoryUpdate{}}
	} else {
		for _, t := range active {
			if !available[t.Name] {
				p.ToolHistory = ToolHistory{Tools: active, Updates: []ToolHistoryUpdate{}}
				break
			}
		}
	}

	return p, nil
}

func jsonEqual(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	var x, y any
	_ = json.Unmarshal(aa, &x)
	_ = json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}

func DeriveEventMessage(e protocol.SessionEvent) *protocol.Message {
	if !e.IsSurface() {
		return nil
	}

	var m protocol.Message
	if e.Type == protocol.UserMessage {
		m = protocol.EventPayload[protocol.Message](e)
	} else {
		var d struct {
			Message protocol.Message `json:"message"`
		}
		_ = json.Unmarshal(e.Data, &d)
		m = d.Message
	}

	if len(m.Content) == 0 &&
		(e.Type == protocol.SystemMessage || e.Type == protocol.DeveloperMessage || e.Type == protocol.AssistantMessage) {
		return nil
	}

	m = m.Clone()

	return &m
}

func ProjectMessages(events []protocol.SessionEvent) ([]protocol.Message, error) {
	p, err := Fold(events)
	return p.Messages, err
}

func toolResultIdentity(raw json.RawMessage) map[string]any {
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	message := data["message"].(map[string]any)
	message["content"] = nil
	return data
}
