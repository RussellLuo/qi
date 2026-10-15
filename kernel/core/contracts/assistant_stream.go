package contracts

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// AgentRuntime is the neutral local reference passed by the attached Agent to
// its Driver. Counters belong to that object, across all Drives and Turns.
type AgentRuntime interface {
	InstanceID() string
	SessionID() protocol.SessionID
	NextAssistantAttempt() int64
	NextAssistantStreamRevision() int64
}

// AssistantStreamState can be embedded in an Agent or a standalone Driver host.
// It must be retained for that host's entire attached lifetime.
type AssistantStreamState struct {
	instanceID string
	mu         sync.Mutex
	attempt    int64
	revision   int64
}

func (s *AssistantStreamState) NextAssistantAttempt() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.attempt++
	return s.attempt
}

func (s *AssistantStreamState) NextAssistantStreamRevision() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.revision++
	return s.revision
}

// AssistantStreamObservation is a local event payload, never a wire envelope.
type AssistantStreamObservation struct {
	InstanceID string
	Agent      AgentRuntime
	Frame      protocol.AssistantStreamFrame
}

func (o AssistantStreamObservation) Clone() AssistantStreamObservation {
	o.Frame = o.Frame.Clone()
	return o
}

var nextRuntimeInstance atomic.Uint64

func (s *AssistantStreamState) InstanceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instanceID == "" {
		s.instanceID = fmt.Sprintf("agent-instance-%d", nextRuntimeInstance.Add(1))
	}
	return s.instanceID
}
