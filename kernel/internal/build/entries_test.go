package build

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeEntryFixture(t *testing.T, root, module string) {
	t.Helper()
	if err := os.CopyFS(root, os.DirFS("testdata/selective/fixture")); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "example.com/qi-n02-fixture", module)), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(strings.ReplaceAll(string(raw), "example.com/qi-n02-fixture", module)), 0600); err != nil {
		t.Fatal(err)
	}
	// The same module exposes both a root factory and subpackage factories.
	if err := os.WriteFile(filepath.Join(root, "extension.go"), []byte("package fixture\nimport \""+module+"/base\"\nvar Extension = base.Extension\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func entryOptions(t *testing.T, output string, args ...string) Options {
	t.Helper()
	core, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	options, err := parseCLI(append([]string{"--replace", CoreModule + "=" + core, "--replace", BuiltinModule + "=" + filepath.Join(core, "internal/build/testdata/defaultbuiltin"), "--output", output}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func TestPackageEntriesShareModuleAndDefaultComposition(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	module := "example.com/package-fixture"
	fixture := t.TempDir()
	writeEntryFixture(t, fixture, module)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, "This Profile does not accept application arguments"},
		{"root", []string{"--with", module + "=" + fixture}, "root task\n"},
		{"subpackages", []string{"--with", module + "/base=" + fixture, "--with", module + "/optional=" + fixture,
			"--replace", module + "=" + fixture}, "subpackages task\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "qi")
			options := entryOptions(t, binary, tc.args...)
			recordDir, err := Build(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			var record requestRecord
			raw, err := os.ReadFile(filepath.Join(recordDir, "record.json"))
			if err != nil || json.Unmarshal(raw, &record) != nil || record.APIVersion != "qi.build-record/v1alpha2" {
				t.Fatal(err, string(raw))
			}
			generated, _ := os.ReadFile(filepath.Join(recordDir, "project/internal/compiledplugins/plugins_gen.go"))
			if strings.Count(string(generated), "builtin.Extension()") != 1 {
				t.Fatal("default builtin composition changed", string(generated))
			}
			profile, args := "bundles: [fixture]\n", []string{tc.name, "task"}
			if tc.name == "default" {
				profile, args = "bundles: [base]\n", []string{"--help"}
			}
			path := filepath.Join(t.TempDir(), "profile.yaml")
			if err := os.WriteFile(path, []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			answer, err := exec.Command(binary, append([]string{"--profile", path, "--"}, args...)...).CombinedOutput()
			if err != nil || !strings.Contains(string(answer), tc.want) {
				t.Fatal(err, string(answer))
			}
			if tc.name == "subpackages" {
				mod, _ := os.ReadFile(filepath.Join(recordDir, "project/go.mod"))
				if strings.Count(string(mod), module+" v0.0.0") != 1 || strings.Count(string(mod), "replace "+module+" =>") != 1 {
					t.Fatal("subpackage requires/replaces were not shared", string(mod))
				}
				for i, selected := range record.Resolved.Selections[1 : len(record.Resolved.Selections)-1] {
					if selected.Module != module || selected.Entry != options.Request.Extensions[i].Module || !selected.DevelopmentPlaceholder || selected.QueryExecuted {
						t.Fatal(selected)
					}
				}
			}
			if _, err := Build(context.Background(), options); err == nil || !strings.Contains(err.Error(), "refusing existing") {
				t.Fatal("overwrote output", err)
			}
		})
	}
}

func TestEntryOwnershipAndSharedSelectionErrors(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	module := "example.com/shared-fixture"
	first, second := t.TempDir(), t.TempDir()
	writeEntryFixture(t, first, module)
	writeEntryFixture(t, second, module)
	nested := filepath.Join(first, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module "+module+"/nested\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"shared explicit", []string{"--with", module + "/base=" + first, "--with", module + "/optional@v1.2.3=" + first}, ""},
		{"query conflict", []string{"--with", module + "/base@v1.0.0=" + first, "--with", module + "/optional@v1.1.0=" + first}, "conflicting queries for module " + module},
		{"source conflict", []string{"--with", module + "/base=" + first, "--with", module + "/optional=" + second}, "source mappings for module " + module},
		{"outside source", []string{"--with", "example.com/foreign/sub=" + first}, "does not belong to source module " + module},
		{"replace package key", []string{"--with", module + "/base", "--replace", module + "/base=" + first}, "source module mismatch"},
		{"nested module", []string{"--with", module + "/nested/sub", "--replace", module + "=" + first}, "crosses nested module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := entryOptions(t, filepath.Join(t.TempDir(), "qi"), tc.args...)
			r := &runner{ctx: context.Background(), dir: t.TempDir(), env: nativeEnvironment()}
			m, resolved, _, err := resolveRequest(r, options.Request, "1.27")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%v; want %s", err, tc.want)
				}
				return
			}
			if err != nil || len(m.Plugins) != 2 {
				t.Fatal(m, err)
			}
			for _, selection := range resolved.Selections[1 : len(resolved.Selections)-1] {
				if selection.RequiredVersion != "v1.2.3" || selection.QueryExecuted || selection.DevelopmentPlaceholder {
					t.Fatal(selection)
				}
			}
		})
	}
}

