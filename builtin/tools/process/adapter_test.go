package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func adapterOptions() Options {
	return Options{Command: childExecutable, Env: []string{"GORACE=atexit_sleep_ms=0"}, StartupTimeout: 5 * time.Second, ExitGrace: 3 * time.Second}
}

type adapterHost struct {
	host     *gordis.Host
	runtime  *tools.Runtime
	instance *gordis.Instance
	plugin   *adapterPlugin
	err      error
}

func mountAdapter(t *testing.T, options Options, raw json.RawMessage, extra ...gordis.Plugin) *adapterHost {
	t.Helper()
	prototype, err := NewAdapter(new(fixture.Plugin), options)
	if err != nil {
		t.Fatal(err)
	}
	p := prototype.(*adapterPlugin)
	newInstance := p.spec.New
	a := &adapterHost{runtime: tools.NewRuntime()}
	p.spec.New = func() gordis.Plugin { a.plugin = newInstance().(*adapterPlugin); return a.plugin }
	plugins := append([]gordis.Plugin{&runtimeProvider{runtime: a.runtime}, p}, extra...)
	a.host, err = gordis.NewHost(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.host.Shutdown(context.Background()); a.runtime.Close() })
	if _, err := a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "runtime", Plugin: providerID}); err != nil {
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

func TestAdapterProcessAndIndependentConfigurations(t *testing.T) {
	for _, prefix := range []string{"Hello, ", "Welcome, "} {
		directory := t.TempDir()
		a := mountAdapter(t, adapterOptions(), fixtureConfig("", directory, prefix))
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

func TestAdapterFailureBeforeVisibility(t *testing.T) {
	for _, mode := range []string{"validate", "activate", "schema", "deferred", "unknown-config", "deployment-config", "package", "version", "missing-executable"} {
		t.Run(mode, func(t *testing.T) {
			o := adapterOptions()
			raw := fixtureConfig(mode, t.TempDir(), "Hello, ")
			switch mode {
			case "unknown-config":
				raw = json.RawMessage(`{"unknown":true}`)
			case "deployment-config":
				raw = json.RawMessage(`{"Command":"/bin/false","Args":[],"Env":[]}`)
			case "package":
				o.PackageID = "wrong.package"
			case "version":
				o.Version = "v9"
			case "missing-executable":
				o.Command = filepath.Join(t.TempDir(), "missing")
			}
			a := mountAdapter(t, o, raw)
			if a.err == nil {
				t.Fatal("invalid activation succeeded")
			}
			if len(a.runtime.Schemas()) != 0 {
				t.Fatal("failed activation exposed Tools")
			}
			if a.plugin.client != nil {
				d := a.plugin.client.Snapshot()
				if !d.Reaped || !d.IOComplete || !d.HandlersComplete {
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

func TestAdapterToolConflictRollsBackBatchAndProcess(t *testing.T) {
	directory := t.TempDir()
	a := mountAdapter(t, adapterOptions(), fixtureConfig("batch", directory, ""), new(conflictingTool))
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

func TestAdapterIdentityOverride(t *testing.T) {
	o := adapterOptions()
	o.PackageID, o.Version = "fixture.override", "v2"
	o.Env = append(o.Env, "QI_PT01_OVERRIDE=1")
	a := mountAdapter(t, o, fixtureConfig("", "", "Hello, "))
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

func TestAdapterDefaultsToEmptyEnvironment(t *testing.T) {
	t.Setenv("QI_PT02_AMBIENT", "host-only")
	o := adapterOptions()
	o.Env = nil
	a := mountAdapter(t, o, fixtureConfig("empty-environment", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	if err := a.instance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClean(t, a.plugin.client)
}

func TestAdapterPropagatesCleanupFailure(t *testing.T) {
	a := mountAdapter(t, adapterOptions(), fixtureConfig("cleanup-error", "", ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	if err := a.instance.Unmount(context.Background()); err == nil {
		t.Fatal("remote cleanup error hidden")
	}
	d := a.plugin.client.Snapshot()
	if !d.Reaped || !d.CleanupKnown || d.Cleanup.Complete || d.Cleanup.Error == "" {
		t.Fatalf("false cleanup success: %+v", d)
	}
}

func TestAdapterFailureCategories(t *testing.T) {
	for _, tc := range []struct {
		mode, code string
		abort      bool
	}{
		{"error", "execution", false}, {"abort-error", "execution", true}, {"cancelled", "cancelled", false},
		{"timeout", "timeout", false}, {"meta", CodeHostValidation, false}, {"panic", CodeTransport, false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			a := mountAdapter(t, adapterOptions(), fixtureConfig(tc.mode, "", ""))
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

func TestAdapterCancelledRemoteWorkerDrains(t *testing.T) {
	directory := t.TempDir()
	a := mountAdapter(t, adapterOptions(), fixtureConfig("waitstop", directory, ""))
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

func TestAdapterWithdrawsBeforeLeaseDrainAndClose(t *testing.T) {
	a := mountAdapter(t, adapterOptions(), fixtureConfig("", "", ""))
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

func TestAdapterWatchesBackgroundFailure(t *testing.T) {
	a := mountAdapter(t, adapterOptions(), fixtureConfig("background", "", ""))
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

func TestAdapterFactoryIsStaticAndDetached(t *testing.T) {
	spec := new(fixture.Plugin).Spec()
	spec.New = func() gordis.Plugin { panic("factory called business New") }
	args, env := []string{"arg"}, []string{"X=original"}
	prototype, err := NewAdapter(&specPlugin{spec}, Options{Command: "/does/not/exist", Args: args, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	args[0], env[0] = "changed", "changed"
	first, second := prototype.Spec().New().(*adapterPlugin), prototype.Spec().New().(*adapterPlugin)
	if first == second || first.Spec().ID != spec.ID || !reflect.DeepEqual(first.Spec().Requires, spec.Requires) || len(first.Spec().Provides) != 0 {
		t.Fatal("incorrect Adapter Spec/New")
	}
	if first.options.Args[0] != "arg" || first.options.Env[0] != "X=original" {
		t.Fatal("Options alias caller")
	}
	first.options.Args[0] = "mutated"
	firstSpec := first.Spec()
	firstSpec.Requires[0] = gordis.NewKey[string]("wrong").Spec()
	if second.options.Args[0] != "arg" || second.Spec().Requires[0] != tools.RuntimeKey.Spec() {
		t.Fatal("instances share mutable declarations/options")
	}
	raw := []byte(`{"Command":"business","prefix":"one"}`)
	if err := json.Unmarshal(raw, first); err != nil {
		t.Fatal(err)
	}
	raw[2] = 'x'
	if string(first.config) != `{"Command":"business","prefix":"one"}` || len(second.config) != 0 || first.options.Command != "/does/not/exist" {
		t.Fatal("business config aliases data or overwrites Options")
	}
	for _, raw := range []string{`null`, `[]`, `123`, `{} {}`} {
		if err := json.Unmarshal([]byte(raw), first); err == nil {
			t.Fatalf("accepted config %s", raw)
		}
	}
	for _, options := range []Options{{}, {Command: "greet", PackageID: "bad/id"}, {Command: "greet", Version: "bad/version"}} {
		if _, err := NewAdapter(new(fixture.Plugin), options); err == nil {
			t.Fatal("accepted invalid Options")
		}
	}
	if _, err := NewAdapter(nil, adapterOptions()); err == nil {
		t.Fatal("accepted nil Plugin")
	}
	path, err := executablePath("greet")
	executable, _ := os.Executable()
	if err != nil || path != filepath.Join(filepath.Dir(executable), "greet") || !filepath.IsAbs(path) {
		t.Fatal(path, err)
	}
}
