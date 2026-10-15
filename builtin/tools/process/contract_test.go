package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	gordisprocess "github.com/RussellLuo/gordis/process"
)

func TestToolPackagePayloadBoundaries(t *testing.T) {
	valid := Tool{Name: "greet", Description: "Greet.", Parameters: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), FailureMode: FailureResult}
	if err := ValidateTools([]Tool{valid}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Tool){
		func(d *Tool) { d.Name = "invalid/name" },
		func(d *Tool) { d.Name = strings.Repeat("x", MaxNameBytes+1) },
		func(d *Tool) { d.Description = "" },
		func(d *Tool) { d.Description = strings.Repeat("x", MaxDescriptionBytes+1) },
		func(d *Tool) { d.Parameters = json.RawMessage(`broken`) },
		func(d *Tool) { d.Output = json.RawMessage(`broken`) },
		func(d *Tool) { d.Output = json.RawMessage(`"` + strings.Repeat("x", MaxSchemaBytes) + `"`) },
		func(d *Tool) { d.TimeoutMillis = -1 },
		func(d *Tool) { d.TimeoutMillis = MaxToolTimeout.Milliseconds() + 1 },
		func(d *Tool) { d.FailureMode = "unknown" },
	} {
		d := valid
		change(&d)
		if err := ValidateTools([]Tool{d}); err == nil {
			t.Fatalf("accepted unsupported definition: %+v", d)
		}
	}
	for _, definitions := range [][]Tool{nil, {valid, valid}, make([]Tool, MaxTools+1)} {
		if err := ValidateTools(definitions); err == nil {
			t.Fatal("accepted invalid tool count or duplicate")
		}
	}
	metadata := Metadata{ID: "test.package", Version: "v1", Capabilities: []string{CapabilityTools}}
	if err := ValidateMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	metadata.Capabilities = append(metadata.Capabilities, "unknown")
	if err := ValidateMetadata(metadata); err == nil {
		t.Fatal("accepted additional capability")
	}
	r := request()
	r.Invocation = Invocation{}
	if err := ValidateCall(r); err == nil {
		t.Fatal("accepted missing invocation IDs")
	}
	r = request()
	r.Arguments = json.RawMessage(`"` + strings.Repeat("x", MaxArgumentsBytes) + `"`)
	if err := ValidateCall(r); err == nil {
		t.Fatal("accepted oversized arguments")
	}
	var decoded CallRequest
	for _, raw := range []string{`{"unexpected":true}`, `{} {}`, `{} trailing`} {
		if err := decodeStrict(json.RawMessage(raw), &decoded); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

type fakeCaller struct {
	err         error
	response    any
	diagnostics gordisprocess.Diagnostics
}

func (f fakeCaller) Call(_ context.Context, _ string, _, result any) error {
	if f.err != nil {
		return f.err
	}
	raw, err := json.Marshal(f.response)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, result)
}
func (f fakeCaller) Snapshot() gordisprocess.Diagnostics { return f.diagnostics }

func TestClientRejectsInvalidRemoteResponses(t *testing.T) {
	for _, response := range []CallResponse{
		{},
		{Result: json.RawMessage(`{}`), Error: NewToolError("both")},
		{Error: &CallError{Code: CodeTimeout, Message: "invalid envelope"}},
		{Error: &CallError{Code: CodeToolError, Message: strings.Repeat("x", MaxErrorMessage+1)}},
		{Result: json.RawMessage(`"` + strings.Repeat("x", MaxResultBytes) + `"`)},
	} {
		client, err := NewClient(fakeCaller{response: response})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Call(context.Background(), request())
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.Code != CodeHostValidation {
			t.Fatalf("invalid response error = %v", err)
		}
	}
	if _, err := NewClient(nil); err == nil {
		t.Fatal("accepted nil caller")
	}
}

func TestClientErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.Canceled, CodeCancelled}, {context.DeadlineExceeded, CodeTimeout},
		{gordisprocess.ErrBackpressure, CodeBackpressure}, {errors.New("broken pipe"), CodeTransport},
		{&gordisprocess.RPCError{Code: CodeCancelled}, CodeCancelled},
		{&gordisprocess.RPCError{Code: CodeTimeout}, CodeTimeout},
		{&gordisprocess.RPCError{Code: CodeBackpressure}, CodeBackpressure},
		{&gordisprocess.RPCError{Code: CodeHostValidation}, CodeHostValidation},
		{gordisprocess.ErrClosed, CodeProcessExit},
	} {
		client, _ := NewClient(fakeCaller{err: tc.err, diagnostics: gordisprocess.Diagnostics{Reaped: true, Error: "process exited"}})
		_, err := client.Call(context.Background(), request())
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.Code != tc.code {
			t.Fatalf("%v mapped to %v, want %s", tc.err, err, tc.code)
		}
	}
}
