package session

import (
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Restore validates committed facts without writing, repairing or publishing.
// The storage owner must hold exclusive ownership until recovery and publication
// finish. Commit runs under the Session mutex, before facts become visible, and
// must durably write one encoded event or leave its storage prefix unchanged.
func Restore(
	header protocol.SessionHeader,
	events []protocol.SessionEvent,
	observer contracts.ObservationDispatcher,
	commit func([]byte) error,
) (*LiveSession, error) {
	if err := header.Validate(); err != nil {
		return nil, err
	}

	state := logState{}

	for i, event := range events {
		if event.Seq != protocol.Sequence(i) {
			return nil, fmt.Errorf("%w: event %d has seq %d", ErrInvalidSequence, i, event.Seq)
		}
		if err := state.apply(event); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
	}

	if _, err := Fold(events); err != nil {
		return nil, err
	}
	// Build owned incremental state only after the independent recovery Fold
	// has accepted the prefix. Caller-owned bytes never enter the cache.
	owned := cloneEvents(events)
	projection, err := newProjectionState(owned)
	if err != nil {
		return nil, err
	}

	return &LiveSession{
		id:         header.ID,
		header:     header.Clone(),
		events:     owned,
		projection: projection,
		state:      state,
		observer:   observer,
		commit:     commit,
	}, nil
}

// Close serializes physical release with appends and prevents future admission.
// A failed physical release remains retryable; successful cleanup runs once.
func (s *LiveSession) Close(release func() error) error {
	s.notifications.Lock()
	defer s.notifications.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.physicalReleased {
		return nil
	}

	s.closed = true
	s.finishSubscriptionsLocked()

	if err := release(); err != nil {
		return err
	}
	s.physicalReleased = true
	return nil
}

// Release refuses attached or running resources without deleting history.
// The storage owner supplies physical runtime cleanup separately from deletion.
func (s *LiveSession) Release(release func() error) error {
	s.notifications.Lock()
	defer s.notifications.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.owner != nil {
		return ErrAttached
	}
	if s.running {
		return ErrRunActive
	}

	if s.physicalReleased {
		return nil
	}
	s.closed = true
	s.finishSubscriptionsLocked()

	if err := release(); err != nil {
		return err
	}
	s.physicalReleased = true
	return nil
}
