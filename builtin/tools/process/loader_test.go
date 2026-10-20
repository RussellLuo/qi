package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func loaderOptions() fixtureOptions {
	return fixtureOptions{Command: childExecutable, Env: []string{"GORACE=atexit_sleep_ms=0"}, StartupTimeout: 5 * time.Second, ExitGrace: 3 * time.Second}
}

type loaderHost struct {
	host     *gordis.Host
	runtime  *tools.Runtime
	instance *gordis.Instance
	plugin   *fixtureLoader
	err      error
}

func mountLoader(t *testing.T, options fixtureOptions, raw json.RawMessage, extra ...gordis.Plugin) *loaderHost {
	t.Helper()
	prototype, err := newFixtureLoader(t, options)
	if err != nil {
		t.Fatal(err)
	}
	p := prototype.(*fixtureLoader)
	newInstance := p.spec.New
	a := &loaderHost{runtime: tools.NewRuntime()}
	p.spec.New = func() gordis.Plugin { a.plugin = newInstance().(*fixtureLoader); return a.plugin }
	plugins := append([]gordis.Plugin{&hostRuntime{r: a.runtime}, p}, extra...)
	a.host, err = gordis.NewHost(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.host.Shutdown(context.Background()); a.runtime.Close() })
	if _, err := a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "runtime", Plugin: "runtime"}); err != nil {
		t.Fatal(err)
	}
	for _, plugin := range extra {
		if _, err := a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: plugin.Spec().ID, Plugin: plugin.Spec().ID}); err != nil {
			t.Fatal(err)
		}
	}
	a.instance, a.err = a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "tool", Plugin: fixture.ID, Config: raw})
	return a
}

