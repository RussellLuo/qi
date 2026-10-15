package profile

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSourceStrictParsing(t *testing.T) {
	cases := map[string]string{
		"empty document":                "",
		"scalar document":               "hello",
		"sequence document":             "[]",
		"null document":                 "null",
		"multiple documents":            "instances: []\n---\ninstances: []",
		"trailing malformed document":   "instances: []\n---\n[",
		"unknown top field":             "instance: []",
		"unsupported version":           "apiVersion: qi.profile/v2",
		"duplicate YAML key":            "bundles: []\nbundles: []",
		"duplicate JSON key":            `{"instances":[],"instances":[]}`,
		"duplicate config key":          "instances: [{id: a, plugin: qi/tools/echo, config: {prefix: a, prefix: b}}]",
		"numeric key":                   "instances: [{id: a, plugin: qi/tools/echo, config: {1: x}}]",
		"custom tag":                    "instances: [{id: a, plugin: qi/tools/echo, config: {prefix: !!js secret}}]",
		"custom string tag":             "bundles: [!bundle base]",
		"timestamp":                     "instances: [{id: a, plugin: qi/tools/echo, config: {prefix: 2026-10-08}}]",
		"YAML merge":                    "instances: [{id: a, plugin: qi/tools/echo, config: {<<: {prefix: x}}}]",
		"cyclic alias":                  "instances: [{id: a, plugin: qi/tools/echo, config: &cycle {x: *cycle}}]",
		"bundles null":                  "bundles: null",
		"bundle object":                 "bundles: [{}]",
		"bundle numeric":                "bundles: [3]",
		"bundle URL":                    "bundles: [https://example.com/base]",
		"duplicate bundles":             "bundles: [base, base]",
		"instances null":                "instances: null",
		"overrides null":                "overrides: null",
		"instance scalar":               "instances: [a]",
		"instance missing id":           "instances: [{plugin: qi/tools/echo}]",
		"instance missing plugin":       "instances: [{id: a}]",
		"invalid id":                    "instances: [{id: a/b, plugin: qi/tools/echo}]",
		"numeric id":                    "instances: [{id: 3, plugin: qi/tools/echo}]",
		"plugin null":                   "instances: [{id: a, plugin: null}]",
		"plugin leading slash":          "instances: [{id: a, plugin: /qi/llm}]",
		"plugin trailing slash":         "instances: [{id: a, plugin: qi/llm/}]",
		"plugin empty segment":          "instances: [{id: a, plugin: qi//llm}]",
		"plugin parent segment":         "instances: [{id: a, plugin: qi/../llm}]",
		"plugin current segment":        "instances: [{id: a, plugin: qi/./llm}]",
		"instance config null":          "instances: [{id: a, plugin: qi/tools/echo, config: null}]",
		"instance config array":         "instances: [{id: a, plugin: qi/tools/echo, config: []}]",
		"instance token bool":           "instances: [{id: a, plugin: qi/tools/echo, restartToken: true}]",
		"disabled instance":             "instances: [{id: a, plugin: qi/tools/echo, disabled: true}]",
		"duplicate instances":           "instances: [{id: a, plugin: qi/tools/echo}, {id: a, plugin: qi/tools/echo}]",
		"empty override":                "overrides: [{id: a}]",
		"missing override id":           "overrides: [{disabled: true}]",
		"duplicate overrides":           "overrides: [{id: a, disabled: true}, {id: a, disabled: false}]",
		"unknown override field":        "overrides: [{id: a, unknown: true}]",
		"override plugin empty":         "overrides: [{id: a, plugin: ''}]",
		"override plugin null":          "overrides: [{id: a, plugin: null}]",
		"override plugin number":        "overrides: [{id: a, plugin: 3}]",
		"override plugin empty segment": "overrides: [{id: a, plugin: qi//llm}]",
		"override config null":          "overrides: [{id: a, config: null}]",
		"override config array":         "overrides: [{id: a, config: []}]",
		"override config scalar":        "overrides: [{id: a, config: secret}]",
		"override token null":           "overrides: [{id: a, restartToken: null}]",
		"override token number":         "overrides: [{id: a, restartToken: 3}]",
		"override disabled null":        "overrides: [{id: a, disabled: null}]",
		"override disabled string":      "overrides: [{id: a, disabled: 'false'}]",
		"nonfinite number":              "instances: [{id: a, plugin: qi/tools/echo, config: {value: .nan}}]",
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSource([]byte(input)); err == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}

	for _, input := range []string{
		`{"overrides":[{"id":"a","config":null}]}`,
		`{"overrides":[{"id":"a","disabled":null}]}`,
		`{"overrides":[{"id":"a","disabled":false,"disabled":true}]}`,
		`{"instances":[],"instances":[]}`,
		`[]`,
		`null`,
		`instances: []`,
	} {
		var source ProfileSource
		if err := json.Unmarshal([]byte(input), &source); err == nil {
			t.Fatalf("JSON transport accepted %s", input)
		}
	}
}

func TestSourceAllowsPlainAliasedJSONData(t *testing.T) {
	source, err := ParseSource([]byte(`
instances:
  - id: a
    plugin: test.data
    config: &shared
      nested: {value: null}
      list: [1, false]
  - id: b
    plugin: test.data
    config: *shared
overrides:
  - id: a
    config: *shared
`))
	if err != nil {
		t.Fatal(err)
	}

	source.Instances[0].Config["nested"].(map[string]any)["value"] = "changed"
	if source.Instances[1].Config["nested"].(map[string]any)["value"] != nil ||
		(*source.Overrides[0].Config)["nested"].(map[string]any)["value"] != nil {
		t.Fatal("parsed aliases share mutable config")
	}

	values := source.Instances[1].Config["list"].([]any)
	if values[0] != float64(1) || values[1] != false {
		t.Fatalf("JSON data changed during parsing: %v", values)
	}
}

