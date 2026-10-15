package session

import (
	"context"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Snapshot contains one committed request view and its visible system facts,
// including empty system rewrites which are absent from Projection.Messages.
// All slices, maps, pointers and event bytes belong to the caller.
type Snapshot struct {
	Projection   Projection
	SystemEvents []protocol.SessionEvent
}

// SnapshotReader is optional; Session and InboxSession retain their existing
// contracts. Storage wrappers can forward it to avoid replaying the whole log.
type SnapshotReader interface {
	Snapshot(context.Context) (Snapshot, error)
}

// ReadSnapshot uses a committed cache when supplied, otherwise the pure Fold.
func ReadSnapshot(ctx context.Context, current contracts.InboxSession) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if reader, ok := current.(SnapshotReader); ok {
		return reader.Snapshot(ctx)
	}
	events := []protocol.SessionEvent{}
	for event, err := range current.Events(ctx, -1) {
		if err != nil {
			return Snapshot{}, err
		}
		events = append(events, event)
	}
	p, err := Fold(events)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Projection: p, SystemEvents: []protocol.SessionEvent{}}
	for _, seq := range p.Surface.Nodes {
		if event := events[seq]; event.Type == protocol.SystemMessage {
			result.SystemEvents = append(result.SystemEvents, event.Clone())
		}
	}
	return result, nil
}

func (s *LiveSession) ensureProjectionLocked() error {
	if s.projection != nil {
		return nil
	}
	// Also supports unpublished, internally assembled recovery prefixes.
	p, err := newProjectionState(s.events)
	if err == nil {
		s.projection = p
	}
	return err
}

func (s *LiveSession) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.commit != nil {
		return Snapshot{}, contracts.ErrClosed
	}
	if err := s.ensureProjectionLocked(); err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Projection: s.projection.snapshot(), SystemEvents: []protocol.SessionEvent{}}
	for _, seq := range s.projection.p.Surface.Nodes {
		if event := s.events[seq]; event.Type == protocol.SystemMessage {
			result.SystemEvents = append(result.SystemEvents, event.Clone())
		}
	}
	return result, nil
}

// PendingInbox supplies only pending input, without cloning messages or other
// request state. Inbox detects this optional capability structurally.
func (s *LiveSession) PendingInbox(ctx context.Context) (contracts.InboxState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.commit != nil {
		return nil, contracts.ErrClosed
	}
	if err := s.ensureProjectionLocked(); err != nil {
		return nil, err
	}
	return cloneInbox(s.projection.p.Inbox), nil
}
