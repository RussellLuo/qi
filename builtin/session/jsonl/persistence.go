package jsonl

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

var ErrCorruptLog = errors.New("corrupt session log")
var ErrStoreLocked = errors.New("JSONL session store already has a writer")

// Backend owns JSONL files and a directory-wide exclusive writer lock.
// It acquires files on demand and never constructs or registers domain Sessions.
// Close refuses outstanding transferred handles and retains the directory lock
// until all file cleanup succeeds. Failed acquisition cleanup stays backend-owned.
type Backend struct {
	mu      sync.Mutex
	dir     string
	handles map[protocol.SessionID]*fileHandle
	// Production uses *os.File; the narrow interface permits package-local
	// close-failure tests while preserving the real directory lock.
	writerLock io.Closer
	closing    bool
}

var _ session.Persistence = (*Backend)(nil)

func NewPersistence(dir string) (*Backend, error) {
	if dir == "" {
		return nil, errors.New("JSONL session directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create JSONL session directory: %w", err)
	}
	lock, err := acquireStoreLock(dir)
	if err != nil {
		return nil, err
	}
	return &Backend{dir: dir, handles: make(map[protocol.SessionID]*fileHandle), writerLock: lock}, nil
}

// logFile permits package-local I/O fault tests against real file prefixes.
// Production always uses *os.File; no runtime-configurable fault hooks exist.
type logFile interface {
	io.Reader
	io.Writer
	io.Seeker
	Stat() (os.FileInfo, error)
	Truncate(int64) error
	Sync() error
	Close() error
}

type fileHandle struct {
	backend                        *Backend
	id                             protocol.SessionID
	path                           string
	file                           logFile
	identity                       os.FileInfo
	created, transferred           bool
	fileClosed, closed, rolledBack bool
	prepared, read                 bool
	committedSize                  int64
	// A failed append whose rollback failed must be repaired before further
	// writes. Read only exposes the preceding known committed byte prefix.
	failedOffset *int64
}

var _ session.PersistenceHandle = (*fileHandle)(nil)

func (p *Backend) path(id protocol.SessionID) string {
	return filepath.Join(p.dir, base64.RawURLEncoding.EncodeToString([]byte(id))+".jsonl")
}

func (p *Backend) Create(ctx context.Context, header protocol.SessionHeader) (session.PersistenceHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := protocol.EncodeSessionHeader(header)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return nil, contracts.ErrClosed
	}
	if p.handles[header.ID] != nil {
		return nil, fmt.Errorf("%w: %s", session.ErrAlreadyExists, header.ID)
	}
	path := p.path(header.ID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w: %s", session.ErrAlreadyExists, header.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("create session log %q: %w", header.ID, err)
	}
	h := &fileHandle{backend: p, id: header.ID, path: path, file: file, created: true, prepared: true}
	p.handles[h.id] = h
	h.identity, err = file.Stat()
	if err == nil {
		err = writeAll(file, append(encoded, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, errors.Join(err, h.rollbackLocked())
	}
	h.transferred = true
	return h, nil
}

func (p *Backend) Open(ctx context.Context, id protocol.SessionID) (session.PersistenceHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return nil, contracts.ErrClosed
	}
	if p.handles[id] != nil {
		return nil, fmt.Errorf("%w: %s", session.ErrAlreadyExists, id)
	}
	path := p.path(id)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", session.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("open session log %q: %w", id, err)
	}
	h := &fileHandle{backend: p, id: id, path: path, file: file}
	p.handles[id] = h
	h.identity, err = file.Stat()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, errors.Join(err, h.closeLocked())
	}
	h.transferred = true
	return h, nil
}

func (p *Backend) Delete(ctx context.Context, id protocol.SessionID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return contracts.ErrClosed
	}
	if p.handles[id] != nil {
		return session.ErrAttached
	}
	if err := os.Remove(p.path(id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", session.ErrNotFound, id)
		}
		return fmt.Errorf("remove session log %q: %w", id, err)
	}
	return nil
}

func (h *fileHandle) Read(ctx context.Context) (protocol.SessionHeader, []protocol.SessionEvent, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionHeader{}, nil, err
	}
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.closed || h.fileClosed {
		return protocol.SessionHeader{}, nil, contracts.ErrClosed
	}
	if _, err := h.file.Seek(0, io.SeekStart); err != nil {
		return protocol.SessionHeader{}, nil, err
	}
	data, err := io.ReadAll(h.file)
	if err != nil {
		return protocol.SessionHeader{}, nil, fmt.Errorf("read session log %q: %w", h.id, err)
	}
	if h.failedOffset != nil {
		if *h.failedOffset > int64(len(data)) {
			return protocol.SessionHeader{}, nil, fmt.Errorf("%w: shortened history %s", ErrCorruptLog, h.id)
		}
		data = data[:*h.failedOffset]
	}
	header, events, size, err := decodeHistory(h.id, data)
	if err != nil {
		return protocol.SessionHeader{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return protocol.SessionHeader{}, nil, err
	}
	h.committedSize, h.read = size, true
	return header, events, nil
}

