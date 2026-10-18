package registration_test

import (
	"errors"
	"fmt"
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/internal/bundles"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
)

func TestPreparePreservesSourceAndBusinessConfig(t *testing.T) {
	activated := false
	registrations := []registration.PluginRegistration{{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true,
		ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
			return map[string]any{"command": command, "config": business}, nil
		},
	}}
	base := t.TempDir()
	for _, ref := range []string{"./missing program", "../missing", filepath.Join(base, "absolute program")} {
		source, err := appprofile.Compose(appprofile.ProfileSource{Instances: []appprofile.InstanceSpec{{ID: "tool", Plugin: ref, Config: map[string]any{"command": "business", "loader": "business", "identity": "business"}}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		source.BaseDir = base
		before := source.Clone()
		execution, err := registration.Prepare(registrations, source)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(source, before) || !reflect.DeepEqual(execution.Source, source) || execution.Source.SourceDigest != source.SourceDigest {
			t.Fatal("source mutated")
		}
		in := execution.Profile.Instances[0]
		expected := ref
		if !filepath.IsAbs(ref) {
			expected = filepath.Join(base, ref)
		}
		if in.Plugin != "test/pure" || in.Config["command"] != expected || !reflect.DeepEqual(in.Config["config"], source.Profile.Instances[0].Config) {
			t.Fatal(in)
		}
		if _, err := registration.InvocationParser(registrations, execution.Profile); err != nil {
			t.Fatal(err)
		}
		if activated {
			t.Fatal("planning activated plugin")
		}
	}
}

func TestPrepareAdmissionAndDisabled(t *testing.T) {
	activated := false
	handler := registration.PluginRegistration{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
		return map[string]any{"command": command, "config": business}, nil
	}}
	disabled := true
	source, err := appprofile.Compose(appprofile.ProfileSource{Instances: []appprofile.InstanceSpec{{ID: "tool", Plugin: "./missing"}}, Overrides: []appprofile.Override{{ID: "tool", Disabled: &disabled}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	source.BaseDir = t.TempDir()
	other := handler
	other.Plugin = &otherPathPrototype{purePrototype{activated: &activated}}
	for _, r := range [][]registration.PluginRegistration{nil, {handler, other}} {
		if _, err := registration.Prepare(r, source); err == nil {
			t.Fatal("missing/ambiguous handler admitted")
		}
	}
	private := handler
	private.ProfileVisible = false
	if _, err := registration.Prepare([]registration.PluginRegistration{private}, source); err == nil {
		t.Fatal("private handler admitted")
	}
	execution, err := registration.Prepare([]registration.PluginRegistration{handler}, source)
	if err != nil || len(execution.Profile.Instances) != 0 {
		t.Fatal(execution, err)
	}
	if len(execution.Declarations[0].Spec.Config["config"].(map[string]any)) != 0 {
		t.Fatal("omitted business config")
	}
	source.BaseDir = ""
	if _, err := registration.Prepare([]registration.PluginRegistration{handler}, source); err == nil || !strings.Contains(err.Error(), "tool") || !strings.Contains(err.Error(), "./missing") {
		t.Fatal(err)
	}
	source.Declarations[0].Spec.Plugin = "example/tool-process"
	if _, err = registration.Prepare(nil, source); err == nil {
		t.Fatal("unknown ID admitted")
	}
}

func TestExecutionChangesWhenPathBusinessOrLocationChanges(t *testing.T) {
	activated := false
	handler := registration.PluginRegistration{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
		return map[string]any{"command": command, "config": business}, nil
	}}
	source, err := appprofile.Compose(appprofile.ProfileSource{Instances: []appprofile.InstanceSpec{{ID: "tool", Plugin: "./a", Config: map[string]any{"prefix": "hello"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	source.BaseDir = t.TempDir()
	first, err := registration.Prepare([]registration.PluginRegistration{handler}, source)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := first.Profile.Digest()
	for _, change := range []func(*appprofile.ResolvedProfile){
		func(p *appprofile.ResolvedProfile) { p.Declarations[0].Spec.Plugin = "./b" },
		func(p *appprofile.ResolvedProfile) { p.Declarations[0].Spec.Config["prefix"] = "changed" },
		func(p *appprofile.ResolvedProfile) { p.BaseDir = t.TempDir() },
		func(p *appprofile.ResolvedProfile) { p.Declarations[0].Spec.Plugin = "test/pure" },
	} {
		next := source.Clone()
		change(&next)
		execution, err := registration.Prepare([]registration.PluginRegistration{handler}, next)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := execution.Profile.Digest()
		if got == digest {
			t.Fatal("execution failed to reflect update")
		}
	}
}

type otherPathPrototype struct{ purePrototype }

func (p *otherPathPrototype) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/other", New: func() gordis.Plugin { return &otherPathPrototype{p.purePrototype} }}
}

func TestPrepareComposedBundleReferences(t *testing.T) {
	activated := false
	handler := registration.PluginRegistration{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
		return map[string]any{"command": command, "config": business}, nil
	}}
	catalog, err := bundles.NewCatalogWith([]byte(`id: tools
instances:
- id: tool
  plugin: ./bundle-program
  config: {prefix: old}
  restartToken: old
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "./replacement", "test/pure"} {
		t.Run(ref, func(t *testing.T) {
			source := appprofile.ProfileSource{Bundles: []string{"tools"}}
			if ref != "" {
				source.Overrides = []appprofile.Override{{ID: "tool", Plugin: &ref}}
			}
			resolved, err := appprofile.Compose(source, catalog)
			if err != nil {
				t.Fatal(err)
			}
			resolved.BaseDir = t.TempDir()
			before := resolved.Clone()
			prepared, err := registration.Prepare([]registration.PluginRegistration{handler}, resolved)
			if err != nil {
				t.Fatal(err)
			}
			in := prepared.Profile.Instances[0]
			if in.Plugin != "test/pure" || !reflect.DeepEqual(resolved, before) || !reflect.DeepEqual(prepared.Source, before) {
				t.Fatal(prepared)
			}
			if ref == "test/pure" {
				if len(in.Config) != 0 || in.RestartToken != "" {
					t.Fatal(in)
				}
			} else {
				target := ref
				if target == "" {
					target = "./bundle-program"
				}
				if in.Config["command"] != filepath.Join(resolved.BaseDir, target) {
					t.Fatal(in)
				}
				business := in.Config["config"].(map[string]any)
				if ref == "" && (business["prefix"] != "old" || in.RestartToken != "old") {
					t.Fatal(in)
				}
				if ref != "" && (len(business) != 0 || in.RestartToken != "") {
					t.Fatal(in)
				}
			}
		})
	}
}

func TestPrepareIDToPathOverride(t *testing.T) {
	activated := false
	handler := registration.PluginRegistration{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
		return map[string]any{"command": command, "config": business}, nil
	}}
	catalog, err := bundles.NewCatalogWith([]byte("id: tools\ninstances: [{id: tool, plugin: test/pure, config: {prefix: old}, restartToken: old}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	ref, token := "./replacement", "new"
	config := map[string]any{"prefix": "new"}
	source, err := appprofile.Compose(appprofile.ProfileSource{Bundles: []string{"tools"}, Overrides: []appprofile.Override{{ID: "tool", Plugin: &ref, Config: &config, RestartToken: &token}}}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	source.BaseDir = t.TempDir()
	result, err := registration.Prepare([]registration.PluginRegistration{handler}, source)
	if err != nil {
		t.Fatal(err)
	}
	in := result.Profile.Instances[0]
	if in.Plugin != "test/pure" || in.RestartToken != "new" || in.Config["command"] != filepath.Join(source.BaseDir, "replacement") || !reflect.DeepEqual(in.Config["config"], config) {
		t.Fatal(in)
	}
}

func TestAssemblyPreservesReferenceMetadata(t *testing.T) {
	activated := false
	wrap := func(prefix string) func(string, map[string]any) (map[string]any, error) {
		return func(command string, business map[string]any) (map[string]any, error) {
			return map[string]any{"command": prefix + command}, nil
		}
	}
	selected := []registration.PluginRegistration{{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, ResolvePath: wrap("a")}}
	bound := append([]registration.PluginRegistration(nil), selected...)
	bound[0].Plugin = &purePrototype{activated: &activated}
	if err := registration.ValidateAssembly(selected, bound); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*registration.PluginRegistration){
		func(r *registration.PluginRegistration) { r.ResolvePath = wrap("b") },
		func(r *registration.PluginRegistration) { r.ProfileVisible = false },
		func(r *registration.PluginRegistration) { r.Completes = true },
		func(r *registration.PluginRegistration) { r.Descriptor.RequiresModels = true },
		func(r *registration.PluginRegistration) {
			r.Plugin = &otherPathPrototype{purePrototype{activated: &activated}}
		},
	} {
		modified := append([]registration.PluginRegistration(nil), bound...)
		change(&modified[0])
		if err := registration.ValidateAssembly(selected, modified); err == nil {
			t.Fatal("assembly metadata change accepted")
		}
	}
}

func TestPlanRequiresPreparedIDsAndDiagnosisIsSpecific(t *testing.T) {
	activated := false
	r := []registration.PluginRegistration{{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true}}
	if _, err := registration.Plan(r, appprofile.Profile{Instances: []appprofile.InstanceSpec{{ID: "one", Plugin: "/program"}}}); err == nil {
		t.Fatal("Plan accepted path")
	}
	p := registration.PreparedProfile{Source: appprofile.ResolvedProfile{Declarations: []appprofile.ComposedInstance{
		{Spec: appprofile.InstanceSpec{ID: "one", Plugin: "./one"}},
		{Spec: appprofile.InstanceSpec{ID: "two", Plugin: "./two"}},
	}}}
	err := p.Diagnose(fmt.Errorf("mount one: failed"))
	if !strings.Contains(err.Error(), "./one") || strings.Contains(err.Error(), "./two") {
		t.Fatal(err)
	}
	unrelated := errors.New("output failed")
	if p.Diagnose(unrelated) != unrelated {
		t.Fatal("unrelated error decorated")
	}
}
