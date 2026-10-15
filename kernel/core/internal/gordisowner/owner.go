// Package gordisowner adapts one exact Gordis Scope generation to the shared
// contribution ownership contract.
package gordisowner

import (
	"context"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
)

// Owner retains Scope identity while lease admission and cleanup remain owned
// by that exact generation.
type Owner struct {
	scope    *gordis.Scope
	identity contracts.OwnerIdentity
}

var _ contracts.ContributionOwner = (*Owner)(nil)

// New captures identity without acquiring a lease. Nil or uninitialized Scopes
// produce an unavailable owner.
func New(scope *gordis.Scope) *Owner {
	identity := contracts.OwnerIdentity{}
	if scope != nil && scope.Scope != nil {
		identity = contracts.OwnerIdentity{ID: scope.ID(), Generation: scope.Generation()}
	} else {
		scope = nil
	}

	return &Owner{scope: scope, identity: identity}
}

func (o *Owner) Identity() contracts.OwnerIdentity {
	if o == nil {
		return contracts.OwnerIdentity{}
	}
	return o.identity
}

func (o *Owner) Acquire() (func(), error) {
	if o == nil || o.scope == nil {
		return nil, contracts.ErrOwnerUnavailable
	}
	return o.scope.AcquireReady()
}

func (o *Owner) DeferCleanup(name string, cleanup func()) error {
	if o == nil || o.scope == nil {
		return contracts.ErrOwnerUnavailable
	}
	return o.scope.OnStop(name, func(context.Context) error {
		cleanup()
		return nil
	})
}
