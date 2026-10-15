package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Registry lends published Agents. It owns identity tokens, never worker or Session resources.
type Registry interface {
	Get(context.Context, ID) (Agent, error)
	Snapshot() []Snapshot
}

type entry struct {
	id        ID
	agent     *loopAgent
	published bool
}

type memoryRegistry struct {
	mu      sync.RWMutex
	entries map[ID]*entry
}

func NewRegistry() Registry { return &memoryRegistry{entries: make(map[ID]*entry)} }

func (r *memoryRegistry) reserve(id ID, parentID ID) (*entry, *ParentView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[id] != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrExists, id)
	}
	var parent *ParentView
	if parentID != "" {
		e := r.entries[parentID]
		if e == nil || !e.published {
			return nil, nil, fmt.Errorf("parent %w: %s", ErrNotFound, parentID)
		}
		e.agent.mu.Lock()
		if e.agent.disposed {
			e.agent.mu.Unlock()
			return nil, nil, ErrClosed
		}
		parent = &ParentView{ID: parentID, SessionID: e.agent.sessionID, Config: cloneConfig(e.agent.config)}
		e.agent.mu.Unlock()
	}
	e := &entry{id: id}
	r.entries[id] = e
	return e, parent, nil
}

func (r *memoryRegistry) publish(e *entry, a *loopAgent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[e.id] != e || e.published {
		return ErrClosed
	}
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	e.agent, e.published = a, true
	return nil
}

func (r *memoryRegistry) withdraw(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[e.id] == e {
		e.published = false
	}
}

func (r *memoryRegistry) remove(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries[e.id] == e {
		delete(r.entries, e.id)
	}
}

func (r *memoryRegistry) Get(ctx context.Context, id ID) (Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.entries[id]
	if e == nil || !e.published || e.agent.Disposed() {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return e.agent, nil
}

func (r *memoryRegistry) Snapshot() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := []Snapshot{}
	for _, e := range r.entries {
		if !e.published {
			continue
		}
		a := e.agent
		a.mu.Lock()
		if !a.disposed {
			result = append(result, Snapshot{ID: a.id, SessionID: protocol.SessionID(a.id), ParentID: a.parentID, Status: a.status})
		}
		a.mu.Unlock()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
