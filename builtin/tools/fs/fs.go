// Package fstool implements local UTF-8 file tools independently of
// plugin registration and the tool runtime.
package fstool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	DefaultReadLimit         = 2000
	DefaultReadMaxLineLength = 2000
	DefaultReadMaxBytes      = 50 * 1024
	DefaultDiffBasisMaxBytes = 10 * 1024 * 1024
)

// Options contains resolved limits. Use DefaultOptions to start with defaults;
// New rejects every non-positive limit, including an explicitly supplied zero.
type Options struct {
	CWD               string
	ReadLimit         int
	ReadMaxLineLength int
	ReadMaxBytes      int
	DiffBasisMaxBytes int
}

func DefaultOptions(cwd string) Options {
	return Options{
		CWD:               cwd,
		ReadLimit:         DefaultReadLimit,
		ReadMaxLineLength: DefaultReadMaxLineLength,
		ReadMaxBytes:      DefaultReadMaxBytes,
		DiffBasisMaxBytes: DefaultDiffBasisMaxBytes,
	}
}

// FS owns file access and target locks. CWD is a fallback base, not confinement.
// Close waits for ongoing operations and is idempotent.
// The caller must retain FS for as long as its tool definitions can execute.
type FS struct {
	options Options
	life    sync.RWMutex
	closed  bool
	mu      sync.Mutex
	writes  map[string]*pathLock
}

type pathLock struct {
	token chan struct{}
	refs  int
}

func New(options Options) (*FS, error) {
	if options.CWD == "" {
		var err error
		options.CWD, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	} else if isBlank(options.CWD) {
		return nil, errors.New("cwd must be a non-empty directory path")
	}
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"read_limit", options.ReadLimit}, {"read_max_line_length", options.ReadMaxLineLength},
		{"read_max_bytes", options.ReadMaxBytes},
		{"diff_basis_max_bytes", options.DiffBasisMaxBytes},
	} {
		if limit.value <= 0 {
			return nil, fmt.Errorf("%s must be a positive integer", limit.name)
		}
	}
	abs, err := filepath.Abs(options.CWD)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("cwd must be a directory")
	}
	options.CWD = abs
	return &FS{options: options, writes: make(map[string]*pathLock)}, nil
}

func (f *FS) Close() error {
	f.life.Lock()
	defer f.life.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	return nil
}

func (f *FS) begin(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.life.RLock()
	if f.closed {
		f.life.RUnlock()
		return nil, os.ErrClosed
	}
	return f.life.RUnlock, nil
}

// lockWrite serializes calls for a normalized path, without retaining a lock
// for every path ever written. Waiting can be canceled.
func (f *FS) lockWrite(ctx context.Context, path string) (func(), error) {
	f.mu.Lock()
	l := f.writes[path]
	if l == nil {
		l = &pathLock{token: make(chan struct{}, 1)}
		l.token <- struct{}{}
		f.writes[path] = l
	}
	l.refs++
	f.mu.Unlock()
	drop := func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		l.refs--
		if l.refs == 0 {
			delete(f.writes, path)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-l.token:
		return func() { l.token <- struct{}{}; drop() }, nil
	}
}
