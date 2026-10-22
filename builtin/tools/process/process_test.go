package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type hostRuntime struct{ r *tools.Runtime }

func (p *hostRuntime) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "runtime", New: func() gordis.Plugin { return p }, Provides: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}}
}
func (p *hostRuntime) Activate(_ context.Context, s *gordis.Scope) error {
	return s.Provide(tools.RuntimeKey, p.r)
}
func TestProcessRuntime(t *testing.T) {
	t.Setenv("QI_PT02_AMBIENT", "must-not-inherit")
	binary := filepath.Join(t.TempDir(), "plugin")
	cmd := exec.Command("go", "build", "-race", "-o", binary, "./testdata/plugin")
	cmd.Env = os.Environ()
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build %v %s", e, out)
	}
	for _, mode := range []string{"normal", "batch", "validate", "activate", "schema", "deferred", "large-timeout", "fractional-timeout", "negative-timeout", "middleware", "meta", "contexts", "concludes", "content", "multiple", "invalid-json", "wrong-output", "oversize", "error", "abort-error", "panic", "cancelled", "timeout", "waitstop", "cleanup-error", "background", "empty-environment"} {
		t.Run("candidate-"+mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			raw, _ := json.Marshal(map[string]string{"mode": mode, "directory": dir, "prefix": "Hello "})
			a, e := newFixtureLoader(t, fixtureOptions{Command: binary, Args: []string{"--pending=1"}, Limits: Limits{MaxPending: 1}, ExitGrace: 2 * time.Second})
			if e != nil {
				t.Fatal(e)
			}
			prototype := a.(*fixtureLoader)
			factory := prototype.spec.New
			var active *fixtureLoader
			prototype.spec.New = func() gordis.Plugin { active = factory().(*fixtureLoader); return active }
			r := tools.NewRuntime()
			defer r.Close()
			h, e := gordis.NewHost(&hostRuntime{r}, a)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				_ = h.Shutdown(c)
			})
			if _, e = h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "runtime", Plugin: "runtime"}); e != nil {
				t.Fatal(e)
			}
			instance, e := h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "remote", Plugin: fixture.ID, Config: raw})
			rejected := mode == "validate" || mode == "activate" || mode == "schema" || mode == "middleware"
			l := active.client
			if rejected {
				if e == nil || len(r.Schemas()) != 0 {
					t.Fatalf("partial activation %v", e)
				}
				_ = h.Shutdown(ctx)
			} else {
				if e != nil {
					t.Fatal(e)
				}
				snapshot, e := r.Prepare()
				if e != nil {
					t.Fatal(e)
				}
				if mode == "batch" && len(snapshot.Descriptions()) != 2 {
					t.Fatal("discovery")
				}
				call, e := snapshot.Prepare(protocol.ToolCall{ID: "test/call", Name: "greet", Arguments: `{"name":"Qi"}`})
				if e != nil {
					t.Fatal(e)
				}
				requestCtx, stop := context.WithCancel(ctx)
				finished := make(chan tools.ExecutionResult, 1)
				callErrors := make(chan error, 1)
				go func() { v, e := call.Execute(requestCtx); callErrors <- e; finished <- v }()
				await := func(name string) {
					t.Helper()
					ticker := time.NewTicker(time.Millisecond)
					defer ticker.Stop()
					for {
						if _, e := os.Stat(filepath.Join(dir, name)); e == nil {
							return
						}
						select {
						case <-ticker.C:
						case <-ctx.Done():
							t.Fatalf("missing %s", name)
						}
					}
				}
				if mode == "waitstop" {
					await("started")
					stop()
					await("cancelled")
					// Caller cancellation and real child completion are different observations.
					result := <-finished
					if !result.IsError {
						t.Fatal("cancel succeeded")
					}
					<-callErrors
					call.Close()
					snapshot.Close()
					client, e := NewClient(l)
					if e != nil {
						t.Fatal(e)
					}
					_, e = client.Call(ctx, CallRequest{Tool: "greet", Arguments: json.RawMessage(`{"name":"Qi"}`), ToolCallID: "capacity"})
					var capacity *CallError
					if !errors.As(e, &capacity) || capacity.Code != CodeBackpressure {
						t.Fatalf("live worker capacity: %v", e)
					}
					if e = instance.Unmount(ctx); e != nil {
						t.Fatal(e)
					}
					await("exited")
				} else {
					result := <-finished
					executeErr := <-callErrors
					stop()
					call.Close()
					snapshot.Close()
					expected := ""
					switch mode {
					case "invalid-json", "wrong-output":
						expected = "invalid_result"
					case "oversize":
						expected = CodeHostValidation
					case "fractional-timeout":
						expected = "ABORTED_BEFORE_DISPATCH"
					case "error", "abort-error":
						expected = "execution"
					case "panic":
						expected = CodePanic
					case "cancelled":
						expected = "cancelled"
					case "timeout":
						expected = "timeout"
					}
					if expected != "" {
						if !result.IsError || result.Error.Info.Code != expected {
							t.Fatalf("%s result=%+v err=%v", mode, result, executeErr)
						}
						if mode == "abort-error" && executeErr == nil {
							t.Fatal("FailureAbort lost")
						}
					} else if mode != "background" && (result.IsError || !json.Valid(result.Value)) {
						t.Fatalf("success %+v %v", result, executeErr)
					}
					if mode == "background" {
						ticker := time.NewTicker(time.Millisecond)
						defer ticker.Stop()
						for {
							found := false
							for _, n := range h.Snapshot() {
								if n.QualifiedID == "remote" && n.Failure != "" {
									found = true
								}
							}
							if found {
								break
							}
							select {
							case <-ticker.C:
							case <-ctx.Done():
								t.Fatal("background failure lost")
							}
						}
					}
					_ = instance.Unmount(ctx)
				}
				_ = h.Shutdown(ctx)
				if len(r.Schemas()) != 0 {
					t.Fatal("proxy remains")
				}
			}
			if l == nil || !l.Snapshot().Reaped || syscall.Kill(l.Snapshot().PID, 0) == nil {
				t.Fatalf("not reaped: %v", l)
			}
			if !rejected && mode != "cleanup-error" && (l.Snapshot().Cleanup == nil || !l.Snapshot().Cleanup.CleanupComplete) {
				t.Fatalf("cleanup %v", l.Snapshot().Cleanup)
			}
			if mode == "cleanup-error" && (l.Snapshot().Cleanup == nil || l.Snapshot().Cleanup.CleanupComplete) {
				t.Fatal("cleanup failure hidden")
			}
			report, _ := json.Marshal(map[string]any{"mode": mode, "pid": l.Snapshot().PID, "pid_absent": true, "reaped": l.Snapshot().Reaped, "cleanup": l.Snapshot().Cleanup, "proxy_revoked": len(r.Schemas()) == 0, "initialization_rejected": rejected})
			t.Logf("P04_RESULT %s", report)
		})
	}
	testComposition(t, binary)
}
