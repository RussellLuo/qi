package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func sealedExport(t *testing.T, invoke ExecuteFunc) (*Export, *testOwner, func()) {
	t.Helper()
	e := NewExport()
	o := newTestOwner("business", 1)
	d := Definition{Name: "echo", Timeout: -time.Nanosecond, Execute: invoke}
	dispose, err := e.Runtime().Register(o, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e, o, dispose
}
func leases(o *testOwner) int { o.mu.Lock(); defer o.mu.Unlock(); return o.active }
func awaitExport(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier timed out")
	}
}
func rawExecution() Execution {
	return Execution{Call: protocol.ToolCall{ID: "call", Name: "echo", Arguments: ` { "原始": 1 } `}, Arguments: json.RawMessage(`{"changed":true}`)}
}

func TestExportMetadataAndRawPolicy(t *testing.T) {
	e := NewExport()
	defer e.Close()
	o := newTestOwner("business", 7)
	schema := JSONSchema(`{"type":"object", "properties":{"n":{"const":9007199254740993}}}`)
	native := errors.New("native business error")
	input := rawExecution()
	var got Execution
	d := Definition{Name: "echo", Parameters: schema, FailureMode: ToolFailureAbort, Timeout: time.Nanosecond, DeferLoading: true, Execute: func(_ context.Context, in Execution) (ExecutionResult, error) {
		got = in
		return ExecutionResult{Value: json.RawMessage(`"unvalidated"`)}, native
	}}
	if _, err := e.Runtime().Register(o, d); err != nil {
		t.Fatal(err)
	}
	schema[0] = '!'
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	if leases(o) != 0 {
		t.Fatal("idle export holds lease")
	}
	ds, err := e.Describe(context.Background())
	if err != nil || len(ds) != 1 {
		t.Fatal(ds, err)
	}
	if string(ds[0].Output) != `{}` || ds[0].Timeout != time.Nanosecond || !ds[0].DeferLoading || ds[0].FailureMode != ToolFailureAbort {
		t.Fatal(ds)
	}
	ds[0].Parameters[0] = '!'
	again, _ := e.Describe(context.Background())
	if again[0].Parameters[0] != '{' {
		t.Fatal("schema alias")
	}
	result, err := e.Execute(context.Background(), input)
	if !errors.Is(err, native) || string(result.Value) != `"unvalidated"` || !reflect.DeepEqual(got, input) {
		t.Fatalf("raw policy/context changed: %+v %v %+v", result, err, got)
	}
	got.Arguments[0] = '!'
	if input.Arguments[0] != '{' {
		t.Fatal("raw input alias")
	}
	if _, err := e.Execute(context.Background(), Execution{Call: protocol.ToolCall{Name: "missing"}}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestExportUnsupportedOperations(t *testing.T) {
	for _, phase := range []string{"collect", "ready"} {
		for _, op := range []string{"middleware", "prepare", "prepare-selected", "schemas", "runtime-close", "register", "disposer"} {
			if phase == "collect" && (op == "register" || op == "disposer") {
				continue
			}
			t.Run(phase+"/"+op, func(t *testing.T) {
				e := NewExport()
				defer e.Close()
				r := e.Runtime()
				o := newTestOwner("business", 3)
				d := Definition{Name: "echo", Execute: func(context.Context, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil }}
				dispose, err := r.Register(o, d)
				if err != nil {
					t.Fatal(err)
				}
				if phase == "ready" {
					if err := e.Seal(); err != nil {
						t.Fatal(err)
					}
				}
				switch op {
				case "middleware":
					_, _ = r.RegisterMiddleware(o, Middleware{})
				case "prepare":
					_, _ = r.Prepare()
				case "prepare-selected":
					_, _ = r.PrepareSelected(nil)
				case "schemas":
					r.Schemas()
				case "runtime-close":
					r.Close()
				case "register":
					_, _ = r.RegisterBatch(o, nil)
				case "disposer":
					dispose()
				}
				awaitExport(t, e.Done())
				if err := e.Err(); err == nil || !strings.Contains(err.Error(), "phase="+phase) || !strings.Contains(err.Error(), "operation="+op) {
					t.Fatal(err)
				}
				if _, err := e.Execute(context.Background(), rawExecution()); err == nil {
					t.Fatal("retired closure called")
				}
			})
		}
	}
}

func TestExportDrainCancelPanicAndConcurrentClose(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "return", true: "panic"}[panics], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			e, o, _ := sealedExport(t, func(context.Context, Execution) (ExecutionResult, error) {
				close(entered)
				<-release
				if panics {
					panic("business panic")
				}
				return ExecutionResult{}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				defer func() {
					if got := recover(); (got != nil) != panics {
						t.Errorf("panic=%v", got)
					}
				}()
				_, _ = e.Execute(ctx, rawExecution())
			}()
			awaitExport(t, entered)
			cancel()
			closed := make(chan struct{})
			closedAgain := make(chan struct{})
			go func() { e.Close(); close(closed) }()
			awaitExport(t, e.Done())
			go func() { e.Close(); close(closedAgain) }()
			if leases(o) != 1 {
				t.Fatal("cancel released actual worker")
			}
			for _, ch := range []<-chan struct{}{finished, closed, closedAgain} {
				select {
				case <-ch:
					t.Fatal("drain overtook worker")
				default:
				}
			}
			once.Do(func() { close(release) })
			awaitExport(t, finished)
			awaitExport(t, closed)
			awaitExport(t, closedAgain)
			if leases(o) != 0 {
				t.Fatal("worker leaked lease")
			}
		})
	}
}

