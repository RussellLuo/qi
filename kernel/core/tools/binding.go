package tools

import (
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/gordisowner"
)

// Binding registers contributions under one exact Scope generation.
// It borrows the Runtime and does not acquire a Ready lease.
type Binding struct {
	runtime *Runtime
	owner   contracts.ContributionOwner
}

// Bind captures the contribution owner without looking up a service or changing
// the Runtime. Operation errors are returned by the registration methods.
func (r *Runtime) Bind(scope *gordis.Scope) Binding {
	return Binding{runtime: r, owner: gordisowner.New(scope)}
}

func (b Binding) Register(definition Definition) (func(), error) {
	if b.runtime == nil || b.runtime.contributions == nil {
		return nil, contracts.ErrClosed
	}
	return b.runtime.Register(b.owner, definition)
}

func (b Binding) RegisterBatch(definitions []Definition) (func(), error) {
	if b.runtime == nil || b.runtime.contributions == nil {
		return nil, contracts.ErrClosed
	}
	return b.runtime.RegisterBatch(b.owner, definitions)
}

func (b Binding) RegisterMiddleware(middleware Middleware) (func(), error) {
	if b.runtime == nil || b.runtime.contributions == nil {
		return nil, contracts.ErrClosed
	}
	return b.runtime.RegisterMiddleware(b.owner, middleware)
}
