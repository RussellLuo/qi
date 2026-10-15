package jsonl

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func newRuntimeBackend(t *testing.T, dir string, options session.RuntimeOptions) (*session.Runtime, *Backend) {
	t.Helper()
	o, err := NewOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	options.ResolvePersistence = resolveOwner(o)
	r := session.NewRuntime(options)
	t.Cleanup(func() {
		if err := r.Close(context.Background()); err != nil {
			t.Error(err)
			return
		}
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, o.backend
}
func readLog(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func expectLocked(t *testing.T, dir string) {
	t.Helper()
	other, err := NewPersistence(dir)
	if other != nil {
		_ = other.Close()
	}
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatal("directory writing ownership lost", err)
	}
}

func TestRuntimeJSONLReleaseOpenAndDelete(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, p := newRuntimeBackend(t, dir, session.RuntimeOptions{})
	current, err := r.Create(ctx, session.CreateRequest{ID: "session / 中文"})
	if err != nil {
		t.Fatal(err)
	}
	appendEvents(t, current, protocol.NewTurnStarted(1), protocol.NewMessageEvent(protocol.UserMessage, 1, 0, protocol.NewTextMessage("u", protocol.RoleUser, protocol.SourceUser, "first")), protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"}))
	facts := collectEvents(t, current)
	header := current.Header()
	path := p.path(current.ID())
	before := readLog(t, path)
	if _, err := r.Open(ctx, current.ID()); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, current.ID()); !errors.Is(err, session.ErrAttached) {
		t.Fatal("deleted acquired history", err)
	}
	for range 2 {
		if err := r.Release(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, readLog(t, path)) {
		t.Fatal("Release changed history")
	}
	if len(p.handles) != 0 {
		t.Fatal("Release leaked file handle")
	}
	expectLocked(t, dir)
	if _, err := r.Get(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("Get implicitly opened history", err)
	}
	restored, err := r.Open(ctx, current.ID())
	if err != nil {
		t.Fatal(err)
	}
	if restored == current || !reflect.DeepEqual(header, restored.Header()) || !reflect.DeepEqual(facts, collectEvents(t, restored)) {
		t.Fatal("Open lost facts/identity")
	}
	if !bytes.Equal(before, readLog(t, path)) {
		t.Fatal("Open appended logical repair")
	}
	if err := r.Release(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("stale Release", err)
	}
	if err := r.RollbackCreate(ctx, restored); !errors.Is(err, session.ErrNotCreated) {
		t.Fatal("rolled back existing history", err)
	}
	lease, err := restored.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Release(ctx, restored); !errors.Is(err, session.ErrAttached) {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Release(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, restored.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Delete retained history", err)
	}
	if _, err := r.Open(ctx, current.ID()); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRuntimeJSONLAdmissionIsLazyAndRejectsWithoutRepair(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"header-check", "header", "filename", "decode", "sequence", "transition"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			header, events, _ := repairFixture(t, "repair-tool-not-started")
			if kind == "sequence" {
				events[0].Seq = 9
			}
			if kind == "transition" {
				events = []protocol.SessionEvent{protocol.NewTurnEnded(1, protocol.TurnEndReason{Kind: "completed"})}
			}
			path := writePrefix(t, dir, header, events, []byte("partial"))
			switch kind {
			case "header":
				if err := os.WriteFile(path, []byte("{\"version\":3}\npartial"), 0600); err != nil {
					t.Fatal(err)
				}
			case "filename":
				wrong := (&Backend{dir: dir}).path("wrong")
				if err := os.Rename(path, wrong); err != nil {
					t.Fatal(err)
				}
				path = wrong
				header.ID = "wrong"
			case "decode":
				raw := append(readLog(t, path), []byte("\n")...)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := readLog(t, path)
			options := session.RuntimeOptions{}
			cause := errors.New("reject header")
			if kind == "header-check" {
				options.HeaderCheck = func(got protocol.SessionHeader) error {
					expectLocked(t, dir)
					if got.ID == "good" {
						return nil
					}
					return cause
				}
			}
			r, p := newRuntimeBackend(t, dir, options)
			if len(p.handles) != 0 || !bytes.Equal(before, readLog(t, path)) {
				t.Fatal("backend startup opened/repaired history")
			}
			if _, err := r.Get(ctx, header.ID); !errors.Is(err, session.ErrNotFound) {
				t.Fatal("Get loaded history", err)
			}
			_, err := r.Open(ctx, header.ID)
			if err == nil {
				t.Fatal("accepted rejected history")
			}
			if kind == "header-check" && !errors.Is(err, cause) {
				t.Fatal(err)
			}
			if kind == "transition" && !errors.Is(err, session.ErrInvalidTransition) {
				t.Fatal(err)
			}
			if kind != "transition" && kind != "header-check" && !errors.Is(err, ErrCorruptLog) {
				t.Fatal(err)
			}
			if len(p.handles) != 0 || !bytes.Equal(before, readLog(t, path)) {
				t.Fatal("rejection leaked/changed history")
			}
			// One bad history never prevents opening a different valid history.
			good, err := r.Create(ctx, session.CreateRequest{ID: "good"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Release(ctx, good); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Open(ctx, good.ID()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeJSONLTailPreparationAndLogicalRecovery(t *testing.T) {
	ctx := context.Background()
	header, events, expected := repairFixture(t, "repair-mixed-assistant-order")
	dir := t.TempDir()
	path := writePrefix(t, dir, header, events, []byte("partial"))
	r, p := newRuntimeBackend(t, dir, session.RuntimeOptions{})
	before := readLog(t, path)
	current, err := r.Open(ctx, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRepairJSON(t, events, collectEvents(t, current))
	if !bytes.Equal(before[:len(before)-len("partial")], readLog(t, path)) {
		t.Fatal("physical repair changed committed prefix")
	}
	lease, err := current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	facts := collectEvents(t, current)
	assertRepairJSON(t, expected, facts[len(events):len(facts)-1])
	if facts[len(facts)-1].Type != protocol.SessionEndSeed {
		t.Fatal("logical marker order")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	after := readLog(t, path)
	if err := r.Release(ctx, current); err != nil {
		t.Fatal(err)
	}
	current, err = r.Open(ctx, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = current.Attach()
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, readLog(t, path)) {
		t.Fatal("repeat Attach changed balanced history")
	}
	if len(p.handles) != 1 {
		t.Fatal("wrong live physical handle count")
	}
}

func TestRuntimeJSONLCommitFailuresPreservePrefixAndOwnership(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("physical write")
	for _, kind := range []string{"partial-write", "sync", "truncate-rollback", "sync-rollback"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			observer := &capturedObserver{}
			r, p := newRuntimeBackend(t, dir, session.RuntimeOptions{Observer: observer})
			current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
			if err != nil {
				t.Fatal(err)
			}
			path := p.path(current.ID())
			before := readLog(t, path)
			h := p.handles[current.ID()]
			fault := &faultFile{logFile: h.file}
			h.file = fault
			switch kind {
			case "partial-write":
				fault.writeErr = cause
				fault.partial = 11
			case "sync":
				fault.syncErr = cause
				fault.syncFailures = 1
			case "truncate-rollback":
				fault.syncErr = cause
				fault.syncFailures = 1
				fault.truncateErr = errors.New("truncate")
			case "sync-rollback":
				fault.syncErr = cause
				fault.syncFailures = 2
			}
			sub, err := current.Subscribe(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if seq, err := current.Append(ctx, protocol.NewTurnStarted(1)); seq != -1 || !errors.Is(err, cause) {
				t.Fatal(seq, err)
			}
			messages, err := current.Messages(ctx)
			if err != nil || len(messages) != 0 || current.Watermark() != -1 || len(observer.sessions) != 0 {
				t.Fatal("uncommitted fact visible", err)
			}
			if kind == "partial-write" || kind == "sync" {
				if !bytes.Equal(before, readLog(t, path)) {
					t.Fatal("failed commit left bytes")
				}
			} else {
				if h.failedOffset == nil {
					t.Fatal("failed rollback dropped responsibility")
				}
				if _, err := current.Append(ctx, protocol.NewTurnStarted(1)); err == nil {
					t.Fatal("unrepaired handle admitted write")
				}
				if kind == "truncate-rollback" {
					if err := r.Release(ctx, current); err == nil {
						t.Fatal("released unsuccessful complete record")
					}
					if _, err := r.Open(ctx, current.ID()); !errors.Is(err, session.ErrAlreadyExists) {
						t.Fatal(err)
					}
					expectLocked(t, dir)
				}
			}
			fault.writeErr, fault.syncErr, fault.truncateErr = nil, nil, nil
			if err := r.Release(ctx, current); err != nil {
				t.Fatal(err)
			}
			for e, err := range sub.Events() {
				t.Fatal("failed commit notification", e, err)
			}
			sub.Close()
			if !bytes.Equal(before, readLog(t, path)) {
				t.Fatal("cleanup did not restore committed prefix")
			}
			restored, err := r.Open(ctx, current.ID())
			if err != nil {
				t.Fatal(err)
			}
			if restored.Watermark() != -1 {
				t.Fatal("failed commit reappeared after Open")
			}
		})
	}
}

func TestJSONLHandleCloseFailureRetainsDirectoryLockAndRetries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, p := newRuntimeBackend(t, dir, session.RuntimeOptions{})
	current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
	if err != nil {
		t.Fatal(err)
	}
	h := p.handles[current.ID()]
	cause := errors.New("close")
	fault := &faultFile{logFile: h.file, closeErr: cause}
	h.file = fault
	if err := r.Release(ctx, current); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := current.Attach(); !errors.Is(err, contracts.ErrClosed) {
		t.Fatal(err)
	}
	if err := p.Close(); !errors.Is(err, session.ErrAttached) {
		t.Fatal("backend accepted outstanding handle", err)
	}
	expectLocked(t, dir)
	fault.closeErr = nil
	if err := r.Release(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	next, err := NewPersistence(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLRollbackUsesExactCreationIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p, err := NewPersistence(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	header, err := session.CreateHeader("history", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Create(ctx, header)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RollbackCreate(); err != nil {
		t.Fatal(err)
	}
	next, err := p.Create(ctx, header)
	if err != nil {
		t.Fatal(err)
	}
	before := readLog(t, p.path(header.ID))
	if err := first.RollbackCreate(); err != nil {
		t.Fatal("rollback not idempotent", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, readLog(t, p.path(header.ID))) {
		t.Fatal("old rollback deleted new history")
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := next.RollbackCreate(); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("released credential deleted existing history", err)
	}
	// Even a filesystem replacement cannot be removed by an old creation handle.
	if err := p.Delete(ctx, header.ID); err != nil {
		t.Fatal(err)
	}
	third, err := p.Create(ctx, header)
	if err != nil {
		t.Fatal(err)
	}
	h := third.(*fileHandle)
	saved := filepath.Join(dir, "saved")
	if err := os.Rename(h.path, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.RollbackCreate(); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	if string(readLog(t, h.path)) != "replacement" {
		t.Fatal("removed unrelated inode")
	}
	if err := os.Remove(h.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, h.path); err != nil {
		t.Fatal(err)
	}
	if err := h.RollbackCreate(); err != nil {
		t.Fatal("rollback cannot retry", err)
	}
}

func TestJSONLPreparationSyncFailureRemainsRetryable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	header, events, _ := repairFixture(t, "repair-tool-not-started")
	path := writePrefix(t, dir, header, events, []byte("partial"))
	p, err := NewPersistence(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	acquired, err := p.Open(ctx, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := acquired.(*fileHandle)
	if _, _, err := h.Read(ctx); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("sync tail")
	fault := &faultFile{logFile: h.file, syncErr: cause, syncFailures: 2}
	h.file = fault
	for range 2 {
		if err := h.Prepare(ctx); !errors.Is(err, cause) {
			t.Fatal("unsynced truncation accepted", err)
		}
	}
	if h.prepared {
		t.Fatal("failed preparation admitted writes")
	}
	if err := h.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if int64(len(readLog(t, path))) != h.committedSize {
		t.Fatal("tail not repaired")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeJSONLRollbackCloseFailureRetriesExactCreation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, p := newRuntimeBackend(t, dir, session.RuntimeOptions{})
	current, err := r.Create(ctx, session.CreateRequest{ID: "history"})
	if err != nil {
		t.Fatal(err)
	}
	h := p.handles[current.ID()]
	cause := errors.New("creation close")
	fault := &faultFile{logFile: h.file, closeErr: cause}
	h.file = fault
	if err := r.RollbackCreate(ctx, current); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, session.CreateRequest{ID: current.ID()}); !errors.Is(err, session.ErrAlreadyExists) {
		t.Fatal("lost creation ownership", err)
	}
	if err := r.Release(ctx, current); !errors.Is(err, session.ErrCleanupPending) {
		t.Fatal("changed pending rollback", err)
	}
	expectLocked(t, dir)
	fault.closeErr = nil
	if err := r.RollbackCreate(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback kept new history", err)
	}
	next, err := r.Create(ctx, session.CreateRequest{ID: current.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RollbackCreate(ctx, current); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("old rollback affected replacement", err)
	}
	if got, err := r.Get(ctx, next.ID()); err != nil || got != next {
		t.Fatal("replacement gone", err)
	}
}

func TestJSONLPrepareRefusesShortenedCommittedHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	header, events, _ := repairFixture(t, "repair-tool-not-started")
	writePrefix(t, dir, header, events, []byte("partial"))
	p, err := NewPersistence(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	acquired, err := p.Open(ctx, header.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := acquired.(*fileHandle)
	if _, _, err := h.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.file.Truncate(h.committedSize - 1); err != nil {
		t.Fatal(err)
	}
	before := readLog(t, h.path)
	if err := h.Prepare(ctx); !errors.Is(err, ErrCorruptLog) {
		t.Fatal("extended shortened committed prefix", err)
	}
	if !bytes.Equal(before, readLog(t, h.path)) {
		t.Fatal("preparation changed rejected prefix")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}
