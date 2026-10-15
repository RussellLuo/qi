package registry

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
)

func TestOwnedRegistryExactDisposerAndImmutableSnapshot(t *testing.T) {
	registry := New(func(values []string) []string { return append([]string(nil), values...) })
	firstOwner := newTestOwner("plugin", 1)
	disposeFirst, err := registry.Register(firstOwner, "entry", 0, []string{"v1"})
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := registry.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	disposeFirst()
	secondOwner := newTestOwner("plugin", 2)
	disposeSecond, err := registry.Register(secondOwner, "entry", 0, []string{"v2"})
	if err != nil {
		t.Fatal(err)
	}

	disposeFirst()
	items := prepared.Items()
	items[0].Value[0] = "mutated"
	if got := prepared.Items()[0].Value[0]; got != "v1" {
		t.Fatalf("snapshot was mutable: %q", got)
	}

	current, err := registry.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	if got := current.Items()[0].Value[0]; got != "v2" {
		t.Fatalf("stale disposer removed replacement: %q", got)
	}

	prepared.Close()
	prepared.Close()
	current.Close()
	disposeSecond()
}

func TestOwnedRegistryPrepareReleasesPartialAcquisition(t *testing.T) {
	registry := New[int](nil)
	var mu sync.Mutex
	var released []string
	first := &recordingOwner{
		identity: contracts.OwnerIdentity{ID: "first", Generation: 1},
		label:    "first",
		mu:       &mu,
		released: &released,
	}
	second := &recordingOwner{
		identity: contracts.OwnerIdentity{ID: "second", Generation: 1},
		label:    "second",
		mu:       &mu,
		released: &released,
	}
	unavailable := &unavailableOwner{identity: contracts.OwnerIdentity{ID: "unavailable", Generation: 1}}
	if _, err := registry.Register(first, "a", 0, 1); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Register(second, "b", 1, 2); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Register(unavailable, "c", 2, 3); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Prepare(); !errors.Is(err, contracts.ErrOwnerUnavailable) {
		t.Fatalf("Prepare error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if got, want := released, []string{"second", "first"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("partial leases released in order %v, want %v", got, want)
	}
}

func TestOwnedRegistryBatchIsAtomicAndUsesExactDisposer(t *testing.T) {
	registry := New[int](nil)
	first := newTestOwner("package", 1)
	dispose, err := registry.RegisterBatch(first, []Registration[int]{
		{Key: "tool/a", Value: 1},
		{Key: "tool/b", Value: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	before := registry.Revision()
	if _, err := registry.RegisterBatch(newTestOwner("conflict", 1), []Registration[int]{
		{Key: "tool/c", Value: 3},
		{Key: "tool/a", Value: 4},
	}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("conflicting batch error = %v", err)
	}

	if registry.Revision() != before {
		t.Fatal("failed batch changed the registry revision")
	}

	snapshot, err := registry.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	items := snapshot.Items()
	snapshot.Close()
	if len(items) != 2 || items[0].Identity.Key != "tool/a" || items[1].Identity.Key != "tool/b" {
		t.Fatalf("batch items = %+v", items)
	}

	dispose()
	dispose()
	if snapshot, err := registry.Prepare(); err != nil {
		t.Fatal(err)
	} else {
		defer snapshot.Close()

		if len(snapshot.Items()) != 0 {
			t.Fatalf("batch remained after dispose: %+v", snapshot.Items())
		}
	}
}

func TestOwnedRegistryConcurrentPrepareRegisterDispose(t *testing.T) {
	registry := New[int](nil)
	const workers = 8
	var wait sync.WaitGroup

	for worker := 0; worker < workers; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()

			for generation := 1; generation <= 50; generation++ {
				owned := newTestOwner("worker", uint64(worker*100+generation))
				key := string(rune('a' + worker))
				dispose, err := registry.Register(owned, key, worker, generation)
				if err != nil {
					t.Errorf("Register: %v", err)
					return
				}

				prepared, err := registry.Prepare()
				if err != nil && !errors.Is(err, contracts.ErrOwnerUnavailable) {
					t.Errorf("Prepare: %v", err)
					return
				}

				if prepared != nil {
					_ = prepared.Items()
					prepared.Close()
				}

				dispose()
				if err := owned.Stop(context.Background()); err != nil {
					t.Errorf("Stop: %v", err)
					return
				}
			}
		}()
	}

	wait.Wait()
	registry.Close()
	if _, err := registry.Register(newTestOwner("late", 1), "late", 0, 0); !errors.Is(err, contracts.ErrClosed) {
		t.Fatalf("Register after Close error = %v", err)
	}
}

type unavailableOwner struct {
	identity contracts.OwnerIdentity
}

func (o *unavailableOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (*unavailableOwner) Acquire() (func(), error) { return nil, contracts.ErrOwnerUnavailable }

func (*unavailableOwner) DeferCleanup(string, func()) error { return nil }

type recordingOwner struct {
	identity contracts.OwnerIdentity
	label    string
	mu       *sync.Mutex
	released *[]string
}

func (o *recordingOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *recordingOwner) Acquire() (func(), error) {
	return func() {
		o.mu.Lock()
		*o.released = append(*o.released, o.label)
		o.mu.Unlock()
	}, nil
}

func (*recordingOwner) DeferCleanup(string, func()) error { return nil }

// testOwner is a deterministic ContributionOwner for runtime and conformance
// tests. Stop rejects new acquisitions, waits for admitted work, and then runs
// cleanup callbacks in reverse registration order.
type testOwner struct {
	mu         sync.Mutex
	identity   contracts.OwnerIdentity
	stopping   bool
	stopped    bool
	active     int
	cleanups   []func()
	activeZero chan struct{}
}

func newTestOwner(id string, generation uint64) *testOwner {
	return &testOwner{identity: contracts.OwnerIdentity{ID: id, Generation: generation}}
}

func (o *testOwner) Identity() contracts.OwnerIdentity { return o.identity }

func (o *testOwner) Acquire() (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return nil, contracts.ErrOwnerUnavailable
	}

	o.active++
	var once sync.Once

	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.active--
			if o.active == 0 && o.activeZero != nil {
				close(o.activeZero)
				o.activeZero = nil
			}

			o.mu.Unlock()
		})
	}, nil
}

func (o *testOwner) DeferCleanup(_ string, cleanup func()) error {
	if cleanup == nil {
		return errors.New("cleanup is required")
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.stopping || o.stopped {
		return contracts.ErrOwnerUnavailable
	}

	o.cleanups = append(o.cleanups, cleanup)

	return nil
}

func (o *testOwner) Stop(ctx context.Context) error {
	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	if !o.stopping {
		o.stopping = true
	}

	wait := o.activeZero
	if o.active > 0 && wait == nil {
		wait = make(chan struct{})
		o.activeZero = wait
	}

	o.mu.Unlock()

	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	o.mu.Lock()
	if o.stopped {
		o.mu.Unlock()
		return nil
	}

	cleanups := o.cleanups
	o.cleanups = nil
	o.stopped = true
	o.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}

	return nil
}
