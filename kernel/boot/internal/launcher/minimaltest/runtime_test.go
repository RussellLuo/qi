package launcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	hostruntime "github.com/RussellLuo/qi/kernel/boot/internal/hostruntime"
	"github.com/RussellLuo/qi/kernel/boot/internal/launcher"
	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/launch"
	profileapi "github.com/RussellLuo/qi/kernel/boot/profile"
	registrationapi "github.com/RussellLuo/qi/kernel/boot/registration"
	builtin "github.com/RussellLuo/qi/kernel/bundle"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type failurePlugin struct {
	fail, cleanup error
}

func (p *failurePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.sibling", New: func() gordis.Plugin { return p }}
}
func (p *failurePlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	if err := scope.Defer("failed sibling cleanup", func(context.Context) error {
		return p.cleanup
	}); err != nil {
		return err
	}
	return p.fail
}

// Publish no Adapter, so the post-mount route Ready check fails.
type modelOnly struct{ *testModel }

func (p *modelOnly) Spec() gordis.PluginSpec {
	spec := p.testModel.Spec()
	spec.New = func() gordis.Plugin { return &modelOnly{new(testModel)} }
	return spec
}
func (p *modelOnly) Activate(_ context.Context, scope *gordis.Scope) error {
	llmRuntime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	models, err := p.DescribeModels()
	if err != nil {
		return err
	}
	return llmRuntime.Bind(scope).RegisterModels(models.Models)
}

func prepareCall(t *testing.T, candidate *registration.Candidate, input launchcontract.Input) launchcontract.Prepared {
	t.Helper()
	invocation, _, err := launchcontract.Parse(candidate.Parser, []string{"--json", "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := invocation.Prepare(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestProductReadyFailureNeverSubmitsAndReleasesWriter(t *testing.T) {
	for _, mode := range []string{"sibling", "adapter"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			profile := testProfile(t)
			profile.Instances[6].Config["directory"] = directory
			var starts, armed atomic.Int32
			registrations := testRegistrations()
			cause, cleanup := errors.New("sibling startup failed"), errors.New("sibling dispose failed")
			for i, registration := range registrations {
				switch p := registration.Plugin.(type) {
				case *privateTool:
					p.BeforeExecute = func(context.Context) error { starts.Add(1); return nil }
				case *testModel:
					if mode == "adapter" {
						registrations[i].Plugin = &modelOnly{p}
					}
				}
			}
			if mode == "sibling" {
				registrations = append(registrations, registrationapi.PluginRegistration{Plugin: &failurePlugin{fail: cause, cleanup: cleanup}, ProfileVisible: true})
				profile.Instances = append(profile.Instances, profileapi.InstanceSpec{ID: "zz-sibling", Plugin: "test.sibling"})
			}
			candidate, err := registration.Plan(registrations, profile)
			if err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			input := launchcontract.Input{Stdout: &out, Stderr: &errout}
			code, _, err := launcher.Execute(context.Background(), func(int) { armed.Add(1) }, candidate, prepareCall(t, candidate, input), input, launcher.Policy{})
			if code != 1 || err == nil || starts.Load() != 0 || out.Len() != 0 || errout.Len() != 0 || armed.Load() != 1 {
				t.Fatal(code, err, starts.Load(), armed.Load(), out.String(), errout.String())
			}
			if mode == "sibling" && (!errors.Is(err, cause) || !errors.Is(err, cleanup)) {
				t.Fatal(err)
			}
			if mode == "adapter" && !strings.Contains(err.Error(), "resolve defaultModel") {
				t.Fatal(err)
			}
			logs, _ := filepath.Glob(filepath.Join(directory, "*.jsonl"))
			if len(logs) != 0 {
				t.Fatal("task Session created before Ready", logs)
			}
			// Retry through the production carrier after rollback, with the same Store.
			profile = testProfile(t)
			profile.Instances[6].Config["directory"] = directory
			candidate, err = registration.Plan(testRegistrations(), profile)
			if err != nil {
				t.Fatal(err)
			}
			code, dispose, err := launcher.Execute(context.Background(), func(int) {}, candidate, prepareCall(t, candidate, input), input, launcher.Policy{})
			if code != 0 || dispose != nil || err != nil || !strings.Contains(out.String(), `"type":"final"`) {
				t.Fatal("writer retained after rollback", code, dispose, err, out.String())
			}
		})
	}
}

