package jsonl_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/builtin/session/jsonl"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	"github.com/RussellLuo/qi/kernel/core/session"
	"gopkg.in/yaml.v3"
)

type admissionProbe struct{ checked *atomic.Bool }

func (p *admissionProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/admission", Requires: []gordis.ServiceSpec{session.StoreKey.Spec()}, New: func() gordis.Plugin { return p }}
}
func (p *admissionProbe) Activate(_ context.Context, s *gordis.Scope) error {
	store, err := s.Get(session.StoreKey)
	if err != nil {
		return err
	}
	return s.OnStop("verify no early Session", func(ctx context.Context) error {
		p.checked.Store(true)
		if _, err := store.Get(ctx, "boot-test-session"); !errors.Is(err, session.ErrNotFound) {
			return errors.New("business created Session before Ready")
		}
		return nil
	})
}

type admissionModel struct{}

func (*admissionModel) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/model", New: func() gordis.Plugin { return new(admissionModel) }}
}
func (*admissionModel) DescribeModels() (modelcatalog.Description, error) {
	return modelcatalog.Provider("test", "test", nil, []modelcatalog.Entry{{ID: "test-model", Model: "test"}})
}
func (*admissionModel) Activate(context.Context, *gordis.Scope) error { return nil }

type failedSibling struct{}

func (*failedSibling) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/failure", New: func() gordis.Plugin { return new(failedSibling) }}
}
func (*failedSibling) Activate(context.Context, *gordis.Scope) error {
	return errors.New("backend composition failed")
}
func TestPersistenceStartupFailureNeverAdmitsBusiness(t *testing.T) {
	for _, kind := range []string{"invalid-directory", "locked-directory", "activation-failure"} {
		t.Run(kind, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "sessions")
			var owner *jsonl.Owner
			if kind == "invalid-directory" {
				if err := os.WriteFile(directory, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "locked-directory" {
				var err error
				owner, err = jsonl.NewOwner(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
			}
			var checked atomic.Bool
			ext := bundle.Extension()
			ext.Registrations = append(ext.Registrations, registration.PluginRegistration{Plugin: new(admissionModel), ProfileVisible: true})
			ext.Registrations = append(ext.Registrations, registration.PluginRegistration{Plugin: &admissionProbe{&checked}, ProfileVisible: true})
			p := profile.Profile{Instances: ext.Bundles[0].Instances}
			p.Instances = append(p.Instances, profile.InstanceSpec{ID: "model", Plugin: "test/model"}, profile.InstanceSpec{ID: "probe", Plugin: "test/admission"}, profile.InstanceSpec{ID: "zz-persistence", Plugin: "qi/session/persistence-jsonl", Config: map[string]any{"directory": directory}}, profile.InstanceSpec{ID: "headless", Plugin: "qi/cli/headless"})
			if kind == "activation-failure" {
				ext.Registrations = append(ext.Registrations, registration.PluginRegistration{Plugin: new(failedSibling), ProfileVisible: true})
				p.Instances = append(p.Instances, profile.InstanceSpec{ID: "zzz-failure", Plugin: "test/failure"})
			}
			app, err := application.Compose(ext)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := yaml.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "profile.yaml")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			code := boot.Run([]string{"--profile", path, "--", "hello"}, launch.Input{Stdout: io.Discard, Stderr: &diagnostic}, app)
			if code != 1 || !checked.Load() || diagnostic.Len() == 0 {
				t.Fatal(code, checked.Load(), diagnostic.String())
			}
			logs, _ := filepath.Glob(filepath.Join(directory, "*.jsonl"))
			if len(logs) != 0 {
				t.Fatal("history created before Ready", logs)
			}
			if owner != nil {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "invalid-directory" {
				reopened, err := jsonl.NewOwner(directory)
				if err != nil {
					t.Fatal("failed startup retained lock", err)
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