func TestExportRegistrationFailureCannotSeal(t *testing.T) {
	for _, tc := range []string{"schema", "duplicate", "conflict", "owner"} {
		t.Run(tc, func(t *testing.T) {
			e := NewExport()
			defer e.Close()
			o := newTestOwner("business", 1)
			d := Definition{Name: "echo", Execute: func(context.Context, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil }}
			ds := []Definition{d, {Name: "other", Execute: d.Execute}}
			switch tc {
			case "schema":
				ds[1].Parameters = JSONSchema(`{"type":42}`)
			case "duplicate":
				ds[1].Name = d.Name
			case "conflict":
				if _, err := e.Runtime().Register(o, d); err != nil {
					t.Fatal(err)
				}
			case "owner":
				o.stopping = true
			}
			if _, err := e.Runtime().RegisterBatch(o, ds); err == nil {
				t.Fatal("invalid registration succeeded")
			}
			awaitExport(t, e.Done())
			// Inspect the registry internally: business observation is intentionally denied.
			s, err := e.Runtime().contributions.Prepare()
			if tc == "owner" && err != nil {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := 0
			if tc == "conflict" {
				want = 1
			}
			if len(s.Items()) != want {
				t.Fatal("partial batch")
			}
			if err := e.Seal(); err == nil {
				t.Fatal("registration failure was ignored")
			}
		})
	}
}

func TestExportOwnerAdmissionAndGeneration(t *testing.T) {
	e, o, _ := sealedExport(t, func(context.Context, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil })
	o.mu.Lock()
	o.stopping = true
	o.mu.Unlock()
	if _, err := e.Execute(context.Background(), rawExecution()); !errors.Is(err, contracts.ErrOwnerUnavailable) {
		t.Fatal(err)
	}
	if err := o.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitExport(t, e.Done())
	next := NewExport()
	defer next.Close()
	o2 := newTestOwner("business", 2)
	_, err := next.Runtime().Register(o2, Definition{Name: "echo", Execute: func(context.Context, Execution) (ExecutionResult, error) {
		return ExecutionResult{Value: json.RawMessage(`2`)}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), rawExecution()); err == nil {
		t.Fatal("old export revived")
	}
	if result, err := next.Execute(context.Background(), rawExecution()); err != nil || string(result.Value) != `2` {
		t.Fatal(result, err)
	}
	if leases(o) != 0 || leases(o2) != 0 {
		t.Fatal("owner lease leaked")
	}
}

func TestExportOwnerFreezeRace(t *testing.T) {
	for n := 0; n < 100; n++ {
		e, o, _ := sealedExport(t, func(context.Context, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil })
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, _ = e.Execute(context.Background(), rawExecution()) }()
		go func() { defer wg.Done(); <-start; _ = o.Stop(context.Background()) }()
		close(start)
		wg.Wait()
		e.Close()
		if leases(o) != 0 {
			t.Fatal("freeze leaked lease")
		}
	}
}

func TestExportSealUsesActualCompiledCatalog(t *testing.T) {
	e := NewExport()
	defer e.Close()
	if _, err := e.Describe(context.Background()); err == nil {
		t.Fatal("unsealed export describes")
	}
	if err := e.Seal(); err == nil {
		t.Fatal("empty export sealed")
	}
	if _, err := e.Execute(context.Background(), rawExecution()); err == nil {
		t.Fatal("failed export executes")
	}
	e2, _, _ := sealedExport(t, func(context.Context, Execution) (ExecutionResult, error) { return ExecutionResult{}, nil })
	if err := e2.Seal(); err != nil {
		t.Fatal(err)
	}
	ds, err := e2.Describe(context.Background())
	if err != nil || len(ds) != 1 || string(ds[0].Parameters) != `{}` || ds[0].FailureMode != ToolFailureResult {
		t.Fatal(ds, err)
	}
}

func TestExportDisposedRegistrationCannotRetireReplacement(t *testing.T) {
	e := NewExport()
	defer e.Close()
	o := newTestOwner("business", 1)
	d := echoDefinition("first")
	old, err := e.Runtime().Register(o, d)
	if err != nil {
		t.Fatal(err)
	}
	old()
	current, err := e.Runtime().Register(o, echoDefinition("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Seal(); err != nil {
		t.Fatal(err)
	}
	old()
	if e.Err() != nil {
		t.Fatal("idempotent old disposer retired replacement", e.Err())
	}
	current()
	awaitExport(t, e.Done())
	if _, err := e.Execute(context.Background(), rawExecution()); err == nil {
		t.Fatal("withdrawn export still executes")
	}
}
