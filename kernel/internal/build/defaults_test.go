package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Both submodules follow the user's moving query, but have independent versions.
// A kernel pseudo-version must never be copied into the builtin requirement.
func TestDefaultModulesResolveRequestedQueryIndependently(t *testing.T) {
	proxy := t.TempDir()
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("QI_VERSION", "")
	versions := map[string]string{CoreModule: "v0.0.0-20261015000100-aaaaaaaaaaaa", BuiltinModule: "v0.0.0-20261015000200-bbbbbbbbbbbb"}
	for module, version := range versions {
		// Go proxy module names escape uppercase letters.
		escaped := "github.com/!russell!luo/qi/" + filepath.Base(module)
		writeProxyModule(t, proxy, escaped, version, map[string]string{"go.mod": "module " + module + "\n\ngo 1.27\n"})
		dir := filepath.Join(proxy, escaped, "@v")
		raw, err := os.ReadFile(filepath.Join(dir, version+".info"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.info"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	core, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	options, err := parseCLI([]string{"main", "--replace", CoreModule + "=" + core, "--replace", BuiltinModule + "=" + filepath.Join(core, "internal/build/testdata/defaultbuiltin")})
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), dir: t.TempDir(), env: nativeEnvironment()}
	m, resolution, _, err := resolveRequest(r, options.Request, "1.27")
	if err != nil {
		t.Fatal(err)
	}
	if m.Core.Version != versions[CoreModule] || m.builtinVersion != versions[BuiltinModule] {
		t.Fatal(m.Core.Version, m.builtinVersion)
	}
	if len(resolution.Selections) != 2 {
		t.Fatal(resolution)
	}
	for _, selection := range resolution.Selections {
		if selection.Query != "main" || !selection.QueryExecuted || selection.RequiredVersion != versions[selection.Module] {
			t.Fatal(selection)
		}
	}
}