func TestProductWaitsForWholePlanAndCannotRerunOnReactivation(t *testing.T) {
	directory := t.TempDir()
	profile := testProfile(t)
	profile.Instances[6].Config["directory"] = directory
	var starts atomic.Int32
	registrations := testRegistrations()
	for _, registration := range registrations {
		if p, ok := registration.Plugin.(*privateTool); ok {
			p.BeforeExecute = func(context.Context) error { starts.Add(1); return nil }
		}
	}
	candidate, err := registration.Plan(registrations, profile)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	input := launchcontract.Input{Stdout: &out, Stderr: &errout}
	prepared := prepareCall(t, candidate, input)
	plugins, err := launchcontract.Bind(prepared, candidate.Plugins)
	if err != nil {
		t.Fatal(err)
	}
	control := launcher.NewCoordinator(func(int) {})
	provider := &carrier{&launchcontract.Context{Parent: context.Background(), Invocation: prepared, Stdout: &out, Stderr: &errout, Control: control}}
	plugins = append(plugins, provider)
	var taskInstance *gordis.Instance
	runtime, err := hostruntime.Start(context.Background(), hostruntime.Plan{Plugins: plugins, Initialize: func(ctx context.Context, r *hostruntime.Runtime) error {
		if err := r.Mount(ctx, []gordis.InstanceSpec{{ID: carrierID, Plugin: carrierID}}); err != nil {
			return err
		}
		for _, spec := range candidate.Mounts {
			if spec.Plugin == testConsumerID {
				taskInstance, err = r.Host.Env().MountReady(ctx, spec)
			} else {
				err = r.Mount(ctx, []gordis.InstanceSpec{spec})
			}
			if err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Shutdown(context.Background())
	if starts.Load() != 0 {
		t.Fatal("tool executed during Mount")
	}
	if err := prepared.(launchcontract.ReadyCheck).CheckReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := control.OpenReady(); err != nil {
		t.Fatal(err)
	}
	select {
	case completion := <-control.Done():
		if completion.Code != 0 || completion.CleanupError != nil {
			t.Fatal(completion)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task did not complete")
	}
	// Unmount waits for the first generation's Scope-owned goroutine to finish.
	if err := taskInstance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, spec := range candidate.Mounts {
		if spec.Plugin == testConsumerID {
			if taskInstance, err = runtime.Host.Env().MountReady(context.Background(), spec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := taskInstance.Unmount(context.Background()); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || strings.Count(out.String(), `"type":"session"`) != 1 || strings.Count(out.String(), `"type":"final"`) != 1 {
		t.Fatal("reactivation resubmitted", starts.Load(), out.String())
	}
	// Online mutation policy is covered by the separate full management tests.

}

type lateFailureDriver struct{ inner agentloop.Driver }

func (d lateFailureDriver) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	return errors.Join(d.inner.Drive(ctx, run, emit), errors.New("late synchronous driver failure"))
}
func TestProductUsesBoundOutcomesRatherThanSuccessfulTurnEnd(t *testing.T) {
	profile := testProfile(t)
	profile.Instances[6].Config["directory"] = t.TempDir()
	registrations := testRegistrations()
	for _, registration := range registrations {
		if p, ok := registration.Plugin.(*agentplugin.Plugin); ok {
			p.WrapDriver = func(inner agentloop.Driver) agentloop.Driver { return lateFailureDriver{inner} }
		}
	}
	candidate, err := registration.Plan(registrations, profile)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	input := launchcontract.Input{Stdout: &out, Stderr: &errout}
	code, cleanup, err := launcher.Execute(context.Background(), func(int) {}, candidate, prepareCall(t, candidate, input), input, launcher.Policy{})
	if code != 1 || cleanup != nil || err != nil || !strings.Contains(errout.String(), "late synchronous driver failure") || strings.Contains(out.String(), `"type":"final"`) || !strings.Contains(out.String(), `"type":"error"`) {
		t.Fatal(code, cleanup, err, out.String(), errout.String())
	}
}

func TestConvenienceProfileMustDeclareConsumerWithoutAugmentation(t *testing.T) {
	profile := testProfile(t)
	for i, spec := range profile.Instances {
		if spec.ID == "headless" {
			profile.Instances = append(profile.Instances[:i], profile.Instances[i+1:]...)
			break
		}
	}
	directory := filepath.Join(t.TempDir(), "unopened")
	profile.Instances[6].Config["directory"] = directory
	var out, errout bytes.Buffer
	code, _ := launcher.Work(context.Background(), func(int) {}, launcher.Options{Registrations: testRegistrations(), Parser: testParser{}, ConsumerType: testConsumerID, Args: []string{"--json", "hello"}, StdinIsTTY: true, Input: launchcontract.Input{Stdout: &out, Stderr: &errout}, Load: func() (profileapi.Profile, error) { return profile, nil }})
	if code != 1 || !strings.Contains(errout.String(), "Profile must declare test.consumer") || out.Len() != 0 {
		t.Fatal(code, out.String(), errout.String())
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid Profile activated Store", err)
	}
}

const carrierID = launchcontract.CarrierID

type carrier struct{ launch *launchcontract.Context }

func (p *carrier) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: carrierID, Provides: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec()}, New: func() gordis.Plugin { return &carrier{p.launch} }}
}
func (p *carrier) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(launchcontract.LaunchContextKey, p.launch)
}

func testProfile(t *testing.T) profileapi.Profile {
	t.Helper()
	base := builtin.Extension().Bundles[0]
	p := profileapi.Profile{APIVersion: profileapi.ProfileAPIVersion, Instances: base.Instances}
	p.Instances[1].Config = nil
	p.Instances = append(p.Instances, profileapi.InstanceSpec{ID: "persistence", Plugin: "test/startup-resource", Config: map[string]any{"directory": ".qi/headless/sessions"}})
	p.Instances = append(p.Instances, profileapi.InstanceSpec{ID: "model", Plugin: "test.model"}, profileapi.InstanceSpec{ID: "headless", Plugin: testConsumerID})
	p.Instances = append(p.Instances, profileapi.InstanceSpec{ID: "test-tool", Plugin: "test.tool"})
	return p
}
func testRegistrations() []registrationapi.PluginRegistration {
	return append(builtin.Extension().Registrations, registrationapi.PluginRegistration{Plugin: new(startupResource), ProfileVisible: true}, registrationapi.PluginRegistration{Plugin: new(privateTool), ProfileVisible: true}, registrationapi.PluginRegistration{Plugin: new(testModel), ProfileVisible: true}, registrationapi.PluginRegistration{Plugin: new(testConsumer), ProfileVisible: true, Invocation: testParser{}, Completes: true})
}

type privateTool struct{ BeforeExecute func(context.Context) error }

func (p *privateTool) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.tool", Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return &privateTool{BeforeExecute: p.BeforeExecute} }}
}
func (p *privateTool) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).Register(tools.Definition{
		Name: "echo", Description: "Private launcher test tool.", Parameters: tools.JSONSchema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Output: tools.JSONSchema(`{"type":"object"}`), Execute: tools.JSONTool(func(ctx context.Context, call tools.Execution) (json.RawMessage, error) {
			if p.BeforeExecute != nil {
				if err := p.BeforeExecute(ctx); err != nil {
					return nil, err
				}
			}
			return append(json.RawMessage(nil), call.Arguments...), nil
		})})
	return err
}

