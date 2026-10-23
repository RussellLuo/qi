package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

// Both deployments use the very same fixture.Plugin and Gordis owner lifecycle.
func mountLifecycle(t *testing.T, remote bool, raw json.RawMessage) *loaderHost {
	t.Helper()
	if remote {
		return mountLoader(t, loaderOptions(), raw)
	}
	a := &loaderHost{runtime: tools.NewRuntime()}
	var err error
	a.host, err = gordis.NewHost(&hostRuntime{a.runtime}, new(fixture.Plugin))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.host.Shutdown(context.Background()); a.runtime.Close() })
	if _, err = a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "runtime", Plugin: "runtime"}); err != nil {
		t.Fatal(err)
	}
	a.instance, a.err = a.host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "tool", Plugin: fixture.ID, Config: raw})
	return a
}

func awaitLifecycle(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("lifecycle barrier not reached")
}

func assertReaped(t *testing.T, s *rpc.Session) {
	t.Helper()
	assertClean(t, s)
	if err := syscall.Kill(s.Snapshot().PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("PID not absent: %v", err)
	}
}

func TestSamePluginSlowWorkerOwnerDrain(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, mode := range []string{"barrier-cancel", "barrier-timeout"} {
			name := "local/"
			if remote {
				name = "child/"
			}
			t.Run(name+mode, func(t *testing.T) {
				directory := t.TempDir()
				// Cleanup runs in reverse registration order: release the deliberately
				// slow worker before the Host cleanup tries to drain it.
				release := func() {
					if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0600); err != nil {
						t.Error(err)
					}
				}
				a := mountLifecycle(t, remote, fixtureConfig(mode, directory, ""))
				t.Cleanup(release)
				if a.err != nil {
					t.Fatal(a.err)
				}
				snapshot, err := a.runtime.Prepare()
				if err != nil {
					t.Fatal(err)
				}
				call, err := snapshot.Prepare(protocol.ToolCall{ID: "slow", Name: "greet", Arguments: `{"name":"Qi"}`})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan tools.ExecutionResult, 1)
				go func() {
					result, err := call.Execute(ctx)
					if err != nil {
						t.Error(err)
					}
					call.Close()
					snapshot.Close()
					done <- result
				}()
				waitMarker(t, directory, "started")
				if mode == "barrier-cancel" {
					cancel()
				}
				waitMarker(t, directory, "cancelled")
				var result tools.ExecutionResult
				if remote {
					// RPC cancellation can return before the Child worker. This observation is
					// deliberately separate from the owner drain and CleanupReport below.
					select {
					case result = <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("RPC cancellation did not return")
					}
				} else {
					select {
					case <-done:
						t.Fatal("local Runtime abandoned its worker")
					default:
					}
				}
				stopped := make(chan error, 1)
				go func() { stopped <- a.instance.Unmount(context.Background()) }()
				waitMarker(t, directory, "stopping")
				if len(a.runtime.Schemas()) != 0 {
					t.Fatal("stopping owner still contributes")
				}
				for _, marker := range []string{"exited", "cleaned"} {
					if _, err := os.Stat(filepath.Join(directory, marker)); !os.IsNotExist(err) {
						t.Fatalf("%s overtook worker: %v", marker, err)
					}
				}
				select {
				case err := <-stopped:
					t.Fatalf("stop overtook worker: %v", err)
				default:
				}
				if remote && (a.plugin.client.Snapshot().Reaped || syscall.Kill(a.plugin.client.Snapshot().PID, 0) != nil) {
					t.Fatal("Child exited before worker release")
				}
				release()
				if !remote {
					select {
					case result = <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("worker stuck")
					}
				}
				want := "cancelled"
				if mode == "barrier-timeout" {
					want = "timeout"
				}
				if !result.IsError || result.Error.Info.Code != want {
					t.Fatalf("result=%+v want=%s", result, want)
				}
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("stop stuck")
				}
				waitMarker(t, directory, "exited")
				waitMarker(t, directory, "cleaned")
				if remote {
					assertReaped(t, a.plugin.client)
				}
			})
		}
	}
}

