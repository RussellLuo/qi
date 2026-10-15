package prompt

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

// Bind captures an owner without looking up a service or changing the Runtime.
func (r *Runtime) Bind(scope *gordis.Scope) Binding {
	return Binding{runtime: r, owner: gordisowner.New(scope)}
}

func (b Binding) Register(contribution Section) (func(), error) {
	if b.runtime == nil || b.runtime.sections == nil {
		return nil, contracts.ErrClosed
	}
	return b.runtime.Register(b.owner, contribution)
}
