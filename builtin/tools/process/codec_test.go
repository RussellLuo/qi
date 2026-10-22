package process

import (
	"context"
	"encoding/json"
	"errors"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/bindings"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
	"reflect"
	"testing"
)

type loop struct {
	handler rpc.Handler
	raw     json.RawMessage
	err     error
	exited  bool
}

func (l *loop) Call(ctx context.Context, m string, in, out any) error {
	if l.err != nil {
		return l.err
	}
	raw := l.raw
	if raw == nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		f := l.handler.Methods[m]
		if f == nil {
			return errors.New("unknown method")
		}
		raw, err = f(ctx, b)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(raw, out)
}
func (l *loop) Stream(context.Context, string, any, func(json.RawMessage) error) error {
	return errors.New("no streams")
}
func (l *loop) Exited() bool { return l.exited }

type codecProvider struct {
	invoke func(CallRequest) (CallResponse, error)
}

func (*codecProvider) ListTools(context.Context) ([]Tool, error) { return nil, nil }
func (p *codecProvider) CallTool(_ context.Context, e CallRequest) (CallResponse, error) {
	return p.invoke(e)
}
func TestWirePreservesRawAndCanonicalFields(t *testing.T) {
	reason := "中文原因"
	for _, content := range [][]protocol.ContentBlock{nil, {}, {protocol.TextBlock{Text: "中文"}, protocol.ReasoningBlock{Text: "思考"}, protocol.ToolCallBlock{ToolCall: protocol.ToolCall{ID: "nested", Name: "echo", Arguments: ` {"n":900719925474099312345} `}}, protocol.ToolAdditionBlock{ToolName: "added"}, protocol.ToolRemovalBlock{ToolName: "removed"}}} {
		for _, contexts := range [][]protocol.Message{nil, {}, {{ID: "ctx", Role: protocol.RoleUser, Source: protocol.MessageSource{Kind: protocol.SourceModel, Provider: "p", Model: "m", ReplayState: json.RawMessage(`{"n":900719925474099312345}`), Extra: map[string]json.RawMessage{"n": json.RawMessage(`900719925474099312345`)}}, Content: []protocol.ContentBlock{protocol.TextBlock{Text: "上下文"}}}, {ID: "tool-context", Role: protocol.RoleUser, Source: protocol.MessageSource{Kind: protocol.SourceTool, CallID: "tool-call"}, Content: []protocol.ContentBlock{protocol.TextBlock{Text: "tool context"}}}}} {
			want := tools.ExecutionResult{IsError: true, Error: &tools.Failure{Message: "failure", Info: &protocol.ToolFailureInfo{Name: "BusinessError", Code: "business", Reason: &reason}}, Value: json.RawMessage(" invalid raw value "), Meta: json.RawMessage(` {"n":900719925474099312345} `), Content: content, AdditionalContexts: contexts, ConcludesTurn: true}
			input := tools.Execution{Identity: tools.ExecutionIdentity{AgentID: "agent", SessionID: "会话", Turn: -1, Step: 1 << 61}, CWD: "/tmp/中文", Call: protocol.ToolCall{ID: "call", Name: "echo", Arguments: " { \"duplicate\":1, \"duplicate\":2 } "}, Arguments: json.RawMessage(" {\"text\":\"修改\"} ")}
			provider := &codecProvider{invoke: func(e CallRequest) (CallResponse, error) {
				if !reflect.DeepEqual(decodeExecution(e), input) {
					t.Fatalf("input changed: %+v", e)
				}
				r, err := encodeResult(want)
				return CallResponse{Result: &r}, err
			}}
			inv := &loop{handler: bindings.ProviderCodec().Server(provider)}
			p := &Client{remote: bindings.ProviderCodec().Client(inv), pending: make(chan struct{}, 1)}

			got, err := p.Call(context.Background(), encodeExecution(input))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("roundtrip changed: got=%+v want=%+v err=%v", got, want, err)
			}
		}
	}
}

func TestWireResultAndNativeErrorAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		cause error
		code  string
	}{{errors.New("业务错误"), CodeToolError}, {context.Canceled, CodeCancelled}, {context.DeadlineExceeded, CodeTimeout}} {
		r := tools.ExecutionResult{Value: json.RawMessage(`null`), Content: []protocol.ContentBlock{}, Meta: json.RawMessage(` {"raw":true} `)}
		encoded, err := encodeResult(r)
		if err != nil {
			t.Fatal(err)
		}
		client := fakeClient(fakePackage{response: CallResponse{Result: &encoded, Error: &CallError{Code: tc.code, Message: tc.cause.Error()}}})
		got, err := client.Call(context.Background(), request())
		var callErr *CallError
		if !reflect.DeepEqual(got, r) || !errors.As(err, &callErr) || callErr.Code != tc.code || callErr.Message != tc.cause.Error() {
			t.Fatal(got, err)
		}
		if tc.code != CodeToolError && !errors.Is(err, tc.cause) {
			t.Fatal("lost cancellation identity", err)
		}
	}
}
