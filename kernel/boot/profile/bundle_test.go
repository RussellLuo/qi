package profile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBundleDefinitionStrictParsing(t *testing.T) {
	for name, input := range map[string]string{
		"nested bundles":     "id: test\nbundles: [base]",
		"unknown field":      "id: test\nextra: x",
		"missing id":         "instances: []",
		"numeric title":      "id: test\ntitle: 3",
		"duplicate override": "id: test\noverrides: [{id: a, disabled: true}, {id: a, disabled: false}]",
		"duplicate JSON":     `{"id":"test","id":"other"}`,
		"custom tag":         "id: test\ninstances: [{id: a, plugin: test.a, config: {key: !exec secret}}]",
		"multiple documents": "id: test\n---\nid: other",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBundleDefinition([]byte(input)); err == nil {
				t.Fatal("accepted invalid definition")
			}
		})
	}
	// A layer may refer to an earlier layer; complete graph checks come later.
	d, err := ParseBundleDefinition([]byte("id: partial\noverrides: [{id: earlier, disabled: true}]"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := MarshalBundleDefinition(d)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseBundleDefinition(raw)
	if err != nil || !reflect.DeepEqual(d, parsed) {
		t.Fatalf("definition round trip: %v, %v", parsed, err)
	}

	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}

	var fromJSON BundleDefinition
	if err := json.Unmarshal(encoded, &fromJSON); err != nil || !reflect.DeepEqual(d, fromJSON) {
		t.Fatalf("definition JSON round trip: %v, %v", fromJSON, err)
	}

	if err := json.Unmarshal([]byte(`{"id":"test","id":"other"}`), &fromJSON); err == nil {
		t.Fatal("definition JSON transport accepted duplicate fields")
	}
}

func TestBundlePresenceDetachedCloneAndInvalidProgrammaticData(t *testing.T) {
	original, err := ParseBundleDefinition([]byte(`id: test
instances: [{id: a, plugin: test.a, config: {nested: {value: old}}}]
overrides: [{id: a, plugin: test.a, disabled: false, restartToken: "", config: {}}]
`))
	if err != nil {
		t.Fatal(err)
	}
	clone := original.Clone()
	clone.Instances[0].Config["nested"].(map[string]any)["value"] = "changed"
	*clone.Overrides[0].Disabled = true
	*clone.Overrides[0].RestartToken = "changed"
	*clone.Overrides[0].Plugin = "changed"
	(*clone.Overrides[0].Config)["changed"] = true
	if original.Instances[0].Config["nested"].(map[string]any)["value"] != "old" || *original.Overrides[0].Disabled || *original.Overrides[0].RestartToken != "" || *original.Overrides[0].Plugin != "test.a" || len(*original.Overrides[0].Config) != 0 {
		t.Fatal("Clone mutated the original declaration")
	}
	raw, err := MarshalBundleDefinition(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseBundleDefinition(raw)
	if err != nil || !reflect.DeepEqual(original, decoded) {
		t.Fatal("presence changed", err, string(raw))
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var fromJSON BundleDefinition
	if err := json.Unmarshal(encoded, &fromJSON); err != nil || !reflect.DeepEqual(original, fromJSON) {
		t.Fatal("JSON presence changed", err)
	}
	nilConfig := map[string]any(nil)
	for _, test := range []struct {
		value BundleDefinition
		want  string
	}{
		{BundleDefinition{ID: "test", Overrides: []Override{{ID: "a", Config: &nilConfig}}}, `bundle "test".overrides[0] instance "a".config: must be an object, not null`},
		{BundleDefinition{ID: "test", Instances: []InstanceSpec{{ID: "a", Plugin: "test.a", Config: map[string]any{"invalid": make(chan int)}}}}, `bundle "test".instances[0] instance "a".config: must contain JSON data`},
	} {
		// Clone must preserve invalid values for validation, never silently replace
		// them with an empty config which could pass schema admission.
		if _, err := MarshalBundleDefinition(test.value.Clone()); err == nil || err.Error() != test.want {
			t.Fatal(err, test.want)
		}
	}
}

func TestRuntimeCodecRetainsDistinctSourceAcceptance(t *testing.T) {
	raw := []byte(`{"instances":null}`)
	value, err := Parse(raw)
	if err != nil || value.Instances != nil {
		t.Fatal("runtime KnownFields codec changed", value, err)
	}
	if _, err := ParseBundleDefinition([]byte(`{"id":"test","instances":null}`)); err == nil || !strings.Contains(err.Error(), "null is not allowed") {
		t.Fatal("Bundle codec accepted explicit null", err)
	}
	if _, err := Parse([]byte("instances: []\nextra: true\n")); err == nil {
		t.Fatal("runtime codec accepted unknown field")
	}
	if _, err := Parse([]byte("instances: []\n---\ninstances: []\n")); err == nil {
		t.Fatal("runtime codec accepted trailing document")
	}
}

// Programmatic config may contain a json.Marshaler. Cloning must retain the
// original invocation order even when later declaration admission fails.
type configMarshaler struct{ calls *int }

func (v configMarshaler) MarshalJSON() ([]byte, error) { *v.calls++; return []byte(`"value"`), nil }

func TestBundleProgrammaticCloneOrderBeforeAdmission(t *testing.T) {
	for _, id := range []string{"a", "invalid id"} {
		calls := 0
		config := map[string]any{"value": configMarshaler{calls: &calls}}
		_, err := MarshalBundleDefinition(BundleDefinition{ID: "test", Overrides: []Override{{ID: id, Config: &config}}})
		if id == "a" {
			if err != nil || calls != 2 {
				t.Fatal("valid config clone/copy order changed", calls, err)
			}
		} else if err == nil || calls != 1 {
			t.Fatal("invalid ID must fail after detached clone", calls, err)
		}
	}
}
