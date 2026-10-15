package build

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Build real local modules and execute the generated entry. The fixture does
// not require teaching examples or optional plugin sources in a kernel export.
func TestLocalExtensionAndDependencyReplacementBuild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	for name, source := range map[string]string{
		"extension/go.mod":       "module example.com/build-extension\n\ngo 1.27\nrequire (\n github.com/RussellLuo/qi/kernel v0.0.0\n example.com/build-helper v0.0.0\n)\n",
		"extension/extension.go": "package extension\nimport (\n\"github.com/RussellLuo/qi/kernel/boot/application\"\n\"example.com/build-helper\"\n)\nfunc Extension() application.Extension { _ = helper.Value; return application.Extension{} }\n",
		"helper/go.mod":          "module example.com/build-helper\n\ngo 1.27\n",
		"helper/value.go":        "package helper\nconst Value = 42\n",
	} {
		path := filepath.Join(fixture, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(fixture, "qi-generated")
	t.Setenv("QI_VERSION", "")
	var out, diagnostic bytes.Buffer
	code := Run(ctx, []string{
		"--with", CoreModule + "=" + root,
		"--replace", BuiltinModule + "=" + filepath.Join(root, "internal/build/testdata/defaultbuiltin"),
		"--with", "example.com/build-extension=" + filepath.Join(fixture, "extension"),
		"--replace", "example.com/build-helper=" + filepath.Join(fixture, "helper"),
		"--output", binary,
	}, &out, &diagnostic)
	if code != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	answer, err := exec.CommandContext(ctx, binary, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(answer), "Usage:") {
		t.Fatal(err, string(answer))
	}
	// Launcher help does not validate plugin registrations against a Profile.
	// Exercise the builtin Bundle too, so renamed upstream plugins cannot leave
	// a successfully built executable with an unusable base graph.
	profile := filepath.Join(fixture, "base.yaml")
	if err := os.WriteFile(profile, []byte("bundles: [base]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	answer, err = exec.CommandContext(ctx, binary, "--profile", profile, "--", "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(answer), "This Profile does not accept application arguments") {
		t.Fatal(err, string(answer))
	}
	out.Reset()
	diagnostic.Reset()
	if code := Run(ctx, []string{"--with", CoreModule + "=" + root,
		"--replace", BuiltinModule + "=" + filepath.Join(root, "internal/build/testdata/defaultbuiltin"), "--output", binary}, &out, &diagnostic); code == 0 {
		t.Fatal("overwrote an existing binary")
	}
}

// The builder owns its real external fixture, including execution assertions.
// testdata's nested module is deliberately built here, outside go test ./...'s
// package discovery. No distribution scheduler or optional plugin is required.
func TestSelectiveBuildPublicCompositionAndBootRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	builder := filepath.Join(t.TempDir(), "qi-build")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", builder, "./cmd/qi-build")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	output, err := exec.CommandContext(ctx, "python3", "testdata/selective/check.py", "--core", root, "--builder", builder, "--output", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	t.Log(string(output))
}
