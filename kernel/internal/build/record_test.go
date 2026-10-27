package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	builder "github.com/RussellLuo/gordis/build"
)

func TestRecordEncodesCLIAndLegacyFacts(t *testing.T) {
	facts := builder.Facts{
		Plan: builder.Plan{Requirements: []builder.ResolvedRequirement{
			{Path: CoreModule, Query: builder.Query{Value: "latest"}, RequiredVersion: "v1.0.0"},
			{Path: BuiltinModule, Query: builder.Query{Value: "latest"}, RequiredVersion: "v2.0.0"},
		}},
		Modules:     []builder.ModuleRecord{{Path: CoreModule, Version: "v1.1.0", Replace: &builder.ModuleRecord{Path: "/src/kernel"}}},
		Inputs:      []builder.InputRecord{{Module: CoreModule, Directory: "/src/kernel", Files: map[string]string{"go.mod": "file-hash"}, SHA256: "input-hash"}},
		Commands:    []builder.CommandRecord{{Stage: "validate", Args: []string{"qi", "--help"}, Log: "logs/001.log"}},
		Environment: map[string]string{"GOWORK": "off"}, GeneratedSHA256: map[string]string{"main.go": "generated-hash"},
		BinarySHA256: "binary-hash", LocalDevelopment: true,
	}
	for _, mode := range []string{"cli", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			var request *Request
			if mode == "cli" {
				request = &Request{Args: []string{"--output", "qi"}, CWD: "/work", Core: Selection{Module: CoreModule, Query: "latest"}, CoreQuerySource: "default"}
			}
			manifestRaw := []byte("apiVersion: qi.build/v1alpha1\ncore: {version: v1.0.0}\n")
			originalRaw := bytes.Clone(manifestRaw)
			manifest, err := Parse(manifestRaw)
			if err != nil {
				t.Fatal(err)
			}
			encode := qiRecord(request, manifest, manifestRaw)
			// Records must retain the intent captured before the build starts.
			manifestRaw[0] = '!'
			if request != nil {
				request.Args[0], request.Core.Query = "changed", "changed"
			}
			files, err := encode(facts)
			if err != nil {
				t.Fatal(err)
			}
			var record Record
			if err := json.Unmarshal(files["record.json"], &record); err != nil {
				t.Fatal(err)
			}
			wantSources := []sourceRecord{{Module: CoreModule, Directory: "/src/kernel", TargetVersion: "v1.0.0", Files: map[string]string{"go.mod": "file-hash"}, SHA256: "input-hash"}}
			if !reflect.DeepEqual(record.Sources, wantSources) || len(record.Modules) != 1 ||
				record.Modules[0].Version != "v1.1.0" || record.Modules[0].Replace == nil || record.Modules[0].Replace.Path != "/src/kernel" ||
				!record.LocalDevelopment || record.BinarySHA256 != "binary-hash" ||
				!reflect.DeepEqual(record.Environment, facts.Environment) || !reflect.DeepEqual(record.GeneratedSHA256, facts.GeneratedSHA256) ||
				!reflect.DeepEqual(record.Commands, []commandRecord{{Args: []string{"qi", "--help"}, Log: "logs/001.log"}}) {
				t.Fatal("public build facts were lost", string(files["record.json"]))
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(files["record.json"], &envelope); err != nil {
				t.Fatal(err)
			}
			keys := []string{"apiVersion", "localDevelopment", "sources", "modules", "environment", "generatedSHA256", "binarySHA256", "commands"}
			var names, wantNames []string
			if mode == "cli" {
				keys = append(keys, "request", "requestSHA256", "resolved")
				wantNames = []string{"record.json", "request.json", "resolved-build.json"}
				var cli requestRecord
				if err := json.Unmarshal(files["record.json"], &cli); err != nil {
					t.Fatal(err)
				}
				if cli.APIVersion != "qi.build-record/v1alpha2" || cli.Request.Core.Query != "latest" || cli.Request.Args[0] != "--output" ||
					cli.RequestSHA256 != fmt.Sprintf("%x", sha256.Sum256(files["request.json"])) {
					t.Fatal(cli)
				}
			} else {
				keys = append(keys, "manifest", "manifestSHA256")
				wantNames = []string{"manifest-input.yaml", "manifest.yaml", "record.json"}
				if record.APIVersion != "qi.build-record/v1alpha1" || record.Manifest.Core.Version != "v1.0.0" ||
					record.ManifestSHA256 != fmt.Sprintf("%x", sha256.Sum256(originalRaw)) || !bytes.Equal(files["manifest-input.yaml"], originalRaw) {
					t.Fatal(record)
				}
				if _, err := Parse(files["manifest.yaml"]); err != nil {
					t.Fatal(err)
				}
			}
			for name := range files {
				names = append(names, name)
			}
			slices.Sort(names)
			if !slices.Equal(names, wantNames) || len(envelope) != len(keys) {
				t.Fatal("Qi record envelope changed", names, envelope)
			}
			for _, key := range keys {
				if _, exists := envelope[key]; !exists {
					t.Fatal("missing record field", key)
				}
			}
		})
	}
}