func TestSourcePresenceRoundTripAndDigest(t *testing.T) {
	input := []byte(`
bundles: [base, fake]
instances:
  - id: extra
    plugin: qi/tools/echo
    config: {prefix: user}
overrides:
  - id: session
    plugin: qi/session
    config: {}
    restartToken: ""
    disabled: false
  - id: extra
    disabled: true
`)
	source, err := ParseSource(input)
	if err != nil {
		t.Fatal(err)
	}

	first := source.Overrides[0]
	if first.Config == nil ||
		*first.Config == nil ||
		len(*first.Config) != 0 ||
		first.RestartToken == nil ||
		*first.RestartToken != "" ||
		first.Disabled == nil ||
		*first.Disabled {
		t.Fatalf("lost explicit empty/false values: %+v", first)
	}

	if source.Overrides[1].Config != nil || source.Overrides[1].Plugin != nil {
		t.Fatal("omitted values gained presence")
	}

	digest, _ := source.Digest()
	raw, err := MarshalSource(source)
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range [][]byte{raw, encoded, append([]byte("# comment\n"), input...)} {
		parsed, err := ParseSource(value)
		if err != nil || !reflect.DeepEqual(source, parsed) {
			t.Fatalf("source round trip: %+v, %v", parsed, err)
		}

		got, _ := parsed.Digest()
		if digest != got {
			t.Fatal("format/comment change affected semantic digest")
		}
	}

	for name, change := range map[string]func(*ProfileSource){
		"bundle order": func(s *ProfileSource) {
			s.Bundles[0], s.Bundles[1] = s.Bundles[1], s.Bundles[0]
		},
		"config omission": func(s *ProfileSource) {
			s.Overrides[0].Config = nil
		},
		"disabled omission": func(s *ProfileSource) {
			s.Overrides[0].Disabled = nil
		},
		"token omission": func(s *ProfileSource) {
			s.Overrides[0].RestartToken = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := source.Clone()
			change(&copy)
			got, err := copy.Digest()
			if err != nil || got == digest {
				t.Fatalf("semantic change did not affect digest: %s, %v", got, err)
			}
		})
	}

	if _, err := Parse(raw); err == nil {
		t.Fatal("execution Profile parser accepted source-only fields")
	}
}

func TestSourceRejectsProgrammaticInvalidValuesAndRenamedPlugins(t *testing.T) {
	for _, config := range []map[string]any{{"key": func() {}}, {"key": math.NaN()}} {
		source := ProfileSource{
			Instances: []InstanceSpec{{
				ID:     "a",
				Plugin: "qi/tools/echo",
				Config: config,
			}},
		}
		if _, err := Compose(source, nil); err == nil {
			t.Fatal("invalid programmatic config accepted")
		}

		if _, err := MarshalSource(source); err == nil {
			t.Fatal("invalid config serialized")
		}
	}

	var null map[string]any
	if _, err := Compose(ProfileSource{Overrides: []Override{{ID: "a", Config: &null}}}, nil); err == nil {
		t.Fatal("programmatic null override accepted")
	}

	_, err := ParseSource([]byte("overrides: [{id: a, config: credential-value} ]"))
	if err == nil ||
		strings.Contains(err.Error(), "credential-value") ||
		!strings.Contains(err.Error(), "user.overrides[0] instance \"a\".config") {
		t.Fatalf("diagnostic leaked value or lost location: %v", err)
	}

	for _, input := range []string{
		"overrides: [{id: a, config: {secret: !exec credential-value}}]",
		"overrides: [{id: a, config: {secret: x, secret: credential-value}}]",
	} {
		_, err := ParseSource([]byte(input))
		if err == nil ||
			strings.Contains(err.Error(), "credential-value") ||
			!strings.Contains(err.Error(), `user.overrides[0] instance "a".config.secret`) {
			t.Fatalf("data diagnostic leaked value or lost instance: %v", err)
		}
	}

	_, err = ParseBundleDefinition([]byte("id: secured\noverrides: [{id: a, config: {secret: !exec credential-value}}]"))
	if err == nil ||
		strings.Contains(err.Error(), "credential-value") ||
		!strings.Contains(err.Error(), `bundle "secured".overrides[0] instance "a"`) {
		t.Fatalf("bundle data diagnostic: %v", err)
	}
}

func TestProfilePluginNamesBelongToCatalog(t *testing.T) {
	for _, name := range []string{"bundle.tool-package", "bundle.client-module"} {
		t.Run(name, func(t *testing.T) {
			raw := []byte("instances: [{id: custom, plugin: " + name + "}]")
			parsed, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(parsed)
			if err != nil {
				t.Fatal(err)
			}
			var transported Profile
			if err := json.Unmarshal(encoded, &transported); err != nil {
				t.Fatal(err)
			}
			if _, err := ParseSource(raw); err != nil {
				t.Fatal(err)
			}
			if _, err := ParseBundleDefinition(append([]byte("id: custom\n"), raw...)); err != nil {
				t.Fatal(err)
			}
			unknown, err := NewCatalog(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := unknown.Validate(transported); err == nil || !strings.Contains(err.Error(), "trusted catalog") {
				t.Fatalf("unregistered plugin accepted: %v", err)
			}
			catalog, err := NewCatalog([]PluginDescriptor{{ID: name}})
			if err != nil {
				t.Fatal(err)
			}
			if err := catalog.Validate(transported); err != nil {
				t.Fatal(err)
			}
		})
	}
}
