package build

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestCorePrecedenceAndMixing(t *testing.T) {
	t.Setenv("QI_VERSION", "environment-query")
	local := t.TempDir()
	core := CoreModule + "=" + local
	for _, tc := range []struct {
		name                string
		args                []string
		query, source, want string
	}{
		{name: "default environment", query: "environment-query", source: "QI_VERSION"},
		{
			name:   "positional mixed",
			args:   []string{"v1.0.0", "--with", core, "--output", "out"},
			query:  "v1.0.0",
			source: "positional",
		},
		{name: "with priority", args: []string{"--with", CoreModule + "@main=" + local}, query: "main", source: "with"},
		{
			name:   "same explicit core",
			args:   []string{"--with", CoreModule + "@main=" + local, "main"},
			query:  "main",
			source: "with",
		},
		{
			name: "conflicting core",
			args: []string{"main", "--with", CoreModule + "@branch=" + local},
			want: "conflicting core",
		},
		{name: "positions", args: []string{"one", "--output", "out", "two"}, want: "at most one"},
		{name: "legacy mixed", args: []string{"--manifest", "file", "--with", core}, want: "cannot be mixed"},
		{name: "source outside legacy", args: []string{"--source", core}, want: "requires legacy"},
		{name: "duplicate with", args: []string{"--with", core, "--with", core}, want: "duplicate --with"},
		{name: "duplicate replace", args: []string{"--replace", core, "--replace", core}, want: "duplicate --replace"},
		{
			name: "overlapping replacements",
			args: []string{"--with", core, "--replace", CoreModule + "@v1.0.0=" + local},
			want: "overlapping",
		},
		{
			name: "conflicting replacements",
			args: []string{"--with", core, "--replace", CoreModule + "=" + t.TempDir()},
			want: "conflicting",
		},
		{name: "branch left", args: []string{"--replace", CoreModule + "@main=" + local}, want: "must be fixed"},
		{
			name:   "remote replacement",
			args:   []string{"--with", CoreModule + "=example.com/fork@main"},
			query:  "environment-query",
			source: "QI_VERSION",
		},
		{name: "missing value", args: []string{"--with"}, want: "requires a value"},
		{name: "empty query", args: []string{"--with", CoreModule + "@=" + local}, want: "invalid module"},
		{name: "unknown", args: []string{"--core", local}, want: "unknown option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := parseCLI(tc.args)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v; want %s", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if options.Request.Core.Query != tc.query || options.Request.CoreQuerySource != tc.source {
				t.Fatalf("request: %+v", options.Request)
			}
		})
	}
	options, err := parseCLI([]string{"--with", core, "--replace", core})
	if err != nil || len(options.Request.Replacements) != 1 || len(options.Request.Extensions) != 0 {
		t.Fatalf("core replacement: %+v, %v", options, err)
	}
	t.Setenv("QI_VERSION", "")
	options, err = parseCLI(nil)
	if err != nil || options.Request.Core.Query != "latest" || options.Output != defaultOutput() {
		t.Fatal(options, err)
	}
}

func TestLocalResolutionDoesNotQueryUnpublishedModules(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	local := t.TempDir()
	args := []string{
		"--with",
		CoreModule + "=" + local,
		"--replace", BuiltinModule + "=" + local,
		"--with",
		"example.com/multi/v2=" + local,
		"--replace",
		"example.com/helper=" + local,
	}
	options, err := parseCLI(args)
	if err != nil {
		t.Fatal(err)
	}
	m, resolved, _, err := prepareRequest(options.Request)
	if err != nil || m.Core.Version != "v0.0.0" || m.Plugins[0].Version != "v2.0.0" {
		t.Fatal(m, err)
	}
	for _, selected := range resolved.Selections {
		if selected.QueryExecuted || !selected.DevelopmentPlaceholder {
			t.Fatal(selected)
		}
	}
	options, err = parseCLI([]string{"v1.0.0", "--with", CoreModule + "=" + local})
	if err != nil {
		t.Fatal(err)
	}
	m, resolved, _, err = prepareRequest(options.Request)
	if err != nil || m.Core.Version != "v1.0.0" || resolved.Selections[0].DevelopmentPlaceholder {
		t.Fatal(m, resolved, err)
	}
	// No local mapping must never reinterpret default latest as a remote v0.0.0.
	options, err = parseCLI(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, resolved, _, err = prepareRequest(options.Request); err != nil || !resolved.Selections[0].QueryExecuted {
		t.Fatal(resolved, err)
	}
}

func TestLegacyIgnoresNewEnvironment(t *testing.T) {
	t.Setenv("QI_VERSION", "invalid new query")
	options, err := parseCLI([]string{"--manifest", "old.yaml", "--output", "out", "--source", CoreModule + "=./qi"})
	if err != nil || options.Request != nil || options.Manifest != "old.yaml" {
		t.Fatal(options, err)
	}
}

func TestHelpDoesNotReadSourcesOrOutput(t *testing.T) {
	t.Setenv("QI_VERSION", "invalid query")
	path := filepath.Join(t.TempDir(), "missing")
	if code := Run(context.Background(), []string{"--with", CoreModule + "=" + path, "--help"}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestNewGraphRecordsMVSAndUnusedReplacements(t *testing.T) {
	resolution := Resolution{
		Selections: []ResolvedSelection{{Selection: Selection{Module: CoreModule}, RequiredVersion: "v0.0.0"}},
		Replacements: []ResolvedReplacement{
			{Replacement: Replacement{Module: CoreModule, Target: "/core"}},
			{Replacement: Replacement{Module: "example.com/unused", Target: "/unused"}},
		},
	}
	err := checkResolution([]moduleInfo{{Path: CoreModule, Version: "v0.1.0", Replace: &moduleInfo{Path: "/core"}}}, &resolution)
	if err != nil || resolution.Selections[0].FinalVersion != "v0.1.0" || !resolution.Replacements[0].Used || resolution.Replacements[1].Used {
		t.Fatal(resolution, err)
	}
}

func TestExistingBareRelativeReplacement(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	parent := t.TempDir()
	t.Chdir(parent)
	if err := os.Mkdir("local core", 0755); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(parent, "local core"))
	if err != nil {
		t.Fatal(err)
	}
	options, err := parseCLI([]string{"--with", CoreModule + "=local core"})
	if err != nil || options.Request.Replacements[0].Target != expected {
		t.Fatal(options, err)
	}
}

func TestHelpFlagValuesAndSeparator(t *testing.T) {
	for _, args := range [][]string{{"--with", "--help"}, {"--", "--help"}} {
		if code := Run(context.Background(), args, io.Discard, io.Discard); code == 0 {
			t.Fatalf("%v incorrectly requested help", args)
		}
	}
}

func TestRemoteReplacementNormalization(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	options, err := parseCLI([]string{
		"--with",
		"example.com/plugin=example.com/fork",
		"--replace",
		"example.com/plugin=example.com/fork@latest",
	})
	if err != nil || len(options.Request.Replacements) != 1 || options.Request.Replacements[0].Target != "example.com/fork@latest" {
		t.Fatal(options, err)
	}
	for _, value := range []string{"example.com/fork@", "example.com/fork@main=./nested"} {
		if _, err := parseCLI([]string{"--replace", "example.com/plugin=" + value}); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