// This boot-local model exercises real Agent/Tool execution without optional modules.
type testModel struct{ calls *atomic.Int32 }

func (p *testModel) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test.model", Requires: []gordis.ServiceSpec{llm.RuntimeKey.Spec()}, New: func() gordis.Plugin { return &testModel{calls: p.calls} }}
}
func (*testModel) DescribeModels() (modelcatalog.Description, error) {
	return modelcatalog.Description{}, nil
}
func (p *testModel) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	return runtime.Bind(scope).RegisterProvider(llm.AdapterRegistration{Route: "fixture", Provider: "fixture", Adapter: bootAdapter{calls: p.calls}}, nil)
}

type bootAdapter struct{ calls *atomic.Int32 }

func (a bootAdapter) Stream(ctx context.Context, request llm.Request) contracts.ChunkStream {
	if a.calls != nil {
		a.calls.Add(1)
	}
	return func(yield func(protocol.StreamChunk, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(protocol.StreamChunk{}, err)
			return
		}
		var last protocol.Message
		for _, m := range request.Messages {
			if m.Role == protocol.RoleUser || m.Role == protocol.RoleTool {
				last = m
			}
		}
		text := "hello"
		if len(last.Content) > 0 {
			if block, ok := last.Content[0].(protocol.TextBlock); ok {
				text = block.Text
			}
		}
		var chunks []protocol.StreamChunk
		if last.Role == protocol.RoleTool {
			var result struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(text), &result); err != nil {
				yield(protocol.StreamChunk{}, err)
				return
			}
			text = "Echoed: " + result.Text
		} else if len(request.Tools) > 0 {
			raw, _ := json.Marshal(map[string]string{"text": text})
			call := protocol.ToolCall{ID: "call-echo", Name: "echo", Arguments: string(raw)}
			chunks = []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockToolCall}, {Type: protocol.ChunkToolCallDelta, Index: 0, ID: call.ID, Name: &call.Name, ArgumentsDelta: call.Arguments}, {Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.ToolCallBlock{ToolCall: call}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls}}}
		}
		if chunks == nil {
			chunks = []protocol.StreamChunk{{Type: protocol.ChunkBlockStart, Index: 0, BlockType: protocol.BlockText}, {Type: protocol.ChunkTextDelta, Index: 0, Text: text}, {Type: protocol.ChunkBlockEnd, Index: 0, Block: protocol.TextBlock{Text: text}}, {Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}}}
		}
		for _, c := range chunks {
			if !yield(c, nil) {
				return
			}
		}
	}
}

