package llm

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Compare expanded streams so valid differences in record packing are allowed.
func TestStreamExpansionAndAssembly(t *testing.T) {
	cases := streamCases()

	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			var input struct {
				Records     []protocol.AssistantStreamRecord `json:"records"`
				TimedChunks []protocol.TimedStreamChunk      `json:"timedChunks"`
			}
			err := json.Unmarshal(c.Input, &input)
			var expanded []protocol.TimedStreamChunk
			if err == nil {
				if input.Records != nil {
					expanded, err = protocol.ExpandAssistantStream(input.Records)
				} else {
					expanded = input.TimedChunks

					for _, v := range expanded {
						_, err = protocol.NewAssistantStreamRecord(v.Time, v.Chunk)
						if err != nil {
							break
						}
					}
				}
			}

			if err != nil {
				t.Fatal(err)
			}

			var expected map[string]json.RawMessage
			if err := json.Unmarshal(c.Expected, &expected); err != nil {
				t.Fatal(err)
			}

			compareJSON(t, expanded, expected["expanded"])
			raw := make([]protocol.AssistantStreamRecord, 0, len(expanded))
			a, err := NewAccumulator("fixture-message", modelSource())
			if err != nil {
				t.Fatal(err)
			}

			for _, v := range expanded {
				record, err := protocol.NewAssistantStreamRecord(v.Time, v.Chunk)
				if err != nil {
					t.Fatal(err)
				}

				raw = append(raw, record)
				if err := a.Accept(v.Chunk); err != nil {
					t.Fatal(err)
				}
			}

			bytes, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}

			var decoded []protocol.AssistantStreamRecord
			if err = json.Unmarshal(bytes, &decoded); err != nil {
				t.Fatal(err)
			}

			roundtrip, err := protocol.ExpandAssistantStream(decoded)
			if err != nil {
				t.Fatal(err)
			}

			compareJSON(t, roundtrip, expected["expanded"])
			blocks, err := a.Blocks()
			if err != nil {
				t.Fatal(err)
			}

			encoded, err := protocol.EncodeContent(blocks)
			if err != nil {
				t.Fatal(err)
			}

			compareJSON(t, json.RawMessage(encoded), expected["blocks"])
			interrupted, err := protocol.EncodeContent(a.InterruptedBlocks())
			if err != nil {
				t.Fatal(err)
			}

			compareJSON(t, json.RawMessage(interrupted), expected["interruptedBlocks"])
			compareJSON(t, a.FinishReason(), expected["finish"])
			if want, ok := expected["usage"]; ok {
				compareJSON(t, a.Usage(), want)
			} else if a.Usage() != nil {
				t.Fatal("unexpected usage")
			}

			replay, err := a.ReplayState()
			if err != nil {
				t.Fatal(err)
			}

			if want, ok := expected["replayState"]; ok {
				compareJSON(t, replay, want)
			} else if replay != nil {
				t.Fatal("unexpected replay state")
			}

			result := map[string]any{
				"accepted":          true,
				"expanded":          expanded,
				"packed":            raw,
				"blocks":            json.RawMessage(encoded),
				"interruptedBlocks": json.RawMessage(interrupted),
				"finish":            a.FinishReason(),
			}
			if a.Usage() != nil {
				result["usage"] = a.Usage()
			}

			if replay != nil {
				result["replayState"] = replay
			}

			checkFixtureResult(t, c, result)
		})
	}
}

func compareJSON(t *testing.T, got any, want json.RawMessage) {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	var actual, expected any
	if err = json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}

	if err = json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("got %s\nwant %s", data, want)
	}
}

// checkFixtureResult checks every result field. Only optional Record packing differs:
// both writers must expand to exactly the same timed chunks.
func checkFixtureResult(t *testing.T, c streamCase, result any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(fixtureSemanticResult(t, encoded), fixtureSemanticResult(t, c.Expected)) {
		t.Fatalf("%s: complete result differs\nGo: %s\nreference: %s", c.ID, encoded, c.Expected)
	}

}

