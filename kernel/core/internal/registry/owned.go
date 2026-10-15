// Package registry stores owner-bound contributions and leases immutable
// snapshots. Disposing a registration changes future selection; snapshots keep
// their acquired owner generations alive until Close.
package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
)

var (
	ErrDuplicate = errors.New("registry key already exists")
	ErrNotFound  = errors.New("registry key not found")
)

// Identity distinguishes a registration from later entries using the same key.
type Identity struct {
	Key          string
	Registration uint64
	Owner        contracts.OwnerIdentity
}

type entry[T any] struct {
	identity Identity
	order    int
	value    T
	owner    contracts.ContributionOwner
}

type Item[T any] struct {
	Identity Identity
	Order    int
	Value    T
}

// Registration is one member of an atomic owner-bound registration batch.
type Registration[T any] struct {
	Key   string
	Order int
	Value T
}

type Registry[T any] struct {
	mu       sync.Mutex
	closed   bool
	revision uint64
	nextID   uint64
	entries  map[string]entry[T]
	clone    func(T) T
}

func New[T any](clone func(T) T) *Registry[T] {
	if clone == nil {
		clone = func(value T) T { return value }
	}

	return &Registry[T]{entries: make(map[string]entry[T]), clone: clone}
}

// Register binds an exact registration to one owner generation. The returned
// disposer is idempotent and can never remove a later registration with the
// same key.
func (r *Registry[T]) Register(owner contracts.ContributionOwner, key string, order int, value T) (func(), error) {
	return r.RegisterBatch(owner, []Registration[T]{{
		Key:   key,
		Order: order,
		Value: value,
	}})
}

// RegisterBatch publishes every registration under one registry lock and
// withdraws the exact batch with one owner cleanup. A validation or conflict
// failure leaves the registry unchanged.
func (r *Registry[T]) RegisterBatch(owner contracts.ContributionOwner, registrations []Registration[T]) (func(), error) {
	if owner == nil || !owner.Identity().Valid() {
		return nil, fmt.Errorf("%w: invalid identity", contracts.ErrOwnerUnavailable)
	}
	if len(registrations) == 0 {
		return nil, errors.New("registry batch is empty")
	}

	seen := make(map[string]struct{}, len(registrations))

	for _, registration := range registrations {
		if registration.Key == "" {
			return nil, errors.New("registry key is required")
		}
		if _, ok := seen[registration.Key]; ok {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, registration.Key)
		}

		seen[registration.Key] = struct{}{}
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, contracts.ErrClosed
	}

	for _, registration := range registrations {
		if _, ok := r.entries[registration.Key]; ok {
			r.mu.Unlock()
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, registration.Key)
		}
	}

	registered := make(map[string]uint64, len(registrations))
	// Assign a fresh identity to every entry before publishing the batch.
	// Disposers compare these identities, so stale cleanup cannot erase a replacement.

	for _, registration := range registrations {
		r.nextID++
		current := entry[T]{
			identity: Identity{
				Key:          registration.Key,
				Registration: r.nextID,
				Owner:        owner.Identity(),
			},
			order: registration.Order,
			value: r.clone(registration.Value),
			owner: owner,
		}
		r.entries[registration.Key] = current
		registered[registration.Key] = current.identity.Registration
	}

	r.revision++
	r.mu.Unlock()

	var once sync.Once
	dispose := func() {
		once.Do(func() { r.removeBatchExact(registered) })
	}
	cleanupName := fmt.Sprintf("unregister %d registry entries", len(registrations))
	if len(registrations) == 1 {
		cleanupName = "unregister " + registrations[0].Key
	}

	if err := owner.DeferCleanup(cleanupName, dispose); err != nil {
		dispose()
		return nil, fmt.Errorf("register batch cleanup: %w", err)
	}

	return dispose, nil
}

func (r *Registry[T]) removeBatchExact(registrations map[string]uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := false

	for key, registration := range registrations {
		current, ok := r.entries[key]
		if !ok || current.identity.Registration != registration {
			continue
		}

		delete(r.entries, key)
		removed = true
	}

	if removed {
		r.revision++
	}
}

func (r *Registry[T]) Revision() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.revision
}

func (r *Registry[T]) Prepare() (*Snapshot[T], error) {
	return r.PrepareWhere(nil)
}

// PrepareWhere freezes and leases only entries accepted by include. A nil
// predicate preserves the ordinary complete-registry snapshot. Selection is
// evaluated before owner acquisition so an Agent-scoped composition does not
// pin contributions outside that composition.
func (r *Registry[T]) PrepareWhere(include func(Item[T]) bool) (*Snapshot[T], error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, contracts.ErrClosed
	}

	revision := r.revision
	entries := make([]entry[T], 0, len(r.entries))

	for _, current := range r.entries {
		current.value = r.clone(current.value)
		if include != nil && !include(Item[T]{
			Identity: current.identity,
			Order:    current.order,
			Value:    current.value,
		}) {
			continue
		}

		entries = append(entries, current)
	}

	r.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}

		return entries[i].identity.Key < entries[j].identity.Key
	})

	// Acquire owners outside the registry lock: owner callbacks may coordinate
	// their own shutdown. Any acquisition failure releases the partial snapshot.
	releases := make([]func(), 0, len(entries))
	items := make([]Item[T], 0, len(entries))

	for _, current := range entries {
		release, err := current.owner.Acquire()
		if err != nil {
			releaseReverse(releases)
			return nil, fmt.Errorf("acquire %q owned by %s/%d: %w", current.identity.Key,
				current.identity.Owner.ID, current.identity.Owner.Generation, err)
		}

		if release == nil {
			release = func() {}
		}

		releases = append(releases, release)
		items = append(items, Item[T]{
			Identity: current.identity,
			Order:    current.order,
			Value:    current.value,
		})
	}

	return &Snapshot[T]{
		revision: revision,
		items:    items,
		releases: releases,
		clone:    r.clone,
	}, nil
}

func (r *Registry[T]) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return
	}

	r.closed = true
	r.entries = nil
	r.revision++
}

// Snapshot holds detached values and the release callbacks for their owners.
type Snapshot[T any] struct {
	mu       sync.Mutex
	closed   bool
	revision uint64
	items    []Item[T]
	releases []func()
	clone    func(T) T
}

func (s *Snapshot[T]) Revision() uint64 {
	if s == nil {
		return 0
	}

	return s.revision
}

func (s *Snapshot[T]) Closed() bool {
	if s == nil {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

func (s *Snapshot[T]) Items() []Item[T] {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	items := make([]Item[T], len(s.items))

	for i, item := range s.items {
		items[i] = item
		items[i].Value = s.clone(item.Value)
	}

	return items
}

func (s *Snapshot[T]) Close() {
	if s == nil {
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	releases := s.releases
	s.releases = nil
	s.items = nil
	s.mu.Unlock()
	releaseReverse(releases)
}

func releaseReverse(releases []func()) {
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
}