// Finite boot test consumer: real Store/Factory/Driver, only local parsing/output.
const testConsumerID = "test.consumer"

type testParser struct{}

func (testParser) Help() string { return "test task" }
func (testParser) Parse(args []string, _ bool) (launchcontract.Invocation, error) {
	return testInvocation{args}, nil
}

type testInvocation struct{ args []string }

func (testInvocation) HelpRequested() bool { return false }
func (i testInvocation) Prepare(context.Context, launchcontract.Input) (launchcontract.Prepared, error) {
	return &testPrepared{task: i.args[len(i.args)-1]}, nil
}

type testPrepared struct {
	launchcontract.Task
	task     string
	mu       sync.Mutex
	driveErr error
}

func (p *testPrepared) Bind(plugins []gordis.Plugin) ([]gordis.Plugin, error) {
	result := append([]gordis.Plugin(nil), plugins...)
	for i, v := range result {
		if original, ok := v.(*agentplugin.Plugin); ok {
			copy := *original
			previous := copy.WrapDriver
			copy.WrapDriver = func(driver agentloop.Driver) agentloop.Driver {
				if previous != nil {
					driver = previous(driver)
				}
				return captureDriver{driver, p}
			}
			result[i] = &copy
		}
	}
	return result, nil
}

type captureDriver struct {
	inner agentloop.Driver
	p     *testPrepared
}