func fixtureSemanticResult(t *testing.T, raw []byte) any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}

	expand := func(records any) any {
		b, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}

		var decoded []protocol.AssistantStreamRecord
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}

		timed, err := protocol.ExpandAssistantStream(decoded)
		if err != nil {
			t.Fatal(err)
		}

		b, err = json.Marshal(timed)
		if err != nil {
			t.Fatal(err)
		}

		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}

		return out
	}
	if packed, ok := value["packed"]; ok {
		value["packed"] = expand(packed)
	}

	return value
}

type streamCase struct {
	ID              string
	Input, Expected json.RawMessage
}

func streamCases() []streamCase {
	return []streamCase{
		{
			ID: "authoritative-close-and-interleave",
			Input: json.RawMessage(`{
  "timedChunks": [
    {"time":4000,"chunk":{"type":"block-start","index":8,"blockType":"text"}},
    {"time":4001,"chunk":{"type":"text-delta","index":8,"text":"partial"}},
    {"time":4002,"chunk":{"type":"reasoning-delta","index":1,"text":"thought"}},
    {"time":4003,"chunk":{"type":"text-delta","index":8,"text":"more"}},
    {"time":4004,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"authoritative"}}},
    {"time":4005,"chunk":{"type":"text-delta","index":8,"text":"late"}},
    {"time":4006,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"wrong duplicate"}}},
    {"time":4007,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":4000,"chunk":{"type":"block-start","index":8,"blockType":"text"}},
    {"time":4001,"chunk":{"type":"text-delta","index":8,"text":"partial"}},
    {"time":4002,"chunk":{"type":"reasoning-delta","index":1,"text":"thought"}},
    {"time":4003,"chunk":{"type":"text-delta","index":8,"text":"more"}},
    {"time":4004,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"authoritative"}}},
    {"time":4005,"chunk":{"type":"text-delta","index":8,"text":"late"}},
    {"time":4006,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"wrong duplicate"}}},
    {"time":4007,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
  ],
  "packed": [
    {"type":"chunk","time":4000,"chunk":{"type":"block-start","index":8,"blockType":"text"}},
    {"type":"text-chunks","time0":4001,"index":8,"dt":[],"texts":["partial"]},
    {"type":"reasoning-chunks","time0":4002,"index":1,"dt":[],"texts":["thought"]},
    {"type":"text-chunks","time0":4003,"index":8,"dt":[],"texts":["more"]},
    {"type":"chunk","time":4004,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"authoritative"}}},
    {"type":"text-chunks","time0":4005,"index":8,"dt":[],"texts":["late"]},
    {"type":"chunk","time":4006,"chunk":{"type":"block-end","index":8,"block":{"type":"text","text":"wrong duplicate"}}},
    {"type":"chunk","time":4007,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
  ],
  "blocks": [{"type":"text","text":"authoritative"},{"type":"reasoning","text":"thought"}],
  "interruptedBlocks": [{"type":"text","text":"authoritative"},{"type":"reasoning","text":"thought"}],
  "finish": {"kind":"stop"}
}`),
		},
		{
			ID: "cancel-interrupted-blocks",
			Input: json.RawMessage(`{
  "timedChunks": [
    {"time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}},
    {"time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}},
    {"time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}},
    {"time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}},
    {"time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}},
    {"time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}},
    {"time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ],
  "packed": [
    {"type":"reasoning-chunks","time0":1010,"index":4,"dt":[],"texts":["先想一想"]},
    {"type":"text-chunks","time0":1011,"index":2,"dt":[],"texts":["已有前缀"]},
    {"type":"tool-call-chunks","time0":1012,"index":8,"dt":[],"id":"c-partial","name":"echo","args":["{\"text\":"]},
    {"type":"chunk","time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ],
  "blocks": [{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"},{"type":"tool-call","id":"c-partial","name":"echo","arguments":"{\"text\":"}],
  "interruptedBlocks": [{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"}],
  "finish": {"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}
}`),
		},
		{
			ID:    "delta-only-default-finish",
			Input: json.RawMessage(`{"timedChunks":[{"time":7000,"chunk":{"type":"text-delta","index":3,"text":"answer"}}]}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [{"time":7000,"chunk":{"type":"text-delta","index":3,"text":"answer"}}],
  "packed": [{"type":"text-chunks","time0":7000,"index":3,"dt":[],"texts":["answer"]}],
  "blocks": [{"type":"text","text":"answer"}],
  "interruptedBlocks": [{"type":"text","text":"answer"}],
  "finish": {"kind":"stop"}
}`),
		},
		{
			ID: "four-record-types",
			Input: json.RawMessage(`{
  "records": [
    {"type":"text-chunks","time0":3000,"index":7,"dt":[-2,5],"texts":["A","B","C"]},
    {"type":"reasoning-chunks","time0":3004,"index":2,"dt":[1],"texts":["想","想"]},
    {"type":"tool-call-chunks","time0":3006,"index":9,"dt":[0],"id":"c-rec","name":"echo","args":["{\"text\":","\"x\"}"]},
    {
      "type": "chunk",
      "time": 3007,
      "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1}}
    },
    {
      "type": "chunk",
      "time": 3008,
      "chunk": {
        "type": "finish",
        "reason": {"kind":"tool-calls"},
        "replayState": {"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}
      }
    }
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":3000,"chunk":{"type":"text-delta","index":7,"text":"A"}},
    {"time":2998,"chunk":{"type":"text-delta","index":7,"text":"B"}},
    {"time":3003,"chunk":{"type":"text-delta","index":7,"text":"C"}},
    {"time":3004,"chunk":{"type":"reasoning-delta","index":2,"text":"想"}},
    {"time":3005,"chunk":{"type":"reasoning-delta","index":2,"text":"想"}},
    {"time":3006,"chunk":{"type":"tool-call-delta","index":9,"id":"c-rec","name":"echo","argumentsDelta":"{\"text\":"}},
    {"time":3006,"chunk":{"type":"tool-call-delta","index":9,"id":"c-rec","name":"echo","argumentsDelta":"\"x\"}"}},
    {
      "time": 3007,
      "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1}}
    },
    {
      "time": 3008,
      "chunk": {
        "type": "finish",
        "reason": {"kind":"tool-calls"},
        "replayState": {"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}
      }
    }
  ],
  "packed": [
    {"type":"text-chunks","time0":3000,"index":7,"dt":[-2,5],"texts":["A","B","C"]},
    {"type":"reasoning-chunks","time0":3004,"index":2,"dt":[1],"texts":["想","想"]},
    {"type":"tool-call-chunks","time0":3006,"index":9,"dt":[0],"id":"c-rec","name":"echo","args":["{\"text\":","\"x\"}"]},
    {
      "type": "chunk",
      "time": 3007,
      "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1}}
    },
    {
      "type": "chunk",
      "time": 3008,
      "chunk": {
        "type": "finish",
        "reason": {"kind":"tool-calls"},
        "replayState": {"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}
      }
    }
  ],
  "blocks": [{"type":"text","text":"ABC"},{"type":"reasoning","text":"想想"},{"type":"tool-call","id":"c-rec","name":"echo","arguments":"{\"text\":\"x\"}"}],
  "interruptedBlocks": [{"type":"text","text":"ABC"},{"type":"reasoning","text":"想想"}],
  "finish": {"kind":"tool-calls"},
  "usage": {"inputTokens":2,"outputTokens":3,"totalTokens":12,"cacheReadTokens":5,"cacheWriteTokens":2,"reasoningTokens":1},
  "replayState": {"response":{"request":"r1"},"blocks":["text-replay","reasoning-replay","tool-replay"]}
}`),
		},
		{
			ID: "max-tokens-replay-pruning",
			Input: json.RawMessage(`{
  "timedChunks": [
    {"time":5000,"chunk":{"type":"text-delta","index":0,"text":"prefix"}},
    {"time":5001,"chunk":{"type":"tool-call-delta","index":1,"id":"c-truncated","name":"echo","argumentsDelta":"{broken"}},
    {"time":5002,"chunk":{"type":"finish","reason":{"kind":"max-tokens"},"replayState":{"response":{"id":"r2"},"blocks":["text-meta","tool-meta"]}}}
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":5000,"chunk":{"type":"text-delta","index":0,"text":"prefix"}},
    {"time":5001,"chunk":{"type":"tool-call-delta","index":1,"id":"c-truncated","name":"echo","argumentsDelta":"{broken"}},
    {"time":5002,"chunk":{"type":"finish","reason":{"kind":"max-tokens"},"replayState":{"response":{"id":"r2"},"blocks":["text-meta","tool-meta"]}}}
  ],
  "packed": [
    {"type":"text-chunks","time0":5000,"index":0,"dt":[],"texts":["prefix"]},
    {"type":"tool-call-chunks","time0":5001,"index":1,"dt":[],"id":"c-truncated","name":"echo","args":["{broken"]},
    {
      "type": "chunk",
      "time": 5002,
      "chunk": {"type":"finish","reason":{"kind":"max-tokens"},"replayState":{"response":{"id":"r2"},"blocks":["text-meta","tool-meta"]}}
    }
  ],
  "blocks": [{"type":"text","text":"prefix"}],
  "interruptedBlocks": [{"type":"text","text":"prefix"}],
  "finish": {"kind":"max-tokens"},
  "replayState": {"response":{"id":"r2"},"blocks":["text-meta"]}
}`),
		},
		{
			ID: "tool-delta-name-presence",
			Input: json.RawMessage(`{
  "timedChunks": [
    {"time":6000,"chunk":{"type":"tool-call-delta","index":0,"id":"c-names","name":"echo","argumentsDelta":"{"}},
    {"time":6001,"chunk":{"type":"tool-call-delta","index":0,"id":"c-names","argumentsDelta":"}"}},
    {"time":6002,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":6000,"chunk":{"type":"tool-call-delta","index":0,"id":"c-names","name":"echo","argumentsDelta":"{"}},
    {"time":6001,"chunk":{"type":"tool-call-delta","index":0,"id":"c-names","argumentsDelta":"}"}},
    {"time":6002,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
  ],
  "packed": [
    {"type":"tool-call-chunks","time0":6000,"index":0,"dt":[],"id":"c-names","name":"echo","args":["{"]},
    {"type":"tool-call-chunks","time0":6001,"index":0,"dt":[],"id":"c-names","args":["}"]},
    {"type":"chunk","time":6002,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
  ],
  "blocks": [{"type":"tool-call","id":"c-names","name":"echo","arguments":"{}"}],
  "interruptedBlocks": [],
  "finish": {"kind":"tool-calls"}
}`),
		},
		{
			ID: "whitespace-cancel-prefix",
			Input: json.RawMessage(`{
  "timedChunks": [
    {"time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}},
    {"time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "expanded": [
    {"time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}},
    {"time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ],
  "packed": [
    {"type":"text-chunks","time0":1010,"index":0,"dt":[],"texts":[" \n"]},
    {"type":"chunk","time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
  ],
  "blocks": [{"type":"text","text":" \n"}],
  "interruptedBlocks": [],
  "finish": {"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}
}`),
		},
	}
}

