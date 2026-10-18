package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/internal/bundles"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/internal/launcher"
	"github.com/RussellLuo/qi/kernel/boot/internal/profile"
	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
)

type noRead struct{}

func (noRead) Read([]byte) (int, error) { panic("launcher/help read stdin") }

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestLauncherSyntaxHasNoDefaultOrBootEffects(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("invalid syntax"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(".qi", 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(".qi/headless.yaml", []byte("invalid profile"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		var out, diagnostics bytes.Buffer
		code, err := Work(
			context.Background(),
			func(int) {},
			args,
			launchcontract.Input{
				Stdin:  noRead{},
				Stdout: &out,
				Stderr: &diagnostics,
			},
			true,
			Application{},
		)
		if code != 0 || err != nil || out.String() != Help || diagnostics.Len() != 0 {
			t.Fatalf("%v: %d %v %q %q", args, code, err, out.String(), diagnostics.String())
		}
	}

	for _, args := range [][]string{
		{"--config", "missing"},
		{"headless", "--help"},
		{"web", "--help"},
		{"plugin", "--help"},
		{"--profile="},
		{"--profile", " "},
		{"--profile"},
		{"--env-file", ""},
		{"--profile", "missing", "--env-file", "credentials.env"},
		{"--profile", "missing", "task"},
		{"--profile", "missing", "headless", "task"},
		{"--profile", "missing", "--json"},
		{"--profile", "missing", "--watch-config"},
		{"--profile", "missing", "--package-store", "store"},
		{"--help", "task"},
	} {
		var out, diagnostics bytes.Buffer
		options, err := Normalize(args, launchcontract.Input{
			Stdin:  noRead{},
			Stdout: &out,
			Stderr: &diagnostics,
		}, Application{})
		if err == nil || options != nil || out.Len() != 0 {
			t.Fatalf("accepted %v: %v %q", args, err, out.String())
		}
	}

	var out bytes.Buffer
	code, _ := Work(
		context.Background(),
		func(int) {},
		[]string{"--profile", "minimal"},
		launchcontract.Input{
			Stdin:  noRead{},
			Stdout: &out,
			Stderr: &out,
		},
		true,
		Application{},
	)
	if code != 1 || !strings.Contains(out.String(), "read runtime profile") {
		t.Fatalf("named profile accepted: %d %q", code, out.String())
	}

	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 2 {
		t.Fatalf("launcher created files: %v %v", entries, err)
	}
}

func TestRawArgvAndApplicationSelectionArePreserved(t *testing.T) {
	selected := []registration.PluginRegistration{{Plugin: fixturePlugin{}}}
	called := false
	app := Application{
		Registrations: selected,
		Assemble: func(p profile.Profile, input AssemblyInput) ([]registration.PluginRegistration, launchcontract.Policy, launchcontract.Hooks, error) {
			called = true
			if input.ProfilePath != "outside/my profile.yaml" {
				t.Fatal(input.ProfilePath)
			}

			return selected, launchcontract.Policy{}, launchcontract.Hooks{}, nil
		},
	}
	args := []string{
		"--profile",
		"outside/my profile.yaml",
		"--",
		"two words",
		"",
		"--",
		"--profile",
		"literal",
		"--env-file",
		"application-owned",
	}
	options, err := Normalize(args, launchcontract.Input{Stdout: io.Discard, Stderr: io.Discard}, app)
	if err != nil || !reflect.DeepEqual(options.Args, args[3:]) || called {
		t.Fatalf("normalize: %+v %v called=%t", options, err, called)
	}

	args[3] = "mutated"
	if options.Args[0] != "two words" {
		t.Fatal("argv aliases caller")
	}

	selected[0] = registration.PluginRegistration{}
	if options.Registrations[0].Plugin == nil {
		t.Fatal("registrations overwritten")
	}

	if _, _, _, err := options.Assemble(profile.Profile{}); err != nil || !called {
		t.Fatal(err, called)
	}
}

func TestHelpShortWritesFail(t *testing.T) {
	_, err := Normalize(nil, launchcontract.Input{Stdout: shortWriter{}}, Application{})
	if err != io.ErrShortWrite {
		t.Fatal(err)
	}
}

type fixturePlugin struct{}

// This consumer observes the same process environment a Provider would read.
// Completing after Ready exercises actual startup rather than the help path.
type environmentConsumer struct {
	observed map[string]string
}

func (p *environmentConsumer) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "fixture.environment",
		Requires: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec()},
		New:      func() gordis.Plugin { return &environmentConsumer{observed: p.observed} },
	}
}

