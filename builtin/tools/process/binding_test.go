package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/bindings"
)

func TestGeneratedProviderRequestBoundary(t *testing.T) {
	handler := bindings.ProviderCodec().Server(fakePackage{response: CallResponse{Result: json.RawMessage(`null`)}})
	for _, raw := range []string{
		`{"args":[{"Tool":"greet","arguments":{},"toolCallId":"call"}]}`,
		`{"args":[{"tool":"greet","tool":"other","arguments":{},"toolCallId":"call"}]}`,
		`{"args":[{"tool":"greet","arguments":{},"toolCallId":"call","extra":true}]}`,
	} {
		if _, err := handler.Methods["CallTool"](context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid Provider DTO: %s", raw)
		}
	}
	// JSON inside arguments remains owned by the tool's schema, not rpcgen.
	if _, err := handler.Methods["CallTool"](context.Background(), json.RawMessage(`{"args":[{"tool":"greet","arguments":{"Name":1,"arbitrary":[null,true]},"toolCallId":"call"}]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCallErrorRetainsOnlyLocalCause(t *testing.T) {
	for _, tc := range []struct {
		code  string
		cause error
	}{{CodeCancelled, context.Canceled}, {CodeTimeout, context.DeadlineExceeded}} {
		client := fakeClient(fakePackage{response: CallResponse{Error: &CallError{Code: tc.code, Message: "safe"}}})
		_, err := client.Call(context.Background(), request())
		var failure *CallError
		if !errors.As(err, &failure) || !errors.Is(err, tc.cause) {
			t.Fatalf("lost local error cause: %v", err)
		}
		raw, err := json.Marshal(failure)
		if err != nil || strings.Contains(string(raw), "cause") {
			t.Fatalf("local cause leaked: %s, %v", raw, err)
		}
		var decoded CallError
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Unwrap() != nil {
			t.Fatalf("wire error acquired a local cause: %v", err)
		}
	}
}

type providerProbe struct {
	binding rpc.Binding
	config  json.RawMessage
	session *rpc.Session
}

func (p *providerProbe) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/provider-probe", New: func() gordis.Plugin { return p }}
}

func (p *providerProbe) Activate(ctx context.Context, scope *gordis.Scope) error {
	var err error
	p.session, err = rpc.Attach(ctx, scope, rpc.AttachOptions{
		Command:  rpc.Command{Path: childExecutable},
		Config:   p.config,
		Boundary: rpc.Boundary{Exports: []rpc.Binding{p.binding}},
	})
	return err
}

func TestOldProviderWireRejectedBeforeInitialize(t *testing.T) {
	if ProviderBinding.Version() != "rpcgen.json/1:1" {
		t.Fatal(ProviderBinding.Version())
	}
	// The deliberately invalid config must not be read before wire rejection.
	plugin := &providerProbe{binding: rpc.NewBinding(ProviderKey, "1", bindings.ProviderCodec()), config: json.RawMessage(`{"mode":"validate"}`)}
	host, err := gordis.NewHost(plugin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	_, err = host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "tool", Plugin: plugin.Spec().ID})
	if err == nil || !strings.Contains(err.Error(), "boundary mismatch") {
		t.Fatalf("old Provider did not fail at the boundary: %v", err)
	}
	if plugin.session == nil {
		t.Fatal("missing failed-session diagnostics")
	}
	d := plugin.session.Snapshot()
	if d.Ready || !d.Reaped || d.PID == 0 {
		t.Fatalf("old Provider child survived rejection: %+v", d)
	}
}

func TestProviderDefinitionValidationBeforeReady(t *testing.T) {
	// No Accept callback or Tool Runtime on the Host: only the child's
	// Codec.Validate can reject this export before Ready.
	plugin := &providerProbe{binding: ProviderBinding, config: json.RawMessage(`{"mode":"deferred"}`)}
	host, err := gordis.NewHost(plugin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Shutdown(context.Background()) })
	_, err = host.Env().MountReady(context.Background(), gordis.InstanceSpec{ID: "tool", Plugin: plugin.Spec().ID})
	if err == nil || !strings.Contains(err.Error(), "unsupported deferred Tool") {
		t.Fatalf("unsupported export reached Ready: %v", err)
	}
	if plugin.session == nil {
		t.Fatal("missing failed-session diagnostics")
	}
	d := plugin.session.Snapshot()
	if d.Ready || !d.Reaped || d.PID == 0 || d.Cleanup == nil || !d.Cleanup.CleanupComplete {
		t.Fatalf("invalid export survived or lost cleanup: %+v", d)
	}
}
