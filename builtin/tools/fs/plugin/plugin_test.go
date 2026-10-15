package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	fstool "github.com/RussellLuo/qi/builtin/tools/fs"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var limitFields = []string{"read_limit", "read_max_line_length", "read_max_bytes"}

func TestConfigurationDefaultsAndValidation(t *testing.T) {
	p := &Plugin{}
	options, err := p.options()
	if err != nil || options != fstool.DefaultOptions("") {
		t.Fatalf("defaults = %+v / %v", options, err)
	}
	if p.ReadLimit != nil || p.ReadMaxBytes != nil {
		t.Fatal("default resolution mutated the configuration")
	}
	var configured Plugin
	if err := json.Unmarshal([]byte(`{"read_limit":3,"read_max_line_length":4,"read_max_bytes":5}`), &configured); err != nil {
		t.Fatal(err)
	}
	options, err = configured.options()
	want := fstool.Options{ReadLimit: 3, ReadMaxLineLength: 4, ReadMaxBytes: 5, DiffBasisMaxBytes: fstool.DefaultDiffBasisMaxBytes}
	if err != nil || options != want {
		t.Fatalf("explicit limits = %+v / %v", options, err)
	}
	for _, field := range limitFields {
		for _, value := range []int{0, -1} {
			raw, _ := json.Marshal(map[string]any{field: value})
			var p Plugin
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("accepted invalid %s=%d: %v", field, value, err)
			}
		}
	}
}

func TestExtensionIsPureAndDescribesConfiguration(t *testing.T) {
	extension := Extension()
	if len(extension.Registrations) != 1 || len(extension.Bundles) != 0 {
		t.Fatalf("extension = %+v", extension)
	}
	registration := extension.Registrations[0]
	p := registration.Plugin.(*Plugin)
	spec := p.Spec()
	if spec.ID != PluginID || len(spec.Requires) != 1 || spec.Requires[0] != tools.RuntimeKey.Spec() || len(spec.Provides) != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.New() == p || spec.New().(*Plugin).files != nil || p.files != nil {
		t.Fatal("metadata or constructor activated file access or reused its prototype")
	}
	descriptor := registration.Descriptor
	if !registration.ProfileVisible || descriptor.UpdatePolicy != profile.UpdateReplaceGeneration || descriptor.Compatibility.HostAPI != profile.HostAPIVersion {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	if _, err := application.Compose(extension); err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(descriptor.ConfigSchema, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("fs-config.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("fs-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{}); err != nil {
		t.Fatal("empty config rejected", err)
	}
	for _, config := range []map[string]any{{"root": "workspace"}, {"cwd": "workspace"}, {"max_write_bytes": 1}, {"diff_basis_max_bytes": 1}, {"extra": true}} {
		if err := schema.Validate(config); err == nil {
			t.Fatalf("schema accepted %+v", config)
		}
	}
	properties := document.(map[string]any)["properties"].(map[string]any)
	defaults := []int{fstool.DefaultReadLimit, fstool.DefaultReadMaxLineLength, fstool.DefaultReadMaxBytes}
	for i, field := range limitFields {
		if properties[field].(map[string]any)["default"] != float64(defaults[i]) {
			t.Fatalf("schema default drifted for %s", field)
		}
		for _, value := range []any{0, -1, 1.5, "1", nil} {
			if err := schema.Validate(map[string]any{field: value}); err == nil {
				t.Fatalf("schema accepted %s=%v", field, value)
			}
		}
		if err := schema.Validate(map[string]any{field: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// The probe observes the actual plugin instance and Scope, keeping lifecycle
// tests on Gordis and Tool Runtime rather than substituting an owner mock.
type observedFS struct {
	*Plugin
	activated         chan<- activation
	undeclaredRuntime bool
}

type activation struct {
	plugin *Plugin
	scope  *gordis.Scope
}

func (p *observedFS) Spec() gordis.PluginSpec {
	spec := p.Plugin.Spec()
	if p.undeclaredRuntime {
		spec.Requires = nil
	}
	spec.New = func() gordis.Plugin {
		return &observedFS{Plugin: new(Plugin), activated: p.activated, undeclaredRuntime: p.undeclaredRuntime}
	}
	return spec
}

func (p *observedFS) Activate(ctx context.Context, scope *gordis.Scope) error {
	err := p.Plugin.Activate(ctx, scope)
	p.activated <- activation{plugin: p.Plugin, scope: scope}
	return err
}

type runtimeProvider struct {
	runtime *tools.Runtime
	ready   chan<- *gordis.Scope
}

func (p *runtimeProvider) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.tools", Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return &runtimeProvider{runtime: p.runtime, ready: p.ready} }}
}

func (p *runtimeProvider) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("close runtime", func(context.Context) error { p.runtime.Close(); return nil }); err != nil {
		return err
	}
	if err := scope.Provide(tools.RuntimeKey, p.runtime); err != nil {
		return err
	}
	p.ready <- scope
	return nil
}

type fixture struct {
	host         *gordis.Host
	runtime      *tools.Runtime
	activated    chan activation
	ctx          context.Context
	runtimeScope *gordis.Scope
}

func newFixture(t *testing.T, undeclaredRuntime bool) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	t.Chdir(t.TempDir())
	f := &fixture{runtime: tools.NewRuntime(), activated: make(chan activation, 4), ctx: ctx}
	ready := make(chan *gordis.Scope, 1)
	host, err := gordis.NewHost(&runtimeProvider{runtime: f.runtime, ready: ready}, &observedFS{Plugin: new(Plugin), activated: f.activated, undeclaredRuntime: undeclaredRuntime})
	if err != nil {
		t.Fatal(err)
	}
	f.host = host
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "tools", Plugin: "test.tools"}); err != nil {
		t.Fatal(err)
	}
	f.runtimeScope = <-ready
	return f
}

func (f *fixture) mount(t *testing.T, id string, config map[string]any) (*gordis.Instance, activation, error) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := f.host.Env().MountReady(f.ctx, gordis.InstanceSpec{ID: id, Plugin: PluginID, Config: raw})
	select {
	case activation := <-f.activated:
		return instance, activation, err
	case <-f.ctx.Done():
		t.Fatal("activation not observed", err, f.ctx.Err())
		return nil, activation{}, err
	}
}