func TestPackageFactoryFailuresPublishNothing(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	module := "example.com/failing-entry"
	fixture := t.TempDir()
	writeEntryFixture(t, fixture, module)
	for _, tc := range []struct{ entry, want string }{
		{"duplicate", "duplicate"}, {"duplicatebundle", "duplicate"}, {"jsonl", "duplicate"},
		{"invalid", "compiled Application validation failed"}, {"wrong", "cannot use"}, {"missing", "undefined"},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "qi")
			options := entryOptions(t, binary, "--with", module+"/base="+fixture, "--with", module+"/"+tc.entry+"="+fixture)
			if _, err := Build(context.Background(), options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v; want %s", err, tc.want)
			}
			for _, path := range []string{binary, binary + ".build"} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed build published output", path, err)
				}
			}
		})
	}
}

// A file GOPROXY exercises Go's real package/module discovery, queries and MVS
// without a network service, published Qi version or test server listener.
func writeProxyModule(t *testing.T, proxy, module, version string, files map[string]string) {
	t.Helper()
	directory := filepath.Join(proxy, filepath.FromSlash(module), "@v")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		version + ".info": `{"Version":"` + version + `","Time":"2026-01-01T00:00:00Z"}`,
		version + ".mod":  files["go.mod"],
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list := filepath.Join(directory, "list")
	old, _ := os.ReadFile(list)
	if err := os.WriteFile(list, append(old, []byte(version+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(directory, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, raw := range files {
		entry, err := archive.Create(module + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedPackageOwnershipQueriesAndMVS(t *testing.T) {
	t.Setenv("QI_VERSION", "")
	proxy := t.TempDir()
	cached, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy)+",file://"+filepath.ToSlash(filepath.Join(strings.TrimSpace(string(cached)), "cache/download")))
	t.Setenv("GOSUMDB", "off")
	// Keep proxy fixtures out of the user's module cache. Go extracts module
	// directories read-only; restore directory permissions before TempDir cleanup.
	cache := t.TempDir()
	t.Setenv("GOMODCACHE", cache)
	t.Cleanup(func() {
		if err := filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0755)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	module := "example.com/qi-r01-published"
	extension := "package extension\nimport \"github.com/RussellLuo/qi/kernel/boot/application\"\nfunc Extension() application.Extension { return application.Extension{} }\n"
	for _, version := range []string{"v1.0.0", "v1.1.0"} {
		writeProxyModule(t, proxy, module, version, map[string]string{
			"go.mod":         "module " + module + "\n\ngo 1.27\nrequire github.com/RussellLuo/qi/kernel v0.0.0\n",
			"a/extension.go": extension, "b/extension.go": extension,
		})
	}
	upgrader := "example.com/qi-r01-upgrader"
	writeProxyModule(t, proxy, upgrader, "v1.0.0", map[string]string{
		"go.mod":       "module " + upgrader + "\n\ngo 1.27\nrequire (\n github.com/RussellLuo/qi/kernel v0.0.0\n " + module + " v1.1.0\n)\n",
		"extension.go": extension,
	})
	nested := module + "/nested"
	writeProxyModule(t, proxy, nested, "v0.1.0", map[string]string{
		"go.mod":         "module " + nested + "\n\ngo 1.27\nrequire github.com/RussellLuo/qi/kernel v0.0.0\n",
		"a/extension.go": extension,
	})
	binary := filepath.Join(t.TempDir(), "qi")
	options := entryOptions(t, binary, "--with", module+"/a@v1.0.0", "--with", module+"/b", "--with", upgrader+"@v1.0.0", "--with", nested+"/a@v0.1.0")
	directory, err := Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "record.json"))
	var record requestRecord
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal(err, string(raw))
	}
	for _, selection := range record.Resolved.Selections[1:3] {
		if selection.Module != module || selection.RequiredVersion != "v1.0.0" || selection.FinalVersion != "v1.1.0" || !selection.QueryExecuted {
			t.Fatal("package query/version attribution changed", selection)
		}
	}
	if output, err := exec.Command(binary, "--help").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if selected := record.Resolved.Selections[3]; selected.Module != nested || selected.Entry != nested+"/a" {
		t.Fatal("nested module attributed to parent", selected)
	}
	queries := 0
	for _, command := range record.Commands {
		for _, arg := range command.Args {
			if arg == module+"@v1.0.0" {
				queries++
			}
		}
	}
	if queries != 1 {
		t.Fatal("subpackage version queries not shared", queries)
	}
	fork := "example.com/qi-r01-fork"
	writeProxyModule(t, proxy, fork, "v1.0.0", map[string]string{
		"go.mod":         "module " + fork + "\n\ngo 1.27\nrequire github.com/RussellLuo/qi/kernel v0.0.0\n",
		"a/extension.go": extension, "b/extension.go": extension, "extension.go": extension,
	})
	options = entryOptions(t, filepath.Join(t.TempDir(), "fork"), "--with", module+"/a@v1.0.0", "--with", module+"/b", "--replace", module+"="+fork+"@v1.0.0")
	directory, err = Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "record.json"))
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal(err)
	}
	var found bool
	for _, replacement := range record.Resolved.Replacements {
		if replacement.Module == module {
			found = replacement.Target == fork && replacement.TargetVersion == "v1.0.0" && replacement.QueryExecuted && replacement.Used
		}
	}
	if !found {
		t.Fatal("remote source ownership/version not recorded", record.Resolved)
	}
	options = entryOptions(t, filepath.Join(t.TempDir(), "inline-fork"),
		"--with", module+"/a@v1.0.0="+fork+"@v1.0.0", "--with", module+"/b="+fork+"@v1.0.0")
	directory, err = Build(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(directory, "record.json"))
	if err != nil || json.Unmarshal(raw, &record) != nil || len(record.Resolved.Replacements) != 3 {
		t.Fatal("inline package fork mappings not shared", err, record.Resolved)
	}
	for _, selection := range record.Resolved.Selections[1 : len(record.Resolved.Selections)-1] {
		if selection.Module != module || selection.RequiredVersion != "v1.0.0" {
			t.Fatal("inline fork attributed to package instead of module", selection)
		}
	}
	options = entryOptions(t, filepath.Join(t.TempDir(), "root-fork"), "--with", module+"@v1.0.0="+fork+"@v1.0.0")
	if _, err := Build(context.Background(), options); err != nil {
		t.Fatal("remote replacement root factory regressed", err)
	}
}