func TestSamePluginRestartRejectsOldGeneration(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			a := mountLifecycle(t, remote, fixtureConfig("", t.TempDir(), "generation: "))
			if a.err != nil {
				t.Fatal(a.err)
			}
			old, err := a.runtime.Prepare()
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			stale, err := old.Prepare(protocol.ToolCall{ID: "stale", Name: "greet", Arguments: `{"name":"Qi"}`})
			if err != nil {
				t.Fatal(err)
			}
			defer stale.Close()
			generation := toolGeneration(a.host)
			var session *rpc.Session
			if remote {
				session = a.plugin.client
			}
			restarted := make(chan error, 1)
			go func() { restarted <- a.instance.Restart(context.Background()) }()
			awaitLifecycle(t, func() bool { return len(a.runtime.Schemas()) == 0 })
			select {
			case err := <-restarted:
				t.Fatalf("restart overtook old Snapshot: %v", err)
			default:
			}
			if remote && session.Snapshot().Reaped {
				t.Fatal("old Session closed before lease drain")
			}
			old.Close()
			select {
			case err := <-restarted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("restart stuck")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.instance.WaitReady(ctx); err != nil {
				t.Fatal(err)
			}
			if toolGeneration(a.host) <= generation {
				t.Fatal("generation did not advance")
			}
			if _, err := stale.Execute(context.Background()); !errors.Is(err, contracts.ErrClosed) {
				t.Fatalf("old call revived: %v", err)
			}
			result, err := executeGreeting(t, a.runtime, context.Background())
			if err != nil || result.IsError {
				t.Fatal(result, err)
			}
			var value struct {
				PID      int
				Greeting string
			}
			if err := json.Unmarshal(result.Value, &value); err != nil || value.Greeting != "generation: Qi" {
				t.Fatal(string(result.Value), err)
			}
			if remote {
				assertReaped(t, session)
				if value.PID == session.Snapshot().PID || value.PID != a.plugin.client.Snapshot().PID {
					t.Fatal("restart reused old process")
				}
				client, err := NewClient(session)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = client.Call(context.Background(), request()); err == nil {
					t.Fatal("old Session reached new generation")
				}
			}
			if err := a.instance.Unmount(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(a.runtime.Schemas()) != 0 {
				t.Fatal("restarted contribution survived stop")
			}
			if remote {
				assertReaped(t, a.plugin.client)
			}
		})
	}
}

func toolGeneration(h *gordis.Host) uint64 {
	for _, s := range h.Snapshot() {
		if s.LocalID == "tool" {
			return s.Generation
		}
	}
	return 0
}

func TestSamePluginActivationRollbackAndRecovery(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "child"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			a := mountLifecycle(t, remote, fixtureConfig("activate-after-register", directory, "failed: "))
			if a.err == nil || len(a.runtime.Schemas()) != 0 {
				t.Fatalf("failed activation published tools: %v", a.err)
			}
			waitMarker(t, directory, "cleaned")
			if remote {
				d := a.plugin.client.Snapshot()
				if !d.Reaped || !errors.Is(syscall.Kill(d.PID, 0), syscall.ESRCH) {
					t.Fatal("failed activation leaked Child", d)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			next, err := a.host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "recovered", Plugin: fixture.ID, Config: fixtureConfig("", "", "recovered: ")})
			if err != nil {
				t.Fatal(err)
			}
			result, err := executeGreeting(t, a.runtime, ctx)
			var value struct{ Greeting string }
			if err != nil || result.IsError || json.Unmarshal(result.Value, &value) != nil || value.Greeting != "recovered: Qi" {
				t.Fatal(string(result.Value), err)
			}
			// Capture the active Session before another Gordis change validates
			// prototypes and invokes New for other (including failed) instances.
			var recovered *rpc.Session
			if remote {
				recovered = a.plugin.client
				if recovered == nil {
					t.Fatal("missing recovered Session")
				}
			}
			if err := next.Unmount(ctx); err != nil {
				t.Fatal(err)
			}
			if len(a.runtime.Schemas()) != 0 {
				t.Fatal("recovered contribution leaked")
			}
			if remote {
				assertReaped(t, recovered)
			}
		})
	}
}

func TestSamePluginFailurePolicyParity(t *testing.T) {
	for _, tc := range []struct {
		mode, arguments, code string
		abort                 bool
	}{
		{"error", `{"name":"Qi"}`, "execution", false},
		{"abort-error", `{"name":"Qi"}`, "execution", true},
		{"panic", `{"name":"Qi"}`, "panic", false},
		{"wrong-output", `{"name":"Qi"}`, "invalid_result", false},
		{"invalid-json", `{"name":"Qi"}`, "invalid_result", false},
		{"", `{"name":""}`, "invalid_arguments", false},
	} {
		for _, remote := range []bool{false, true} {
			name := "local/"
			if remote {
				name = "child/"
			}
			t.Run(name+tc.mode+"/"+tc.code, func(t *testing.T) {
				a := mountLifecycle(t, remote, fixtureConfig(tc.mode, "", ""))
				if a.err != nil {
					t.Fatal(a.err)
				}
				snapshot, err := a.runtime.Prepare()
				if err != nil {
					t.Fatal(err)
				}
				call, err := snapshot.Prepare(protocol.ToolCall{ID: "failure", Name: "greet", Arguments: tc.arguments})
				if err != nil {
					t.Fatal(err)
				}
				result, executeErr := call.Execute(context.Background())
				call.Close()
				snapshot.Close()
				if !result.IsError || result.Error.Info.Code != tc.code || (executeErr != nil) != tc.abort {
					t.Fatalf("result=%+v err=%v", result, executeErr)
				}
				if tc.abort {
					var e *tools.ExecutionError
					if !errors.As(executeErr, &e) {
						t.Fatal(executeErr)
					}
				}
				if tc.mode == "error" || tc.mode == "abort-error" {
					if result.Error.Message != "business failed" {
						t.Fatal(result.Error.Message)
					}
				}
				if err := a.instance.Unmount(context.Background()); err != nil {
					t.Fatal(err)
				}
				if remote {
					assertReaped(t, a.plugin.client)
				}
			})
		}
	}
}
