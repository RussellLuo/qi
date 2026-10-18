package gordisrpc

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
)

type probeService interface{ Value() string }
type probeValue struct{}

func (*probeValue) Value() string { return "value" }

func TestProcessDeclarationAndIsolation(t *testing.T) {
	key := gordis.NewKey[probeService]("probe/service")
	binding := NewBinding(key, "1", Codec[probeService]{Client: func(Invoker) probeService { return new(probeValue) }, Server: func(probeService) Handler { return Handler{} }})
	valid := ProcessOptions{PluginID: "probe", Command: Command{Path: "/does/not/exist", Args: []string{"single"}, Env: []string{"A=B"}}, Boundary: Boundary{Imports: []Binding{binding}, Exports: []Binding{binding}}}
	for _, name := range []string{"id", "command", "binding", "duplicate", "identity", "mode"} {
		t.Run(name, func(t *testing.T) {
			options := valid
			switch name {
			case "id":
				options.PluginID = " "
			case "command":
				options.Command.Path = ""
			case "binding":
				options.Boundary = Boundary{Exports: []Binding{{}}}
			case "duplicate":
				options.Boundary = Boundary{Exports: []Binding{binding, binding}}
			case "identity":
				options.Identity = Identity{ID: "partial"}
			case "mode":
				options.ConfigMode = 99
			}
			if _, err := NewProcess(options); err == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
	p, err := NewProcess(valid)
	if err != nil {
		t.Fatal(err)
	}
	valid.Command.Args[0] = "changed"
	valid.Command.Env[0] = "changed"
	valid.Boundary.Exports[0] = Binding{}
	valid.Boundary.Imports[0] = Binding{}
	a := p.Spec().New().(*ProcessPlugin)
	b := p.Spec().New().(*ProcessPlugin)
	if a == b || a == p || a.options.Command.Args[0] != "single" || a.options.Command.Env[0] != "A=B" || a.Spec().Provides[0] != key.Spec() || a.Spec().Requires[0] != key.Spec() {
		t.Fatal("mutable options leaked")
	}
	spec := a.Spec()
	spec.Requires[0], spec.Provides[0] = gordis.ServiceSpec{}, gordis.ServiceSpec{}
	if a.Spec().Requires[0] != key.Spec() || a.Spec().Provides[0] != key.Spec() {
		t.Fatal("Spec exposed mutable declaration slices")
	}
	raw := []byte(` { "prefix" : "Welcome" } `)
	if err := json.Unmarshal(raw, a); err != nil {
		t.Fatal(err)
	}
	want := string(a.config)
	raw[3] = '!'
	if string(a.config) != want || len(b.config) != 0 || len(p.config) != 0 || a.session != nil || b.session != nil {
		t.Fatal("instance state leaked")
	}
	if !reflect.DeepEqual(a.Spec().Requires, RequiredServices(a.options.Boundary.Imports)) {
		t.Fatal("requires mismatch")
	}
	for _, raw := range []string{`null`, `[]`, `1`, `"value"`, `{} {}`} {
		if err := a.UnmarshalJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	no, err := NewProcess(ProcessOptions{PluginID: "empty", Command: Command{Path: "/does/not/exist"}, ConfigMode: NoConfig})
	if err != nil {
		t.Fatal(err)
	}
	if err := no.UnmarshalJSON([]byte(` { } `)); err != nil || no.config != nil {
		t.Fatalf("NoConfig empty: %v", err)
	}
	if err := no.UnmarshalJSON([]byte(`{"prefix":"Welcome"}`)); err == nil {
		t.Fatal("NoConfig discarded business fields")
	}
}
