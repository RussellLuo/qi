package plugin_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	promptruntime "github.com/RussellLuo/qi/kernel/core/prompt/plugin"
)

func TestPromptContributionLeaseCleanupAndRemount(t *testing.T) {
	host, runtime, instance := mountPromptContributor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prepared, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()

	done := make(chan error, 1)
	go func() {
		done <- instance.Unmount(ctx)
	}()

	// A prepared prompt leases the original plugin generation until Close.
	select {
	case err := <-done:
		t.Fatalf("owner cleanup overtook prepared prompt: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	snapshot, err := prepared.Assemble(ctx, prompt.Context{})
	if err != nil || snapshot.Text != "test section" {
		t.Fatalf("leased prompt = %+v, error = %v", snapshot, err)
	}
	prepared.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	empty, err := runtime.Assemble(ctx, prompt.Context{})
	if err != nil || empty.Text != "" || len(empty.SectionIDs) != 0 {
		t.Fatalf("prompt after owner cleanup = %+v, error = %v", empty, err)
	}

	if _, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "contributor", Plugin: contributorID}); err != nil {
		t.Fatal(err)
	}
	replacement, err := runtime.Assemble(ctx, prompt.Context{})
	if err != nil || replacement.Text != snapshot.Text || !reflect.DeepEqual(replacement.SectionIDs, snapshot.SectionIDs) {
		t.Fatalf("replacement prompt = %+v, error = %v", replacement, err)
	}
}

func mountPromptContributor(t *testing.T) (*gordis.Host, *prompt.Runtime, *gordis.Instance) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ready := make(chan *prompt.Runtime, 1)
	host, err := gordis.NewHost(&promptruntime.Plugin{}, &promptContributor{}, &promptProbe{ready: ready})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})

	for _, spec := range []gordis.InstanceSpec{
		{ID: "runtime", Plugin: "qi/prompt"},
		{ID: "probe", Plugin: "test.prompt-probe"},
	} {
		if _, err := host.Env().MountReady(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}

	instance, err := host.Env().MountReady(ctx, gordis.InstanceSpec{ID: "contributor", Plugin: contributorID})
	if err != nil {
		t.Fatal(err)
	}

	return host, <-ready, instance
}

type promptProbe struct {
	ready chan<- *prompt.Runtime
}

func (p *promptProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       "test.prompt-probe",
		Requires: []gordis.ServiceSpec{prompt.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return &promptProbe{ready: p.ready} },
	}
}

func (p *promptProbe) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(prompt.RuntimeKey)
	if err != nil {
		return err
	}

	p.ready <- runtime
	return nil
}

const contributorID = "test.prompt-contributor"

type promptContributor struct{}

func (*promptContributor) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: contributorID, Requires: []gordis.ServiceSpec{prompt.RuntimeKey.Spec()}, New: func() gordis.Plugin { return new(promptContributor) }}
}
func (*promptContributor) Activate(_ context.Context, scope *gordis.Scope) error {
	runtime, err := scope.Get(prompt.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).Register(prompt.Section{ID: "test", Render: func(context.Context, prompt.Context) (string, error) { return "test section", nil }})
	return err
}
