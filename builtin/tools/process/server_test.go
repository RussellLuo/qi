package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	gordisprocess "github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

var childExecutable string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "qi-pt01-child-")
	if err != nil {
		panic(err)
	}
	childExecutable = filepath.Join(directory, "greet")
	// Compile the thin public entry point as an independent race-enabled program.
	command := exec.Command("go", "build", "-race", "-o", childExecutable, "./testdata/greet")
	if output, err := command.CombinedOutput(); err != nil {
		_ = os.RemoveAll(directory)
		panic(string(output) + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

func fixtureOptions(t *testing.T, mode, directory string) gordisprocess.Options {
	t.Helper()
	executable := childExecutable
	identity, err := pluginIdentity(fixture.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(fixture.Plugin{Mode: mode, Directory: directory, Prefix: "Hello, "})
	return gordisprocess.Options{
		Path:     executable,
		Env:      []string{"GORACE=atexit_sleep_ms=0"},
		Identity: identity, Instance: "host-fixture", Generation: 7, Config: config,
		Limits: defaultLimits(gordisprocess.Limits{}), StartupTimeout: 5 * time.Second, ExitGrace: 3 * time.Second,
	}
}

func startFixture(t *testing.T, mode, directory string) (*gordisprocess.Client, *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := gordisprocess.Start(ctx, fixtureOptions(t, mode, directory))
	if err != nil {
		if client == nil {
			t.Fatal("start:", err)
		}
		t.Fatalf("start: %v, diagnostics: %+v", err, client.Snapshot())
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	wire, err := NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	return client, wire
}

func request() CallRequest {
	return CallRequest{Tool: "greet", Arguments: json.RawMessage(`{"name":"Qi"}`), Invocation: Invocation{RequestID: "request", ToolCallID: "call", InvocationID: "invocation"}}
}

func assertClean(t *testing.T, client *gordisprocess.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Fatal(err, client.Snapshot())
	}
	d := client.Snapshot()
	if d.Forced || !d.Reaped || d.ExitCode != 0 || !d.IOComplete || !d.HandlersComplete || !d.CleanupKnown || !d.Cleanup.Complete || d.Cleanup.Error != "" {
		t.Fatalf("unclean shutdown: %+v", d)
	}
}

func TestServePluginRealProcess(t *testing.T) {
	directory := t.TempDir()
	client, wire := startFixture(t, "", directory)
	metadata, err := wire.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := pluginIdentity(fixture.ID, "", "")
	if metadata.ID != identity.Package || metadata.Version != "dev" {
		t.Fatalf("metadata = %+v", metadata)
	}
	definitions, err := wire.ListTools(context.Background())
	if err != nil || len(definitions) != 1 || definitions[0].Name != "greet" || definitions[0].FailureMode != FailureResult {
		t.Fatalf("definitions = %+v, %v", definitions, err)
	}
	result, err := wire.Call(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		PID      int    `json:"pid"`
		Greeting string `json:"greeting"`
	}
	if err := json.Unmarshal(result, &value); err != nil {
		t.Fatal(err)
	}
	if value.PID != client.Snapshot().PID || value.PID == os.Getpid() || value.Greeting != "Hello, Qi" {
		t.Fatalf("child result = %s", result)
	}
	assertClean(t, client)
	if _, err := os.Stat(filepath.Join(directory, "cleaned")); err != nil {
		t.Fatal(err)
	}
}

func TestServePluginInitializationFailures(t *testing.T) {
	for _, mode := range []string{"validate", "activate", "deferred", "large-timeout", "fractional-timeout", "schema", "unknown-config"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			o := fixtureOptions(t, mode, directory)
			if mode == "unknown-config" {
				o.Config = json.RawMessage(`{"unknown":true}`)
			}
			client, err := gordisprocess.Start(context.Background(), o)
			if err == nil {
				_ = client.Close(context.Background())
				t.Fatal("invalid initialization succeeded")
			}
			if client == nil {
				t.Fatal("no startup diagnostics", err)
			}
			d := client.Snapshot()
			if !d.Reaped || d.Forced || !d.CleanupKnown || !d.Cleanup.Complete {
				t.Fatalf("initialization did not clean up: %+v", d)
			}
			if mode != "validate" && mode != "unknown-config" {
				if _, err := os.Stat(filepath.Join(directory, "cleaned")); err != nil {
					t.Fatal("activation resources leaked", err)
				}
			}
		})
	}
}

func TestServePluginResultAndErrorBoundaries(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{
		{"error", CodeToolError}, {"abort-error", CodeToolError}, {"cancelled", CodeCancelled}, {"timeout", CodeTimeout}, {"panic", CodeTransport},
		{"meta", CodeHostValidation}, {"contexts", CodeHostValidation}, {"concludes", CodeHostValidation}, {"content", CodeHostValidation},
		{"multiple", CodeHostValidation}, {"invalid-json", CodeHostValidation}, {"wrong-output", CodeHostValidation}, {"oversize", CodeHostValidation},
		{"invalid-arguments", CodeHostValidation},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			client, wire := startFixture(t, tc.mode, "")
			r := request()
			if tc.mode == "invalid-arguments" {
				r.Arguments = json.RawMessage(`{"name":3}`)
			}
			_, err := wire.Call(context.Background(), r)
			var callErr *CallError
			if !errors.As(err, &callErr) || callErr.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
			if tc.code == CodeCancelled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause = %v", err)
			}
			if tc.code == CodeTimeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout cause = %v", err)
			}
			assertClean(t, client)
		})
	}
}

