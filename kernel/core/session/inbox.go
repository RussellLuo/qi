package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Inbox reads committed Session state, with a Fold fallback. Its write gate also admits
// ordinary Loop appends, so splice reads and commits share the same writer.
type Inbox struct {
	mu      sync.Mutex
	current contracts.InboxSession
}

func NewInbox(current contracts.InboxSession) *Inbox { return &Inbox{current: current} }

func (i *Inbox) EnqueueContexts(ctx context.Context, messages []protocol.Message) error {
	if len(messages) == 0 {
		return nil
	}

	_, err := i.Splice(ctx, contracts.NextStep, int(^uint(0)>>1), 0, messages, false)
	return err
}

func (i *Inbox) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.current.Append(ctx, e)
}

func (i *Inbox) pending(ctx context.Context) (contracts.InboxState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if reader, ok := i.current.(interface {
		PendingInbox(context.Context) (contracts.InboxState, error)
	}); ok {
		return reader.PendingInbox(ctx)
	}
	snapshot, err := ReadSnapshot(ctx, i.current)
	return snapshot.Projection.Inbox, err
}

func (i *Inbox) Pending(ctx context.Context) (contracts.InboxState, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.pending(ctx)
}

// Splice applies integer array-splice normalization. Explicit removals are
// canceled; claim removals omit outcome. No-op mutations produce no fact.
func (i *Inbox) Splice(
	ctx context.Context,
	target contracts.InboxTarget,
	start, count int,
	inserted []protocol.Message,
	discard bool,
) ([]protocol.Message, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.splice(ctx, target, start, count, inserted, discard)
}

func (i *Inbox) splice(
	ctx context.Context,
	target contracts.InboxTarget,
	start, count int,
	inserted []protocol.Message,
	discard bool,
) ([]protocol.Message, error) {
	if target != contracts.NextStep && target != contracts.NextTurn {
		return nil, fmt.Errorf("invalid inbox target %q", target)
	}

	state, err := i.pending(ctx)
	if err != nil {
		return nil, err
	}

	q := state[target]
	if start < 0 {
		start = len(q) + start
	}

	if start < 0 {
		start = 0
	}

	if start > len(q) {
		start = len(q)
	}

	if count < 0 {
		count = 0
	}

	if count > len(q)-start {
		count = len(q) - start
	}

	if count == 0 && len(inserted) == 0 {
		return []protocol.Message{}, nil
	}

	data := protocol.InboxSpliceData{
		Target:   target,
		Start:    int64(start),
		Inserted: []protocol.Message{},
	}

	for _, m := range inserted {
		data.Inserted = append(data.Inserted, m.Clone())
	}

	if count > 0 {
		n := int64(count)
		data.RemovedCount = &n
		if discard {
			data.Outcome = "canceled"
		}
	}

	removed := []protocol.Message{}

	for _, m := range q[start : start+count] {
		removed = append(removed, m.Clone())
	}

	if _, err := i.current.Append(ctx, protocol.NewSessionEvent(protocol.InboxSpliced, data)); err != nil {
		return nil, err
	}

	return removed, nil
}

func (i *Inbox) Claim(ctx context.Context, target contracts.InboxTarget, _ protocol.TurnID) ([]protocol.Message, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if target != contracts.NextStep && target != contracts.NextTurn {
		return nil, fmt.Errorf("invalid inbox target %q", target)
	}

	state, err := i.pending(ctx)
	if err != nil {
		return nil, err
	}

	claimed, err := i.splice(ctx, contracts.NextStep, 0, len(state[contracts.NextStep]), nil, false)
	if err != nil {
		return nil, err
	}
	if target == contracts.NextTurn {
		turn, err := i.splice(ctx, contracts.NextTurn, 0, 1, nil, false)
		if err != nil {
			return nil, err
		}

		claimed = append(claimed, turn...)
	}

	return claimed, nil
}

// Clear commits next-step before next-turn; callers publish discarded only for
// each successful removal, including a committed prefix on a later failure.
func (i *Inbox) Clear(ctx context.Context, discarded func(protocol.Message)) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	state, err := i.pending(ctx)
	if err != nil {
		return err
	}

	for _, target := range []string{contracts.NextStep, contracts.NextTurn} {
		removed, err := i.splice(ctx, target, 0, len(state[target]), nil, true)
		if err != nil {
			return err
		}

		for _, m := range removed {
			discarded(m)
		}
	}

	return nil
}
