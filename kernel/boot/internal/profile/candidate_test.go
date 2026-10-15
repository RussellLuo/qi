package profile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

func TestCompletePreflightRejectsUnusedDuplicateProvidersAndCycles(t *testing.T) {
	for _, scenario := range []struct {
		plugins []PluginDescriptor
		want    string
	}{
		{
			[]PluginDescriptor{
				{ID: "a", Provides: []CapabilityRef{{ID: "x"}}},
				{ID: "b", Provides: []CapabilityRef{{ID: "x"}}},
			},
			"multiple providers",
		},
		{
			[]PluginDescriptor{
				{
					ID:       "a",
					Provides: []CapabilityRef{{ID: "x"}},
					Requires: []CapabilityRef{{ID: "y"}},
				},
				{
					ID:       "b",
					Provides: []CapabilityRef{{ID: "y"}},
					Requires: []CapabilityRef{{ID: "x"}},
				},
			},
			"cyclic",
		},
	} {
		catalog, err := NewCatalog(scenario.plugins)
		if err != nil {
			t.Fatal(err)
		}

		profile := Profile{Instances: []InstanceSpec{{ID: "one", Plugin: "a"}, {ID: "two", Plugin: "b"}}}
		if _, err := catalog.NormalizeComplete(profile); err == nil || !strings.Contains(err.Error(), scenario.want) {
			t.Fatalf("expected %s: %v", scenario.want, err)
		}
	}
}

func TestTrustedExternalCapabilityIsNotAPublicInstance(t *testing.T) {
	catalog, err := NewCatalog([]PluginDescriptor{
		{ID: "consumer", Requires: []CapabilityRef{{ID: "launch.context"}}},
		{ID: "shadow", Provides: []CapabilityRef{{ID: "launch.context"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	profile := Profile{Instances: []InstanceSpec{{ID: "task", Plugin: "consumer"}}}
	if _, _, err := catalog.MountPlan(profile); err == nil {
		t.Fatal("missing internal carrier accepted")
	}

	catalog.ProvideExternalCapabilities(CapabilityRef{ID: "launch.context"})
	_, mounts, err := catalog.MountPlan(profile)
	if err != nil || len(mounts) != 1 || mounts[0].ID != "task" {
		t.Fatal("external capability introduced a synthetic public instance", mounts, err)
	}

	profile.Instances = append(profile.Instances, InstanceSpec{ID: "shadow", Plugin: "shadow"})
	if _, _, err := catalog.MountPlan(profile); err == nil || !strings.Contains(err.Error(), "multiple providers") {
		t.Fatal("public shadow of internal service accepted", err)
	}
}

func TestModelCatalogAllowsExplicitSelectionWithoutDefaults(t *testing.T) {
	description, err := modelcatalog.Provider("text", "example", []string{"text"}, []modelcatalog.Entry{{ID: "chosen", Model: "text"}})
	if err != nil {
		t.Fatal(err)
	}

	descriptors := []PluginDescriptor{
		{
			ID:             "provider",
			ConfigSchema:   json.RawMessage(`{"type":"object"}`),
			DescribeModels: func(json.RawMessage) (modelcatalog.Description, error) { return description, nil },
		},
	}
	profile := Profile{
		APIVersion: ProfileAPIVersion,
		Instances:  []InstanceSpec{{ID: "model", Plugin: "provider"}},
	}

	for _, test := range []struct {
		name     string
		defaults *modelcatalog.Defaults
		want     string
	}{
		{"explicit only", nil, ""},
		{"valid default", &modelcatalog.Defaults{Model: "chosen"}, ""},
		{"unknown default", &modelcatalog.Defaults{Model: "missing"}, "not declared"},
		{"empty declared default", &modelcatalog.Defaults{}, "not declared"},
	} {
		t.Run(test.name, func(t *testing.T) {
			description.Defaults = test.defaults
			catalog, err := NewCatalog(descriptors)
			if err != nil {
				t.Fatal(err)
			}

			err = catalog.Validate(profile)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatal(err)
			}
		})
	}
}

// Default conflicts are planning rules; no provider or consumer needs activation.
func TestModelCatalogRejectsMultipleDefaultDeclarations(t *testing.T) {
	description, err := modelcatalog.Provider("text", "example", nil, []modelcatalog.Entry{{ID: "chosen", Model: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog([]PluginDescriptor{
		{ID: "provider", DescribeModels: func(json.RawMessage) (modelcatalog.Description, error) { return description, nil }},
		{ID: "choice", DescribeModels: func(json.RawMessage) (modelcatalog.Description, error) {
			return modelcatalog.Description{Defaults: &modelcatalog.Defaults{Model: "chosen"}}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := Profile{Instances: []InstanceSpec{{ID: "model", Plugin: "provider"}, {ID: "first", Plugin: "choice"}}}
	if err := catalog.Validate(p); err != nil {
		t.Fatal(err)
	}
	p.Instances = append(p.Instances, InstanceSpec{ID: "second", Plugin: "choice"})
	if err := catalog.Validate(p); err == nil || !strings.Contains(err.Error(), "multiple default model declarations") {
		t.Fatal("conflicting defaults accepted", err)
	}
}
