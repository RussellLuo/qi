package bundle_test

import (
	"reflect"
	"testing"

	builtin "github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/builtin/cli/headless"
	openaiplugin "github.com/RussellLuo/qi/builtin/llm/openai/plugin"
	jsonlplugin "github.com/RussellLuo/qi/builtin/session/jsonl/plugin"
	fsplugin "github.com/RussellLuo/qi/builtin/tools/fs/plugin"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	llmplugin "github.com/RussellLuo/qi/kernel/core/llm/plugin"
	promptplugin "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
	memoryplugin "github.com/RussellLuo/qi/kernel/core/session/plugin"
	toolsplugin "github.com/RussellLuo/qi/kernel/core/tools/plugin"
)

func TestBuiltinRegistrationsAndBaseBundle(t *testing.T) {
	ext := builtin.Extension()
	ids := make([]string, 0, len(ext.Registrations))
	for _, reg := range ext.Registrations {
		id := reg.Plugin.Spec().ID
		ids = append(ids, id)
		if !reg.ProfileVisible || reg.Descriptor.Compatibility.HostAPI != profile.HostAPIVersion {
			t.Fatalf("invalid public registration: %s", id)
		}
		if id == "qi/cli/headless" && (reg.Invocation == nil || !reg.Completes || !reg.Descriptor.RequiresModels || reg.ValidateCandidate != nil) {
			t.Fatal("invalid headless invocation contract", reg)
		}
	}
	wantIDs := []string{"gordis/events", "qi/llm", "qi/prompt", "qi/tools", "qi/agent-loop", "qi/session", "qi/session/persistence-jsonl", "qi/cli/headless", "qi/llm/openai-compatible", "qi/tools/fs"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("registrations = %v, want %v", ids, wantIDs)
	}
	app, err := application.Compose(ext)
	if err != nil {
		t.Fatal(err)
	}
	base, digest, ok := app.BundleCatalog.Lookup("base")
	if !ok || digest == "" {
		t.Fatal("missing base Bundle or digest")
	}
	wantInstances := []profile.InstanceSpec{
		{ID: "events", Plugin: "gordis/events"},
		{ID: "session", Plugin: "qi/session"},
		{ID: "llm-runtime", Plugin: "qi/llm"},
		{ID: "prompt-runtime", Plugin: "qi/prompt"},
		{ID: "tools", Plugin: "qi/tools"},
		{ID: "agent", Plugin: "qi/agent-loop"},
	}
	if !reflect.DeepEqual(base.Instances, wantInstances) {
		t.Fatalf("base instances = %#v, want %#v", base.Instances, wantInstances)
	}
	base.Instances[0].Plugin = "mutated"
	fresh, _, _ := app.BundleCatalog.Lookup("base")
	if fresh.Instances[0].Plugin != "gordis/events" {
		t.Fatal("catalog aliases lookup")
	}
	ext.Bundles[0].Instances[0].Plugin = "mutated"
	if builtin.Extension().Bundles[0].Instances[0].Plugin != "gordis/events" {
		t.Fatal("extension aliases later call")
	}
}

func TestCapabilityFactoriesAreDetachedAndRejectDuplicates(t *testing.T) {
	for _, factory := range []func() application.Extension{
		agentplugin.Extension, llmplugin.Extension, promptplugin.Extension, toolsplugin.Extension,
		memoryplugin.Extension, openaiplugin.Extension, jsonlplugin.Extension, headless.Extension,
		fsplugin.Extension,
	} {
		first, second := factory(), factory()
		if len(first.Registrations) != 1 || len(first.Bundles) != 0 {
			t.Fatal("capability factory must describe exactly one type")
		}
		id := first.Registrations[0].Plugin.Spec().ID
		t.Run(id, func(t *testing.T) {
			if first.Registrations[0].Plugin == second.Registrations[0].Plugin {
				// Empty structs may share a Go address; compare their mutable descriptor instead.
				if id == agentplugin.PluginID || id == llmplugin.PluginID || id == openaiplugin.PluginID || id == jsonlplugin.PluginID || id == fsplugin.PluginID {
					t.Fatal("factory reused a configurable prototype")
				}
			}
			first.Registrations[0].Descriptor.ConfigSchema[0] = '!'
			if second.Registrations[0].Descriptor.ConfigSchema[0] != '{' {
				t.Fatal("factory shared mutable metadata")
			}
			if _, err := application.Compose(factory(), factory()); err == nil {
				t.Fatal("duplicate capability accepted")
			}
			if _, err := application.Compose(builtin.Extension(), factory()); err == nil {
				t.Fatal("duplicate builtin capability accepted")
			}
		})
	}
}
