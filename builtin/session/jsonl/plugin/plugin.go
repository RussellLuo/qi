package plugin

import (
	"context"
	"errors"
	"strings"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/session/jsonl"
	"github.com/RussellLuo/qi/kernel/core/session"
)

const PluginID = "qi/session/persistence-jsonl"

type Plugin struct {
	Directory string `json:"directory"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Provides: []gordis.ServiceSpec{session.PersistenceKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (p *Plugin) Validate() error {
	if strings.TrimSpace(p.Directory) == "" {
		return errors.New("qi/session/persistence-jsonl requires directory")
	}
	return nil
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := p.Validate(); err != nil {
		return err
	}
	owner, err := jsonl.NewOwner(p.Directory)
	if err != nil {
		return err
	}
	if err := scope.OnStop("stop JSONL persistence acquisition", func(context.Context) error { owner.Stop(); return nil }); err != nil {
		return errors.Join(err, owner.Close())
	}
	if err := scope.Defer("release JSONL plugin reference", func(context.Context) error { return owner.Close() }); err != nil {
		return errors.Join(err, owner.Close())
	}
	return scope.Provide[session.PersistenceProvider](session.PersistenceKey, owner)
}
