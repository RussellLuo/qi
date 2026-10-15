package profile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type compositionCatalog map[string]BundleDefinition

func (c compositionCatalog) Lookup(id string) (BundleDefinition, string, bool) {
	d, ok := c[id]
	// Deliberately return shared maps: Compose must detach them itself.
	return d, "sha256-fixture-" + id, ok
}

func compositionFixture(t *testing.T) (ProfileSource, compositionCatalog) {
	t.Helper()
	catalog := compositionCatalog{}

	for _, raw := range []string{
		`id: first
instances:
  - id: session
    plugin: qi/session
    config: {old: value, nested: {keep: old}, list: [old]}
    restartToken: old-generation
  - id: tool
    plugin: qi/tools/echo
    config: {prefix: original}
overrides:
  - id: tool
    disabled: true
`,
		`id: second
overrides:
  - id: session
    plugin: session.alternative
  - id: tool
    config: {prefix: middle}
`,
		`id: third
overrides:
  - id: session
    config: {directory: sessions, discarded: true}
    restartToken: new-generation
  - id: tool
    disabled: false
`,
	} {
		definition, err := ParseBundleDefinition([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}

		catalog[definition.ID] = definition
	}

	source, err := ParseSource([]byte(`
bundles: [first, second, third]
instances:
  - id: extra
    plugin: qi/tools/echo
    config: {prefix: initial}
overrides:
  - id: session
    config: {directory: user-sessions}
    restartToken: ""
  - id: tool
    config: {}
  - id: extra
    config: {prefix: user, nested: {only: final}, list: [a, b]}
`))
	if err != nil {
		t.Fatal(err)
	}

	return source, catalog
}

func TestComposeFixedLayersAndIsolation(t *testing.T) {
	source, catalog := compositionFixture(t)
	beforeSource, _ := json.Marshal(source)
	beforeCatalog, _ := json.Marshal(catalog)
	resolved, err := Compose(source, catalog)
	if err != nil {
		t.Fatal(err)
	}

	expected, err := Parse([]byte(`
apiVersion: qi.profile/v1alpha1
instances:
  - id: session
    plugin: session.alternative
    config: {directory: user-sessions}
  - id: tool
    plugin: qi/tools/echo
    config: {}
  - id: extra
    plugin: qi/tools/echo
    config: {prefix: user, nested: {only: final}, list: [a, b]}
`))
	if err != nil {
		t.Fatal(err)
	}
	// Compare canonical JSON because YAML integers and JSON-normalized configs
	// have different Go representations but identical execution data.
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(resolved.Profile)
	if string(want) != string(got) {
		t.Fatalf("effective graph\ngot  %s\nwant %s", got, want)
	}

	if len(resolved.Declarations) != 3 ||
		resolved.Declarations[0].Layer != "user" ||
		len(resolved.BundleInputs) != 3 ||
		resolved.BundleInputs[1].ID != "second" {
		t.Fatalf("lost logical table/inputs: %+v", resolved)
	}

	intermediate, err := Compose(ProfileSource{Bundles: []string{"first", "second", "third"}}, catalog)
	if err != nil {
		t.Fatal(err)
	}

	if intermediate.Declarations[1].Layer != `bundle "third"` || intermediate.Declarations[1].ConfigLayer != `bundle "second"` {
		t.Fatal("re-enabling replaced the last whole-config write layer")
	}

	for range 3 {
		again, err := Compose(source, catalog)
		if err != nil || !reflect.DeepEqual(resolved, again) {
			t.Fatalf("non-deterministic composition: %v", err)
		}
	}

	afterSource, _ := json.Marshal(source)
	afterCatalog, _ := json.Marshal(catalog)
	if string(beforeSource) != string(afterSource) || string(beforeCatalog) != string(afterCatalog) {
		t.Fatal("composition mutated input")
	}

	resolved.Profile.Instances[2].Config["nested"].(map[string]any)["only"] = "profile mutation"
	resolved.Declarations[0].Spec.Config["directory"] = "declaration mutation"
	(*resolved.Source.Overrides[2].Config)["prefix"] = "snapshot mutation"
	afterSource, _ = json.Marshal(source)
	afterCatalog, _ = json.Marshal(catalog)
	if string(beforeSource) != string(afterSource) || string(beforeCatalog) != string(afterCatalog) {
		t.Fatal("resolved snapshot aliases input")
	}

	if resolved.Declarations[2].Spec.Config["nested"].(map[string]any)["only"] != "final" ||
		resolved.Profile.Instances[0].Config["directory"] != "user-sessions" {
		t.Fatal("execution graph aliases logical table")
	}
}

func TestComposeOverridePresenceAndPluginReplacement(t *testing.T) {
	catalog := compositionCatalog{
		"start": {
			ID: "start",
			Instances: []InstanceSpec{
				{
					ID:           "a",
					Plugin:       "test.old",
					Config:       map[string]any{"old": "value"},
					RestartToken: "old-token",
				},
			},
		},
	}

	for _, test := range []struct {
		name, override, plugin, token string
		config                        map[string]any
		disabled                      bool
	}{
		{"same plugin preserves", "plugin: test.old", "test.old", "old-token", map[string]any{"old": "value"}, false},
		{"omitted config preserves", "disabled: false", "test.old", "old-token", map[string]any{"old": "value"}, false},
		{"empty object clears", "config: {}", "test.old", "old-token", map[string]any{}, false},
		{"new plugin resets", "plugin: test.new", "test.new", "", map[string]any{}, false},
		{
			"new plugin explicit values",
			"plugin: test.new, config: {new: value}, restartToken: new-token",
			"test.new",
			"new-token",
			map[string]any{"new": "value"},
			false,
		},
		{"token clears", "restartToken: ''", "test.old", "", map[string]any{"old": "value"}, false},
		{"disabled", "disabled: true", "test.old", "old-token", map[string]any{"old": "value"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := ParseSource([]byte("bundles: [start]\noverrides: [{id: a, " + test.override + "}]"))
			if err != nil {
				t.Fatal(err)
			}

			resolved, err := Compose(source, catalog)
			if err != nil {
				t.Fatal(err)
			}

			entry := resolved.Declarations[0]
			if entry.Spec.Plugin != test.plugin ||
				entry.Spec.RestartToken != test.token ||
				!reflect.DeepEqual(entry.Spec.Config, test.config) ||
				entry.Disabled != test.disabled {
				t.Fatalf("override result: %+v", entry)
			}

			if (len(resolved.Profile.Instances) == 0) != test.disabled {
				t.Fatal("disabled filtering failed")
			}
		})
	}
}

func TestComposeStructuralErrors(t *testing.T) {
	source, catalog := compositionFixture(t)

	for name, change := range map[string]func(*ProfileSource){
		"unknown bundle": func(s *ProfileSource) {
			s.Bundles = []string{"absent"}
		},
		"duplicate bundle": func(s *ProfileSource) {
			s.Bundles = []string{"first", "first"}
		},
		"unknown target": func(s *ProfileSource) {
			s.Overrides[0].ID = "absent"
		},
		"wrong layer order": func(s *ProfileSource) {
			s.Bundles = []string{"second", "first"}
		},
		"duplicate addition": func(s *ProfileSource) {
			s.Instances[0].ID = "session"
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := source.Clone()
			change(&copy)
			if _, err := Compose(copy, catalog); err == nil {
				t.Fatal("invalid composition accepted")
			}
		})
	}

	if _, err := Compose(source, nil); err == nil {
		t.Fatal("missing bundle catalog accepted")
	}

	_, err := Compose(ProfileSource{Bundles: []string{"second"}}, catalog)
	if err == nil || !strings.Contains(err.Error(), `bundle "second".overrides[0] instance "session"`) {
		t.Fatalf("lost layer/instance: %v", err)
	}

	catalog["second"] = BundleDefinition{ID: "second", Instances: []InstanceSpec{{ID: "session", Plugin: "qi/session"}}}
	if _, err := Compose(ProfileSource{Bundles: []string{"first", "second"}}, catalog); err == nil {
		t.Fatal("cross-layer addition reused ID")
	}
}

