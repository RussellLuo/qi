package llm

import (
	"context"
	"encoding/json"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelInfo struct {
	Ref                ModelRef
	ContextWindow      int
	Capabilities       []string
	SystemPromptUpdate string
	ToolUpdate         bool
	Defaults           protocol.CallConfig
}

func (i ModelInfo) Clone() ModelInfo {
	i.Defaults = i.Defaults.Clone()
	i.Capabilities = append([]string(nil), i.Capabilities...)
	return i
}

type ToolHistoryUpdate struct {
	MessageID protocol.MessageID `json:"messageId"`
	Additions []tools.Schema     `json:"additions"`
}

type ToolHistory struct {
	Tools   []tools.Schema      `json:"tools"`
	Updates []ToolHistoryUpdate `json:"updates"`
}

func (h ToolHistory) Clone() ToolHistory {
	h.Tools = cloneToolSchemas(h.Tools)
	updates := h.Updates
	h.Updates = make([]ToolHistoryUpdate, len(updates))

	for i, u := range updates {
		h.Updates[i] = ToolHistoryUpdate{u.MessageID, cloneToolSchemas(u.Additions)}
	}

	return h
}

type Request struct {
	Config      protocol.CallConfig
	Messages    []protocol.Message
	Tools       []tools.Schema
	ToolHistory ToolHistory
	SessionID   protocol.SessionID
}

func (r Request) Clone() Request {
	r.Config = r.Config.Clone()
	r.Messages = cloneAgentMessages(r.Messages)
	r.Tools = cloneToolSchemas(r.Tools)
	r.ToolHistory = r.ToolHistory.Clone()
	return r
}

// Adapter supplies the canonical model stream. Model metadata is optional;
// adapters that know it can also implement ModelResolver.
type Adapter interface {
	Stream(context.Context, Request) contracts.ChunkStream
}

// ModelResolver optionally supplies model metadata and validates model support.
// Without it, Runtime uses only the requested Ref and leaves metadata zero-valued.
type ModelResolver interface {
	ResolveModel(context.Context, ModelRef) (ModelInfo, error)
}

type AdapterRegistration struct {
	Route string
	// Models optionally binds exact models; these take precedence over provider-wide registrations.
	Models      []string
	Provider    string
	Defaults    json.RawMessage
	Adapter     Adapter
	RetryPolicy *ResolvedRetryPolicy
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
