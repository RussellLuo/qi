package plugin

import (
	"context"
	"errors"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

const PluginID = "qi/session"

type Plugin struct {
	HeaderCheck func(protocol.SessionHeader) error `json:"-"`
}

func (p *Plugin) WithHeaderCheck(check func(protocol.SessionHeader) error) gordis.Plugin {
	copy := *p
	previous := p.HeaderCheck
	copy.HeaderCheck = func(header protocol.SessionHeader) error {
		if check != nil {
			if err := check(header); err != nil {
				return err
			}
		}
		if previous != nil {
			return previous(header)
		}
		return nil
	}
	return &copy
}

func (p *Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
		Provides: []gordis.ServiceSpec{session.StoreKey.Spec()},
		New:      func() gordis.Plugin { return &Plugin{HeaderCheck: p.HeaderCheck} },
	}
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	dispatcher, err := gordisbridge.NewEventDispatcher(scope, 0)
	if err != nil {
		return err
	}
	options := session.RuntimeOptions{Observer: dispatcher, HeaderCheck: p.HeaderCheck}
	// Resolve only after the launcher admits business, once the whole
	// composition is Ready. Lookup protects discovery only; Acquire transfers
	// physical resource ownership to this transaction's independent lease.
	options.ResolvePersistence = func(ctx context.Context) (session.PersistenceLease, bool, error) {
		provider, found, err := scope.Lookup(session.PersistenceKey)
		if err != nil || !found {
			return nil, found, err
		}
		if provider == nil {
			return nil, false, errors.New("qi/session discovered nil persistence provider")
		}
		lease, err := provider.Acquire(ctx)
		return lease, err == nil, err
	}
	runtime := session.NewRuntime(options)
	if err := scope.Defer("close Session Runtime", func(ctx context.Context) error { return runtime.Close(ctx) }); err != nil {
		return errors.Join(err, runtime.Close(context.Background()))
	}
	return scope.Provide[session.Store](session.StoreKey, runtime)
}