func assertNames(t *testing.T, runtime *tools.Runtime, want ...string) {
	t.Helper()
	snapshot, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	names := make([]string, 0)
	for _, schema := range snapshot.Schemas() {
		names = append(names, schema.Name)
	}
	if len(names) != len(want) || (len(names) > 0 && !reflect.DeepEqual(names, want)) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func assertClosed(t *testing.T, p *Plugin) {
	t.Helper()
	if p.files == nil {
		t.Fatal("expected a local access object")
	}
	if _, _, err := p.files.Read(context.Background(), "file.txt", 1, 1); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("local access object not closed: %v", err)
	}
}

func execute(t *testing.T, ctx context.Context, snapshot *tools.Snapshot, name, arguments string) json.RawMessage {
	t.Helper()
	call, err := snapshot.Prepare(protocol.ToolCall{ID: protocol.ToolCallID(name), Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.Execute(ctx)
	if err != nil || result.IsError {
		t.Fatalf("%s failed: %+v / %v", name, result, err)
	}
	return result.Value
}

func TestToolsRequireActivationAndAreRevokedOnStop(t *testing.T) {
	f := newFixture(t, false)
	assertNames(t, f.runtime)
	instance, activation, err := f.mount(t, "fs", map[string]any{"read_limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	assertNames(t, f.runtime, "read_file", "write_file")
	snapshot, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.Close)
	execute(t, f.ctx, snapshot, "write_file", `{"file_path":"file.txt","content":"a\nb"}`)
	var read fstool.ReadResult
	if err := json.Unmarshal(execute(t, f.ctx, snapshot, "read_file", `{"file_path":"file.txt"}`), &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Lines) != 1 || read.TotalLines != 2 {
		t.Fatalf("configured read limit not applied: %+v", read)
	}
	call, err := snapshot.Prepare(protocol.ToolCall{ID: "too-large", Name: "read_file", Arguments: `{"file_path":"file.txt","limit":2}`})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := call.Execute(f.ctx); err != nil || !result.IsError {
		t.Fatalf("configured read maximum not applied: %+v / %v", result, err)
	}
	snapshot.Close()
	if err := instance.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertNames(t, f.runtime)
	assertClosed(t, activation.plugin)
	// A later mount can reuse both names after the previous owner is gone.
	if _, _, err := f.mount(t, "next", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	assertNames(t, f.runtime, "read_file", "write_file")
}

func TestBatchConflictRollsBackAndClosesFailedActivation(t *testing.T) {
	for _, conflict := range []string{"read_file", "write_file"} {
		t.Run(conflict, func(t *testing.T) {
			f := newFixture(t, false)
			// A separate local access object keeps the pre-existing definition independent.
			files, err := fstool.New(fstool.DefaultOptions(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { files.Close() })
			definition := fstool.NewReadDefinition(files)
			if conflict == "write_file" {
				definition = fstool.NewWriteDefinition(files)
			}
			if _, err := f.runtime.Bind(f.runtimeScope).Register(definition); err != nil {
				t.Fatal(err)
			}
			instance, failed, err := f.mount(t, "conflicting", map[string]any{})
			if err == nil || !strings.Contains(err.Error(), conflict) {
				t.Fatalf("expected %s conflict: %v", conflict, err)
			}
			assertNames(t, f.runtime, conflict)
			if err := instance.Unmount(f.ctx); err != nil {
				t.Fatal(err)
			}
			assertClosed(t, failed.plugin)
			assertNames(t, f.runtime, conflict)
		})
	}
}

func TestSnapshotKeepsAccessOpenUntilReleased(t *testing.T) {
	f := newFixture(t, false)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	instance, activation, err := f.mount(t, "fs", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.Close)
	stopped := make(chan struct{})
	if err := activation.scope.OnStop("observe stopping", func(context.Context) error { close(stopped); return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- instance.Unmount(f.ctx) }()
	select {
	case <-stopped:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case err := <-done:
		t.Fatalf("stop overtook Snapshot lease: %v", err)
	default:
	}
	// Fresh preparation cannot admit the stopping generation. The old lease
	// still has both definitions and can perform real file operations.
	if fresh, err := f.runtime.Prepare(); err == nil {
		defer fresh.Close()
		if len(fresh.Schemas()) != 0 {
			t.Fatal("stopping owner admitted a fresh Snapshot")
		}
	}
	execute(t, f.ctx, snapshot, "write_file", `{"file_path":"file.txt","content":"leased"}`)
	execute(t, f.ctx, snapshot, "read_file", `{"file_path":"file.txt"}`)
	if data, err := os.ReadFile(filepath.Join(cwd, "file.txt")); err != nil || string(data) != "leased" {
		t.Fatalf("leased write = %q / %v", data, err)
	}
	snapshot.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	assertClosed(t, activation.plugin)
	assertNames(t, f.runtime)
}

func TestActivationFailureReleasesOpenedRoot(t *testing.T) {
	f := newFixture(t, true)
	instance, activation, err := f.mount(t, "fs", map[string]any{})
	if err == nil {
		t.Fatal("undeclared Tool Runtime access succeeded")
	}
	if err := instance.Unmount(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, activation.plugin)
	assertNames(t, f.runtime)
	// A stopped Scope rejects Defer; Activate must close immediately itself.
	p := &Plugin{}
	if err := p.Activate(f.ctx, activation.scope); err == nil {
		t.Fatal("activation on stopped Scope succeeded")
	}
	assertClosed(t, p)
}

func TestPluginFallbackCWDFixedAtActivation(t *testing.T) {
	f := newFixture(t, false)
	first, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.mount(t, "fs", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	s, err := f.runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	execute(t, f.ctx, s, "write_file", `{"file_path":"file.txt","content":"fallback"}`)
	if data, err := os.ReadFile(filepath.Join(first, "file.txt")); err != nil || string(data) != "fallback" {
		t.Fatalf("fallback moved: %q / %v", data, err)
	}
}
