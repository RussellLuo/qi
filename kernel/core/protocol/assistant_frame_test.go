package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestFrameJSONRoundTrip(t *testing.T) {
	for _, raw := range []string{
		`{"type":"start","attemptId":"s1:1","revision":1,"turn":1,"step":1}`,
		`{"type":"chunk","attemptId":"s1:1","revision":2,"index":0,"time":1008,"chunk":{"type":"text-delta","index":0,"text":"你好！"}}`,
		`{"type":"chunk","attemptId":"s1:1","revision":3,"index":1,"time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"end","attemptId":"s1:1","revision":4,"index":2,"outcome":{"kind":"committed","eventType":"assistant/message","seq":8}}`,
		`{"type":"chunk","attemptId":"s1:1","revision":2,"index":0,"time":1008,"chunk":{"type":"text-delta","index":0,"text":" \n"}}`,
		`{"type":"chunk","attemptId":"s1:1","revision":3,"index":1,"time":1009,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}`,
		`{"type":"end","attemptId":"s1:1","revision":4,"index":2,"outcome":{"kind":"committed","eventType":"assistant/attempt","seq":8}}`,
		`{"type":"start","attemptId":"s1:2","revision":5,"turn":1,"step":1}`,
		`{"type":"chunk","attemptId":"s1:2","revision":6,"index":0,"time":1010,"chunk":{"type":"text-delta","index":0,"text":"你好！"}}`,
		`{"type":"chunk","attemptId":"s1:2","revision":7,"index":1,"time":1011,"chunk":{"type":"finish","reason":{"kind":"stop"}}}`,
		`{"type":"end","attemptId":"s1:2","revision":8,"index":2,"outcome":{"kind":"committed","eventType":"assistant/message","seq":9}}`,
		`{"type":"end","attemptId":"s1:1","revision":4,"index":2,"outcome":{"kind":"abandoned"}}`,
	} {
		var frame protocol.AssistantStreamFrame
		if err := json.Unmarshal([]byte(raw), &frame); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual([]byte(raw), encoded) {
			t.Fatalf("Frame changed: %s -> %s", raw, encoded)
		}
	}
}

func TestFrameRejectsInvalidOutcomeAndIndexes(t *testing.T) {
	for _, raw := range []string{
		`{"type":"start","attemptId":"s:1","revision":0,"turn":1,"step":1}`,
		`{"type":"start","attemptId":"s:1","revision":1,"turn":1,"step":1,"index":0}`,
		`{"type":"start","attemptId":"s:1","revision":9007199254740992,"turn":1,"step":1}`,
		`{"type":"chunk","attemptId":"s:1","revision":1,"index":-1,"time":0,"chunk":{"type":"text-delta","index":0,"text":"x"}}`,
		`{"type":"chunk","attemptId":"s:1","revision":1,"index":0.5,"time":0,"chunk":{"type":"text-delta","index":0,"text":"x"}}`,
		`{"type":"end","attemptId":"s:1","revision":1,"index":0,"outcome":{"kind":"committed","eventType":"tool/call","seq":0}}`,
		`{"type":"end","attemptId":"s:1","revision":1,"index":0,"outcome":{"kind":"committed","eventType":"assistant/message"}}`,
		`{"type":"end","attemptId":"s:1","revision":1,"index":0,"outcome":{"kind":"abandoned","seq":0}}`,
		`{"type":"end","attemptId":"s:1","revision":1,"index":0,"outcome":{"kind":"abandoned","eventType":"assistant/message"}}`,
	} {
		var f protocol.AssistantStreamFrame
		if err := json.Unmarshal([]byte(raw), &f); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
