package jsonl

import (
	"context"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	. "github.com/RussellLuo/qi/kernel/core/session"
)

func appendEvents(t *testing.T, target Session, events ...protocol.SessionEvent) {
	t.Helper()
	for _, event := range events {
		if _, err := target.Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
	}
}

func collectEvents(t *testing.T, target Session) []protocol.SessionEvent {
	t.Helper()
	var events []protocol.SessionEvent
	for event, err := range target.Events(context.Background(), -1) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func cloneEvents(events []protocol.SessionEvent) []protocol.SessionEvent {
	cloned := make([]protocol.SessionEvent, len(events))
	for i, event := range events {
		cloned[i] = event.Clone()
	}
	return cloned
}
func inboxMessage(id string) protocol.Message {
	return protocol.NewTextMessage(protocol.MessageID(id), protocol.RoleUser, protocol.SourceUser, id)
}
func inboxIDs(q []protocol.Message) []string {
	ids := []string{}
	for _, m := range q {
		ids = append(ids, string(m.ID))
	}
	return ids
}

// faultFile injects physical failures while preserving the real file. A failed
// operation fails before touching it, except Write may append a partial prefix.
type faultFile struct {
	logFile
	writeErr, syncErr, truncateErr, closeErr error
	failWriteAt, writes                      int
	syncFailures                             int
	partial                                  int
}

func (f *faultFile) Write(b []byte) (int, error) {
	f.writes++
	if f.writeErr != nil && (f.failWriteAt == 0 || f.writes == f.failWriteAt) {
		if f.partial > 0 {
			n, err := f.logFile.Write(b[:min(len(b), f.partial)])
			if err != nil {
				return n, err
			}
			return n, f.writeErr
		}
		return 0, f.writeErr
	}
	return f.logFile.Write(b)
}
func (f *faultFile) Sync() error {
	if f.syncErr != nil && f.syncFailures != 0 {
		if f.syncFailures > 0 {
			f.syncFailures--
		}
		return f.syncErr
	}
	return f.logFile.Sync()
}
func (f *faultFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.logFile.Truncate(size)
}
func (f *faultFile) Close() error {
	if f.closeErr != nil {
		return f.closeErr
	}
	return f.logFile.Close()
}

// runtimeFixture composes real domain and file owners for this package's tests.
// Construction never opens history. Tests explicitly Open and Attach it.
type runtimeFixture struct {
	*Runtime
	persistence *Backend
	owner       *Owner
}

func resolveOwner(owner *Owner) PersistenceResolver {
	return func(ctx context.Context) (PersistenceLease, bool, error) {
		lease, err := owner.Acquire(ctx)
		return lease, err == nil, err
	}
}

func newRuntimeFixture(dir string, observer contracts.ObservationDispatcher) (*runtimeFixture, error) {
	o, err := NewOwner(dir)
	if err != nil {
		return nil, err
	}
	return &runtimeFixture{Runtime: NewRuntime(RuntimeOptions{ResolvePersistence: resolveOwner(o), Observer: observer}), persistence: o.backend, owner: o}, nil
}
func (f *runtimeFixture) Close() error {
	if err := f.Runtime.Close(context.Background()); err != nil {
		return err
	}
	return f.owner.Close()
}

// openRecovered exercises physical admission followed by a borrowed reservation
// for logical repair, then gives that reservation back before returning.
func openRecovered(ctx context.Context, r StoreOpener, id protocol.SessionID) (Session, error) {
	current, err := r.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	lease, err := current.Attach()
	if err != nil {
		return nil, err
	}
	if err := lease.Close(); err != nil {
		return nil, err
	}
	return current, nil
}
