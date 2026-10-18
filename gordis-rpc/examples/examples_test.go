package examples_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBasic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Build each real program once for all standard and configuration cases.
	directory := t.TempDir()
	host, child := filepath.Join(directory, "host"), filepath.Join(directory, "plugin")
	for path, pkg := range map[string]string{host: "./basic/host/cmd", child: "./basic/plugin/cmd"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-race", "-o", path, pkg)
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, output)
		}
	}

	for _, tc := range []struct {
		name, config, want string
		fail               bool
	}{
		{name: "standard", want: "Hello, Gordis!\n"},
		{name: "default-config", config: `{}`, want: "Hello, Gordis!\n"},
		{name: "configured", config: `{"prefix":"Bonjour"}`, want: "Bonjour, Gordis!\n"},
		{name: "empty-prefix", config: `{"prefix":""}`, fail: true},
		{name: "wrong-type", config: `{"prefix":7}`, fail: true},
		{name: "unknown-field", config: `{"unexpected":true}`, fail: true},
		{name: "malformed-json", config: `{"prefix":`, fail: true},
		{name: "non-object", config: `null`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			args := []string{child}
			if tc.config != "" {
				args = []string{"-config", tc.config, child}
			}
			cmd := exec.CommandContext(callCtx, host, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if callCtx.Err() != nil {
				t.Fatalf("Host failed to finish: %v\n%s", callCtx.Err(), stderr.String())
			}
			if tc.fail {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || stderr.Len() == 0 {
					t.Fatalf("invalid config: %v\n%s", err, stderr.String())
				}
			} else if err != nil {
				t.Fatalf("Host/child: %v\n%s", err, stderr.String())
			}
			if stdout.String() != tc.want {
				t.Fatalf("business output: got %q, want %q", stdout.String(), tc.want)
			}
		})
	}

	t.Run("production-dependencies", func(t *testing.T) {
		const module = "github.com/RussellLuo/gordis-rpc"
		for _, pkg := range []string{"basic/host/cmd", "basic/plugin/cmd", "basic/host", "basic/plugin", "basic/bindings", "basic/contract"} {
			cmd := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-deps", "./"+pkg)
			cmd.Env = append(os.Environ(), "GOWORK=off")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("audit %s: %v\n%s", pkg, err, output)
			}
			deps := strings.Fields(string(output))
			for _, dependency := range deps {
				for _, forbidden := range []string{"github.com/RussellLuo/qi", "github.com/RussellLuo/gordis/process", "github.com/RussellLuo/gordis/processbridge"} {
					if dependency == forbidden || strings.HasPrefix(dependency, forbidden+"/") {
						t.Errorf("%s imports forbidden dependency %s", pkg, dependency)
					}
				}
				if (pkg == "basic/host/cmd" || pkg == "basic/host") &&
					(dependency == module+"/examples/basic/plugin" || strings.HasPrefix(dependency, module+"/examples/basic/plugin/")) {
					t.Errorf("Host %s imports child business implementation %s", pkg, dependency)
				}
				if strings.HasPrefix(dependency, "golang.org/x/tools/") ||
					strings.HasPrefix(dependency, module+"/internal/") || strings.HasPrefix(dependency, module+"/cmd/") {
					t.Errorf("%s imports generator or fixture dependency %s", pkg, dependency)
				}
				if pkg == "basic/host" || pkg == "basic/plugin" || pkg == "basic/contract" {
					// These packages live inside this module but may only use the
					// ordinary business/contract packages, never the RPC runtime.
					if (dependency == module || strings.HasPrefix(dependency, module+"/")) &&
						dependency != module+"/examples/"+pkg && dependency != module+"/examples/basic/contract" {
						t.Errorf("ordinary package %s imports RPC/example implementation %s", pkg, dependency)
					}
				}
			}
			t.Logf("%s: %d production dependencies audited", pkg, len(deps))
		}
	})
}
