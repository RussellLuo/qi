package gordisbridge

import (
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/gordisowner"
)

// ScopeOwner binds a qi contribution to one exact Gordis generation.
type ScopeOwner struct {
	owner *gordisowner.Owner
}

func NewScopeOwner(scope *gordis.Scope) *ScopeOwner {
	return &ScopeOwner{owner: gordisowner.New(scope)}
}

func (o *ScopeOwner) Identity() contracts.OwnerIdentity {
	if o == nil {
		return contracts.OwnerIdentity{}
	}

	return o.owner.Identity()
}

func (o *ScopeOwner) Acquire() (func(), error) {
	if o == nil {
		return nil, contracts.ErrOwnerUnavailable
	}

	return o.owner.Acquire()
}

func (o *ScopeOwner) DeferCleanup(name string, cleanup func()) error {
	if o == nil {
		return contracts.ErrOwnerUnavailable
	}

	return o.owner.DeferCleanup(name, cleanup)
}
