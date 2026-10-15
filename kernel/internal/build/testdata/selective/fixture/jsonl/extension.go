package jsonl

import (
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"

	"context"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

func Extension() applicationapi.Extension {
	return applicationapi.Extension{Registrations: []registration.PluginRegistration{{Plugin: new(provider), ProfileVisible: true}}}
}

type provider struct{}

func (*provider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "qi/session/persistence-jsonl", New: func() gordis.Plugin { return new(provider) }}
}
func (*provider) Activate(context.Context, *gordis.Scope) error { return nil }