func waitMarker(t *testing.T, directory, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(directory, name)); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("marker %s not reached", name)
}

func TestServePluginCancelledWorkerDrainsAfterScopeStop(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			testWorkerDrain(t, deadline)
		})
	}
}

func testWorkerDrain(t *testing.T, deadline bool) {
	directory := t.TempDir()
	client, wire := startFixture(t, "waitstop", directory)
	ctx, cancel := context.WithCancel(context.Background())
	wantErr := context.Canceled
	if deadline {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		wantErr = context.DeadlineExceeded
	}
	defer cancel()
	returned := make(chan error, 1)
	go func() { _, err := wire.Call(ctx, request()); returned <- err }()
	waitMarker(t, directory, "started")
	if !deadline {
		cancel()
	}
	select {
	case err := <-returned:
		if !errors.Is(err, wantErr) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller did not return")
	}
	waitMarker(t, directory, "cancelled")
	if _, err := os.Stat(filepath.Join(directory, "exited")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker exited before Scope stop")
	}
	assertClean(t, client) // Must initiate Scope stop before waiting for the worker.
	for _, name := range []string{"stopping", "exited", "cleaned"} {
		waitMarker(t, directory, name)
	}
}

func TestServePluginCleanupErrorAndBackgroundFailure(t *testing.T) {
	t.Run("cleanup error", func(t *testing.T) {
		client, _ := startFixture(t, "cleanup-error", "")
		if err := client.Close(context.Background()); err == nil {
			t.Fatal("cleanup error hidden")
		}
		d := client.Snapshot()
		if !d.Reaped || !d.CleanupKnown || d.Cleanup.Complete || d.Cleanup.Error == "" {
			t.Fatalf("false cleanup success: %+v", d)
		}
	})
	t.Run("background failure", func(t *testing.T) {
		client, wire := startFixture(t, "background", "")
		_, _ = wire.Call(context.Background(), request())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Watch(ctx); err == nil || ctx.Err() != nil {
			t.Fatalf("failure was not reported: %v", err)
		}
		assertClean(t, client)
	})
}

