// Package bundle is the builder's self-contained default-provider fixture.
// Real ten-type default application regressions belong to builtin.
package bundle

import (
	"context"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	corebundle "github.com/RussellLuo/qi/kernel/bundle"
)

func Extension() application.Extension {
	ext := corebundle.Extension()
	ext.Registrations = append(ext.Registrations, registration.PluginRegistration{Plugin: new(Provider), ProfileVisible: true})
	return ext
}

type Provider struct{}

func (*Provider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "qi/session/persistence-jsonl", New: func() gordis.Plugin { return new(Provider) }}
}
func (*Provider) Activate(context.Context, *gordis.Scope) error { return nil }