func TestResolutionPreservesModuleAndForkVersions(t *testing.T) {
	request := Request{Core: Selection{Module: CoreModule, Query: "latest"}, Extensions: []Selection{{Module: "example.com/extension/sub", Query: "feature", ExplicitQuery: true}}}
	facts := builder.Facts{
		Plan: builder.Plan{
			Packages: []builder.ResolvedPackage{{Path: "example.com/extension/sub", Module: "example.com/extension"}},
			Requirements: []builder.ResolvedRequirement{
				{Path: CoreModule, Query: builder.Query{Value: "latest"}, RequiredVersion: "v1.0.0", QueryExecuted: true},
				{Path: BuiltinModule, Query: builder.Query{Value: "latest"}, RequiredVersion: "v0.0.0", DevelopmentPlaceholder: true},
				{Path: "example.com/extension", Query: builder.Query{Value: "feature", Explicit: true}, RequiredVersion: "v1.2.0", QueryExecuted: true},
			},
		},
		Modules: []builder.ModuleRecord{{Path: CoreModule, Version: "v1.1.0"}, {Path: BuiltinModule, Version: "v0.0.0"}, {Path: "example.com/extension", Version: "v1.3.0"}},
		Replacements: []builder.ReplacementRecord{
			{ResolvedReplacement: builder.ResolvedReplacement{Module: "example.com/extension", Source: builder.Source{Module: "example.com/fork", Query: "main"}, TargetVersion: "v4.0.0", QueryExecuted: true}, Used: true},
			{ResolvedReplacement: builder.ResolvedReplacement{Module: "example.com/unused", Source: builder.Source{Directory: "/src/unused"}}},
		},
	}
	got, err := qiResolution(request, facts)
	if err != nil {
		t.Fatal(err)
	}
	want := Resolution{
		Selections: []ResolvedSelection{
			{Selection: Selection{CoreModule, "latest", false}, RequiredVersion: "v1.0.0", FinalVersion: "v1.1.0", QueryExecuted: true},
			{Selection: Selection{"example.com/extension", "feature", true}, Entry: "example.com/extension/sub", RequiredVersion: "v1.2.0", FinalVersion: "v1.3.0", QueryExecuted: true},
			{Selection: Selection{BuiltinModule, "latest", false}, Entry: BuiltinModule + "/bundle", RequiredVersion: "v0.0.0", FinalVersion: "v0.0.0", DevelopmentPlaceholder: true},
		},
		Replacements: []ResolvedReplacement{
			{Replacement: Replacement{Module: "example.com/extension", Target: "example.com/fork"}, TargetVersion: "v4.0.0", QueryExecuted: true, Used: true},
			{Replacement: Replacement{Module: "example.com/unused", Target: "/src/unused"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	facts.Plan.Requirements = facts.Plan.Requirements[:1]
	if _, err := qiResolution(request, facts); err == nil || !strings.Contains(err.Error(), "missing requirement") {
		t.Fatal("missing public facts were accepted", err)
	}
}
