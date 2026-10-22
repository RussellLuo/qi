package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
)

// Export bridges an isolated Runtime's activation-time Tool contributions. Business
// plugins use the ordinary RuntimeKey and Bind/Register API. Only the framework
// creates, seals and closes Export; local Runtimes retain their normal behavior.
// Middleware, local preparation and mutations after Seal retire this export.
// Idle exports own no leases; every raw invocation pins its original owner.
type Export struct {
	mu           sync.Mutex
	runtime      *Runtime
	phase        string
	err          error
	done, closed chan struct{}
	entries      map[string]registry.Identity
	descriptions []DefinitionInfo
	wait         sync.WaitGroup
}

func NewExport() *Export {
	e := &Export{phase: "collect", done: make(chan struct{}), closed: make(chan struct{})}
	e.runtime = NewRuntime()
	e.runtime.export = e
	return e
}

func (e *Export) Runtime() *Runtime { return e.runtime }

// Done closes on unsupported operations, original owner stop, or framework Close.
// A bridge must withdraw its contributions when Done closes.
func (e *Export) Done() <-chan struct{} { return e.done }
func (e *Export) Err() error            { e.mu.Lock(); defer e.mu.Unlock(); return e.err }

func (e *Export) retireLocked(phase, operation string, owner contracts.OwnerIdentity, names []string, cause error) error {
	if e.err == nil {
		e.err = fmt.Errorf("tool export phase=%s operation=%s owner=%s/%d tools=%v: %w", phase, operation, owner.ID, owner.Generation, names, cause)
		e.phase = phase
		close(e.done)
	}
	return e.err
}
func (r *Runtime) rejectExport(operation string, owner contracts.ContributionOwner) error {
	if r == nil || r.export == nil {
		return nil
	}
	e := r.export
	e.mu.Lock()
	defer e.mu.Unlock()
	var id contracts.OwnerIdentity
	if owner != nil {
		id = owner.Identity()
	}
	return e.retireLocked(e.phase, operation, id, nil, fmt.Errorf("unsupported operation; export retired"))
}

// Seal freezes the actual compiled descriptions after business activation. No
// precompiled business metadata or permanent owner snapshot is required. Repeated
// Seal is harmless. Failed sealing permanently retires the export.
func (e *Export) Seal() (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if e.phase == "ready" {
		return nil
	}
	defer func() {
		if err != nil {
			err = e.retireLocked(e.phase, "seal", contracts.OwnerIdentity{}, nil, err)
		}
	}()
	s, err := e.runtime.contributions.Prepare()
	if err != nil {
		return err
	}
	defer s.Close()
	entries := make(map[string]registry.Identity)
	var ds []DefinitionInfo
	for _, item := range s.Items() {
		if item.Value.definition == nil {
			return fmt.Errorf("unsupported non-tool contribution")
		}
		d := item.Value.definition.definition
		entries[d.Name] = item.Identity
		ds = append(ds, DefinitionInfo{Name: d.Name, Description: d.Description, Parameters: d.Parameters.Clone(), Output: d.Output.Clone(), Timeout: d.Timeout, FailureMode: d.FailureMode, DeferLoading: d.DeferLoading})
	}
	if len(entries) == 0 {
		return fmt.Errorf("tool export is empty")
	}
	e.entries, e.descriptions, e.phase = entries, ds, "ready"
	return nil
}
func (e *Export) readyLocked() error {
	if e.err != nil {
		return e.err
	}
	if e.phase != "ready" {
		return fmt.Errorf("tool export is not sealed")
	}
	return nil
}

// Describe returns independent copies of the sealed compiled descriptions.
func (e *Export) Describe(ctx context.Context) ([]DefinitionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.readyLocked(); err != nil {
		return nil, err
	}
	ds := make([]DefinitionInfo, len(e.descriptions))
	for i, d := range e.descriptions {
		d.Parameters = d.Parameters.Clone()
		d.Output = d.Output.Clone()
		ds[i] = d
	}
	return ds, nil
}

// Execute invokes only Definition.Execute, with no middleware, schema validation,
// timeout, result validation or failure policy. The original owner generation is
// held until the actual worker returns, including cancellation or panic. Close
// must run outside owner OnStop callbacks so that it can wait for these workers.
func (e *Export) Execute(ctx context.Context, input Execution) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	e.mu.Lock()
	if err := e.readyLocked(); err != nil {
		e.mu.Unlock()
		return ExecutionResult{}, err
	}
	id, ok := e.entries[input.Call.Name]
	if !ok {
		e.mu.Unlock()
		return ExecutionResult{}, fmt.Errorf("%w: %s", ErrNotFound, input.Call.Name)
	}
	// Selection includes the exact registration and owner generation, never a
	// replacement registered under the same Tool name.
	lease, err := e.runtime.contributions.PrepareWhere(func(i registry.Item[contribution]) bool { return i.Identity == id })
	if err != nil {
		e.mu.Unlock()
		return ExecutionResult{}, err
	}
	items := lease.Items()
	if len(items) != 1 {
		lease.Close()
		e.mu.Unlock()
		return ExecutionResult{}, contracts.ErrOwnerUnavailable
	}
	e.wait.Add(1)
	e.mu.Unlock()
	defer e.wait.Done()
	defer lease.Close()
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	return items[0].Value.definition.definition.Execute(ctx, input.Clone())
}

// Close stops admission, signals retirement, drains workers and closes Runtime.
// Concurrent callers wait for the same cleanup. Uncooperative code can delay it.
func (e *Export) Close() {
	e.mu.Lock()
	if e.phase == "closed" {
		done := e.closed
		e.mu.Unlock()
		<-done
		return
	}
	e.retireLocked(e.phase, "close", contracts.OwnerIdentity{}, nil, contracts.ErrClosed)
	e.phase = "closed"
	e.mu.Unlock()
	e.wait.Wait()
	e.runtime.contributions.Close()
	close(e.closed)
}

type exportOwner struct {
	contracts.ContributionOwner
	export *Export
	names  []string
}

func (o exportOwner) DeferCleanup(name string, cleanup func()) error {
	return o.ContributionOwner.DeferCleanup(name, func() {
		cleanup()
		e := o.export
		e.mu.Lock()
		defer e.mu.Unlock()
		e.retireLocked("stopping", "owner-stop", o.Identity(), o.names, contracts.ErrOwnerUnavailable)
	})
}
