package process

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/tools"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestRegistrationDerivesDeclarationsWithoutBusinessConstruction(t *testing.T) {
	spec := new(fixture.ProfilePlugin).Spec()
	spec.New = func() gordis.Plugin { panic("Registration constructed business Plugin") }
	descriptor := profile.PluginDescriptor{
		Title: "Test process", ConfigSchema: json.RawMessage(`{"type":"object"}`),
		UpdatePolicy: profile.UpdateImmutable, Compatibility: profile.Compatibility{HostAPI: profile.HostAPIVersion},
		Requires: []profile.CapabilityRef{{ID: "ignored"}}, Provides: []profile.CapabilityRef{{ID: "ignored"}},
		ValidateConfig: func(json.RawMessage) error { return nil },
	}
	r, err := Registration(&specPlugin{spec}, descriptor, Options{Command: "/does/not/exist"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.ProfileVisible || r.Plugin.Spec().ID != spec.ID || r.Descriptor.ID != spec.ID ||
		r.Descriptor.Title != descriptor.Title || r.Descriptor.UpdatePolicy != descriptor.UpdatePolicy ||
		r.Descriptor.Compatibility.HostAPI != descriptor.Compatibility.HostAPI || r.Descriptor.ValidateConfig == nil ||
		!reflect.DeepEqual(r.Descriptor.Requires, []profile.CapabilityRef{{ID: tools.RuntimeKey.Name()}}) || len(r.Descriptor.Provides) != 0 {
		t.Fatalf("registration = %+v", r)
	}
	ext := application.Extension{Registrations: []registration.PluginRegistration{r}}
	if _, err := application.Compose(ext); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Compose(ext, ext); err == nil {
		t.Fatal("duplicate Adapter ID accepted")
	}
	local := application.Extension{Registrations: []registration.PluginRegistration{{Plugin: new(fixture.ProfilePlugin), Descriptor: descriptor, ProfileVisible: true}}}
	if _, err := application.Compose(ext, local); err == nil {
		t.Fatal("same-ID local/process registration accepted")
	}
	descriptor.ConfigSchema[0] = '['
	if string(r.Descriptor.ConfigSchema) != `{"type":"object"}` {
		t.Fatal("descriptor schema aliases caller")
	}
	descriptor.ID = "wrong"
	if _, err := Registration(new(fixture.ProfilePlugin), descriptor, adapterOptions()); err == nil {
		t.Fatal("mismatched descriptor ID accepted")
	}
}

// Registration/configuration validation is pure; transport execution and
// process cleanup are covered by adapter_test.go and server_test.go.
func TestRegistrationPreservesPureConfigurationValidation(t *testing.T) {
	forbidden := errors.New("forbidden prefix")
	descriptor := profile.PluginDescriptor{
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"prefix":{"type":"string"},"mode":{"type":"string"}},"required":["prefix"],"additionalProperties":false}`),
		ValidateConfig: func(raw json.RawMessage) error {
			var config fixture.Plugin
			if err := json.Unmarshal(raw, &config); err != nil {
				return err
			}
			if config.Prefix == "forbidden" {
				return forbidden
			}
			return nil
		},
	}
	r, err := Registration(new(fixture.ProfilePlugin), descriptor, Options{Command: "/does/not/exist"})
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(r.Descriptor.ConfigSchema, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("config.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("config.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		wantError bool
	}{
		{"valid", `{"prefix":"Hello, "}`, false},
		{"deferred-business-validation", `{"prefix":"Hello, ","mode":"validate"}`, false},
		{"wrong-type", `{"prefix":42}`, true},
		{"unknown-field", `{"prefix":"x","unknown":true}`, true},
		{"custom-validator", `{"prefix":"forbidden"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			if err := json.Unmarshal([]byte(tc.raw), &value); err != nil {
				t.Fatal(err)
			}
			err := schema.Validate(value)
			if err == nil {
				err = r.Descriptor.ValidateConfig(json.RawMessage(tc.raw))
			}
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if tc.name == "custom-validator" && !errors.Is(err, forbidden) {
				t.Fatal("lost custom validation", err)
			}
		})
	}
}