func TestServePluginIdentityOverrides(t *testing.T) {
	for _, mismatch := range []string{"", "package", "version"} {
		t.Run("mismatch="+mismatch, func(t *testing.T) {
			o := fixtureOptions(t, "", "")
			o.Env = append(o.Env, "QI_PT01_OVERRIDE=1")
			o.Identity, _ = pluginIdentity(fixture.ID, "fixture.override", "v2")
			if mismatch == "package" {
				o.Identity.Package = "wrong"
			}
			if mismatch == "version" {
				o.Identity.Version = "wrong"
			}
			client, err := gordisprocess.Start(context.Background(), o)
			if mismatch != "" {
				if err == nil {
					_ = client.Close(context.Background())
					t.Fatal("incompatible identity accepted")
				}
				if client == nil || !client.Snapshot().Reaped {
					t.Fatal("failed handshake process leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := NewClient(client)
			metadata, err := wire.Describe(context.Background())
			if err != nil || metadata.ID != "fixture.override" || metadata.Version != "v2" {
				t.Fatalf("metadata = %+v, %v", metadata, err)
			}
			assertClean(t, client)
		})
	}
}

type specPlugin struct{ spec gordis.PluginSpec }

func (p *specPlugin) Spec() gordis.PluginSpec                     { return p.spec }
func (*specPlugin) Activate(context.Context, *gordis.Scope) error { panic("must not activate") }

type specOnly struct{}

func (specOnly) Spec() gordis.PluginSpec { return new(fixture.Plugin).Spec() }

type panicSpec struct{}

func (panicSpec) Spec() gordis.PluginSpec { panic("bad Spec") }

func TestServePluginRejectsInvalidDeclarations(t *testing.T) {
	valid := new(fixture.Plugin).Spec()
	called := false
	valid.New = func() gordis.Plugin { called = true; return new(fixture.Plugin) }
	if _, err := inspectPlugin(&specPlugin{valid}); err != nil || called {
		t.Fatalf("inspection called constructor: %v", err)
	}
	var typedNil *fixture.Plugin
	for _, p := range []gordis.Plugin{
		nil, typedNil, panicSpec{}, specOnly{}, &specPlugin{},
		&specPlugin{gordis.PluginSpec{ID: "missing-new", Requires: valid.Requires}},
		&specPlugin{gordis.PluginSpec{New: valid.New, Requires: valid.Requires}},
		&specPlugin{gordis.PluginSpec{ID: "missing-runtime", New: valid.New}},
		&specPlugin{gordis.PluginSpec{ID: "wrong-type", New: valid.New, Requires: []gordis.ServiceSpec{gordis.NewKey[string](tools.RuntimeKey.Name()).Spec()}}},
		&specPlugin{gordis.PluginSpec{ID: "extra-service", New: valid.New, Requires: append(append([]gordis.ServiceSpec{}, valid.Requires...), gordis.NewKey[string]("extra").Spec())}},
		&specPlugin{gordis.PluginSpec{ID: "provides", New: valid.New, Requires: valid.Requires, Provides: valid.Requires}},
		&specPlugin{gordis.PluginSpec{ID: providerID, New: valid.New, Requires: valid.Requires}},
	} {
		if err := ServePlugin(context.Background(), nil, nil, p); err == nil {
			t.Fatalf("accepted invalid prototype %T", p)
		}
	}
	if called {
		t.Fatal("invalid declaration constructed business code")
	}
	if err := ServePlugin(context.Background(), nil, nil, new(fixture.Plugin), ServerOptions{}, ServerOptions{}); err == nil {
		t.Fatal("accepted multiple options")
	}
	if err := ServePlugin(context.Background(), nil, nil, new(fixture.Plugin), ServerOptions{PackageID: "bad/id"}); err == nil {
		t.Fatal("accepted invalid identity")
	}
	if err := ServePlugin(context.Background(), nil, nil, new(fixture.Plugin), ServerOptions{Version: "bad/version"}); err == nil {
		t.Fatal("accepted invalid version")
	}
}

func TestDefaultsAndIdentity(t *testing.T) {
	o := (Options{}).defaults()
	if o.StartupTimeout != 3*time.Second || o.ExitGrace != time.Second || o.Env == nil || !reflect.DeepEqual(o.Limits, gordisprocess.Limits{MaxMessage: 256 * 1024, MaxPending: 16, Queue: 16}) {
		t.Fatalf("defaults = %+v", o)
	}
	args, env := []string{"arg"}, []string{"X=y"}
	o = (Options{Args: args, Env: env}).defaults()
	args[0], env[0] = "changed", "changed"
	if o.Args[0] != "arg" || o.Env[0] != "X=y" {
		t.Fatal("options did not copy slices")
	}
	identity, err := pluginIdentity(fixture.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Fixed full SHA256 of the UTF-8 Plugin ID, independent of module versions.
	if identity.Package != "qi.tool-plugin.554820ab6925e693237ef0797e4ef6d0b728fc5f6bd4ad2da27b04ad5a1c1deb" || identity.Version != "dev" {
		t.Fatalf("identity = %+v", identity)
	}
}
