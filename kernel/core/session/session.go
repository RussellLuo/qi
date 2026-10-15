// Package session owns the canonical Session event log, input queues, and
// projections. Storage implementations supply a commit hook to the same state
// machine; Agent reservations control attachment and active-run admission.
package session

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	ErrNotFound          = errors.New("session not found")
	ErrAlreadyExists     = errors.New("session already exists")
	ErrAttached          = errors.New("session already has an attached owner")
	ErrRunActive         = errors.New("session already has an active run")
	ErrInvalidSequence   = errors.New("invalid session event sequence")
	ErrUnclosedLog       = errors.New("unclosed session requires recovery through Attach before a new run")
	ErrInvalidTransition = errors.New("invalid session event transition")
)

type CreateRequest struct {
	ID     protocol.SessionID
	Header *protocol.SessionHeader
}

type Session interface {
	ID() protocol.SessionID
	Subscribe(context.Context) (SessionSubscription, error)
	Header() protocol.SessionHeader
	Watermark() protocol.Sequence
	LastTurn() protocol.TurnID
	// Append allocates seq itself; the candidate seq is ignored, including zero.
	// On failure the returned cursor is -1 and the log is unchanged.
	Append(context.Context, protocol.SessionEvent) (protocol.Sequence, error)
	// Events reads strictly after a watermark; use -1 to include seq=0.
	Events(context.Context, protocol.Sequence) iter.Seq2[protocol.SessionEvent, error]
	Messages(context.Context) ([]protocol.Message, error)
	BeginRun() (func(), error)
	BeginRunOwned(contracts.SessionReservation) (func(), error)
	Attach() (contracts.SessionReservation, error)
}

type Store interface {
	Create(context.Context, CreateRequest) (Session, error)
	Get(context.Context, protocol.SessionID) (Session, error)
	Delete(context.Context, protocol.SessionID) error
}

// StoreReleaser releases a particular Session's runtime resources after its
// Agent has drained and detached. It must not delete persistent history or
// close the shared Store. Implementations must check instance identity so a
// stale release cannot affect a replacement with the same ID. Non-persistent
// Stores withdraw the instance; persistent Stores define their own release.
// Release is idempotent for the same released instance. A different live
// instance with the same ID must never be affected. Factory.Attach only borrows.
type StoreReleaser interface {
	Release(context.Context, Session) error
}

// ResourceStore is the required owned Create/Resume contract. Factory validates
// it at construction; the narrower Store remains usable by Attach-only owners.
type ResourceStore interface {
	Store
	StoreReleaser
	StoreCreationRollback
}

var ErrResumeUnsupported = errors.New("session Store does not support opening history")

// StoreOpener acquires an exclusively owned runtime instance, validating the
// historical prefix before physical preparation. Get only queries opened instances.
// Open must reject an already opened ID, never silently borrow it. On failure
// it retains responsibility for any partial acquisition; on success the caller
// owns the exact instance and releases it with ResourceStore.Release. Logical
// repair is committed by Session.Attach before Factory initialization/publication.
type StoreOpener interface {
	Open(context.Context, protocol.SessionID) (Session, error)
}

// StoreCreationRollback removes only the exact, untransferred Session created
// by a failed owned Create transaction. Factory.Create requires this capability;
// borrowed Attach transactions never roll back caller-owned Sessions.
type StoreCreationRollback interface {
	RollbackCreate(context.Context, Session) error
}

// LiveSession validates log transitions and publishes only committed events.
// Its mutex protects both the event log and attachment/run state.
type LiveSession struct {
	notifications    sync.Mutex
	subscribers      map[*observation.Subscription[protocol.SessionEvent]]struct{}
	mu               sync.Mutex
	id               protocol.SessionID
	header           protocol.SessionHeader
	events           []protocol.SessionEvent
	state            logState
	projection       *projectionState
	needsRecovery    bool
	running          bool
	closed           bool
	physicalReleased bool
	runtime          *Runtime // provenance for idempotent Runtime.Release
	runtimeReleased  bool
	owner            *reservation
	observer         contracts.ObservationDispatcher
	commit           func([]byte) error
}

func (s *LiveSession) ID() protocol.SessionID { return s.id }

func (s *LiveSession) Header() protocol.SessionHeader { return s.header.Clone() }

func (s *LiveSession) Watermark() protocol.Sequence {
	s.mu.Lock()
	defer s.mu.Unlock()

	return protocol.Sequence(len(s.events)) - 1
}

