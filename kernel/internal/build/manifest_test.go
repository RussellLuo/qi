package build

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManifestRejectsInvalidSelections(t *testing.T) {
	base := "apiVersion: qi.build/v1alpha1\ncore: {version: v0.1.0}\n"

	for _, tc := range []struct{ name, raw, want string }{
		{"unknown", base + "output: qi\n", "field output"},
		{"core unknown", strings.ReplaceAll(base, "version: v0.1.0", "version: v0.1.0, module: other"), "field module"},
		{"floating", strings.ReplaceAll(base, "v0.1.0", "latest"), "fixed"},
		{"branch", strings.ReplaceAll(base, "v0.1.0", "main"), "fixed"},
		{"multiple", base + "---\n" + base, "one document"},
		{"duplicate yaml", base + "core: {}\n", "already defined"},
		{"process", base + "plugins: [{module: example.com/echo, version: v1.0.0, execution: process}]", "unsupported"},
		{"missing execution", base + "plugins: [{module: example.com/echo, version: v1.0.0}]", "unsupported"},
		{
			"outside module",
			base + "plugins: [{module: example.com/echo, version: v1.0.0, package: example.com/other, execution: local}]",
			"belong",
		},
		{
			"invalid path",
			base + "plugins: [{module: example.com/../echo, version: v1.0.0, execution: local}]",
			"invalid",
		},
		{
			"duplicate package",
			base + "plugins: [{module: example.com/echo, version: v1.0.0, execution: local}, {module: example.com/echo, version: v1.0.0, execution: local}]",
			"duplicate selected",
		},
		{
			"conflict",
			base + "plugins: [{module: example.com/echo, version: v1.0.0, execution: local}, {module: example.com/echo, version: v1.1.0, package: example.com/echo/sub, execution: local}]",
			"conflicting versions",
		},
		{
			"core conflict",
			base + "plugins: [{module: github.com/RussellLuo/qi/kernel, version: v0.2.0, execution: local}]",
			"conflicting versions",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
		})
	}
}

func TestGenerationStableAcrossSelectionOrder(t *testing.T) {
	base := "apiVersion: qi.build/v1alpha1\ncore: {version: v0.1.0}\nplugins:\n"
	a := "  - {module: example.com/a, version: v1.0.0, execution: local}\n"
	b := "  - {module: example.com/b, version: v0.0.0-20260930034706-7864fda10514, package: example.com/b/echo, execution: local}\n"
	first, err := Parse([]byte(base + a + b))
	if err != nil {
		t.Fatal(err)
	}

	second, err := Parse([]byte(base + b + a))
	if err != nil {
		t.Fatal(err)
	}

	x, err := generateApplication(first)
	if err != nil {
		t.Fatal(err)
	}

	y, err := generateApplication(second)
	if err != nil {
		t.Fatal(err)
	}

	xJSON, _ := json.Marshal(x)
	yJSON, _ := json.Marshal(y)
	if string(xJSON) != string(yJSON) {
		t.Fatal("selection order changed generated files")
	}

	if strings.Contains(string(x["main.go"]), CoreModule+"/internal/") {
		t.Fatal("generated main crossed SDK boundary")
	}
	plugins := string(x["internal/compiledplugins/plugins_gen.go"])
	if len(x) != 2 || strings.Count(plugins, "builtin.Extension()") != 1 ||
		!strings.Contains(plugins, "selected0.Extension()") || !strings.Contains(plugins, "selected1.Extension()") ||
		!strings.Contains(plugins, "application.Compose(") || !strings.Contains(string(x["main.go"]), "boot.Run(") {
		t.Fatal("generated Qi assembly changed", plugins)
	}
}
