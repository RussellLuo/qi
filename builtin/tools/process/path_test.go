package process

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPathHookPreservesBusinessFieldsAndDiscoversIdentity(t *testing.T) {
	r := Extension().Registrations[0]
	business := map[string]any{"command": "business command", "loader": "business loader", "identity": map[string]any{"id": "business identity"}}
	wrapped, err := r.ResolvePath(childExecutable, business)
	if err != nil {
		t.Fatal(err)
	}
	business["identity"].(map[string]any)["id"] = "changed"
	raw, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"command": "business command", "loader": "business loader", "identity": map[string]any{"id": "business identity"}}
	if !reflect.DeepEqual(envelope.Config, want) {
		t.Fatal("hook changed or aliased business fields", envelope.Config)
	}
	plugin := r.Plugin.Spec().New().(*loaderPlugin)
	if err := json.Unmarshal(raw, plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.identity.ID != "" || plugin.command != childExecutable {
		t.Fatal("unexpected expected identity", plugin.identity)
	}
	var received map[string]any
	if err := json.Unmarshal(plugin.config, &received); err != nil || !reflect.DeepEqual(received, want) {
		t.Fatal(received, err)
	}
	empty, err := r.ResolvePath(childExecutable, nil)
	if err != nil {
		t.Fatal(empty, err)
	}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, plugin); err != nil || string(plugin.config) != `{}` {
		t.Fatal("nil business config must remain an object", string(plugin.config), err)
	}
	if _, err := r.ResolvePath(childExecutable, map[string]any{"invalid": func() {}}); err == nil {
		t.Fatal("accepted non-JSON business config")
	}
}
