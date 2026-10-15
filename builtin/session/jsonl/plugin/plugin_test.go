package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestMetadataAndConfigurationDoNotTouchStorage(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "untouched")
	registration := Extension().Registrations[0]
	prototype := registration.Plugin.(*Plugin)
	spec := prototype.Spec()
	if spec.ID != PluginID || !reflect.DeepEqual(spec.Provides, []gordis.ServiceSpec{session.PersistenceKey.Spec()}) || len(spec.Requires) != 0 || spec.New() == prototype {
		t.Fatalf("persistence declaration: %+v", spec)
	}
	var document any
	if err := json.Unmarshal(registration.Descriptor.ConfigSchema, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("persistence.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("persistence.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{}`, false}, {`{"directory":""}`, false}, {`{"directory":42}`, false},
		{`{"directory":"sessions","unknown":true}`, false}, {`{"directory":"sessions"}`, true},
	} {
		var config any
		if err := json.Unmarshal([]byte(tc.raw), &config); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(config); (err == nil) != tc.valid {
			t.Fatalf("config %s: %v", tc.raw, err)
		}
	}
	for _, value := range []string{"", " ", directory} {
		p := Plugin{Directory: value}
		if err := p.Validate(); (err == nil) != (value == directory) {
			t.Fatal(value, err)
		}
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pure metadata/validation touched storage", err)
	}
}
