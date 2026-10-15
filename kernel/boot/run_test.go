package boot_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testdata is a real external module, outside Qi's namespace. The owning test
// copies and executes it explicitly; ./... does not discover nested modules.
func TestExternalModulePublicContractsAndSelectedExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "external")
	if err := os.CopyFS(tree, os.DirFS("testdata/external")); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir, cmd.Env = tree, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(args, err, string(out))
		}
		return string(out)
	}
	if err := filepath.WalkDir(tree, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(raw), "/internal/") || strings.Contains(string(raw), "/qi/sdk") {
				t.Fatalf("external fixture imports private/facade API: %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run("go", "mod", "edit", "-replace", "github.com/RussellLuo/qi/kernel="+root)
	run("go", "mod", "tidy")
	run("go", "test", "./...")
	profile := filepath.Join(tree, "explicit.yaml")
	if err := os.WriteFile(profile, []byte("bundles: [fixture]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"with-probe", "without-probe"} {
		binary := filepath.Join(tree, name)
		run("go", "build", "-mod=readonly", "-o", binary, "./cmd/"+name)
		deps := run("go", "list", "-deps", "./cmd/"+name)
		if strings.Contains(deps, "example.com/qi-boot-fixture/probe") != (name == "with-probe") {
			t.Fatal("unselected extension in closure", deps)
		}
		cmd := exec.CommandContext(ctx, binary, "--profile", profile, "--", "external", "task")
		cmd.Dir = tree
		out, err := cmd.CombinedOutput()
		if name == "with-probe" {
			if err != nil || string(out) != "external task\n" {
				t.Fatal(err, string(out))
			}
		} else if err == nil || !strings.Contains(string(out), "fixture") {
			t.Fatal("missing Bundle accepted", err, string(out))
		}
	}
}

// Signals/forced exit need a separate process: the public entry owns os.Exit.
func TestBootProcessFaultsAndSignalDrain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process/signal assertions")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/process")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cmd = exec.CommandContext(ctx, "python3", "testdata/process/check.py", binary)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	} else {
		t.Log(string(out))
	}
}
