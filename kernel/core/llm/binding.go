package llm

import (
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/gordisowner"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

// Binding registers contributions under one exact Scope generation.
// It borrows the Runtime and does not acquire a Ready lease or own transports.
type Binding struct {
	runtime *Runtime
	owner   contracts.ContributionOwner
}

// Bind captures an owner without looking up a service or changing the Runtime.
func (r *Runtime) Bind(scope *gordis.Scope) Binding {
	return Binding{runtime: r, owner: gordisowner.New(scope)}
}

func (b Binding) RegisterAdapter(registration AdapterRegistration) (func(), error) {
	if b.runtime == nil || b.runtime.routes == nil {
		return nil, contracts.ErrClosed
	}
	return b.runtime.RegisterAdapter(b.owner, registration)
}

func (b Binding) RegisterModels(models []modelcatalog.Spec) error {
	if b.runtime == nil || b.runtime.models == nil {
		return contracts.ErrClosed
	}
	return b.runtime.RegisterModels(b.owner, models)
}

func (b Binding) RegisterDefaults(defaults modelcatalog.Defaults) error {
	if b.runtime == nil || b.runtime.defaults == nil {
		return contracts.ErrClosed
	}
	return b.runtime.RegisterDefaults(b.owner, defaults)
}

// RegisterProvider binds an adapter and its static catalog to the same owner.
// Catalog failure withdraws only the adapter registered by this call. The two
// registries do not publish atomically; this method promises failure rollback.
func (b Binding) RegisterProvider(registration AdapterRegistration, models []modelcatalog.Spec) error {
	dispose, err := b.RegisterAdapter(registration)
	if err != nil {
		return err
	}
	if err := b.RegisterModels(models); err != nil {
		dispose()
		return err
	}
	return nil
}