func (d captureDriver) Drive(ctx context.Context, run agentloop.Run, emit agentloop.EmitFunc) error {
	err := d.inner.Drive(ctx, run, emit)
	d.p.mu.Lock()
	d.p.driveErr = err
	d.p.mu.Unlock()
	return err
}

type testConsumer struct{}

func (*testConsumer) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: testConsumerID, New: func() gordis.Plugin { return new(testConsumer) }, Requires: []gordis.ServiceSpec{launchcontract.LaunchContextKey.Spec(), session.StoreKey.Spec(), agent.FactoryKey.Spec(), llm.RuntimeKey.Spec()}}
}
func (*testConsumer) Activate(_ context.Context, scope *gordis.Scope) error {
	call, err := scope.Get(launchcontract.LaunchContextKey)
	if err != nil {
		return err
	}
	p := call.Invocation.(*testPrepared)
	store, err := scope.Get(session.StoreKey)
	if err != nil {
		return err
	}
	provider, err := scope.Get(agent.FactoryKey)
	if err != nil {
		return err
	}
	factory, err := provider.Bind(scope)
	if err != nil {
		return err
	}
	llmRuntime, err := scope.Get(llm.RuntimeKey)
	if err != nil {
		return err
	}
	config := agent.Config{Loop: agentloop.Config{LLM: protocol.CallConfig{Provider: "fixture", Model: "fake"}}}
	return p.Start(scope, call, "boot task", func(ctx context.Context) error {
		lease, err := llmRuntime.PrepareCall(ctx, config.Loop.LLM)
		if err != nil {
			return fmt.Errorf("resolve defaultModel: %w", err)
		}
		lease.Close()
		return nil
	}, func(ctx context.Context, begin func(int)) (result launchcontract.Completion) {
		var handle agent.Handle
		var runErr error
		defer func() {
			if runErr != nil {
				result.Code = 1
			}
			begin(result.Code)
			if handle != nil {
				result.CleanupError = handle.Dispose(context.Background())
			}
			if runErr != nil {
				result.Code = 1
				fmt.Fprintln(call.Stderr, runErr)
				_, _ = io.WriteString(call.Stdout, "{\"type\":\"error\"}\n")
			}
		}()
		current, err := store.Create(ctx, session.CreateRequest{ID: "boot-test-session"})
		if err != nil {
			runErr = err
			return
		}
		handle, runErr = factory.Attach(ctx, agent.AttachOptions{Session: current, Agent: agent.AgentOptions{Config: config}})
		if runErr != nil {
			return
		}
		watermark := current.Watermark()
		_, runErr = io.WriteString(call.Stdout, "{\"type\":\"session\"}\n")
		if runErr != nil {
			return
		}
		runErr = handle.Agent().Followup(ctx, protocol.NewTextMessage("question", protocol.RoleUser, protocol.SourceUser, p.task))
		if runErr != nil {
			return
		}
		runErr = handle.Agent().WhenIdle(ctx)
		if runErr != nil {
			return
		}
		p.mu.Lock()
		runErr = p.driveErr
		p.mu.Unlock()
		if runErr != nil {
			return
		}
		completed := false
		for event, err := range current.Events(ctx, watermark) {
			if err != nil {
				runErr = err
				return
			}
			if event.Type == protocol.TurnEnded {
				completed = protocol.EventPayload[protocol.TurnEndData](event).Reason.Kind == "completed"
			}
		}
		if !completed {
			runErr = errors.New("task did not complete")
			return
		}
		_, runErr = io.WriteString(call.Stdout, "{\"type\":\"final\"}\n")
		return
	})
}

// An ordinary lifecycle resource exercises rollback and release without a concrete persistence provider.
type startupResource struct {
	Directory string `json:"directory"`
}

func (*startupResource) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/startup-resource", New: func() gordis.Plugin { return new(startupResource) }}
}
func (p *startupResource) Activate(_ context.Context, s *gordis.Scope) error {
	lock := filepath.Join(p.Directory, "fixture.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return err
	}
	return s.Defer("release fixture resource", func(context.Context) error { return os.Remove(lock) })
}
