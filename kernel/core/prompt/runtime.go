// Package prompt renders ordered, owner-bound sections from a frozen selection.
// A prepared snapshot leases its sections until rendering has drained.
package prompt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
	toolsapi "github.com/RussellLuo/qi/kernel/core/tools"
)

type Runtime struct {
	sections *registry.Registry[Section]
}

func NewRuntime() *Runtime {
	return &Runtime{sections: registry.New(cloneSection)}
}

func (r *Runtime) Register(owner contracts.ContributionOwner, section Section) (func(), error) {
	if section.ID == "" || section.Render == nil {
		return nil, errors.New("prompt section requires id and render function")
	}

	return r.sections.Register(owner, section.ID, section.Order, section)
}

func (r *Runtime) Prepare() (*Prepared, error) {
	return r.PrepareSelected(nil)
}

// PrepareSelected freezes only the named Prompt sections. A nil selection
// means all sections; a non-nil empty selection intentionally renders none.
func (r *Runtime) PrepareSelected(ids []string) (*Prepared, error) {
	if r == nil || r.sections == nil {
		return nil, contracts.ErrClosed
	}

	selected := selectionSet(ids)
	snapshot, err := r.sections.PrepareWhere(func(item registry.Item[Section]) bool {
		if selected == nil {
			return true
		}

		_, ok := selected[item.Identity.Key]
		return ok
	})
	if err != nil {
		return nil, err
	}

	return &Prepared{snapshot: snapshot}, nil
}

func selectionSet(values []string) map[string]struct{} {
	if values == nil {
		return nil
	}

	result := make(map[string]struct{}, len(values))

	for _, value := range values {
		result[value] = struct{}{}
	}

	return result
}

func (r *Runtime) Assemble(ctx context.Context, promptContext Context) (Snapshot, error) {
	prepared, err := r.Prepare()
	if err != nil {
		return Snapshot{}, err
	}

	defer prepared.Close()

	return prepared.Assemble(ctx, promptContext)
}

func (r *Runtime) Close() {
	if r != nil && r.sections != nil {
		r.sections.Close()
	}
}

type Prepared struct {
	mu       sync.Mutex
	closed   bool
	wait     sync.WaitGroup
	snapshot *registry.Snapshot[Section]
}

func (p *Prepared) Revision() uint64 {
	if p == nil || p.snapshot == nil {
		return 0
	}

	return p.snapshot.Revision()
}

func (p *Prepared) Assemble(ctx context.Context, promptContext Context) (Snapshot, error) {
	if p == nil || p.snapshot == nil {
		return Snapshot{}, contracts.ErrClosed
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return Snapshot{}, contracts.ErrClosed
	}

	p.wait.Add(1)
	p.mu.Unlock()
	defer p.wait.Done()

	items := p.snapshot.Items()
	if p.snapshot.Closed() {
		return Snapshot{}, contracts.ErrClosed
	}

	// Renderers receive detached context so one section cannot mutate the
	// inputs observed by later sections.
	contextCopy := cloneContext(promptContext)
	result := Snapshot{Revision: p.snapshot.Revision()}
	parts := make([]string, 0, len(items))

	for _, item := range items {
		text, err := item.Value.Render(ctx, cloneContext(contextCopy))
		if err != nil {
			return Snapshot{}, fmt.Errorf("render prompt section %q: %w", item.Value.ID, err)
		}

		text = strings.TrimSpace(text)
		result.SectionIDs = append(result.SectionIDs, item.Value.ID)
		result.Diagnostics = append(result.Diagnostics, Diagnostic{SectionID: item.Value.ID, Bytes: len(text)})
		if text != "" {
			parts = append(parts, text)
		}
	}

	result.Text = strings.Join(parts, "\n\n")

	return result, nil
}

func (p *Prepared) Close() {
	if p == nil || p.snapshot == nil {
		return
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}

	p.closed = true
	p.mu.Unlock()
	p.wait.Wait()
	p.snapshot.Close()
}

func cloneSection(section Section) Section { return section }

func cloneContext(value Context) Context {
	tools := value.Tools
	value.Tools = make([]toolsapi.Schema, len(tools))

	for i, tool := range tools {
		value.Tools[i] = tool.Clone()
	}

	return value
}
