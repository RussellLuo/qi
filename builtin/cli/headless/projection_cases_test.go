package headless

import (
	"encoding/json"
	"strconv"
	"strings"
)

type projectionCase struct {
	ID, Kind, SessionID, CWD, Final, Want string
	Events                                []json.RawMessage
	Payload                               json.RawMessage
	MaxStringBytes, MaxEventBytes         int
}

func projectionCases() []projectionCase {
	numbers := make([]string, 20000)
	for i := range numbers {
		numbers[i] = strconv.Itoa(i)
	}
	return []projectionCase{
		{
			ID:        "ordinary-tools",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `answer`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"turn/start","seq":10,"time":1,"data":{"turn":1}}`),
				json.RawMessage(`{"type":"step/start","seq":11,"time":1,"data":{"turn":1,"step":1}}`),
				json.RawMessage(`{"type":"tool/call","seq":12,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{\"text\":\"hi\"}"}}`),
				json.RawMessage(`{
  "type": "tool/result",
  "seq": 13,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {
      "id": "r",
      "role": "tool",
      "source": {"kind":"tool","callId":"c"},
      "toolCallId": "c",
      "isError": false,
      "content": [{"type":"text","text":"a"},{"type":"tool-addition","toolName":"echo"},{"type":"text","text":"b"}]
    },
    "meta": {"private":"ignored"}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":14,"time":1,"data":{"turn":1,"step":1}}`),
				json.RawMessage(`{"type":"step/start","seq":15,"time":1,"data":{"turn":1,"step":2}}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 16,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {
      "id": "m",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [
        {"type":"reasoning","text":"think"},
        {"type":"text","text":""},
        {"type":"tool-call","id":"c","name":"echo","arguments":"{}"},
        {"type":"text","text":"answer"}
      ]
    },
    "stream": [],
    "usage": {"inputTokens":3,"outputTokens":4}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":17,"time":1,"data":{"turn":1,"step":2}}`),
				json.RawMessage(`{"type":"turn/end","seq":18,"time":1,"data":{"turn":1,"reason":{"kind":"completed"}}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"turn_start","turn":1}` + "\n" +
				`{"type":"status","phase":"step_start","turn":1,"step":1}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":{"text":"hi"}}` + "\n" +
				`{"type":"tool_result","callId":"c","status":"completed","result":"ab"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1}` + "\n" +
				`{"type":"status","phase":"step_start","turn":1,"step":2}` + "\n" +
				`{"type":"thinking","text":"think"}` + "\n" +
				`{"type":"text","text":""}` + "\n" +
				`{"type":"text","text":"answer"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":2,"usage":{"inputTokens":3,"outputTokens":4}}` + "\n" +
				`{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"completed"}}` + "\n" +
				`{"type":"final","text":"answer"}` + "\n",
		},
		{
			ID:        "error-final",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     "",
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"turn/start","seq":10,"time":1,"data":{"turn":1}}`),
				json.RawMessage(`{"type":"turn/end","seq":11,"time":1,"data":{"turn":1,"reason":{"kind":"error","error":{"code":"failed","message":"failure"}}}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"turn_start","turn":1}` + "\n" +
				`{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"error","error":{"code":"failed","message":"failure"}}}` + "\n" +
				`{"type":"final","text":""}` + "\n",
		},
		{
			ID:        "multi-turn-empty",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     "",
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"turn/start","seq":10,"time":1,"data":{"turn":1}}`),
				json.RawMessage(`{"type":"turn/end","seq":11,"time":1,"data":{"turn":1,"reason":{"kind":"completed"}}}`),
				json.RawMessage(`{"type":"turn/start","seq":12,"time":1,"data":{"turn":2}}`),
				json.RawMessage(`{"type":"turn/end","seq":13,"time":1,"data":{"turn":2,"reason":{"kind":"aborted","reason":{"kind":"parent"}}}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"turn_start","turn":1}` + "\n" +
				`{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"completed"}}` + "\n" +
				`{"type":"status","phase":"turn_start","turn":2}` + "\n" +
				`{"type":"status","phase":"turn_end","turn":2,"reason":{"kind":"aborted","reason":{"kind":"parent"}}}` + "\n" +
				`{"type":"final","text":""}` + "\n",
		},
		{
			ID:        "replace-and-error",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "tool/result",
  "seq": 10,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {
      "id": "r",
      "role": "tool",
      "source": {"kind":"tool","callId":"c"},
      "toolCallId": "c",
      "isError": false,
      "content": [{"type":"text","text":"a"},{"type":"tool-addition","toolName":"echo"},{"type":"text","text":"b"}]
    },
    "meta": {"private":"ignored"}
  },
  "surfaceOp": {"op":"replace","startSeq":0,"endSeq":1}
}`),
				json.RawMessage(`{
  "type": "tool/result",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {
      "id": "r",
      "role": "tool",
      "source": {"kind":"tool","callId":"c"},
      "toolCallId": "c",
      "isError": true,
      "content": [{"type":"text","text":"a"},{"type":"tool-addition","toolName":"echo"},{"type":"text","text":"b"}]
    },
    "meta": {"private":"ignored"},
    "error": {"name":"ToolError","code":"failed","reason":"private error"}
  },
  "surfaceOp": "append"
}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_result","callId":"c","status":"error","result":"ab"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "retry-commit-only",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {"turn":1,"step":1,"stream":[{"type":"text-chunks","time0":1,"index":0,"dt":[],"texts":["discarded"]}]}
}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"committed"}]},
    "stream": []
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"text","text":"committed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "ignore-other-events",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"session/title","seq":10,"time":1,"data":{"title":"ignored"},"ignorable":true}`),
				json.RawMessage(`{"type":"custom/optional","seq":11,"time":1,"data":{"unknown":1},"ignorable":true}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-retry",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "stream": [{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":10,"outputTokens":2,"totalTokens":12,"cacheReadTokens":4}}}]
  }
}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"ok"}]},
    "stream": [],
    "usage": {"inputTokens":3,"outputTokens":1,"totalTokens":4}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
				json.RawMessage(`{"type":"step/end","seq":13,"time":1,"data":{"turn":1,"step":2}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"text","text":"ok"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":13,"outputTokens":3,"totalTokens":16}}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":2}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-all-buckets",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "stream": [
      {
        "type": "chunk",
        "time": 1,
        "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}}
      }
    ]
  }
}`),
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "stream": [
      {
        "type": "chunk",
        "time": 1,
        "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}}
      }
    ]
  }
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":4,"outputTokens":2,"totalTokens":6,"cacheReadTokens":2,"cacheWr` +
				`iteTokens":2,"reasoningTokens":2}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-later-bucket",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {"turn":1,"step":1,"stream":[{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":1,"outputTokens":1}}}]}
}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},
    "stream": [],
    "usage": {"inputTokens":1,"outputTokens":1,"cacheWriteTokens":2}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":2,"outputTokens":2}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-missing-earlier",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"assistant/attempt","seq":10,"time":1,"data":{"turn":1,"step":1,"stream":[]}}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 11,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},
    "stream": [],
    "usage": {"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-missing-later",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "stream": [
      {
        "type": "chunk",
        "time": 1,
        "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}}
      }
    ]
  }
}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 11,
  "time": 1,
  "data": {"turn":1,"step":1,"message":{"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},"stream":[]},
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-last-and-override",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 10,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},
    "stream": [
      {
        "type": "chunk",
        "time": 1,
        "chunk": {"type":"usage","usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}}
      },
      {"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":3,"outputTokens":1,"totalTokens":4}}}
    ]
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":11,"time":1,"data":{"turn":1,"step":1}}`),
				json.RawMessage(`{
  "type": "assistant/message",
  "seq": 12,
  "time": 1,
  "data": {
    "turn": 1,
    "step": 1,
    "message": {"id":"m","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[]},
    "stream": [{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":3,"outputTokens":1,"totalTokens":4}}}],
    "usage": {"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWriteTokens":1,"reasoningTokens":1}
  },
  "surfaceOp": "append"
}`),
				json.RawMessage(`{"type":"step/end","seq":13,"time":1,"data":{"turn":1,"step":2}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":3,"outputTokens":1,"totalTokens":4}}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":2,"usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3,"cacheReadTokens":1,"cacheWr` +
				`iteTokens":1,"reasoningTokens":1}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "usage-safe-integer-sum",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 10,
  "time": 1,
  "data": {"turn":1,"step":1,"stream":[{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":9007199254740991,"outputTokens":1}}}]}
}`),
				json.RawMessage(`{
  "type": "assistant/attempt",
  "seq": 11,
  "time": 1,
  "data": {"turn":1,"step":1,"stream":[{"type":"chunk","time":1,"chunk":{"type":"usage","usage":{"inputTokens":2,"outputTokens":1}}}]}
}`),
				json.RawMessage(`{"type":"step/end","seq":12,"time":1,"data":{"turn":1,"step":1}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"status","phase":"step_end","turn":1,"step":1,"usage":{"inputTokens":9007199254740992,"outputTokens":2}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-0",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":""}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":{}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-1",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"  "}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":"  "}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-2",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{oops"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":"{oops"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-3",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"1e400"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":"1e400"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-4",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{\"n\":1e400}"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":"{\"n\":1e400}"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-5",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{\"n\":1e400,\"n\":1}"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":{"n":1}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-6",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"9007199254740993"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":9007199254740992}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-7",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"null"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":null}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-8",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"[1,true,\"x\"]"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":[1,true,"x"]}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-9",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"true"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":true}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-10",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"\"scalar\""}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":"scalar"}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-11",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{\"a\":1,\"a\":2,\"__proto__\":{\"x\":1}}"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":{"a":2,"__proto__":{"x":1}}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-12",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"-0"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":0}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-13",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"1e-7"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":1e-7}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-14",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"1e21"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":1e+21}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-15",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"5e-324"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":5e-324}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-16",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"1.7976931348623157e308"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":1.7976931348623157e+308}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "arguments-17",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"{\"s\":\"\\ud800\",\"\\udfff\":1}"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":{"s":"\ud800","\udfff":1}}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:        "binary64-number-corpus",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     `done`,
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"tool/call","seq":10,"time":1,"data":{"turn":1,"step":1,"callId":"c","name":"echo","arguments":"[-1.6602278812080957e-145,1.84197830` +
					`8329735e-178,4.052184489818676e+60,1.9432074656054558e-171,-3.813424772434269e-156,-1.486015218826819e+127,4.5960171494881736e+25,9.58207712` +
					`0967826e-87,-2.3936108156132162e-293,2.697667982809639e-93,-6.226871111445136,2.8592198117126124e-251,-1.0277367402713131e-121,5.84048173396` +
					`2764e-196,4226.6123648030525,-1.1116805665847714e+101,6.9596219554830885e-196,-3.017208405533001e+149,-9.270853192862114e-74,5.0200230178641` +
					`15e-300,4.696678330512516e-85,-4.62054322728846e-156,137839762462415.62,-3.109070130580795e+40,1.735463734239114e-253,-4.289693428727573e+13` +
					`9,-1.6869444775594857e+112,2.301854237843658e+272,-2.1588529878952583e-255,-4.061030880949195e-111,-9.486859928665802e+52,-1.461095688332968` +
					`2e+229,6.087579882506885e+99,7.515294838595918e-248,-1.5359005600354034e-73,-4.3617164261642953e-141,7.305212905006197e-171,6.13396055589707` +
					`5e-177,1.5819416483426747e-106,-8.81807084137668e+108,8.387672284039237e-28,8.730697044510162e-193,1.0081083637568873e-169,-2.00442163540862` +
					`17e+245,-1.231832035949786e+282,6.030082863310974e+155,1.6797560361590916e-271,7.702220981256911e-212,2.1199188359945697e+271,1.410947062648` +
					`0928e+265,5.643030913674924e-79,-5.669171799811774e+65,4.794066798551883e-282,2.7843823381963514e-58,93.55647381005303,1.237039403834357e-26` +
					`4,-2.1280996675935714e-167,-2.239918811080225e+153,1.080356218708711e-29,1.893480306270067e+179,3.2413902370319646e-292,-6612.237841151426,-` +
					`6.27314230309775e+236,-1.0627259512579456e+180,4.4142736117086283e+39,1.2675996542653863e+198,9.6284366984931e+185,-2.0570601502825238e-277,` +
					`-1.1907992403611036e+168,7.812923564492689e-98,-7.910324065345395e-303,-7.257029309591252e+77,9.298189779775748e-17,1.4929668323515957e+155,` +
					`-6.6963291795796295e+84,-1.7270655609664305e+125,-2.969635913407189e+217,1.0591335525241445e-55,1.161202289550604e-93,2.468222684129948e+64,` +
					`-6.374803736573062e-254,-6.241165348635095e+74,-8.515049106780359e+198,1.2718610663494915e-140,-4.120093184899565e-177,1.8756347647958043e+5` +
					`0,-7.2584365119744e+26,2.220680805205478e-157,2.8961699938053915e+167,6.250749772630148e-49,-1.5684477644940072e+196,-2.037945469110652e+51,` +
					`-7.554728485823299e-238,-6.149535088425504e+301,-7.816906261968974e-154,-2.997906403054357e-66,3922.1760173005177,-3.36357870335698e-42,-632` +
					`6779352471428,3.1938644411918396e+201,-1.0353708034674305e+250,1.160301511480752e-61,-1.2425255103255342e-125,-1.3551524926442785e+187,-9.13` +
					`5576100124212e+218,1.9997892694235252e+163,-8.02686258044865e-240,4.102418248487499e+140,1.5564772898666758e-16,-6.480891342485161e+30,4.746` +
					`1212341979656e+117,2.7368291957105324e-239,-1.8415926259024432e-212,2.768909814344076e-115,-2.4868435225792397e+285,5642796674720915,1537878` +
					`01215.7199,-5.740103560886015e+32,-5.348755762389177e-257,1.5035414508375754e-226,-9.319776757511413e-297,-7.177444667089622e-145,-1.4201746` +
					`50199859e+219,-4.757176009991129e-29,-3.985861808918559e-228,-1.9052541043489516e+27,2.1561907403131063e+186,-1.3582000257814076e-203,-1.293` +
					`5547028868314e-150,2.776833935653783e-113,16695220.145699568,-5.15793579831574e-303,5.246626699757044e+98,2.8487003995112563e+291,-4.0635828` +
					`31388387e+92,-2.653764131515657e+113,3.680106281277774e-216,-1.0694312004706226e-9,-1.398757292195943e+198,-1.2337746474556746e+172,-3.44264` +
					`33239803584e-157,3.889991319106817e+287,-3.3503436674732272e-124,5.1288562741728335e-294,-7.5829763882669445e-298,-1.5665375453996978e-224,-` +
					`5.0514392403647726e+277,2.5920578344116816e+200,3.8980975541333936e-144,-1366157335499.0696,1.0812217352963537e+151,-8.552325537342577e-247,` +
					`-2.1991022728319803e-275,-3.09732428000144e-65,-6.669528229702698e+30,-2.359501108662232e+155,4.976294506293988e+62,4.93943748571977e-96,-4.` +
					`993556461423871e+85,-7.22171833819392e+78,3.354612237696038e+303,-4.697182829988476e-307,-4.893132148025659e-188,-4.8699904806727105e-292,3.` +
					`476431957686789e-71,1.3909073305826453e+200,-7.723590242862078e-237,-7.308474283652162e+43,1.3913873710734933e-175,1.1214518585684098e-287,-` +
					`1.2383472537562705e-141,-1.8864559294756376e-162,-1.368117601305175e+46,4.622742025368585e+287,2.358978029065407e+71,-3.070080294192739e+121` +
					`,6.101200835071364e+172,7.250791586442873e+28,-7.932055528756304e-104,-7.262417097698302e+182,2.4092893948561122e+173,3.5333228765839003e+70` +
					`,1.0893318035070314e+299,9.791635476112972e+259,-4.685892272462387e-157,-4.633977429525127e+166,9.229357851553531e-47,4.1955057625399795e+18` +
					`0,-4.98526189279989e-164,3.1534394356827815e+224,-2.6349161355741647e-181,-6.358843392812917e+250,2.4940385440240556e+193,1.4660815896640772` +
					`e-168,-2.3461771555826166e+165,4.0839826071829704e-280,-4.268606005200114e-250,3.206510362508435e-291,1.828175745882115e+72,1.35942110472598` +
					`98e-255,-4.2011760636201614e-110,8.859459584887021e-168,5.87285618371831e+171,-6.138947092553481e-295,-1.6978321889690225e+221,5.88766022351` +
					`1905e-74,5.9726228157688145e-93,-2.4419350843715518e+241,1.701948845281814e+128,1.9629160025581463e+52,-144.90333307571348,-1.75122042240513` +
					`52e+214,-2.1723692609556928e+220,-9.500943197876567e+280,1.2393453053117257e-111,-1.0132387186435111e-87,2.224856855513578e-308,1.6090513722` +
					`904247e+173,3.256496362783322e-290,-6.28522308258554e+166,-2.997919882698011e-293,-3.020134167997346e-157,5.029943459430422e-284,-1.01435621` +
					`50968417e+297,6.331944041755464e+202,6.268688617695669e-268,6.212435182086192e+291,-1.1119173602932997e+242,3.8715102521818945e-171,1.881451` +
					`995176535e+200,-9.375991795394695e-115,4.8663406751500586e+85,2.5810661376175684e-289,-4.901726081928758e-67,-8.402616043930314e+288,-2.2386` +
					`945802012133e+283,-8.173669415651568e-78,-6.99646404608607e+108,-5796572647.426178,1.716536637537916e+199,-2.721451908737134e-71,1.743938691` +
					`9703716e+38,-2.6758794575722118e-219,-8.069726046611361e+43,-130.8244634872146,-2.8233234123740646e-216,4.6232975816032116e+39,-9.1620057914` +
					`33136e-32,-7.84551545200061e-42,1.7833255032360453e+133,-4.341966201268767e-237,-1.2362234129291069e-228,-3.0135379135717476e+137,1.32896292` +
					`06585313e+24,6.640965068061132e-119,-1.6704190193157748e-227,-6.779541739289421e+130,-5.2939314117114404e-303,8.68802654860366e+271,-1.83756` +
					`55855826744e+101,-7.3825873439402675e+301,-2.650963069219953e+263,12352.063368168005,-2.1166881186633964e+60,-6.811325055517824e-188,-716.18` +
					`5400105389,-8.34138334393833e+177,1.5928088914690915e-302,-1.1211092625533602e-181,1.6345973589919753e+55,3.902160842432061e-30,4.3862230756` +
					`11851e+86,-4.587845216066382e+51,-3.223432943957856e+90,-1.986264136355627e+289,-1.469586571977811e+71,5.108484148469347e-300,2.378248601698` +
					`9163e-126,-1.9538025065946406e+174,2.3241613176712115e+182,8.75379615119685e+171,1.2195326745143765e+212,3.3420438402543726e+283,-1.18201313` +
					`3651281e-261,7.85290161080761e-235,1.20545808434536e+200,-3.1468133608533995e-251,-9.91322341870608e+31,2.1496178748565897e+50,1.52173736879` +
					`5013e+63,-4.5616339696080845e-20,4.7143786200037044e+290,-5.465651680495812e-219,6.020001711543058e-166,8.157950475172453e-137,1.91670515919` +
					`2834e-245,-9.985189787747701e-219,6.555612398258881e-103,1.6603665566525916e+162]"}}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"tool_call","callId":"c","tool":"echo","input":[-1.6602278812080957e-145,1.841978308329735e-178,4.052184489818676e+60,1.943207465605` +
				`4558e-171,-3.813424772434269e-156,-1.486015218826819e+127,4.5960171494881736e+25,9.582077120967826e-87,-2.3936108156132162e-293,2.6976679828` +
				`09639e-93,-6.226871111445136,2.8592198117126124e-251,-1.0277367402713131e-121,5.840481733962764e-196,4226.6123648030525,-1.1116805665847714e` +
				`+101,6.9596219554830885e-196,-3.017208405533001e+149,-9.270853192862114e-74,5.020023017864115e-300,4.696678330512516e-85,-4.62054322728846e-` +
				`156,137839762462415.62,-3.109070130580795e+40,1.735463734239114e-253,-4.289693428727573e+139,-1.6869444775594857e+112,2.301854237843658e+272` +
				`,-2.1588529878952583e-255,-4.061030880949195e-111,-9.486859928665802e+52,-1.4610956883329682e+229,6.087579882506885e+99,7.515294838595918e-2` +
				`48,-1.5359005600354034e-73,-4.3617164261642953e-141,7.305212905006197e-171,6.133960555897075e-177,1.5819416483426747e-106,-8.81807084137668e` +
				`+108,8.387672284039237e-28,8.730697044510162e-193,1.0081083637568873e-169,-2.0044216354086217e+245,-1.231832035949786e+282,6.030082863310974` +
				`e+155,1.6797560361590916e-271,7.702220981256911e-212,2.1199188359945697e+271,1.4109470626480928e+265,5.643030913674924e-79,-5.66917179981177` +
				`4e+65,4.794066798551883e-282,2.7843823381963514e-58,93.55647381005303,1.237039403834357e-264,-2.1280996675935714e-167,-2.239918811080225e+15` +
				`3,1.080356218708711e-29,1.893480306270067e+179,3.2413902370319646e-292,-6612.237841151426,-6.27314230309775e+236,-1.0627259512579456e+180,4.` +
				`4142736117086283e+39,1.2675996542653863e+198,9.6284366984931e+185,-2.0570601502825238e-277,-1.1907992403611036e+168,7.812923564492689e-98,-7` +
				`.910324065345395e-303,-7.257029309591252e+77,9.298189779775748e-17,1.4929668323515957e+155,-6.6963291795796295e+84,-1.7270655609664305e+125,` +
				`-2.969635913407189e+217,1.0591335525241445e-55,1.161202289550604e-93,2.468222684129948e+64,-6.374803736573062e-254,-6.241165348635095e+74,-8` +
				`.515049106780359e+198,1.2718610663494915e-140,-4.120093184899565e-177,1.8756347647958043e+50,-7.2584365119744e+26,2.220680805205478e-157,2.8` +
				`961699938053915e+167,6.250749772630148e-49,-1.5684477644940072e+196,-2.037945469110652e+51,-7.554728485823299e-238,-6.149535088425504e+301,-` +
				`7.816906261968974e-154,-2.997906403054357e-66,3922.1760173005177,-3.36357870335698e-42,-6326779352471428,3.1938644411918396e+201,-1.03537080` +
				`34674305e+250,1.160301511480752e-61,-1.2425255103255342e-125,-1.3551524926442785e+187,-9.135576100124212e+218,1.9997892694235252e+163,-8.026` +
				`86258044865e-240,4.102418248487499e+140,1.5564772898666758e-16,-6.480891342485161e+30,4.7461212341979656e+117,2.7368291957105324e-239,-1.841` +
				`5926259024432e-212,2.768909814344076e-115,-2.4868435225792397e+285,5642796674720915,153787801215.7199,-5.740103560886015e+32,-5.348755762389` +
				`177e-257,1.5035414508375754e-226,-9.319776757511413e-297,-7.177444667089622e-145,-1.420174650199859e+219,-4.757176009991129e-29,-3.985861808` +
				`918559e-228,-1.9052541043489516e+27,2.1561907403131063e+186,-1.3582000257814076e-203,-1.2935547028868314e-150,2.776833935653783e-113,1669522` +
				`0.145699568,-5.15793579831574e-303,5.246626699757044e+98,2.8487003995112563e+291,-4.063582831388387e+92,-2.653764131515657e+113,3.6801062812` +
				`77774e-216,-1.0694312004706226e-9,-1.398757292195943e+198,-1.2337746474556746e+172,-3.4426433239803584e-157,3.889991319106817e+287,-3.350343` +
				`6674732272e-124,5.1288562741728335e-294,-7.5829763882669445e-298,-1.5665375453996978e-224,-5.0514392403647726e+277,2.5920578344116816e+200,3` +
				`.8980975541333936e-144,-1366157335499.0696,1.0812217352963537e+151,-8.552325537342577e-247,-2.1991022728319803e-275,-3.09732428000144e-65,-6` +
				`.669528229702698e+30,-2.359501108662232e+155,4.976294506293988e+62,4.93943748571977e-96,-4.993556461423871e+85,-7.22171833819392e+78,3.35461` +
				`2237696038e+303,-4.697182829988476e-307,-4.893132148025659e-188,-4.8699904806727105e-292,3.476431957686789e-71,1.3909073305826453e+200,-7.72` +
				`3590242862078e-237,-7.308474283652162e+43,1.3913873710734933e-175,1.1214518585684098e-287,-1.2383472537562705e-141,-1.8864559294756376e-162,` +
				`-1.368117601305175e+46,4.622742025368585e+287,2.358978029065407e+71,-3.070080294192739e+121,6.101200835071364e+172,7.250791586442873e+28,-7.` +
				`932055528756304e-104,-7.262417097698302e+182,2.4092893948561122e+173,3.5333228765839003e+70,1.0893318035070314e+299,9.791635476112972e+259,-` +
				`4.685892272462387e-157,-4.633977429525127e+166,9.229357851553531e-47,4.1955057625399795e+180,-4.98526189279989e-164,3.1534394356827815e+224,` +
				`-2.6349161355741647e-181,-6.358843392812917e+250,2.4940385440240556e+193,1.4660815896640772e-168,-2.3461771555826166e+165,4.0839826071829704` +
				`e-280,-4.268606005200114e-250,3.206510362508435e-291,1.828175745882115e+72,1.3594211047259898e-255,-4.2011760636201614e-110,8.85945958488702` +
				`1e-168,5.87285618371831e+171,-6.138947092553481e-295,-1.6978321889690225e+221,5.887660223511905e-74,5.9726228157688145e-93,-2.44193508437155` +
				`18e+241,1.701948845281814e+128,1.9629160025581463e+52,-144.90333307571348,-1.7512204224051352e+214,-2.1723692609556928e+220,-9.5009431978765` +
				`67e+280,1.2393453053117257e-111,-1.0132387186435111e-87,2.224856855513578e-308,1.6090513722904247e+173,3.256496362783322e-290,-6.28522308258` +
				`554e+166,-2.997919882698011e-293,-3.020134167997346e-157,5.029943459430422e-284,-1.0143562150968417e+297,6.331944041755464e+202,6.2686886176` +
				`95669e-268,6.212435182086192e+291,-1.1119173602932997e+242,3.8715102521818945e-171,1.881451995176535e+200,-9.375991795394695e-115,4.86634067` +
				`51500586e+85,2.5810661376175684e-289,-4.901726081928758e-67,-8.402616043930314e+288,-2.2386945802012133e+283,-8.173669415651568e-78,-6.99646` +
				`404608607e+108,-5796572647.426178,1.716536637537916e+199,-2.721451908737134e-71,1.7439386919703716e+38,-2.6758794575722118e-219,-8.069726046` +
				`611361e+43,-130.8244634872146,-2.8233234123740646e-216,4.6232975816032116e+39,-9.162005791433136e-32,-7.84551545200061e-42,1.783325503236045` +
				`3e+133,-4.341966201268767e-237,-1.2362234129291069e-228,-3.0135379135717476e+137,1.3289629206585313e+24,6.640965068061132e-119,-1.6704190193` +
				`157748e-227,-6.779541739289421e+130,-5.2939314117114404e-303,8.68802654860366e+271,-1.8375655855826744e+101,-7.3825873439402675e+301,-2.6509` +
				`63069219953e+263,12352.063368168005,-2.1166881186633964e+60,-6.811325055517824e-188,-716.185400105389,-8.34138334393833e+177,1.5928088914690` +
				`915e-302,-1.1211092625533602e-181,1.6345973589919753e+55,3.902160842432061e-30,4.386223075611851e+86,-4.587845216066382e+51,-3.2234329439578` +
				`56e+90,-1.986264136355627e+289,-1.469586571977811e+71,5.108484148469347e-300,2.3782486016989163e-126,-1.9538025065946406e+174,2.324161317671` +
				`2115e+182,8.75379615119685e+171,1.2195326745143765e+212,3.3420438402543726e+283,-1.182013133651281e-261,7.85290161080761e-235,1.205458084345` +
				`36e+200,-3.1468133608533995e-251,-9.91322341870608e+31,2.1496178748565897e+50,1.521737368795013e+63,-4.5616339696080845e-20,4.71437862000370` +
				`44e+290,-5.465651680495812e-219,6.020001711543058e-166,8.157950475172453e-137,1.916705159192834e-245,-9.985189787747701e-219,6.5556123982588` +
				`81e-103,1.6603665566525916e+162]}` + "\n" +
				`{"type":"final","text":"done"}` + "\n",
		},
		{
			ID:             "key-collision",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"tool_call","input":{"abcdefghZ":1,"abcdefghA":2}}`),
			MaxStringBytes: 8,
			MaxEventBytes:  32768,
			Want:           `{"type":"tool_cal","input":{"abcdefgh":2},"truncated":true}` + "\n",
		},
		{
			ID:             "index-key-order",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"tool_call","input":{"2":4,"10":3,"10longer":1,"2longer":2}}`),
			MaxStringBytes: 2,
			MaxEventBytes:  32768,
			Want:           `{"ty":"to","in":{"2":4,"10":1,"2l":2},"truncated":true}` + "\n",
		},
		{
			ID:             "literal-proto",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"tool_call","input":{"__proto__":{"x":1}}}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want:           `{"type":"tool_call","input":{"__proto__":{"x":1}}}` + "\n",
		},
		{
			ID:             "split-utf8",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"text","text":"\u00e9\u00e9\u00e9"}`),
			MaxStringBytes: 5,
			MaxEventBytes:  32768,
			Want:           `{"type":"text","text":"éé","truncated":true}` + "\n",
		},
		{
			ID:             "replacement-at-cut",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"text","text":"ab\ufffdcd"}`),
			MaxStringBytes: 5,
			MaxEventBytes:  32768,
			Want:           `{"type":"text","text":"ab","truncated":true}` + "\n",
		},
		{
			ID:             "surrogate-utf8",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"text","text":"\ud800abc"}`),
			MaxStringBytes: 3,
			MaxEventBytes:  32768,
			Want:           `{"typ":"tex","tex":"","truncated":true}` + "\n",
		},
		{
			ID:             "unicode-no-escaping",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"text","text":"<>&\u2028\u2029\u96ea\ud83d\ude00"}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want:           `{"type":"text","text":"<>&  雪😀"}` + "\n",
		},
		{
			ID:   "control-string-line-fallback",
			Kind: "bound",
			Payload: json.RawMessage(`{"type":"error","message":"` +
				strings.Repeat("\\u0000", 8192) +
				`"}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want:           `{"type":"error","truncated":true}` + "\n",
		},
		{
			ID:             "structured-line-fallback",
			Kind:           "bound",
			Payload:        json.RawMessage("{\"type\":\"tool_call\",\"callId\":\"c\",\"tool\":\"echo\",\"input\":[" + strings.Join(numbers, ",") + "]}"),
			MaxStringBytes: 8192,
			MaxEventBytes:  1024,
			Want:           `{"type":"tool_call","callId":"c","tool":"echo","truncated":true}` + "\n",
		},
		{
			ID:   "scalar-line-fallback",
			Kind: "bound",
			Payload: json.RawMessage(`{"type":"text","text":"` +
				strings.Repeat("x", 2000) +
				`"}`),
			MaxStringBytes: 4096,
			MaxEventBytes:  64,
			Want:           `{"type":"text","truncated":true}` + "\n",
		},
		{
			ID:             "newline-reserved",
			Kind:           "bound",
			Payload:        json.RawMessage(`{"type":"text","text":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`),
			MaxStringBytes: 4096,
			MaxEventBytes:  64,
			Want:           `{"type":"text","truncated":true}` + "\n",
		},
		{
			ID:   "exact-string-cap",
			Kind: "bound",
			Payload: json.RawMessage(`{"type":"text","text":"` +
				strings.Repeat("x", 8192) +
				`"}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want: `{"type":"text","text":"` +
				strings.Repeat("x", 8192) +
				`"}` + "\n",
		},
		{
			ID:   "over-string-cap",
			Kind: "bound",
			Payload: json.RawMessage(`{"type":"text","text":"` +
				strings.Repeat("x", 8193) +
				`"}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want: `{"type":"text","text":"` +
				strings.Repeat("x", 8192) +
				`","truncated":true}` + "\n",
		},
		{
			ID:             "null-scalar",
			Kind:           "bound",
			Payload:        json.RawMessage("{\"type\":\"tool_call\",\"input\":null,\"large\":[" + strings.Repeat("1,", 1999) + "1]}"),
			MaxStringBytes: 8192,
			MaxEventBytes:  64,
			Want:           `{"type":"tool_call","input":null,"truncated":true}` + "\n",
		},
		{
			ID:   "deep-array",
			Kind: "bound",
			Payload: json.RawMessage(`{"type":"tool_call","input":` +
				strings.Repeat("[", 200) +
				`"leaf"` +
				strings.Repeat("]", 200) +
				`}`),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want: `{"type":"tool_call","input":[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[["[truncated: depth]"]]]]]]]]]]]]]]]]]]]]]]]]]]]]]` +
				`]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]],"truncated":true}` + "\n",
		},
		{
			ID:             "deep-object",
			Kind:           "bound",
			Payload:        json.RawMessage("{\"type\":\"tool_call\",\"input\":" + strings.Repeat("{\"next\":", 200) + "\"leaf\"" + strings.Repeat("}", 200) + "}"),
			MaxStringBytes: 8192,
			MaxEventBytes:  32768,
			Want: `{"type":"tool_call","input":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":` +
				`{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"ne` +
				`xt":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":` +
				`{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":{"next":"[truncated: depth]"}}}}}}}}` +
				`}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}}},"truncated":true}` + "\n",
		},
		{
			ID:        "long-intermediate-lossless-final",
			Kind:      "projection",
			SessionID: `session-fixed`,
			CWD:       `/fixed`,
			Final:     strings.Repeat("雪", 12000),
			Events: []json.RawMessage{
				json.RawMessage(`{"type":"assistant/message","seq":10,"time":1,"data":{"turn":1,"step":1,"message":{"id":"m","role":"assistant","source":{"kind":"model","pro` +
					`vider":"p","model":"m"},"content":[{"type":"reasoning","text":"` +
					strings.Repeat("r", 9000) +
					`"},{"type":"text","text":"` +
					strings.Repeat("\\u96ea", 12000) +
					`"}]},"stream":[]},"surfaceOp":"append"}`),
			},
			Want: `{"type":"session","sessionId":"session-fixed","cwd":"/fixed"}` + "\n" +
				`{"type":"thinking","text":"` +
				strings.Repeat("r", 8192) +
				`","truncated":true}` + "\n" +
				`{"type":"text","text":"` +
				strings.Repeat("雪", 2730) +
				`","truncated":true}` + "\n" +
				`{"type":"final","text":"` +
				strings.Repeat("雪", 12000) +
				`"}` + "\n",
		},
	}
}
