package jsonl_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/builtin/session/jsonl"
	jsonlplugin "github.com/RussellLuo/qi/builtin/session/jsonl/plugin"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	runtimeplugin "github.com/RussellLuo/qi/kernel/core/session/plugin"
)

// Only the Session runtime, persistence plugin and observing consumer are
// needed to verify HeaderCheck wiring and lazy history admission.
func startSessionRuntime(t *testing.T, directory string, check func(protocol.SessionHeader) error) (*gordis.Host, session.Store) {
	t.Helper()
	var scope *gordis.Scope
	var store session.Store
	host, err := gordis.NewHost(&events.Plugin{}, &runtimeplugin.Plugin{HeaderCheck: check}, &jsonlplugin.Plugin{}, &lookupProbe{&scope, &store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	raw, err := json.Marshal(map[string]string{"directory": directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []gordis.InstanceSpec{
		{ID: "events", Plugin: "gordis/events"},
		{ID: "session", Plugin: runtimeplugin.PluginID},
		{ID: "backend", Plugin: jsonlplugin.PluginID, Config: raw},
		{ID: "probe", Plugin: "test.lookup-probe"},
	} {
		if _, err := host.Env().MountReady(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	return host, store
}

func TestRuntimeHeaderCheckRejectsBeforeTailRepairAndStartupIsLazy(t *testing.T) {
	directory := t.TempDir()
	owner, err := jsonl.NewOwner(directory)
	if err != nil {
		t.Fatal(err)
	}
	seed := session.NewRuntime(session.RuntimeOptions{ResolvePersistence: func(ctx context.Context) (session.PersistenceLease, bool, error) {
		lease, err := owner.Acquire(ctx)
		return lease, err == nil, err
	}})
	current, err := seed.Create(context.Background(), session.CreateRequest{ID: "reject"})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, base64.RawURLEncoding.EncodeToString([]byte("reject"))+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"uncommitted":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(directory, base64.RawURLEncoding.EncodeToString([]byte("bad"))+".jsonl")
	if err := os.WriteFile(bad, []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("entry rejected header")
	calls := 0
	host, store := startSessionRuntime(t, directory, func(h protocol.SessionHeader) error {
		calls++
		if h.ID != "reject" {
			t.Errorf("wrong header %s", h.ID)
		}
		return rejected
	})
	if calls != 0 {
		t.Fatal("startup opened history")
	}
	if _, err := store.Get(context.Background(), "reject"); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("Get loaded history", err)
	}
	if _, err := store.(session.StoreOpener).Open(context.Background(), "reject"); !errors.Is(err, rejected) || calls != 1 {
		t.Fatal("Runtime lost HeaderCheck", err, calls)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejection modified history", err)
	}
	if _, err := store.(session.StoreOpener).Open(context.Background(), "bad"); !errors.Is(err, jsonl.ErrCorruptLog) {
		t.Fatal("corruption was not deferred to Open", err)
	}
	if calls != 1 {
		t.Fatal("corrupt history reached entry admission")
	}
	if err := host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Removing entry admission permits physical repair only when this ID opens.
	_, store = startSessionRuntime(t, directory, nil)
	current, err = store.(session.StoreOpener).Open(context.Background(), "reject")
	if err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(after), "uncommitted") {
		t.Fatal("Open did not prepare tail", err)
	}
	if err := store.(session.ResourceStore).Release(context.Background(), current); err != nil {
		t.Fatal(err)
	}
}
