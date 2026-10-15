package protocol

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSessionEventJSONRoundTripCoversCoreKindsAndUnknownKind(t *testing.T) {
	events := []SessionEvent{
		NewTurnStarted(1),
		NewMessageEvent(SystemMessage, 1, 1, NewTextMessage("system-1", RoleSystem, SourceSystem, "system")),
		NewMessageEvent(UserMessage, 1, 0, NewTextMessage("user-1", RoleUser, SourceUser, "hello")),
		NewStepStarted(1, 1),
		NewAssistantMessageEvent(1, 1, NewAssistantTextMessage("assistant-1", "test", "test", "answer"), []AssistantStreamRecord{}, nil),
		NewAttemptEvent(1, 1, []AssistantStreamRecord{}),
		NewToolCallEvent(1, 1, ToolCall{
			ID:        "call-1",
			Name:      "echo",
			Arguments: string(`{}`),
		}),
		NewToolResultEvent(
			1,
			1,
			ToolResult{
				Message: NewToolResultMessage("result-message", "call-1", []ContentBlock{TextBlock{Text: `{}`}}, false),
			},
			0,
		),
		NewStepEnded(1, 1),
		NewTurnEnded(1, TurnEndReason{Kind: "aborted", Reason: &AgentCancelCause{Kind: "user"}}),
		{
			Type:      "extension/future",
			Ignorable: true,
			Data:      json.RawMessage(`{"value":1}`),
		},
	}

	for i := range events {
		events[i].Seq = Sequence(i)
		event := events[i]
		t.Run(string(event.Type), func(t *testing.T) {
			assertJSONRoundTrip(t, event, func(data []byte) error {
				decoded, err := DecodeSessionEvent(data)
				if err != nil {
					return err
				}

				reencoded, err := EncodeSessionEvent(decoded)
				if err != nil {
					return err
				}
				if string(data) != string(reencoded) {
					t.Fatalf("event JSON changed:\n%s\n%s", data, reencoded)
				}

				return nil
			})
		})
	}
}

func TestCodecRejectsUnknownVersionAndTrailingJSON(t *testing.T) {
	if _, err := EncodeSessionEvent(NewTurnStarted(1)); err != nil {
		t.Fatalf("seq=0 rejected: %v", err)
	}

	if _, err := DecodeSessionHeader([]byte(`{"version":3,"id":"s","createdAt":0,"isSeeded":false}`)); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("unsupported header: %v", err)
	}

	if _, err := DecodeSessionEvent([]byte(`{"apiVersion":"qi.profile/v2","sequence":1,"sessionId":"session-1","kind":"session/created"}`)); !errors.Is(err, ErrInvalidProtocol) {
		t.Fatalf("legacy envelope: %v", err)
	}

}

func assertJSONRoundTrip(t *testing.T, value any, check func([]byte) error) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	if err := check(data); err != nil {
		t.Fatal(err)
	}
}

func TestSessionEnvelopeRejectsMalformedPositionsAndPayloads(t *testing.T) {
	valid := `{"type":"assistant/message","seq":0,"time":0,"data":{"turn":1,"step":1,"message":{"id":"a","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},"stream":[]},"surfaceOp":"append"}`

	for _, raw := range []string{
		strings.Replace(valid, `"seq":0`, `"seq":-0`, 1),
		strings.Replace(valid, `"seq":0`, `"seq":9007199254740992`, 1),
		strings.Replace(valid, `"seq":0`, `"seq":0.5`, 1),
		strings.Replace(valid, `"time":0`, `"time":9007199254740992`, 1),
		strings.Replace(valid, `,"stream":[]`, "", 1),
		strings.Replace(valid, `"stream":[]`, `"stream":null`, 1),
		strings.Replace(valid, `"stream":[]`, `"stream":[],"interrupted":false`, 1),
		`{"type":"step/end","seq":0,"time":0,"data":{"turn":1,"step":1,"reason":{"kind":"completed"}}}`,
		`{"type":"user/message","seq":1,"time":0,"data":{"id":"u","role":"user","source":{"kind":"user"},"content":[]},"surfaceOp":"append","sourceEventSeqs":[-0]}`,
		`{"type":"user/message","seq":1,"time":0,"data":{"id":"u","role":"user","source":{"kind":"user"},"content":[]},"surfaceOp":"append","sourceEventSeqs":[null]}`,
		`{"type":"turn/start","seq":0,"time":0,"data":{"turn":1},"surfaceOp":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := DecodeSessionEvent([]byte(raw)); err == nil {
				t.Fatal("accepted invalid core event")
			}
		})
	}
}

func TestUnknownIgnorableMetadataAndNullToolMetaRemainJSON(t *testing.T) {
	for _, raw := range []string{
		`{"type":"extension","seq":0,"time":-1,"data":null,"ignorable":true,"surfaceOp":{"future":[1,null]},"sourceEventSeqs":"opaque"}`,
		`{"type":"tool/result","seq":1,"time":0,"data":{"turn":1,"step":1,"message":{"id":"r","role":"tool","source":{"kind":"tool","callId":"c"},"toolCallId":"c","content":[]},"meta":null},"surfaceOp":"append","sourceEventSeqs":[0]}`,
	} {
		e, err := DecodeSessionEvent([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}

		encoded, err := EncodeSessionEvent(e.Clone())
		if err != nil {
			t.Fatal(err)
		}

		var a, b any
		_ = json.Unmarshal([]byte(raw), &a)
		_ = json.Unmarshal(encoded, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("opaque JSON changed: %s", encoded)
		}
	}
}