func (p *environmentConsumer) Activate(_ context.Context, scope *gordis.Scope) error {
	launch, err := scope.Get(launchcontract.LaunchContextKey)
	if err != nil {
		return err
	}

	p.observed["inherited"] = os.Getenv("QI_CLI_TEST_INHERITED")
	p.observed["file"] = os.Getenv("QI_CLI_TEST_FILE_ONLY")

	return scope.Go("finish invocation", func(ctx context.Context) error {
		select {
		case <-launch.Control.Ready():
		case <-ctx.Done():
			return ctx.Err()
		}

		if err := launch.Control.Claim(); err != nil {
			return err
		}

		return launch.Control.Complete(0, nil)
	})
}

func TestExecutionIgnoresDotEnvAndPreservesProcessEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{"valid", "QI_CLI_TEST_INHERITED=file-override\nQI_CLI_TEST_FILE_ONLY=file-only\n"},
		{"invalid", "this is not an environment assignment\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("QI_CLI_TEST_INHERITED", "process-value")
			t.Setenv("QI_CLI_TEST_FILE_ONLY", "")
			if err := os.Unsetenv("QI_CLI_TEST_FILE_ONLY"); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(".env", []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("task.yaml", []byte("instances: [{id: consumer, plugin: fixture.environment}]\n"), 0600); err != nil {
				t.Fatal(err)
			}

			observed := make(map[string]string)
			app := Application{Registrations: []registration.PluginRegistration{{
				Plugin:         &environmentConsumer{observed: observed},
				ProfileVisible: true,
				Completes:      true,
			}}}
			var out, diagnostics bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			code, err := Work(ctx, func(int) {}, []string{"--profile", "task.yaml"}, launchcontract.Input{
				Stdin:  noRead{},
				Stdout: &out,
				Stderr: &diagnostics,
			}, true, app)
			if code != 0 || err != nil || out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatalf("execution: %d %v %q %q", code, err, out.String(), diagnostics.String())
			}

			if !reflect.DeepEqual(observed, map[string]string{"inherited": "process-value", "file": ""}) {
				t.Fatalf("plugin environment: %v", observed)
			}
			if _, exists := os.LookupEnv("QI_CLI_TEST_FILE_ONLY"); exists {
				t.Fatal("launcher loaded a variable from .env")
			}

			content, err := os.ReadFile(".env")
			if err != nil || string(content) != tc.content {
				t.Fatalf("launcher changed .env: %q %v", content, err)
			}
		})
	}
}

func TestEnvFileIsNotALauncherOption(t *testing.T) {
	var out bytes.Buffer
	options, err := Normalize([]string{"--profile", "task.yaml", "--env-file", "credentials.env"},
		launchcontract.Input{Stdout: &out, Stderr: io.Discard}, Application{})
	if err == nil || options != nil || !strings.Contains(err.Error(), "flag provided but not defined: -env-file") {
		t.Fatalf("obsolete flag accepted: %v %v", options, err)
	}
	if strings.Contains(Help, "--env-file") || out.Len() != 0 {
		t.Fatal("launcher advertises the obsolete flag")
	}
}

func (fixturePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "fixture.consumer", New: func() gordis.Plugin { return fixturePlugin{} }}
}

func (fixturePlugin) Activate(context.Context, *gordis.Scope) error {
	panic("help activated a plugin")
}

type fixtureParser struct{ args *[]string }

func (fixtureParser) Help() string { return "fixture help\n" }

func (p fixtureParser) Parse(args []string, _ bool) (launchcontract.Invocation, error) {
	*p.args = args
	return fixtureInvocation{}, nil
}

type fixtureInvocation struct{}

type emptyHelpParser struct{ fixtureParser }

func (emptyHelpParser) Help() string { return "" }

func (fixtureInvocation) HelpRequested() bool { return true }

func (fixtureInvocation) Prepare(context.Context, launchcontract.Input) (launchcontract.Prepared, error) {
	panic("help prepared stdin")
}

