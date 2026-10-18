package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProviderPayloadBoundaries(t *testing.T) {
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
	r := request()
	r.ToolCallID = ""
	if err := ValidateCall(r); err == nil {
		t.Fatal("accepted missing tool-call identity")
	}
	r = request()
	r.Arguments = json.RawMessage(`"` + strings.Repeat("x", MaxArgumentsBytes) + `"`)
	if err := ValidateCall(r); err == nil {
		t.Fatal("accepted oversized arguments")
	}
	var decoded CallRequest
	for _, raw := range []string{`{"unexpected":true}`, `{} {}`, `{} trailing`, `{"tool":"greet","arguments":{},"toolCallId":"call","invocation":{"requestId":"old"}}`} {
		if err := decodeStrict(json.RawMessage(raw), &decoded); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

type fakePackage struct {
	response CallResponse
	err      error
	exited   bool
}

func (f fakePackage) ListTools(context.Context) ([]Tool, error) { return nil, f.err }
func (f fakePackage) CallTool(context.Context, CallRequest) (CallResponse, error) {
	return f.response, f.err
}
func (f fakePackage) Exited() bool { return f.exited }
func fakeClient(f fakePackage) *Client {
	return &Client{remote: f, loader: f, pending: make(chan struct{}, 16)}
}
func TestClientErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		exited bool
	}{
		{context.Canceled, CodeCancelled, false}, {context.DeadlineExceeded, CodeTimeout, false}, {errors.New("broken pipe"), CodeTransport, false},
		{status.Error(codes.Canceled, "cancelled"), CodeCancelled, false}, {status.Error(codes.DeadlineExceeded, "timeout"), CodeTimeout, false},
		{status.Error(codes.ResourceExhausted, "oversize"), CodeHostValidation, false}, {errors.New("closed"), CodeProcessExit, true},
	} {
		client := fakeClient(fakePackage{err: tc.err, exited: tc.exited})
		_, err := client.Call(context.Background(), request())
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.Code != tc.code {
			t.Fatalf("%v mapped to %v, want %s", tc.err, err, tc.code)
		}
	}
}
func TestClientRejectsInvalidRemoteResponses(t *testing.T) {
	for _, response := range []CallResponse{
		{},
		{Result: json.RawMessage(`{}`), Error: NewToolError("both")},
		{Error: &CallError{Code: "unknown-code", Message: "invalid envelope"}},
		{Error: &CallError{Code: CodeToolError, Message: strings.Repeat("x", MaxErrorMessage+1)}},
		{Result: json.RawMessage(`"` + strings.Repeat("x", MaxResultBytes) + `"`)},
	} {
		client := fakeClient(fakePackage{response: response})
		var err error
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

func TestToolErrorPreservesUTF8AtLimit(t *testing.T) {
	message := strings.Repeat("x", MaxErrorMessage-1) + "你好"
	failure := NewToolError(message)
	if !utf8.ValidString(failure.Message) || len(failure.Message) > MaxErrorMessage {
		t.Fatalf("invalid bounded message: %q", failure.Message)
	}
	raw, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CallError
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.Message != failure.Message {
		t.Fatalf("message changed during wire round trip: %q %v", decoded.Message, err)
	}
}
