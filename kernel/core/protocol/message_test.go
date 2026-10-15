package protocol_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestMessageJSONRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}`,
		`{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}`,
		`{"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]}`,
		`{"id":"a-tool","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]}`,
		`{"id":"r1","role":"tool","source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"echo"}],"toolCallId":"c1"}`,
		`{"id":"a-final","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"echo"}]}`,
		`{"id":"a-partial","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"}]}`,
		`{"id":"interrupted-tool-result-c1-9","role":"tool","toolCallId":"c1","isError":true,"source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]}`,
		`{"id":"interrupted-tool-result-c1-10","role":"tool","toolCallId":"c1","isError":true,"source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."}]}`,
		`{"id":"a-tool","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},{"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}]}`,
		`{"id":"interrupted-tool-result-c1-10","role":"tool","toolCallId":"c1","isError":true,"source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]}`,
		`{"id":"interrupted-tool-result-c2-11","role":"tool","toolCallId":"c2","isError":true,"source":{"kind":"tool","callId":"c2"},"content":[{"type":"text","text":"The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."}]}`,
		`{"id":"sys-old","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"old prompt"}]}`,
		`{"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]}`,
		`{"id":"sys-clear","role":"system","source":{"kind":"system-prompt"},"content":[]}`,
		`{"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]}`,
		`{"id":"dev-add","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"extra"}]}`,
		`{"id":"dev-remove","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-removal","toolName":"echo"}]}`,
		`{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}`,
		`{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}`,
	} {
		var message protocol.Message
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual([]byte(raw), encoded) {
			t.Fatalf("Message changed: %s -> %s", raw, encoded)
		}
	}
}

func jsonEqual(a, b []byte) bool {
	decode := func(raw []byte) any {
		var value any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&value) != nil {
			return nil
		}

		return value
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

func TestMessageSourceExtensionsAndOptionalFieldsAreDetached(t *testing.T) {
	for _, raw := range []string{
		`{"id":"u","role":"user","source":{"kind":"model","provider":null,"model":"","custom":true},"content":[]}`,
		`{"id":"d","role":"developer","source":{"kind":"tool"},"content":[]}`,
		`{"id":"u","role":"user","source":{"kind":"plugin-context","form":"snapshot","provider":{"private":true},"sections":[{"name":"n","text":"t"}],"number":9007199254740993},"content":[{"type":"text","text":""}]}`,
		`{"id":"d","role":"developer","source":{"kind":"tool-registry","revision":3},"content":[{"type":"tool-addition","toolName":"echo"},{"type":"tool-removal","toolName":"old"}]}`,
		`{"id":"a","role":"assistant","source":{"kind":"model","provider":"p","model":"m","replayState":{"blocks":[null],"response":{"id":"r"}},"private":{"a":1}},"content":[{"type":"tool-call","id":"c","name":"echo","arguments":" {broken\n"}]}`,
		`{"id":"s","role":"system","source":{"kind":"system-prompt"},"content":[]}`,
		`{"id":"t","role":"tool","source":{"kind":"tool","callId":"c"},"toolCallId":"c","isError":false,"content":[]}`,
		`{"id":"t","role":"tool","source":{"kind":"tool","callId":"c"},"toolCallId":"c","content":[]}`,
	} {
		var message protocol.Message
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			t.Fatal(err)
		}

		clone := message.Clone()
		encoded, err := json.Marshal(clone)
		if err != nil {
			t.Fatal(err)
		}

		if !jsonEqual([]byte(raw), encoded) {
			t.Fatalf("lost fields: %s", encoded)
		}

		if clone.Source.Extra != nil {
			clone.Source.Extra["private"] = json.RawMessage(`{"mutated":true}`)
		}

		if len(clone.Source.ReplayState) > 0 {
			clone.Source.ReplayState[0] = '['
		}

		if clone.IsError != nil {
			*clone.IsError = true
		}

		original, err := json.Marshal(message)
		if err != nil || !jsonEqual([]byte(raw), original) {
			t.Fatalf("clone mutated source: %s / %v", original, err)
		}
	}
}

func TestMessageRejectsOldShapeAndInvalidRequiredFields(t *testing.T) {
	for _, raw := range []string{
		`{"apiVersion":"qi.profile/v1alpha1","id":"u","role":"user","source":"user","content":[{"kind":"text","text":"hi"}]}`,
		`{"id":"a","role":"assistant","source":{"kind":"model"},"content":[]}`,
		`{"id":"t","role":"tool","source":{"kind":"tool","callId":"other"},"toolCallId":"c","content":[]}`,
		`{"id":"u","role":"user","source":{"kind":"user"},"toolCallId":"c","content":[]}`,
		`{"id":"d","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"echo","tool":{}}]}`,
		`{"id":"a","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"tool-call","id":"c","name":"echo","arguments":{}}]}`,
		`{"id":"u","role":"user","source":{"kind":"user"},"content":null}`,
		`{"id":"u","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":null}]}`,
	} {
		var message protocol.Message
		if err := json.Unmarshal([]byte(raw), &message); err == nil {
			t.Fatalf("accepted invalid Message: %s", raw)
		}
	}
}