func executeGreeting(t *testing.T, runtime *tools.Runtime, ctx context.Context) (tools.ExecutionResult, error) {
	t.Helper()
	snapshot, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	call, err := snapshot.Prepare(protocol.ToolCall{ID: "test-call", Name: "greet", Arguments: `{"name":"Qi"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	return call.Execute(ctx)
}

func fixtureConfig(mode, directory, prefix string) json.RawMessage {
	raw, _ := json.Marshal(fixture.Plugin{Mode: mode, Directory: directory, Prefix: prefix})
	return raw
}

func TestLoaderProgramConfiguration(t *testing.T) {
	description, err := rpc.DescribeProgram(context.Background(), rpc.Command{Path: childExecutable}, rpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(description.ConfigSchema, &schema); err != nil || schema.Properties["prefix"] == nil {
		t.Fatalf("tool program schema not exposed: %s, %v", description.ConfigSchema, err)
	}
	for _, tc := range []struct{ name, config, want string }{
		{"defaults", `{}`, "Hello, Qi"},
		{"partial", `{"mode":""}`, "Hello, Qi"},
		{"override", `{"prefix":"Welcome, "}`, "Welcome, Qi"},
		{"explicit-zero", `{"prefix":""}`, "Qi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := mountLoader(t, loaderOptions(), json.RawMessage(tc.config))
			if a.err != nil {
				t.Fatal(a.err)
			}
			result, err := executeGreeting(t, a.runtime, context.Background())
			var value struct{ Greeting string }
			if err != nil || result.IsError || json.Unmarshal(result.Value, &value) != nil || value.Greeting != tc.want {
				t.Fatalf("greeting = %s, %v", result.Value, err)
			}
			if err := a.instance.Unmount(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertClean(t, a.plugin.client)
		})
	}
	for _, mode := range []string{"schema", "configure-error"} {
		t.Run(mode+"-before-activation", func(t *testing.T) {
			directory := t.TempDir()
			prefix := "Hello, "
			want := "program config Configure"
			if mode == "schema" {
				prefix = strings.Repeat("x", 101)
				want = "program config schema"
			}
			a := mountLoader(t, loaderOptions(), fixtureConfig(mode, directory, prefix))
			if a.err == nil || !strings.Contains(a.err.Error(), want) || len(a.runtime.Schemas()) != 0 {
				t.Fatalf("invalid program config exposed tools: %v", a.err)
			}
			if a.plugin.client == nil || !a.plugin.client.Snapshot().Reaped {
				t.Fatal("failed program was not reaped")
			}
			if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
				t.Fatalf("business lifecycle ran: %v, %v", entries, err)
			}
		})
	}
}

func TestLoaderWithoutProgramConfiguration(t *testing.T) {
	for _, raw := range []string{`{}`, `{"prefix":"unexpected"}`} {
		t.Run(raw, func(t *testing.T) {
			options := loaderOptions()
			options.Env = append(options.Env, "QI_C03_NO_CONFIG=1")
			a := mountLoader(t, options, json.RawMessage(raw))
			if raw == `{}` {
				if a.err != nil {
					t.Fatal(a.err)
				}
				if _, err := executeGreeting(t, a.runtime, context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := a.instance.Unmount(context.Background()); err != nil {
					t.Fatal(err)
				}
				assertClean(t, a.plugin.client)
			} else if a.err == nil || len(a.runtime.Schemas()) != 0 || a.plugin.client == nil || !a.plugin.client.Snapshot().Reaped {
				t.Fatalf("undeclared business configuration accepted: %v", a.err)
			}
		})
	}
}

func TestLoaderProcessAndIndependentConfigurations(t *testing.T) {
	for _, prefix := range []string{"Hello, ", "Welcome, "} {
		directory := t.TempDir()
		a := mountLoader(t, loaderOptions(), fixtureConfig("", directory, prefix))
		if a.err != nil {
			t.Fatal(a.err)
		}
		result, err := executeGreeting(t, a.runtime, context.Background())
		if err != nil || result.IsError {
			t.Fatal(result, err)
		}
		var value struct {
			PID      int    `json:"pid"`
			Greeting string `json:"greeting"`
		}
		if err := json.Unmarshal(result.Value, &value); err != nil {
			t.Fatal(err)
		}
		if value.PID == os.Getpid() || value.PID != a.plugin.client.Snapshot().PID || value.Greeting != prefix+"Qi" {
			t.Fatalf("result = %s", result.Value)
		}
		if err := a.instance.Unmount(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(a.runtime.Schemas()) != 0 {
			t.Fatal("Tools survived Scope stop")
		}
		assertClean(t, a.plugin.client)
		waitMarker(t, directory, "cleaned")
	}
}

func TestLoaderFailureBeforeVisibility(t *testing.T) {
	for _, mode := range []string{"validate", "activate", "schema", "deferred", "unknown-config", "deployment-config", "package", "version", "missing-executable"} {
		t.Run(mode, func(t *testing.T) {
			o := loaderOptions()
			raw := fixtureConfig(mode, t.TempDir(), "Hello, ")
			switch mode {
			case "unknown-config":
				raw = json.RawMessage(`{"unknown":true}`)
			case "deployment-config":
				raw = json.RawMessage(`{"Command":"/bin/false","Args":[],"Env":[]}`)
			case "package":
				o.ID = "wrong.package"
			case "version":
				o.Version = "v9"
			case "missing-executable":
				o.Command = filepath.Join(t.TempDir(), "missing")
			}
			a := mountLoader(t, o, raw)
			if a.err == nil {
				t.Fatal("invalid activation succeeded")
			}
			if len(a.runtime.Schemas()) != 0 {
				t.Fatal("failed activation exposed Tools")
			}
			if a.plugin.client != nil {
				d := a.plugin.client.Snapshot()
				if d.PID > 0 && !d.Reaped {
					t.Fatalf("startup leaked child: %+v", d)
				}
			}
		})
	}
}

type conflictingTool struct{}

func (*conflictingTool) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/conflict", Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return new(conflictingTool) }}
}
func (*conflictingTool) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).Register(tools.Definition{Name: "greet", Execute: tools.JSONTool(func(context.Context, tools.Execution) (json.RawMessage, error) {
		return json.RawMessage(`"existing"`), nil
	})})
	return err
}

func TestLoaderToolConflictRollsBackBatchAndProcess(t *testing.T) {
	directory := t.TempDir()
	a := mountLoader(t, loaderOptions(), fixtureConfig("batch", directory, ""), new(conflictingTool))
	if a.err == nil {
		t.Fatal("Tool collision accepted")
	}
	schemas := a.runtime.Schemas()
	if len(schemas) != 1 || schemas[0].Name != "greet" {
		t.Fatalf("partial batch published: %+v", schemas)
	}
	result, err := executeGreeting(t, a.runtime, context.Background())
	if err != nil || string(result.Value) != `"existing"` {
		t.Fatal(result, err)
	}
	assertClean(t, a.plugin.client)
	waitMarker(t, directory, "cleaned")
}

func TestLoaderIdentityOverride(t *testing.T) {
	o := loaderOptions()
	o.ID, o.Version = "fixture.override", "v2"
	o.Env = append(o.Env, "QI_PT01_OVERRIDE=1")
	a := mountLoader(t, o, fixtureConfig("", "", "Hello, "))
	if a.err != nil {
		t.Fatal(a.err)
	}
	result, err := executeGreeting(t, a.runtime, context.Background())
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	if err := a.instance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClean(t, a.plugin.client)
}

func TestLoaderDefaultsToEmptyEnvironment(t *testing.T) {
	t.Setenv("QI_PT02_AMBIENT", "host-only")
	o := loaderOptions()
	o.Env = nil
	a := mountLoader(t, o, fixtureConfig("empty-environment", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	if err := a.instance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClean(t, a.plugin.client)
}

func TestLoaderPropagatesCleanupFailure(t *testing.T) {
	a := mountLoader(t, loaderOptions(), fixtureConfig("cleanup-error", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	if err := a.instance.Unmount(context.Background()); err == nil {
		t.Fatal("remote cleanup error hidden")
	}
	d := a.plugin.client.Snapshot()
	if !d.Reaped || d.Cleanup == nil || d.Cleanup.CleanupComplete || len(d.Cleanup.Errors) == 0 {
		t.Fatalf("false cleanup success: %+v", d)
	}
}

func TestLoaderFailureCategories(t *testing.T) {
	for _, tc := range []struct {
		mode, code string
		abort      bool
	}{
		{"error", "execution", false}, {"abort-error", "execution", true}, {"cancelled", "cancelled", false},
		{"timeout", "timeout", false}, {"meta", CodeHostValidation, false}, {"panic", CodeTransport, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			a := mountLoader(t, loaderOptions(), fixtureConfig(tc.mode, "", ""))
			if a.err != nil {
				t.Fatal(a.err)
			}
			result, err := executeGreeting(t, a.runtime, context.Background())
			if !result.IsError || result.Error.Info.Code != tc.code || (err != nil) != tc.abort {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if tc.abort {
				var executionErr *tools.ExecutionError
				if !errors.As(err, &executionErr) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLoaderCancelledRemoteWorkerDrains(t *testing.T) {
	directory := t.TempDir()
	a := mountLoader(t, loaderOptions(), fixtureConfig("waitstop", directory, ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan tools.ExecutionResult, 1)
	go func() { result, _ := executeGreeting(t, a.runtime, ctx); done <- result }()
	waitMarker(t, directory, "started")
	cancel()
	select {
	case result := <-done:
		if !result.IsError || result.Error.Info.Code != "cancelled" {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled host call did not return")
	}
	waitMarker(t, directory, "cancelled")
	if err := a.instance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClean(t, a.plugin.client)
	for _, name := range []string{"stopping", "exited", "cleaned"} {
		waitMarker(t, directory, name)
	}
}

func TestLoaderWithdrawsBeforeLeaseDrainAndClose(t *testing.T) {
	a := mountLoader(t, loaderOptions(), fixtureConfig("", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	snapshot, err := a.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	done := make(chan error, 1)
	go func() { done <- a.instance.Unmount(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.runtime.Schemas()) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(a.runtime.Schemas()) != 0 {
		t.Fatal("contribution not withdrawn")
	}
	select {
	case err := <-done:
		t.Fatalf("did not wait for retained Snapshot: %v", err)
	default:
	}
	if a.plugin.client.Snapshot().Reaped {
		t.Fatal("closed process before Tool lease drained")
	}
	snapshot.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup stuck")
	}
	assertClean(t, a.plugin.client)
}

func TestLoaderWatchesBackgroundFailure(t *testing.T) {
	a := mountLoader(t, loaderOptions(), fixtureConfig("background", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	_, _ = executeGreeting(t, a.runtime, context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, snapshot := range a.host.Snapshot() {
			if snapshot.Plugin == fixture.ID && snapshot.Failure != "" {
				if err := a.host.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(a.runtime.Schemas()) != 0 {
					t.Fatal("failed process retained Tools")
				}
				assertClean(t, a.plugin.client)
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("AfterReady did not report process failure")
}

func TestLoaderConfigurationAndFactory(t *testing.T) {
	args, env := []string{"arg"}, []string{"X=original"}
	prototype := HostExtension("test/loader", HostOptions{Args: args, Env: env}).Registrations[0].Plugin
	args[0], env[0] = "changed", "changed"
	first := prototype.Spec().New().(*loaderPlugin)
	second := prototype.Spec().New().(*loaderPlugin)
	if first == second || first.options.Args[0] != "arg" || first.options.Env[0] != "X=original" {
		t.Fatal("loader factory shares caller options")
	}
	first.options.Args[0] = "mutated"
	if second.options.Args[0] != "arg" {
		t.Fatal("loader instances share options")
	}
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"command":" "}`, `{"command":"missing","unknown":true}`,
		`{"command":"missing","identity":{}}`, `{"command":"missing","identity":{"id":"bad id","version":"dev"}}`, `{"manifest":"old"}`,
		`{"command":"missing","config":null}`, `{"command":"missing","config":[]}`,
	} {
		if err := json.Unmarshal([]byte(raw), first); err == nil {
			t.Fatalf("accepted invalid deployment configuration: %s", raw)
		}
	}
	// Decoding deployment config does not read the artifact or construct business code.
	raw := []byte(`{"command":"missing","config":{"prefix":"Hello"}}`)
	if err := json.Unmarshal(raw, first); err != nil {
		t.Fatal(err)
	}
	raw[2] = 'x'
	if string(first.config) != `{"prefix":"Hello"}` || second.command != "" || second.config != nil {
		t.Fatal("loader configuration aliases caller or another instance")
	}
	if err := first.Validate(); err == nil {
		t.Fatal("accepted missing artifact")
	}
}
