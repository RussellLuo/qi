package protocol_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestChunkAndRecordJSONRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"type":"finish","reason":{"kind":"stop"}}`,
		`{"type":"text-delta","index":0,"text":"你好！"}`,
		`{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}`,
		`{"type":"finish","reason":{"kind":"tool-calls"}}`,
		`{"type":"text-delta","index":0,"text":"echo"}`,
		`{"type":"reasoning-delta","index":4,"text":"先想一想"}`,
		`{"type":"text-delta","index":2,"text":"已有前缀"}`,
		`{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}`,
		`{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}`,
		`{"type":"text-delta","index":0,"text":" \n"}`,
		`{"type":"finish","reason":{"kind":"error","failure":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}}`,
		`{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}`,
		`{"type":"usage","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1}}`,
		`{"type":"finish","reason":{"kind":"tool-calls"},"replayState":{"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}}`,
		`{"type":"text-delta","index":7,"text":"A"}`,
		`{"type":"text-delta","index":7,"text":"B"}`,
		`{"type":"text-delta","index":7,"text":"C"}`,
		`{"type":"reasoning-delta","index":2,"text":"想"}`,
		`{"type":"tool-call-delta","index":9,"id":"c-rec","name":"echo","argumentsDelta":"{\"text\":"}`,
		`{"type":"tool-call-delta","index":9,"id":"c-rec","name":"echo","argumentsDelta":"\"x\"}"}`,
		`{"type":"block-start","index":8,"blockType":"text"}`,
		`{"type":"text-delta","index":8,"text":"partial"}`,
		`{"type":"reasoning-delta","index":1,"text":"thought"}`,
		`{"type":"text-delta","index":8,"text":"more"}`,
		`{"type":"block-end","index":8,"block":{"type":"text","text":"authoritative"}}`,
		`{"type":"text-delta","index":8,"text":"late"}`,
		`{"type":"block-end","index":8,"block":{"type":"text","text":"wrong duplicate"}}`,
		`{"type":"text-delta","index":0,"text":"prefix"}`,
		`{"type":"tool-call-delta","index":1,"id":"c-truncated","name":"echo","argumentsDelta":"{broken"}`,
		`{"type":"finish","reason":{"kind":"max-tokens"},"replayState":{"response":{"id":"r2"},"blocks":["text-meta","tool-meta"]}}`,
		`{"type":"tool-call-delta","index":0,"id":"c-names","name":"echo","argumentsDelta":"{"}`,
		`{"type":"tool-call-delta","index":0,"id":"c-names","argumentsDelta":"}"}`,
		`{"type":"text-delta","index":3,"text":"answer"}`,
	} {
		chunk, err := protocol.DecodeStreamChunk([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := protocol.EncodeStreamChunk(chunk)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual([]byte(raw), encoded) {
			t.Fatalf("Chunk changed: %s -> %s", raw, encoded)
		}
	}
	for _, raw := range []string{
		`{"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]}`,
		`{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}}`,
		`{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}`,
		`{"type":"chunk","time":1013,"chunk":{"type":"text-delta","index":0,"text":"echo"}}`,
		`{"type":"chunk","time":1014,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"chunk","time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}}`,
		`{"type":"chunk","time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}}`,
		`{"type":"chunk","time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}}`,
		`{"type":"chunk","time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}`,
		`{"type":"chunk","time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}}`,
		`{"type":"chunk","time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}`,
		`{"type":"chunk","time":1010,"chunk":{"type":"finish","reason":{"kind":"error","failure":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}}}`,
		`{"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}}`,
		`{"type":"text-chunks","time0":3000,"index":7,"dt":[-2,5],"texts":["A","B","C"]}`,
		`{"type":"reasoning-chunks","time0":3004,"index":2,"dt":[1],"texts":["想","想"]}`,
		`{"type":"tool-call-chunks","time0":3006,"index":9,"dt":[0],"id":"c-rec","name":"echo","args":["{\"text\":","\"x\"}"]}`,
		`{"type":"chunk","time":3007,"chunk":{"type":"usage","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1}}}`,
		`{"type":"chunk","time":3008,"chunk":{"type":"finish","reason":{"kind":"tool-calls"},"replayState":{"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}}}`,
		`{"type":"chunk","time":4000,"chunk":{"type":"block-start","index":8,"blockType":"text"}}`,
		`{"type":"text-chunks","time0":4001,"index":8,"dt":[],"texts":["partial"]}`,
		`{"type":"reasoning-chunks","time0":4002,"index":1,"dt":[],"texts":["thought"]}`,
		`{"type":"text-chunks","time0":4003,"index":8,"dt":[],"texts":["more"]}`,
		`{"type":"chunk","time":4004,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"authoritative"}}}`,
		`{"type":"text-chunks","time0":4005,"index":8,"dt":[],"texts":["late"]}`,
		`{"type":"chunk","time":4006,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"wrong duplicate"}}}`,
		`{"type":"chunk","time":4007,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"text-chunks","time0":5000,"index":0,"dt":[],"texts":["prefix"]}`,
		`{"type":"tool-call-chunks","time0":5001,"index":1,"dt":[],"id":"c-truncated","name":"echo","args":["{broken"]}`,
		`{"type":"chunk","time":5002,"chunk":{"type":"finish","reason":{"kind":"max-tokens"},"replayState":{"response":{"id":"r2"},"blocks":["text-meta","tool-meta"]}}}`,
		`{"type":"text-chunks","time0":1010,"index":0,"dt":[],"texts":[" \n"]}`,
		`{"type":"reasoning-chunks","time0":1010,"index":4,"dt":[],"texts":["先想一想"]}`,
		`{"type":"text-chunks","time0":1011,"index":2,"dt":[],"texts":["已有前缀"]}`,
		`{"type":"tool-call-chunks","time0":1012,"index":8,"dt":[],"id":"c-partial","name":"echo","args":["{\"text\":"]}`,
		`{"type":"tool-call-chunks","time0":6000,"index":0,"dt":[],"id":"c-names","name":"echo","args":["{"]}`,
		`{"type":"tool-call-chunks","time0":6001,"index":0,"dt":[],"id":"c-names","args":["}"]}`,
		`{"type":"chunk","time":6002,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}`,
		`{"type":"text-chunks","time0":7000,"index":3,"dt":[],"texts":["answer"]}`,
		`{"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":[" \n"]}`,
		`{"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}`,
		`{"type":"text-chunks","time0":1010,"index":0,"dt":[],"texts":["你好！"]}`,
		`{"type":"chunk","time":1011,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
	} {
		var record protocol.AssistantStreamRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual([]byte(raw), encoded) {
			t.Fatalf("Record changed: %s -> %s", raw, encoded)
		}
	}
}

func TestStreamDurableValidation(t *testing.T) {
	invalid := []string{
		`{"type":"unknown"}`,
		`{"type":"text-delta","index":-1,"text":"bad"}`,
		`{"kind":"text.delta","blockId":"old","delta":"old"}`,
		`{"type":"text-delta","index":0}`,
		`{"type":"text-delta","index":0,"text":null}`,
		`{"type":"text-delta","index":-0,"text":""}`,
		`{"type":"text-delta","index":1.5,"text":""}`,
		`{"type":"text-delta","index":9007199254740992,"text":""}`,
		`{"type":"finish","reason":{"kind":"error"}}`,
		`{"type":"finish","reason":{"kind":"stop","failure":null}}`,
		`{"type":"finish","reason":{"kind":"stop"},"replayState":{"blocks":[]}}`,
		`{"type":"finish","reason":{"kind":"stop"},"replayState":{"response":{},"blocks":null}}`,
		`{"type":"usage","usage":{"inputTokens":1}}`,
		`{"type":"block-end","index":0,"block":{"type":"tool-call","id":"c","name":"echo","arguments":{}}}`,
	}

	for _, raw := range invalid {
		if _, err := protocol.DecodeStreamChunk([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}

	records := []string{
		`{"type":"chunk","time":9007199254740992,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"text-chunks","time0":1,"index":0,"dt":[],"texts":["x","y"]}`,
		`{"type":"text-chunks","time0":0,"index":0,"dt":[],"texts":[null]}`,
		`{"type":"text-chunks","time0":0,"index":0,"dt":[null],"texts":["a","b"]}`,
		`{"type":"tool-call-chunks","time0":0,"index":0,"dt":[],"args":[null],"id":"c"}`,
		`{"type":"text-chunks","time0":0,"index":0,"dt":[],"texts":[]}`,
		`{"type":"text-chunks","time0":9007199254740991,"index":0,"dt":[1],"texts":["a","b"]}`,
		`{"type":"tool-call-chunks","time0":0,"index":0,"dt":[],"args":[""],"id":"c","name":""}`,
		`{"type":"chunk","time":0,"chunk":{"type":"text-delta","index":0,"text":""},"error":"extra"}`,
	}

	for _, raw := range records {
		var r protocol.AssistantStreamRecord
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestRawRecordSnapshotsReplayAndRejectsUnserializableValues(t *testing.T) {
	response := map[string]any{"key": "before"}
	chunk := protocol.StreamChunk{
		Type:        protocol.ChunkFinish,
		Reason:      protocol.FinishReason{Kind: protocol.FinishStop},
		ReplayState: &protocol.ReplayEnvelope{Response: response, Blocks: []any{}},
	}
	record, err := protocol.NewAssistantStreamRecord(0, chunk)
	if err != nil {
		t.Fatal(err)
	}

	response["key"] = "after"
	if record.Chunk.ReplayState.Response.(map[string]any)["key"] != "before" {
		t.Fatal("replay alias")
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}

	var node map[string]any
	_ = json.Unmarshal(data, &node)
	envelope := node["chunk"].(map[string]any)["replayState"].(map[string]any)
	if _, ok := envelope["blocks"]; !ok {
		t.Fatal("lost explicit empty blocks")
	}

	for _, bad := range []any{func() {}, make(chan int), errors.New("opaque Go error"), struct{}{}} {
		chunk.ReplayState.Response = bad
		if _, err := protocol.NewAssistantStreamRecord(0, chunk); err == nil {
			t.Fatal("non-JSON replay accepted")
		}
	}
}
