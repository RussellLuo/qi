package agent

import (
	"context"
	"errors"
	"iter"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	ErrNotFound     = errors.New("agent not found")
	ErrExists       = errors.New("agent already exists")
	ErrNotActive    = errors.New("agent invocation is not active")
	ErrClosed       = errors.New("agent is closed")
	ErrInitializing = errors.New("agent initialization is not complete")
)

type ID string

func (id ID) String() string { return string(id) }

type Status = contracts.AgentStatus

const (
	StatusIdle    = contracts.AgentIdle
	StatusRunning = contracts.AgentRunning
)

type CancelCause = protocol.AgentCancelCause
type CancelOptions = contracts.CancelOptions

var CancelRequested = CancelCause{Kind: "user"}

type Input struct{ Text string }
type Config struct{ Loop agentloop.Config }

type CreateRequest struct {
	// ID is an optional caller label; the core Agent ID is always SessionID.
	ID        ID
	SessionID protocol.SessionID
	Config    Config
	ParentID  ID

	// DefaultConfig supplies the fallback when no extension resolves configuration.
	// It runs outside Registry locks, before initialization.
	DefaultConfig func(context.Context) (Config, error)

	// ComposeConfig overrides the Factory composer for this creation.
	ComposeConfig ConfigComposer
}

type State struct {
	Status           Status
	ActiveInvocation protocol.InvocationID
	Disposed         bool
	Dropped          uint64
}

type Snapshot struct {
	ID        ID                 `json:"id"`
	SessionID protocol.SessionID `json:"sessionId"`
	ParentID  ID                 `json:"parentId,omitempty"`
	Status    Status             `json:"status"`
}

type Agent interface {
	Subscribe(context.Context) (AgentSubscription, error)
	SubscribeAssistant(context.Context) (AssistantSubscription, error)
	contracts.AgentControl
	ID() ID
	Configure(context.Context, Config) error
	Config() Config
	Submit(context.Context, Input) (protocol.InvocationID, iter.Seq2[contracts.DriverEvent, error], error)
	Cancel(context.Context, protocol.InvocationID, CancelCause) error
	WaitIdle(context.Context) error
	State() State
}

// AgentOptions is the creation configuration, including legacy identity fields.
type AgentOptions = CreateRequest

type ParentView struct {
	ID        ID
	SessionID protocol.SessionID
	Config    Config
}

type Creation struct {
	ID            ID
	Parent        *ParentView
	Config        Config
	DefaultConfig func(context.Context) (Config, error)
}

type Cleanup func(context.Context) error
type ConfigComposer func(context.Context, Creation) (Config, Cleanup, error)