func decodeHistory(id protocol.SessionID, data []byte) (protocol.SessionHeader, []protocol.SessionEvent, int64, error) {
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return protocol.SessionHeader{}, nil, 0, fmt.Errorf("%w: no committed version=4 header for %q", ErrCorruptLog, id)
	}
	committed := data[:last+1]
	lines := bytes.Split(committed[:len(committed)-1], []byte{'\n'})
	header, err := protocol.DecodeSessionHeader(lines[0])
	if err != nil {
		return header, nil, 0, fmt.Errorf("%w: header for %q (legacy/intermediate logs are unsupported): %w", ErrCorruptLog, id, err)
	}
	if header.ID != id {
		return header, nil, 0, fmt.Errorf("%w: session header id does not match filename", ErrCorruptLog)
	}
	events := make([]protocol.SessionEvent, 0, len(lines)-1)
	for index, line := range lines[1:] {
		event, err := protocol.DecodeSessionEvent(line)
		if err != nil {
			return header, nil, 0, fmt.Errorf("%w: event %d for %q: %w", ErrCorruptLog, index, id, err)
		}
		if event.Seq != protocol.Sequence(index) {
			return header, nil, 0, fmt.Errorf("%w: %w: event %d has seq %d", ErrCorruptLog, session.ErrInvalidSequence, index, event.Seq)
		}
		events = append(events, event)
	}
	return header, events, int64(len(committed)), nil
}

func (h *fileHandle) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.closed || h.fileClosed {
		return contracts.ErrClosed
	}
	if h.prepared {
		return nil
	}
	if !h.read {
		return errors.New("JSONL history must be read and validated before preparation")
	}
	info, err := h.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() < h.committedSize {
		return fmt.Errorf("%w: shortened committed history %s", ErrCorruptLog, h.id)
	}
	if info.Size() > h.committedSize {
		if err := h.file.Truncate(h.committedSize); err != nil {
			return fmt.Errorf("truncate interrupted tail for %q: %w", h.id, err)
		}
	}
	if err := h.file.Sync(); err != nil {
		return fmt.Errorf("sync repaired tail for %q: %w", h.id, err)
	}
	h.failedOffset = nil
	h.prepared = true
	return nil
}

func (h *fileHandle) Commit(encoded []byte) error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	if h.closed || h.fileClosed {
		return contracts.ErrClosed
	}
	if !h.prepared || h.failedOffset != nil {
		return errors.New("JSONL history is not prepared for writes")
	}
	offset, err := h.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("seek session log %q: %w", h.id, err)
	}
	record := append(append([]byte(nil), encoded...), '\n')
	if err := writeAll(h.file, record); err != nil {
		return h.undoAppend(offset, fmt.Errorf("append session log %q: %w", h.id, err))
	}
	if err := h.file.Sync(); err != nil {
		return h.undoAppend(offset, fmt.Errorf("sync session log %q: %w", h.id, err))
	}
	return nil
}

func (h *fileHandle) undoAppend(offset int64, cause error) error {
	h.failedOffset = &offset
	h.prepared = false
	if err := h.file.Truncate(offset); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback append %q: %w", h.id, err))
	}
	if err := h.file.Sync(); err != nil {
		return errors.Join(cause, fmt.Errorf("sync rolled back append %q: %w", h.id, err))
	}
	h.failedOffset = nil
	h.prepared = true
	return cause
}

func (h *fileHandle) Close() error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	return h.closeLocked()
}

func (h *fileHandle) closeFileLocked() error {
	if h.fileClosed {
		return nil
	}
	if err := h.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	h.fileClosed = true
	return nil
}

func (h *fileHandle) closeLocked() error {
	if h.closed {
		return nil
	}
	if h.backend.handles[h.id] != h {
		return fmt.Errorf("%w: physical acquisition %s", session.ErrNotFound, h.id)
	}
	// A failed commit must not leave an unsuccessful complete record on disk for
	// a later Open. Keep the handle/lock until rollback has durably succeeded.
	if h.failedOffset != nil {
		if err := h.file.Truncate(*h.failedOffset); err != nil {
			return err
		}
		if err := h.file.Sync(); err != nil {
			return err
		}
		h.failedOffset = nil
	}
	if err := h.closeFileLocked(); err != nil {
		return err
	}
	delete(h.backend.handles, h.id)
	h.closed = true
	return nil
}

func (h *fileHandle) RollbackCreate() error {
	p := h.backend
	p.mu.Lock()
	defer p.mu.Unlock()
	return h.rollbackLocked()
}

func (h *fileHandle) rollbackLocked() error {
	if h.rolledBack {
		return nil
	}
	if !h.created {
		return session.ErrNotCreated
	}
	// Closing a successfully transferred handle ends its rollback authority.
	if h.closed || h.backend.handles[h.id] != h {
		return fmt.Errorf("%w: creation acquisition %s", session.ErrNotFound, h.id)
	}
	if h.identity == nil {
		info, err := h.file.Stat()
		if err != nil {
			return err
		}
		h.identity = info
	}
	if err := h.closeFileLocked(); err != nil {
		return err
	}
	info, err := os.Stat(h.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if !os.SameFile(h.identity, info) {
			return fmt.Errorf("%w: replaced creation history %s", session.ErrNotFound, h.id)
		}
		if err := os.Remove(h.path); err != nil {
			return err
		}
	}
	delete(h.backend.handles, h.id)
	h.closed, h.rolledBack = true, true
	return nil
}

func (p *Backend) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var result error
	for _, h := range p.handles {
		if h.transferred {
			result = errors.Join(result, fmt.Errorf("%w: outstanding persistence handle %s", session.ErrAttached, h.id))
		}
	}
	if result != nil {
		return result
	}
	p.closing = true
	for _, h := range p.handles {
		if h.created {
			result = errors.Join(result, h.rollbackLocked())
		} else {
			result = errors.Join(result, h.closeLocked())
		}
	}
	if result != nil {
		return result
	}
	if p.writerLock == nil {
		return nil
	}
	// Keep the inode in place; unlinking would allow a second writer lock.
	if err := p.writerLock.Close(); err != nil {
		return err
	}
	p.writerLock = nil
	return nil
}

func writeAll(file io.Writer, data []byte) error {
	for len(data) != 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