func (s *LiveSession) LastTurn() protocol.TurnID {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state.lastTurn
}

func (s *LiveSession) Append(ctx context.Context, event protocol.SessionEvent) (protocol.Sequence, error) {
	s.notifications.Lock()
	defer s.notifications.Unlock()
	if err := ctx.Err(); err != nil {
		return -1, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return -1, contracts.ErrClosed
	}

	if event.Type == protocol.SessionEndSeed {
		s.mu.Unlock()
		return -1, fmt.Errorf("%w: end-seed is lifecycle-owned", protocol.ErrInvalidProtocol)
	}

	if s.needsRecovery {
		s.mu.Unlock()
		return -1, ErrUnclosedLog
	}

	next, err := s.appendLocked(event)
	if err == nil {
		event = s.events[next].Clone()
	}

	s.mu.Unlock()
	if err != nil {
		return -1, err
	}
	s.publish(event)

	return next, nil
}

// appendLocked requires s.mu or an unpublished Session under the Store lock.
func (s *LiveSession) appendLocked(event protocol.SessionEvent) (protocol.Sequence, error) {
	next := protocol.Sequence(len(s.events))
	event.Seq = next
	encoded, err := protocol.EncodeSessionEvent(event)
	if err != nil {
		return -1, fmt.Errorf("invalid session event: %w", err)
	}

	event, err = protocol.DecodeSessionEvent(encoded)
	if err != nil {
		return -1, err
	}
	if err := s.ensureProjectionLocked(); err != nil {
		return -1, err
	}
	publish, err := s.projection.prepare(s.events, event)
	if err != nil {
		return -1, err
	}

	nextState := s.state
	if err := nextState.apply(event); err != nil {
		return -1, err
	}
	// Persist the normalized event before publishing either the log or its new
	// state. A failed commit must leave both in-memory views unchanged.
	if s.commit != nil {
		if err := s.commit(encoded); err != nil {
			return -1, err
		}
	}

	s.events = append(s.events, event.Clone())
	s.state = nextState
	publish()

	return next, nil
}

func (s *LiveSession) Events(ctx context.Context, after protocol.Sequence) iter.Seq2[protocol.SessionEvent, error] {
	s.mu.Lock()
	start, cursorErr := readStart(after, len(s.events))
	snapshot := cloneEvents(s.events[start:])
	closed := s.closed && s.commit != nil
	s.mu.Unlock()
	return func(yield func(protocol.SessionEvent, error) bool) {
		if cursorErr != nil {
			yield(protocol.SessionEvent{}, cursorErr)
			return
		}

		if closed {
			yield(protocol.SessionEvent{}, contracts.ErrClosed)
			return
		}

		for _, event := range snapshot {
			if err := ctx.Err(); err != nil {
				yield(protocol.SessionEvent{}, err)
				return
			}

			if !yield(event.Clone(), nil) {
				return
			}
		}
	}
}

func (s *LiveSession) Messages(ctx context.Context) ([]protocol.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed && s.commit != nil {
		s.mu.Unlock()
		return nil, contracts.ErrClosed
	}

	defer s.mu.Unlock()
	if err := s.ensureProjectionLocked(); err != nil {
		return nil, err
	}
	return s.projection.messageSnapshot(), nil
}

func (s *LiveSession) Attach() (contracts.SessionReservation, error) {
	s.notifications.Lock()
	defer s.notifications.Unlock()
	s.mu.Lock()
	var recovered []protocol.SessionEvent
	defer func() {
		s.mu.Unlock()
		for _, event := range recovered {
			s.publish(event)
		}
	}()

	if s.closed {
		return nil, contracts.ErrClosed
	}
	if s.owner != nil {
		return nil, ErrAttached
	}
	if s.running {
		return nil, ErrRunActive
	}

	s.needsRecovery = s.needsRecovery || s.state.turn != 0
	if err := recoverPrefix(s.events, func(event protocol.SessionEvent) error {
		seq, err := s.appendLocked(event)
		if err == nil {
			recovered = append(recovered, s.events[seq].Clone())
		}

		return err
	}, s.needsRecovery); err != nil {
		return nil, err
	}

	s.needsRecovery = false
	source := "startup"
	if len(s.events) > 0 {
		source = "resume"
	}

	owner := &reservation{id: s.id, source: source}
	owner.begin = func() (func(), error) { return s.beginRun(owner) }
	owner.close = func() error {
		s.mu.Lock()
		defer s.mu.Unlock()

		if s.owner != owner {
			return nil
		}
		if s.running {
			return ErrRunActive
		}

		s.owner = nil

		return nil
	}
	s.owner = owner

	return owner, nil
}