func TestStreamAccumulatorTextReasoningToolCallUsage(t *testing.T) {
	call := protocol.ToolCall{
		ID:        "call-1",
		Name:      "echo",
		Arguments: `{"text":"hi"}`,
	}
	chunks := []protocol.StreamChunk{
		{
			Type:  protocol.ChunkReasoningDelta,
			Index: 1,
			Text:  "think",
		},
		{
			Type:  protocol.ChunkBlockEnd,
			Index: 1,
			Block: protocol.ReasoningBlock{Text: "think"},
		},
		{
			Type:  protocol.ChunkTextDelta,
			Index: 0,
			Text:  "answer",
		},
		{
			Type:  protocol.ChunkBlockEnd,
			Index: 0,
			Block: protocol.TextBlock{Text: "answer"},
		},
		{
			Type:           protocol.ChunkToolCallDelta,
			Index:          2,
			ID:             call.ID,
			Name:           &call.Name,
			ArgumentsDelta: call.Arguments,
		},
		{
			Type:  protocol.ChunkBlockEnd,
			Index: 2,
			Block: protocol.ToolCallBlock{ToolCall: call},
		},
		{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 2, OutputTokens: 3}},
		{
			Type:   protocol.ChunkFinish,
			Reason: protocol.FinishReason{Kind: protocol.FinishToolCalls},
		},
	}
	result, err := Accumulate("message-1", sequence(chunks, nil), modelSource())
	if err != nil {
		t.Fatal(err)
	}

	if result.Finish.Kind != protocol.FinishToolCalls ||
		!reflect.DeepEqual(result.Usage, &protocol.Usage{InputTokens: 2, OutputTokens: 3}) {
		t.Fatalf("finish/usage = %+v/%+v", result.Finish, result.Usage)
	}

	if got := []protocol.BlockKind{
		result.Message.Content[0].Kind(),
		result.Message.Content[1].Kind(),
		result.Message.Content[2].Kind(),
	}; !reflect.DeepEqual(got, []protocol.BlockKind{protocol.BlockReasoning, protocol.BlockText, protocol.BlockToolCall}) {
		t.Fatalf("block kinds = %v", got)
	}
}

