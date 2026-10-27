package build

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	builder "github.com/RussellLuo/gordis/build"
)

func TestPublicCLIQiPolicy(t *testing.T) {
	t.Setenv("QI_VERSION", "env-query")
	for _, tc := range []struct {
		name                        string
		positionals                 []string
		packages                    []builder.Package
		core, builtin, source, want string
	}{
		{name: "environment", core: "env-query", builtin: "env-query", source: "QI_VERSION"},
		{name: "positional", positionals: []string{"main"}, core: "main", builtin: "main", source: "positional"},
		{name: "kernel with", packages: []builder.Package{{Path: CoreModule, Query: builder.Query{Value: "branch", Explicit: true}}}, core: "branch", builtin: "branch", source: "with"},
		{name: "builtin override", packages: []builder.Package{{Path: BuiltinModule + "/extra", Query: builder.Query{Value: "v1.2.3", Explicit: true}}}, core: "env-query", builtin: "v1.2.3", source: "QI_VERSION"},
		{name: "core conflict", positionals: []string{"main"}, packages: []builder.Package{{Path: CoreModule, Query: builder.Query{Value: "other", Explicit: true}}}, want: "conflicting core"},
		{name: "too many versions", positionals: []string{"one", "two"}, want: "at most one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, adapter, err := prepareCLI(builder.Arguments{
				Request:     builder.Request{Dir: t.TempDir(), Output: "qi", Packages: tc.packages},
				Positionals: tc.positionals,
			})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v; want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if request.Modules[0].Query.Value != tc.core || request.Modules[1].Query.Value != tc.builtin {
				t.Fatal(request.Modules)
			}
			for i, path := range []string{CoreModule + "/boot", CoreModule + "/boot/application", CoreModule + "/boot/launch", BuiltinModule + "/bundle"} {
				query := request.Modules[0].Query
				if i == 3 {
					query = request.Modules[1].Query
				}
				if request.Packages[i].Path != path || request.Packages[i].Query != query {
					t.Fatal("bootstrap package inherited the wrong module query", request.Packages[i])
				}
			}
			project, err := adapter.Generate(builder.Plan{})
			if err != nil || project.Files["go.mod"] != nil || project.Files["go.sum"] != nil || !reflect.DeepEqual(adapter.Validate.Args, []string{"--help"}) {
				t.Fatal(project, adapter.Validate, err)
			}
			facts := builder.Facts{Plan: builder.Plan{Requirements: []builder.ResolvedRequirement{
				{Path: CoreModule, Query: request.Modules[0].Query}, {Path: BuiltinModule, Query: request.Modules[1].Query},
			}}}
			if len(tc.packages) > 0 && tc.packages[0].Path != CoreModule {
				facts.Plan.Packages = []builder.ResolvedPackage{{Path: tc.packages[0].Path, Module: BuiltinModule}}
			}
			files, err := adapter.Record(facts)
			if err != nil {
				t.Fatal(err)
			}
			var record requestRecord
			if err := json.Unmarshal(files["record.json"], &record); err != nil || record.Request.CoreQuerySource != tc.source {
				t.Fatal(record, err)
			}
		})
	}
	t.Setenv("QI_VERSION", "")
	defaults, _, err := prepareCLI(builder.Arguments{Request: builder.Request{Dir: t.TempDir(), Output: "qi"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range defaults.Modules {
		if m.Query.Value != "latest" || m.Query.Explicit {
			t.Fatal(defaults.Modules)
		}
	}
	// These values belong to the legacy parser, and cannot request help.
	for _, flag := range []string{"--manifest", "--source", "-manifest", "-source"} {
		if code := Run(context.Background(), []string{flag, "--help", "--output", filepath.Join(t.TempDir(), "qi")}, io.Discard, io.Discard); code == 0 {
			t.Fatalf("%s value incorrectly requested help", flag)
		}
	}
}

func TestLegacyAdapterKeepsExactRequirementsAndPackageOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.yaml")
	raw := []byte("apiVersion: qi.build/v1alpha1\ncore: {version: v0.1.0}\nplugins: [{module: example.com/extension, package: example.com/extension/sub, version: v1.0.0, execution: local}]\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	options := Options{Manifest: path, Output: "qi"}
	request, adapter, err := prepareOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{CoreModule: "v0.1.0", BuiltinModule: "v0.1.0", "example.com/extension": "v1.0.0"}
	if len(request.Modules) != len(versions) {
		t.Fatal(request.Modules)
	}
	for _, module := range request.Modules {
		if !module.Exact || !module.Query.Explicit || module.Query.Value != versions[module.Path] {
			t.Fatal(module)
		}
	}
	plan := builder.Plan{Packages: []builder.ResolvedPackage{{Path: "example.com/extension/sub", Module: "example.com/extension"}}}
	if _, err := adapter.Generate(plan); err != nil {
		t.Fatal(err)
	}
	plan.Packages[0].Module = "example.com/extension/sub"
	if _, err := adapter.Generate(plan); err == nil || !strings.Contains(err.Error(), "must belong") {
		t.Fatal("legacy module identity was not checked", err)
	}
	for _, sources := range [][]string{{"example.com/unselected=./source"}, {CoreModule + "=./one", CoreModule + "=./two"}} {
		options.Sources = sources
		if _, _, err := prepareOptions(options); err == nil {
			t.Fatal("accepted invalid legacy sources", sources)
		}
	}
}