func TestComposeDisabledTypeAdmissionWithoutConfigValidation(t *testing.T) {
	catalog, err := NewCatalog([]PluginDescriptor{{
		ID:           "test.provider",
		ConfigSchema: json.RawMessage(`{"type":"object","required":["required"]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}

	source, err := ParseSource([]byte("instances: [{id: a, plugin: test.provider}]\noverrides: [{id: a, disabled: true}]"))
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := Compose(source, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := catalog.ValidateDeclarations(resolved.Declarations); err != nil {
		t.Fatal(err)
	}

	if err := catalog.Validate(resolved.Profile); err != nil {
		t.Fatal(err)
	}

	source.Overrides[0].Disabled = boolPointer(false)
	active, err := Compose(source, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := catalog.Validate(active.Profile); err == nil {
		t.Fatal("active missing config passed existing schema")
	}

	for _, plugin := range []string{"test.unknown", "package.client-module"} {
		resolved.Declarations[0].Spec.Plugin = plugin
		if err := catalog.ValidateDeclarations(resolved.Declarations); err == nil || !strings.Contains(err.Error(), `instance "a"`) {
			t.Fatalf("disabled public type admission: %v", err)
		}
	}
}

func boolPointer(value bool) *bool { return &value }

func TestCompleteInstanceSourcePreservesProfile(t *testing.T) {
	raw := []byte("apiVersion: " + ProfileAPIVersion + "\ninstances: [{id: a, plugin: qi/tools/echo, config: {prefix: value}, restartToken: generation}]\n")
	profile, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	source, err := ParseSource(raw)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := Compose(source, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(profile, resolved.Profile) {
		t.Fatalf("complete source changed graph: %+v", resolved.Profile)
	}
}

func TestSourceAndExecutionDigestsHaveSeparateMeanings(t *testing.T) {
	source := ProfileSource{Instances: []InstanceSpec{{ID: "a", Plugin: "qi/tools/echo"}}}
	first, err := Compose(source, nil)
	if err != nil {
		t.Fatal(err)
	}

	source.Overrides = []Override{{ID: "a", Disabled: boolPointer(false)}}
	second, err := Compose(source, nil)
	if err != nil {
		t.Fatal(err)
	}

	firstDigest, _ := first.Profile.Digest()
	secondDigest, _ := second.Profile.Digest()
	if firstDigest != secondDigest || first.SourceDigest == second.SourceDigest {
		t.Fatal("source presence must affect only the source digest for an unchanged execution graph")
	}
}
