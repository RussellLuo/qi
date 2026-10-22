package process

import (
	"context"
	"encoding/json"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func testComposition(t *testing.T, binary string) {
	for _, name := range []string{"isolation", "conflict", "identity", "crash"} {
		t.Run("candidate-"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			o := fixtureOptions{Command: binary}
			if name == "identity" {
				o.ID = "custom.tool"
				o.Version = "v2"
				o.Args = []string{"--package=custom.tool", "--version=v2"}
			}
			a, e := newFixtureLoader(t, o)
			if e != nil {
				t.Fatal(e)
			}
			p := a.(*fixtureLoader)
			factory := p.spec.New
			var active []*fixtureLoader
			p.spec.New = func() gordis.Plugin { v := factory().(*fixtureLoader); active = append(active, v); return v }
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
			config := []byte(`{"prefix":"first"}`)
			if name == "conflict" { // A duplicate tool forces atomic batch registration rollback.
				config = []byte(`{"prefix":"second","mode":"batch"}`)
			}
			first, e := h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "first", Plugin: fixture.ID, Config: []byte(`{"prefix":"first"}`)})
			if e != nil {
				t.Fatal(e)
			}
			if name == "isolation" || name == "conflict" {
				// Runtime name conflict is global: a second independent Runtime/Host demonstrates instance isolation.
				if name == "conflict" {
					_, e = h.Env().MountReady(ctx, gordis.InstanceSpec{ID: "second", Plugin: fixture.ID, Config: config})
					if e == nil || len(r.Schemas()) != 1 {
						t.Fatalf("partial batch %v", e)
					}
				} else {
					b, e := newFixtureLoader(t, fixtureOptions{Command: binary})
					if e != nil {
						t.Fatal(e)
					}
					bp := b.(*fixtureLoader)
					bf := bp.spec.New
					var bi *fixtureLoader
					bp.spec.New = func() gordis.Plugin { bi = bf().(*fixtureLoader); return bi }
					r2 := tools.NewRuntime()
					defer r2.Close()
					h2, e := gordis.NewHost(&hostRuntime{r2}, b)
					if e != nil {
						t.Fatal(e)
					}
					defer h2.Shutdown(context.Background())
					for _, i := range []gordis.InstanceSpec{{ID: "runtime", Plugin: "runtime"}, {ID: "second", Plugin: fixture.ID, Config: []byte(`{"prefix":"second"}`)}} {
						if _, e = h2.Env().MountReady(ctx, i); e != nil {
							t.Fatal(e)
						}
					}
					var running []*fixtureLoader
					for _, p := range active {
						if p.client != nil {
							running = append(running, p)
						}
					}
					if len(running) != 1 {
						t.Fatalf("active instances %d", len(running))
					}
					c1, e := NewClient(running[0].client)
					if e != nil {
						t.Fatal(e)
					}
					c2, e := NewClient(bi.client)
					if e != nil {
						t.Fatal(e)
					}
					invoke := func(c *Client) map[string]any {
						v, e := c.Call(ctx, CallRequest{Tool: "greet", Arguments: json.RawMessage(`{"name":"Qi"}`), ToolCallID: "independent"})
						if e != nil {
							t.Fatal(e)
						}
						var out map[string]any
						if e = json.Unmarshal(v.Value, &out); e != nil {
							t.Fatal(e)
						}
						return out
					}
					if invoke(c1)["greeting"] != "firstQi" || invoke(c2)["greeting"] != "secondQi" {
						t.Fatal("configuration mixed")
					}
					if e = first.Unmount(ctx); e != nil {
						t.Fatal(e)
					}
					if invoke(c2)["greeting"] != "secondQi" {
						t.Fatal("second stopped")
					}
					_ = h2.Shutdown(ctx)
					if !bi.client.Snapshot().Reaped {
						t.Fatal("second remains")
					}
				}
			}
			var running []*fixtureLoader
			for _, p := range active {
				if p.client != nil {
					running = append(running, p)
				}
			}
			if name == "crash" {
				p, _ := os.FindProcess(running[0].client.Snapshot().PID)
				if e = p.Kill(); e != nil {
					t.Fatal(e)
				}
				_ = first.Unmount(ctx)
				if running[0].client.Snapshot().Cleanup == nil || running[0].client.Snapshot().Cleanup.CleanupComplete {
					t.Fatal("crash claimed business cleanup")
				}
				client, _ := NewClient(running[0].client)
				_, e = client.Call(ctx, CallRequest{Tool: "greet", Arguments: json.RawMessage(`{"name":"Qi"}`), ToolCallID: "crash"})
				if ce, ok := e.(*CallError); !ok || ce.Code != CodeProcessExit {
					t.Fatalf("crash category %v", e)
				}
			}
			_ = h.Shutdown(ctx)
			for _, p := range running {
				if !p.client.Snapshot().Reaped || syscall.Kill(p.client.Snapshot().PID, 0) == nil {
					t.Fatal("PID remains")
				}
			}
			report, _ := json.Marshal(map[string]any{"mode": name, "instances": len(running), "pid_absent": true, "production_boundary": "candidate API"})
			t.Logf("P04_RESULT %s", report)
		})
	}
}
