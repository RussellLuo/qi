package application_test

import (
	"context"
	"encoding/json"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
	"testing"

	"github.com/RussellLuo/gordis"
)

type dormant struct{ id string }

func (p *dormant) Spec() gordis.PluginSpec {
	id := p.id
	if id == "" {
		id = "fixture/tool"
	}
	return gordis.PluginSpec{ID: id, New: func() gordis.Plugin { return &dormant{id: id} }}
}

func (*dormant) Activate(context.Context, *gordis.Scope) error { panic("Compose activated plugin") }

func TestComposeNamespacedPluginIDs(t *testing.T) {
	for _, id := range []string{"acme/model", "acme/llm/custom", "gordis/events"} {
		ext := applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{{
			Plugin: &dormant{id: id}, ProfileVisible: true,
		}}}
		app, err := applicationapi.Compose(ext)
		if err != nil {
			t.Fatalf("plugin %q rejected: %v", id, err)
		}
		if app.Registrations[0].Plugin.Spec().ID != id {
			t.Fatalf("plugin ID %q was rewritten", id)
		}
		if _, err := applicationapi.Compose(ext, ext); err == nil {
			t.Fatalf("duplicate plugin %q accepted", id)
		}
	}

	for _, id := range []string{"/acme/model", "acme/model/", "acme//model", "acme/../model", "acme/./model", "https://acme/model"} {
		if _, err := applicationapi.Compose(applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{{
			Plugin: &dormant{id: id}, ProfileVisible: true,
		}}}); err == nil {
			t.Fatalf("invalid plugin %q accepted", id)
		}
	}
}

func TestComposeRejectsDuplicatesAndInvalidMetadataWithoutActivation(t *testing.T) {
	registration := registrationapi.PluginRegistration{Plugin: new(dormant), ProfileVisible: true}
	valid := applicationapi.Extension{Registrations: []registrationapi.PluginRegistration{registration}}

	for _, test := range []struct {
		name       string
		extensions []applicationapi.Extension
	}{
		{"duplicate plugin", []applicationapi.Extension{valid, valid}},
		{"nil prototype", []applicationapi.Extension{{Registrations: []registrationapi.PluginRegistration{{}}}}},
		{
			"mismatched descriptor",
			[]applicationapi.Extension{
				{Registrations: []registrationapi.PluginRegistration{{Plugin: new(dormant), Descriptor: profileapi.PluginDescriptor{ID: "wrong"}}}},
			},
		},
		{
			"private bad schema",
			[]applicationapi.Extension{
				{
					Registrations: []registrationapi.PluginRegistration{
						{
							Plugin:     new(dormant),
							Descriptor: profileapi.PluginDescriptor{ConfigSchema: json.RawMessage(`{"type":"unknown"}`)},
						},
					},
				},
			},
		},
		{
			"bad schema",
			[]applicationapi.Extension{
				{
					Registrations: []registrationapi.PluginRegistration{
						{
							Plugin:         new(dormant),
							ProfileVisible: true,
							Descriptor:     profileapi.PluginDescriptor{ConfigSchema: json.RawMessage(`{"type":"unknown"}`)},
						},
					},
				},
			},
		},
		{
			"bad policy",
			[]applicationapi.Extension{
				{
					Registrations: []registrationapi.PluginRegistration{
						{
							Plugin:         new(dormant),
							ProfileVisible: true,
							Descriptor:     profileapi.PluginDescriptor{UpdatePolicy: "unknown"},
						},
					},
				},
			},
		},
		{"duplicate bundle", []applicationapi.Extension{{Bundles: []profileapi.BundleDefinition{{ID: "layer"}, {ID: "layer"}}}}},
		{"bad bundle", []applicationapi.Extension{{Bundles: []profileapi.BundleDefinition{{ID: "bad/id"}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := applicationapi.Compose(test.extensions...); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}

	if _, err := applicationapi.Compose(valid); err != nil {
		t.Fatal(err)
	}
}

func TestComposeHasNoDefaultSetAndFreezesBundles(t *testing.T) {
	empty, err := applicationapi.Compose()
	if err != nil || len(empty.Registrations) != 0 {
		t.Fatal(empty, err)
	}

	if _, _, ok := empty.BundleCatalog.Lookup("base"); ok {
		t.Fatal("implicit official Bundle")
	}

	definition := profileapi.BundleDefinition{
		ID: "private",
		Instances: []profileapi.InstanceSpec{{
			ID:     "tool",
			Plugin: "fixture/tool",
			Config: map[string]any{"prefix": "original"},
		}},
	}
	app, err := applicationapi.Compose(applicationapi.Extension{Bundles: []profileapi.BundleDefinition{definition}})
	if err != nil {
		t.Fatal(err)
	}

	definition.Instances[0].Config["prefix"] = "changed"
	got, digest, ok := app.BundleCatalog.Lookup("private")
	if !ok || digest == "" || got.Instances[0].Config["prefix"] != "original" {
		t.Fatal(got, digest)
	}

	got.Instances[0].Config["prefix"] = "changed again"
	got, _, _ = app.BundleCatalog.Lookup("private")
	if got.Instances[0].Config["prefix"] != "original" {
		t.Fatal("lookup aliases catalog")
	}
}
