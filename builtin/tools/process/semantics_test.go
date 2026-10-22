package process

import (
	"context"
	"encoding/json"
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

type policyProbe struct{ pre, execute, invoke, post int }

func (p *policyProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/policy", New: func() gordis.Plugin { return p }, Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}}
}
func (p *policyProbe) Activate(_ context.Context, s *gordis.Scope) error {
	r, err := s.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = r.Bind(s).RegisterMiddleware(tools.Middleware{ID: "host", Pre: func(_ context.Context, e tools.Execution) (tools.Execution, error) {
		p.pre++
		e.Arguments = json.RawMessage(` {"name":"pre"} `)
		return e, nil
	}, Execute: func(_ context.Context, plan tools.ExecutePlan) (tools.ExecutePlan, error) {
		p.execute++
		invoke := plan.Invoke
		plan.Execution.Arguments = json.RawMessage(` {"name":"Host改写"} `)
		plan.Invoke = func(ctx context.Context, e tools.Execution) (tools.ExecutionResult, error) {
			p.invoke++
			return invoke(ctx, e)
		}
		return plan, nil
	}, Post: func(_ context.Context, _ tools.Execution, r tools.ExecutionResult) (tools.ExecutionResult, error) {
		p.post++
		return r, nil
	}})
	return err
}
func TestSamePluginLocalAndChildFullSemantics(t *testing.T) {
	var results []tools.ExecutionResult
	var descriptions [][]tools.DefinitionInfo
	input := protocol.ToolCall{ID: "call/语义", Name: "greet", Arguments: ` { "name": "original" } `}
	attribution := tools.ExecutionContext{Identity: tools.ExecutionIdentity{AgentID: "agent", SessionID: "session", Turn: 2, Step: 3}, CWD: t.TempDir()}
	for _, remote := range []bool{false, true} {
		var r *tools.Runtime
		var h *gordis.Host
		var instance *gordis.Instance
		probe := &policyProbe{}
		if remote {
			a := mountLoader(t, loaderOptions(), fixtureConfig("semantic", "", ""), probe)
			if a.err != nil {
				t.Fatal(a.err)
			}
			r, h, instance = a.runtime, a.host, a.instance
		} else {
			r = tools.NewRuntime()
			defer r.Close()
			var err error
			h, err = gordis.NewHost(&hostRuntime{r}, new(fixture.Plugin), probe)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Shutdown(context.Background()) })
			for _, spec := range []gordis.InstanceSpec{{ID: "runtime", Plugin: "runtime"}, {ID: "policy", Plugin: probe.Spec().ID}, {ID: "tool", Plugin: fixture.ID, Config: fixtureConfig("semantic", "", "")}} {
				instance, err = h.Env().MountReady(context.Background(), spec)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		s, err := r.Prepare()
		if err != nil {
			t.Fatal(err)
		}
		descriptions = append(descriptions, s.Descriptions())
		c, err := s.PrepareWithContext(input, attribution)
		if err != nil {
			t.Fatal(err)
		}
		result, err := c.Execute(context.Background())
		c.Close()
		s.Close()
		if err != nil || result.IsError {
			t.Fatal(result, err)
		}
		if *probe != (policyProbe{1, 1, 1, 1}) {
			t.Fatalf("policy counts remote=%v: %+v", remote, probe)
		}
		var got tools.Execution
		if err := json.Unmarshal(result.Value, &got); err != nil {
			t.Fatal(err)
		}
		if got.Identity != attribution.Identity || got.CWD != attribution.CWD || got.Call != input || string(got.Arguments) != `{"name":"Host改写"}` {
			t.Fatalf("execution changed: %+v", got)
		}
		results = append(results, result)
		if err := instance.Unmount(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := h.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(descriptions[0], descriptions[1]) {
		t.Fatalf("description drift: %+v", descriptions)
	}
	if d := descriptions[1][0]; !d.DeferLoading || d.Timeout != -time.Nanosecond {
		t.Fatal(d)
	}
	if !reflect.DeepEqual(results[0], results[1]) {
		t.Fatalf("result drift: local=%+v child=%+v", results[0], results[1])
	}
	if len(results[1].Content) != 4 || len(results[1].AdditionalContexts) != 1 || !results[1].ConcludesTurn || string(results[1].Meta) != ` {"n":900719925474099312345} ` {
		t.Fatal(results[1])
	}
}

func TestLoaderRetiresExportAfterLateRegistration(t *testing.T) {
	directory := t.TempDir()
	a := mountLoader(t, loaderOptions(), fixtureConfig("late-register", directory, ""))
	if a.err != nil {
		t.Fatal(a.err)
	}
	if err := os.WriteFile(filepath.Join(directory, "mutate"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	waitMarker(t, directory, "rejected")
	deadline := time.Now().Add(5 * time.Second)
	for len(a.runtime.Schemas()) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(a.runtime.Schemas()) != 0 {
		t.Fatal("retired export remains visible")
	}
	if err := a.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClean(t, a.plugin.client)
}