func (s *LiveSession) BeginRun() (func(), error) { return s.beginRun(nil) }

func (s *LiveSession) BeginRunOwned(lease contracts.SessionReservation) (func(), error) {
	owner, ok := lease.(*reservation)
	if !ok || owner == nil {
		return nil, ErrAttached
	}

	return s.beginRun(owner)
}

func (s *LiveSession) beginRun(owner *reservation) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, contracts.ErrClosed
	}
	if s.owner != owner {
		return nil, ErrAttached
	}
	if owner != nil && s.owner == nil {
		return nil, contracts.ErrClosed
	}
	if s.running {
		return nil, ErrRunActive
	}
	if s.needsRecovery || s.state.turn != 0 {
		return nil, ErrUnclosedLog
	}

	s.running = true
	var once sync.Once

	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		})
	}, nil
}

func cloneEvents(events []protocol.SessionEvent) []protocol.SessionEvent {
	cloned := make([]protocol.SessionEvent, len(events))

	for i, event := range events {
		cloned[i] = event.Clone()
	}

	return cloned
}

// Admission tracks execution brackets without using invocation identifiers.
type logState struct {
	lastTurn protocol.TurnID
	turn     protocol.TurnID
	step     protocol.StepID
	lastStep protocol.StepID
}

func (s *logState) apply(e protocol.SessionEvent) error {
	fail := func() error {
		return fmt.Errorf("%w: %s does not match turn %d/step %d", ErrInvalidTransition, e.Type, s.turn, s.step)
	}
	d := protocol.EventPayload[protocol.StepData](e)

	switch e.Type {
	case protocol.SessionEndSeed:
		if s.turn != 0 {
			return fail()
		}
	case protocol.TurnStarted:
		if s.turn != 0 || d.Turn != s.lastTurn+1 {
			return fail()
		}
		s.turn = d.Turn
		s.lastTurn = d.Turn
		s.lastStep = 0
	case protocol.TurnEnded:
		if s.turn != d.Turn || s.turn == 0 || s.step != 0 {
			return fail()
		}
		s.turn = 0
	case protocol.StepStarted:
		if s.turn != d.Turn || s.turn == 0 || s.step != 0 || d.Step != s.lastStep+1 {
			return fail()
		}
		s.step = d.Step
		s.lastStep = d.Step
	case protocol.StepEnded:
		if s.turn != d.Turn || s.step != d.Step || s.step == 0 {
			return fail()
		}
		s.step = 0
	case protocol.SystemMessage,
		protocol.DeveloperMessage,
		protocol.AssistantMessage,
		protocol.AssistantAttempt,
		protocol.ToolCalled,
		protocol.ToolResulted:
		if e.Type == protocol.ToolResulted && e.SurfaceOp.Op == "replace" {
			if s.turn == 0 {
				return fail()
			}

			break
		}
		if s.turn != d.Turn || s.step != d.Step || s.step == 0 {
			return fail()
		}
	}

	return nil
}

func CreateHeader(id protocol.SessionID, supplied *protocol.SessionHeader) (protocol.SessionHeader, error) {
	h := protocol.SessionHeader{
		Version:   protocol.SessionFormatVersion,
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
	}
	if supplied != nil {
		if err := supplied.Validate(); err != nil {
			return protocol.SessionHeader{}, err
		}

		h = supplied.Clone()
		if h.ID != id {
			return h, fmt.Errorf("%w: header id differs from session", protocol.ErrInvalidProtocol)
		}
		if h.IsSeeded {
			return h, fmt.Errorf("%w: seeded creation is outside this store's scope", protocol.ErrInvalidProtocol)
		}
	}

	return h, h.Validate()
}

func readStart(after protocol.Sequence, length int) (int, error) {
	if after < -1 || int64(after) > 9007199254740991 {
		return 0, fmt.Errorf("%w: invalid watermark %d", ErrInvalidSequence, after)
	}
	if after >= protocol.Sequence(length) {
		return length, nil
	}

	return int(after + 1), nil
}
