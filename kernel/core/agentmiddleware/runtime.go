// Package agentmiddleware applies ordered behavior hooks from a run's leased
// contribution snapshot. Value hooks pass detached state between stages;
// stream hooks wrap the iterator consumed by the Agent loop.
package agentmiddleware

import (
	"context"
	"errors"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/registry"
)

type Runtime struct {
	entries *registry.Registry[Middleware]
}

func NewRuntime() *Runtime {
	return &Runtime{
		entries: registry.New(func(value Middleware) Middleware { return value }),
	}
}

func (r *Runtime) Register(owner contracts.ContributionOwner, middleware Middleware) (func(), error) {
	if middleware.ID == "" ||
		(middleware.PreStep == nil &&
			middleware.Request == nil &&
			middleware.RequestError == nil &&
			middleware.Stream == nil &&
			middleware.TurnStopping == nil) {
		return nil, errors.New("agent middleware requires id and at least one stage")
	}

	return r.entries.Register(owner, middleware.ID, middleware.Order, middleware)
}

func (r *Runtime) Prepare() (*Snapshot, error) {
	return r.PrepareSelected(nil)
}

// PrepareSelected freezes only the named Agent middleware contributions. A
// nil selection means all middleware; an empty non-nil selection means none.
func (r *Runtime) PrepareSelected(ids []string) (*Snapshot, error) {
	if r == nil || r.entries == nil {
		return nil, contracts.ErrClosed
	}

	selected := middlewareSet(ids)
	prepared, err := r.entries.PrepareWhere(func(item registry.Item[Middleware]) bool {
		if selected == nil {
			return true
		}

		_, ok := selected[item.Identity.Key]
		return ok
	})
	if err != nil {
		return nil, err
	}

	return &Snapshot{prepared: prepared}, nil
}

func middlewareSet(values []string) map[string]struct{} {
	if values == nil {
		return nil
	}

	result := make(map[string]struct{}, len(values))

	for _, value := range values {
		result[value] = struct{}{}
	}

	return result
}

func (r *Runtime) Close() {
	if r != nil && r.entries != nil {
		r.entries.Close()
	}
}

type Snapshot struct {
	prepared *registry.Snapshot[Middleware]
}

func (s *Snapshot) Revision() uint64 {
	if s == nil || s.prepared == nil {
		return 0
	}

	return s.prepared.Revision()
}

func (s *Snapshot) PreStep(ctx context.Context, input StepInput) (StepInput, error) {
	if s == nil || s.prepared == nil || s.prepared.Closed() {
		return StepInput{}, contracts.ErrClosed
	}

	current := input.Clone()

	for _, item := range s.prepared.Items() {
		if err := ctx.Err(); err != nil {
			return StepInput{}, err
		}
		if item.Value.PreStep == nil {
			continue
		}

		next, err := item.Value.PreStep(ctx, current.Clone())
		if err != nil {
			return StepInput{}, err
		}

		current = next.Clone()
	}

	if err := ctx.Err(); err != nil {
		return StepInput{}, err
	}

	return current, nil
}

func (s *Snapshot) Request(ctx context.Context, request Request) (Request, error) {
	if s == nil || s.prepared == nil || s.prepared.Closed() {
		return Request{}, contracts.ErrClosed
	}

	current := request.Clone()

	for _, item := range s.prepared.Items() {
		if err := ctx.Err(); err != nil {
			return Request{}, err
		}
		if item.Value.Request == nil {
			continue
		}

		next, err := item.Value.Request(ctx, current.Clone())
		if err != nil {
			return Request{}, err
		}

		current = next.Clone()
	}

	if err := ctx.Err(); err != nil {
		return Request{}, err
	}

	return current, nil
}

func (s *Snapshot) Stream(ctx context.Context, stream contracts.ChunkStream) (contracts.ChunkStream, error) {
	if s == nil || s.prepared == nil || s.prepared.Closed() {
		return nil, contracts.ErrClosed
	}
	if stream == nil {
		return nil, errors.New("LLM stream is nil")
	}

	current := stream

	for _, item := range s.prepared.Items() {
		if item.Value.Stream == nil {
			continue
		}

		next, err := item.Value.Stream(ctx, current)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return nil, errors.New("llm/stream middleware returned a nil stream")
		}

		current = next
	}

	return current, nil
}

func (s *Snapshot) TurnStopping(ctx context.Context, stop TurnStop) error {
	if s == nil || s.prepared == nil || s.prepared.Closed() {
		return contracts.ErrClosed
	}

	for _, item := range s.prepared.Items() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Value.TurnStopping != nil {
			if err := item.Value.TurnStopping(ctx, stop.Clone()); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Snapshot) RequestError(ctx context.Context, event RequestError) (*RequestErrorAction, error) {
	if s == nil || s.prepared == nil || s.prepared.Closed() {
		return nil, contracts.ErrClosed
	}

	var action *RequestErrorAction

	for _, item := range s.prepared.Items() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Value.RequestError == nil {
			continue
		}

		var input *RequestErrorAction
		if action != nil {
			v := *action
			input = &v
		}

		next, err := item.Value.RequestError(ctx, event.Clone(), input)
		if err != nil {
			return nil, err
		}
		if next != nil {
			if next.Kind != "retry" {
				return nil, errors.New("invalid request-error action")
			}

			v := *next
			action = &v
		} else {
			action = nil
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return action, nil
}

func (s *Snapshot) Close() {
	if s != nil && s.prepared != nil {
		s.prepared.Close()
	}
}