func TestAccumulatorAllowsDeltaOnlyAndDefaultStop(t *testing.T) {
	result, err := Accumulate("message-1", sequence([]protocol.StreamChunk{{
		Type:  protocol.ChunkTextDelta,
		Index: 7,
		Text:  "hello",
	}}, nil), modelSource())
	if err != nil ||
		result.Finish.Kind != protocol.FinishStop ||
		result.Message.Content[0].(protocol.TextBlock).Text != "hello" {
		t.Fatalf("result/error = %+v/%v", result, err)
	}
}

func TestAccumulatorRejectsInvalidChunkAndAfterFinish(t *testing.T) {
	for _, chunks := range [][]protocol.StreamChunk{
		{{
			Type:  protocol.ChunkTextDelta,
			Index: -1,
			Text:  "bad",
		}},
		{{Type: protocol.ChunkBlockEnd, Index: 0}},
		{
			{Type: protocol.ChunkFinish, Reason: protocol.FinishReason{Kind: protocol.FinishStop}},
			{Type: protocol.ChunkUsage, Usage: &protocol.Usage{InputTokens: 1}},
		},
	} {
		if _, err := Accumulate("message-1", sequence(chunks, nil), modelSource()); err == nil {
			t.Fatal("invalid stream accepted")
		}
	}
}

