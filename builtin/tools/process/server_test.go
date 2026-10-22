package process

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

var childExecutable, legacyExecutable string

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
	legacyExecutable = filepath.Join(directory, "legacy")
	command = exec.Command("go", "build", "-race", "-o", legacyExecutable, "./testdata/legacy")
	if output, err := command.CombinedOutput(); err != nil {
		panic(string(output) + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

func request() CallRequest {
	return CallRequest{Tool: "greet", Arguments: json.RawMessage(`{"name":"Qi"}`), ToolCallID: "call"}
}

func assertClean(t *testing.T, s *rpc.Session) {
	t.Helper()
	d := s.Snapshot()
	if !d.Reaped || d.Cleanup == nil || d.Cleanup.Forced || !d.Cleanup.CleanupComplete || len(d.Cleanup.Errors) > 0 {
		t.Fatalf("unclean shutdown: %+v", d)
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
		if err := ServePlugin(context.Background(), p); err == nil {
			t.Fatalf("accepted invalid prototype %T", p)
		}
	}
	if called {
		t.Fatal("invalid declaration constructed business code")
	}
	if err := ServePlugin(context.Background(), new(fixture.Plugin), ServerOptions{}, ServerOptions{}); err == nil {
		t.Fatal("accepted multiple options")
	}
	if err := ServePlugin(context.Background(), new(fixture.Plugin), ServerOptions{ID: "bad id"}); err == nil {
		t.Fatal("accepted invalid identity")
	}
	if err := ServePlugin(context.Background(), new(fixture.Plugin), ServerOptions{Version: "bad/version"}); err == nil {
		t.Fatal("accepted invalid version")
	}
}

func TestDefaultsAndIdentity(t *testing.T) {
	o := (HostOptions{}).defaults()
	if o.StartupTimeout != 3*time.Second || o.ExitGrace != 3*time.Second || o.Env == nil || !reflect.DeepEqual(o.Limits, Limits{MaxMessage: 256 * 1024, MaxPending: 16}) {
		t.Fatalf("defaults = %+v", o)
	}
	args, env := []string{"arg"}, []string{"X=y"}
	o = (HostOptions{Args: args, Env: env}).defaults()
	args[0], env[0] = "changed", "changed"
	if o.Args[0] != "arg" || o.Env[0] != "X=y" {
		t.Fatal("options did not copy slices")
	}
	identity, err := pluginIdentity(fixture.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The default identity preserves the readable Plugin ID, including Unicode.
	if identity.ID != fixture.ID || identity.Version != "dev" {
		t.Fatalf("identity = %+v", identity)
	}
}
