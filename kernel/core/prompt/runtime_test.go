package prompt

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	toolsapi "github.com/RussellLuo/qi/kernel/core/tools"
)

func TestPromptStableOrderAndSnapshotIsolation(t *testing.T) {
	runtime := NewRuntime()
	owned := newTestOwner("prompt", 1)
	sections := []Section{
		{
			ID:     "z",
			Order:  10,
			Render: static("z"),
		},
		{
			ID:     "b",
			Order:  0,
			Render: static("b"),
		},
		{
			ID:     "a",
			Order:  10,
			Render: static("a"),
		},
	}

	for _, section := range sections {
		if _, err := runtime.Register(owned, section); err != nil {
			t.Fatal(err)
		}
	}

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}

	defer prepared.Close()

	if _, err := runtime.Register(newTestOwner("late", 1), Section{
		ID:     "late",
		Order:  -1,
		Render: static("late"),
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := prepared.Assemble(context.Background(), Context{})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"b", "a", "z"}; !reflect.DeepEqual(snapshot.SectionIDs, want) {
		t.Fatalf("section order = %v, want %v", snapshot.SectionIDs, want)
	}

	if snapshot.Text != "b\n\na\n\nz" {
		t.Fatalf("prompt = %q", snapshot.Text)
	}
}

func TestPromptRenderFailureReturnsNoPartialSnapshot(t *testing.T) {
	runtime := NewRuntime()
	owned := newTestOwner("prompt", 1)
	if _, err := runtime.Register(owned, Section{ID: "first", Render: static("partial")}); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("render failed")
	if _, err := runtime.Register(owned, Section{
		ID:    "second",
		Order: 1,
		Render: func(context.Context, Context) (string, error) {
			return "", wantErr
		},
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := runtime.Assemble(context.Background(), Context{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Assemble error = %v", err)
	}

	if !reflect.DeepEqual(snapshot, Snapshot{}) {
		t.Fatalf("partial snapshot escaped: %+v", snapshot)
	}
}

func TestPromptToolContext(t *testing.T) {
	echo := toolsapi.Schema{
		Name:         "echo",
		Description:  "Echo text",
		Parameters:   toolsapi.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}}}`),
		DeferLoading: true,
	}

	tests := []struct {
		name  string
		tools []toolsapi.Schema
	}{
		{name: "echo", tools: []toolsapi.Schema{echo}},
		{
			name: "multiple tools",
			tools: []toolsapi.Schema{
				echo,
				{
					Name:        "search",
					Description: "Search documents",
					Parameters:  toolsapi.JSONSchema(`{"type":"object","properties":{"query":{"type":"string"}}}`),
				},
			},
		},
		{name: "nil tools"},
		{name: "empty tools", tools: []toolsapi.Schema{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewRuntime()
			defer runtime.Close()

			// Keep the expected schemas independent of the caller's mutable input.
			input := Context{
				AgentID:   "audit-agent",
				SessionID: "audit-session",
				Tools:     make([]toolsapi.Schema, len(test.tools)),
			}

			for i, tool := range test.tools {
				input.Tools[i] = tool.Clone()
			}

			if test.tools == nil {
				input.Tools = nil
			}

			assertContext := func(got Context) {
				t.Helper()

				if got.AgentID != input.AgentID || got.SessionID != input.SessionID {
					t.Errorf("renderer identity = %q/%q, want %q/%q", got.AgentID, got.SessionID, input.AgentID, input.SessionID)
				}

				if len(got.Tools) != len(test.tools) {
					t.Fatalf("renderer tool count = %d, want %d", len(got.Tools), len(test.tools))
				}

				for i, want := range test.tools {
					if !reflect.DeepEqual(got.Tools[i], want) {
						t.Errorf("renderer tool[%d] = %+v, want %+v", i, got.Tools[i], want)
					}
				}
			}

			owned := newTestOwner("audit", 1)
			sections := []Section{
				{
					ID:    "mutate",
					Order: 10,
					Render: func(_ context.Context, got Context) (string, error) {
						assertContext(got)

						for i := range got.Tools {
							if len(got.Tools[i].Parameters) > 0 {
								got.Tools[i].Parameters[0] = '['
							}

							got.Tools[i] = toolsapi.Schema{Name: "mutated"}
						}

						return "first", nil
					},
				},
				{
					ID:    "observe",
					Order: 20,
					Render: func(_ context.Context, got Context) (string, error) {
						assertContext(got)
						return "second", nil
					},
				},
			}

			for _, section := range sections {
				if _, err := runtime.Register(owned, section); err != nil {
					t.Fatal(err)
				}
			}

			snapshot, err := runtime.Assemble(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}

			assertContext(input)

			if snapshot.Text != "first\n\nsecond" || !reflect.DeepEqual(snapshot.SectionIDs, []string{"mutate", "observe"}) {
				t.Fatalf("assembled snapshot = %+v", snapshot)
			}
		})
	}
}

func static(value string) func(context.Context, Context) (string, error) {
	return func(context.Context, Context) (string, error) { return value, nil }
}

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