func modelSource() protocol.MessageSource {
	return protocol.MessageSource{
		Kind:     protocol.SourceModel,
		Provider: "test",
		Model:    "test",
	}
}

func sequence(chunks []protocol.StreamChunk, finalErr error) func(func(protocol.StreamChunk, error) bool) {
	return func(yield func(protocol.StreamChunk, error) bool) {
		for _, chunk := range chunks {
			if !yield(chunk, nil) {
				return
			}
		}

		if finalErr != nil {
			yield(protocol.StreamChunk{}, finalErr)
		}
	}
}

func TestAccumulatorPrunesMismatchedReplayAndAttempts(t *testing.T) {
	a, _ := NewAccumulator("message", modelSource())
	name := "echo"

	for _, chunk := range []protocol.StreamChunk{
		{
			Type:  protocol.ChunkTextDelta,
			Index: 5,
			Text:  " \n",
		},
		{
			Type:           protocol.ChunkToolCallDelta,
			Index:          0,
			ID:             "c",
			Name:           &name,
			ArgumentsDelta: "invalid JSON",
		},
		{
			Type:  protocol.ChunkReasoningDelta,
			Index: 3,
			Text:  "prefix",
		},
		{
			Type:        protocol.ChunkFinish,
			Reason:      protocol.FinishReason{Kind: protocol.FinishMaxTokens},
			ReplayState: &protocol.ReplayEnvelope{Response: map[string]any{"r": "one"}, Blocks: []any{"wrong-count"}},
		},
	} {
		if err := a.Accept(chunk); err != nil {
			t.Fatal(err)
		}
	}

	result, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Message.Content) != 2 || result.Message.Source.ReplayState != nil {
		t.Fatalf("result=%+v", result)
	}

	attempt := a.Attempt()
	if attempt == nil ||
		len(attempt.Content) != 1 ||
		attempt.Content[0].Kind() != protocol.BlockReasoning ||
		attempt.Source.ReplayState != nil {
		t.Fatalf("attempt=%+v", attempt)
	}
}