func TestConsumerHelpUsesSelectedMetadataBeforeAssembly(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("invalid syntax"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile("task.yaml", []byte("instances: [{id: consumer, plugin: fixture.consumer}]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var parsed []string
	app := Application{
		Registrations: []registration.PluginRegistration{{
			Plugin:         fixturePlugin{},
			ProfileVisible: true,
			Invocation:     fixtureParser{&parsed},
		}},
		Assemble: func(profile.Profile, AssemblyInput) ([]registration.PluginRegistration, launchcontract.Policy, launchcontract.Hooks, error) {
			panic("help assembled runtime")
		},
	}
	var out, diagnostics bytes.Buffer
	args := []string{"--help", "two words", "--", "literal"}
	argv := append([]string{"--profile", "task.yaml", "--"}, args...)
	code, err := Work(
		context.Background(),
		func(int) {},
		argv,
		launchcontract.Input{
			Stdin:  noRead{},
			Stdout: &out,
			Stderr: &diagnostics,
		},
		true,
		app,
	)
	if code != 0 ||
		err != nil ||
		out.String() != "Profile: task.yaml\nInvocation: qi --profile task.yaml -- [application arguments...]\n\nfixture help\n" ||
		diagnostics.Len() != 0 ||
		!reflect.DeepEqual(parsed, args) {
		t.Fatalf("help: %d %v %q %q %v", code, err, out.String(), diagnostics.String(), parsed)
	}
	// Even an empty consumer description is a help request, never permission
	// to start a graph whose invocation has already been discarded by Parse.
	app.Registrations[0].Invocation = emptyHelpParser{fixtureParser{&parsed}}
	out.Reset()
	code, err = Work(
		context.Background(),
		func(int) {},
		argv,
		launchcontract.Input{
			Stdin:  noRead{},
			Stdout: &out,
			Stderr: &diagnostics,
		},
		true,
		app,
	)
	if code != 0 || err != nil || !strings.Contains(out.String(), "Profile: task.yaml") || diagnostics.Len() != 0 {
		t.Fatal(code, err, out.String(), diagnostics.String())
	}
}

func TestResidentHelpAndConsumerConflictsBeforeBoot(t *testing.T) {
	t.Chdir(t.TempDir())
	var parsed []string
	app := Application{
		Registrations: []registration.PluginRegistration{{
			Plugin:         fixturePlugin{},
			ProfileVisible: true,
			Invocation:     fixtureParser{&parsed},
		}},
		Assemble: func(profile.Profile, AssemblyInput) ([]registration.PluginRegistration, launchcontract.Policy, launchcontract.Hooks, error) {
			panic("help/error assembled runtime")
		},
	}

	for _, c := range []struct {
		source, want string
		args         []string
		code         int
	}{
		{"instances: []\n", "does not accept application arguments", []string{"--help"}, 0},
		{"instances: []\n", "no CLI argument consumer", []string{"-h"}, 1},
		{"instances: []\n", "no CLI argument consumer", []string{"--help", "extra"}, 1},
		{
			"instances: [{id: first, plugin: fixture.consumer}, {id: second, plugin: fixture.consumer}, {id: third, plugin: fixture.consumer}]\n",
			"first second third",
			[]string{"--help"},
			1,
		},
		{
			"instances: [{id: first, plugin: fixture.consumer}, {id: second, plugin: fixture.consumer}]\n",
			"first second",
			[]string{"task"},
			1,
		},
		{
			"instances: [{id: active, plugin: fixture.consumer}, {id: off, plugin: fixture.consumer}]\noverrides: [{id: off, disabled: true}]\n",
			"fixture help",
			[]string{"--help"},
			0,
		},
		{"bundles: [unknown]\n", "unknown", []string{"--help"}, 1},
		{"instances: [\n", "", []string{"--help"}, 1},
	} {
		if err := os.WriteFile("my 'profile.yaml", []byte(c.source), 0600); err != nil {
			t.Fatal(err)
		}

		var out, diagnostics bytes.Buffer
		argv := append([]string{"--profile", "my 'profile.yaml", "--"}, c.args...)
		code, err := Work(
			context.Background(),
			func(int) {},
			argv,
			launchcontract.Input{
				Stdin:  noRead{},
				Stdout: &out,
				Stderr: &diagnostics,
			},
			false,
			app,
		)
		got := diagnostics.String()
		if c.code == 0 {
			got = out.String()
		}

		if code != c.code || err != nil || !strings.Contains(got, c.want) {
			t.Fatalf("%+v: %d %v %q %q", c, code, err, out.String(), diagnostics.String())
		}

		if c.code == 0 && (!strings.Contains(got, "qi --profile 'my '\"'\"'profile.yaml'") || diagnostics.Len() != 0) {
			t.Fatal(got, diagnostics.String())
		}
	}

	var diagnostics bytes.Buffer
	if err := os.WriteFile("resident.yaml", []byte("instances: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	diagnostics.Reset()
	code, _ := Work(
		context.Background(),
		func(int) {},
		[]string{"--profile", "resident.yaml", "--", "--help"},
		launchcontract.Input{
			Stdin:  noRead{},
			Stdout: shortWriter{},
			Stderr: &diagnostics,
		},
		false,
		app,
	)
	if code != 1 || !strings.Contains(diagnostics.String(), "short write") {
		t.Fatal(code, diagnostics.String())
	}
}

// One selected immutable catalog must survive file loading and launcher planning.
func TestApplicationSelectsItsBundleCatalogWithoutFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("task.yaml", []byte("bundles: [private]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	catalog, err := bundles.NewCatalogWith([]byte("id: private\ninstances: [{id: consumer, plugin: fixture.consumer}]\n"))
	if err != nil {
		t.Fatal(err)
	}

	var parsed []string
	app := Application{
		BundleCatalog: catalog,
		Registrations: []registration.PluginRegistration{{
			Plugin:         fixturePlugin{},
			ProfileVisible: true,
			Invocation:     fixtureParser{&parsed},
		}},
	}
	var out, diagnostics bytes.Buffer
	streams := launchcontract.Input{
		Stdin:  noRead{},
		Stdout: &out,
		Stderr: &diagnostics,
	}
	argv := []string{"--profile", "task.yaml", "--", "--help"}
	options, err := Normalize(argv, streams, app)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := options.LoadSource()
	if err != nil || len(resolved.BundleInputs) != 1 || options.BundleCatalog != catalog {
		t.Fatal(resolved, err)
	}

	code, err := Work(context.Background(), func(int) {}, argv, streams, true, app)
	if code != 0 || err != nil || !strings.Contains(out.String(), "fixture help") || diagnostics.Len() != 0 {
		t.Fatal(code, err, out.String(), diagnostics.String())
	}
	// Removing the selection must fail explicitly, not reuse a previous catalog.
	app.BundleCatalog = nil
	out.Reset()
	code, err = Work(context.Background(), func(int) {}, argv, streams, true, app)
	if code != 1 || err != nil || out.Len() != 0 || !strings.Contains(diagnostics.String(), `unknown bundle "private"`) {
		t.Fatal(code, err, out.String(), diagnostics.String())
	}
}

func TestNilBundleCatalogDoesNotSelectOfficialLayers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte("bundles: [base, fake, headless]\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var out, diagnostics bytes.Buffer
	code, err := Work(context.Background(), func(int) {}, []string{"--profile", path, "--", "--help"},
		launchcontract.Input{
			Stdin:  noRead{},
			Stdout: &out,
			Stderr: &diagnostics,
		}, true, Application{})
	if code != 1 || err != nil || out.Len() != 0 || !strings.Contains(diagnostics.String(), `unknown bundle "base"`) {
		t.Fatal(code, err, out.String(), diagnostics.String())
	}
}

func TestPathHelpDoesNotExecuteOrInspectProgram(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "marker")
	program := filepath.Join(directory, "program")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(directory, "profile.yaml")
	if err := os.WriteFile(selected, []byte("instances: [{id: external, plugin: ./program}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	app := Application{Registrations: []registration.PluginRegistration{{Plugin: fixturePlugin{}, ProfileVisible: true, ResolvePath: func(command string, business map[string]any) (map[string]any, error) {
		if command != program {
			t.Fatalf("base dir: %s", command)
		}
		return map[string]any{"command": command, "config": business}, nil
	}}}}
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(program); err != nil {
				t.Fatal(err)
			}
		}
		var out, diagnostics bytes.Buffer
		code, err := Work(context.Background(), func(int) {}, []string{"--profile", selected, "--", "--help"}, launchcontract.Input{Stdin: noRead{}, Stdout: &out, Stderr: &diagnostics}, false, app)
		if err != nil || code != 0 || out.Len() == 0 {
			t.Fatal(code, err, diagnostics.String())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("help executed program", err)
		}
	}
}
