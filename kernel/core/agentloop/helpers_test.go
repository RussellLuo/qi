package agentloop

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

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

type scriptedScript struct {
	Chunks        []protocol.StreamChunk
	Err           error
	WaitForCancel bool
}

type scriptedAdapter struct {
	mu      sync.Mutex
	Version string
	Scripts []scriptedScript
	Seen    []llm.Request
}

func (a *scriptedAdapter) ResolveModel(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	if ref.Provider == "" || ref.Model == "" {
		return llm.ModelInfo{}, errors.New("provider and model are required")
	}

	return llm.ModelInfo{
		Ref:           ref,
		ContextWindow: 4096,
		Capabilities:  []string{"tools", a.Version},
	}, nil
}

func (a *scriptedAdapter) Stream(ctx context.Context, request llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	a.mu.Lock()
	a.Seen = append(a.Seen, request.Clone())
	if len(a.Scripts) == 0 {
		a.mu.Unlock()
		return func(yield func(protocol.StreamChunk, error) bool) {
			yield(protocol.StreamChunk{}, errors.New("script exhausted"))
		}
	}

	script := a.Scripts[0]
	a.Scripts = a.Scripts[1:]
	a.mu.Unlock()
	return func(yield func(protocol.StreamChunk, error) bool) {
		if script.WaitForCancel {
			<-ctx.Done()
			yield(protocol.StreamChunk{}, ctx.Err())
			return
		}

		for _, chunk := range script.Chunks {
			if !yield(chunk, nil) {
				return
			}
		}

		if script.Err != nil {
			yield(protocol.StreamChunk{}, script.Err)
		}
	}
}
