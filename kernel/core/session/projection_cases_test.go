package session

import "encoding/json"

type projectionCase struct {
	ID              string
	Operation       string
	Accepted        bool
	Input, Expected json.RawMessage
}

func projectionCases() []projectionCase {
	return []projectionCase{
		{
			ID:        "cancel-empty",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/attempt",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "stream": [
          {"type":"chunk","time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}},
          {"type":"chunk","time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
        ]
      }
    },
    {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1010,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"parent"}}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/attempt",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "stream": [
            {"type":"chunk","time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}},
            {"type":"chunk","time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
          ]
        }
      },
      {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
      {"type":"turn/end","seq":10,"time":1010,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"parent"}}}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":11,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[4,5],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}
  ],
  "requestHeader": {"config":{"provider":"p","model":"m"}},
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1010,"chunk":{"type":"text-delta","index":0,"text":" \n"}},
        {"time":1011,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
      ]
    }
  ]
}`),
		},
		{
			ID:        "cancel-prefix",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-partial",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"}]
        },
        "stream": [
          {"type":"chunk","time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}},
          {"type":"chunk","time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}},
          {"type":"chunk","time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}},
          {"type":"chunk","time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
        ],
        "interrupted": true
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1010,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"user"}}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {
            "id": "a-partial",
            "role": "assistant",
            "source": {"kind":"model","provider":"p","model":"m"},
            "content": [{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"}]
          },
          "stream": [
            {"type":"chunk","time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}},
            {"type":"chunk","time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}},
            {"type":"chunk","time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}},
            {"type":"chunk","time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
          ],
          "interrupted": true
        },
        "surfaceOp": "append"
      },
      {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
      {"type":"turn/end","seq":10,"time":1010,"data":{"turn":1,"reason":{"kind":"aborted","reason":{"kind":"user"}}}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":11,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {
      "id": "a-partial",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [{"type":"reasoning","text":"先想一想"},{"type":"text","text":"已有前缀"}]
    }
  ],
  "requestHeader": {"config":{"provider":"p","model":"m"}},
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1010,"chunk":{"type":"reasoning-delta","index":4,"text":"先想一想"}},
        {"time":1011,"chunk":{"type":"text-delta","index":2,"text":"已有前缀"}},
        {"time":1012,"chunk":{"type":"tool-call-delta","index":8,"id":"c-partial","name":"echo","argumentsDelta":"{\"text\":"}},
        {"time":1013,"chunk":{"type":"finish","reason":{"kind":"aborted","failure":{"message":"request canceled","code":"ABORTED"}}}}
      ]
    }
  ]
}`),
		},
		{
			ID:        "empty-session",
			Operation: "session",
			Accepted:  true,
			Input:     json.RawMessage(`{"header":{"version":4,"id":"s1","createdAt":1000,"isSeeded":false},"events":[],"restoreTime":2000,"create":true}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {"header":{"version":4,"id":"s1","createdAt":1000,"isSeeded":false},"events":[]},
  "restoreSuffix": [],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[],"replacements":[]},
  "messages": [],
  "requestHeader": null,
  "requestContext": null,
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": []
}`),
		},
		{
			ID:        "inbox-claim-no-canceled-outcome",
			Operation: "inbox",
			Accepted:  true,
			Input: json.RawMessage(`{
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1000,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    },
    {
      "type": "agent/inbox/spliced",
      "seq": 1,
      "time": 1001,
      "data": {
        "target": "next-step",
        "start": 0,
        "inserted": [{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}]
      }
    },
    {"type":"agent/inbox/spliced","seq":2,"time":1002,"data":{"target":"next-step","start":0,"removedCount":1,"inserted":[]}},
    {"type":"agent/inbox/spliced","seq":3,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}}
  ]
}`),
			Expected: json.RawMessage(`{"accepted":true,"inbox":{"next-turn":[],"next-step":[]}}`),
		},
		{
			ID:        "inbox-clear-order",
			Operation: "inbox",
			Accepted:  true,
			Input: json.RawMessage(`{
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1000,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    },
    {
      "type": "agent/inbox/spliced",
      "seq": 1,
      "time": 1001,
      "data": {
        "target": "next-step",
        "start": 0,
        "inserted": [{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}]
      }
    },
    {"type":"agent/inbox/spliced","seq":2,"time":1002,"data":{"target":"next-step","start":0,"removedCount":1,"inserted":[],"outcome":"canceled"}},
    {"type":"agent/inbox/spliced","seq":3,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[],"outcome":"canceled"}}
  ]
}`),
			Expected: json.RawMessage(`{"accepted":true,"inbox":{"next-turn":[],"next-step":[]}}`),
		},
		{
			ID:        "inbox-pending",
			Operation: "inbox",
			Accepted:  true,
			Input: json.RawMessage(`{
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1000,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    },
    {
      "type": "agent/inbox/spliced",
      "seq": 1,
      "time": 1001,
      "data": {
        "target": "next-step",
        "start": 0,
        "inserted": [{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}]
      }
    }
  ]
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "inbox": {
    "next-turn": [{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}],
    "next-step": [{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}]
  }
}`),
		},
		{
			ID:        "invalid-assistant-source-refs",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
        "stream": [
          {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ]
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [5]
    },
    {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-developer-header",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "request/header",
      "seq": 0,
      "time": 1000,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial",
        "startsSeries": true
      }
    },
    {
      "type": "request/header",
      "seq": 1,
      "time": 1001,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
          "adapterDefaults": {"maxTokens":true},
          "tools": [
            {"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}},
            {"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}
          ]
        },
        "reason": "change"
      }
    },
    {
      "type": "developer/message",
      "seq": 2,
      "time": 1002,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"dev-add","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"extra"}]},
        "headerSeq": 0
      },
      "surfaceOp": "append"
    },
    {"type":"request/context","seq":3,"time":1003,"data":{"provider":"p","model":"m","contextWindow":4096,"systemPromptUpdate":"in-history"}},
    {
      "type": "request/header",
      "seq": 4,
      "time": 1004,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
          "tools": [{"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}]
        },
        "reason": "change"
      }
    },
    {
      "type": "developer/message",
      "seq": 5,
      "time": 1005,
      "data": {
        "turn": 1,
        "step": 2,
        "message": {"id":"dev-remove","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-removal","toolName":"echo"}]}
      },
      "surfaceOp": "append"
    }
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-empty-header-tools",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {"type":"request/header","seq":0,"time":1000,"data":{"header":{"config":{"provider":"p","model":"m"},"tools":[]},"reason":"initial"}},
    {"type":"request/header","seq":1,"time":1001,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"resume","startsSeries":true}},
    {"type":"request/header","seq":2,"time":1002,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"series"}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-future-source-ref",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {"type":"turn/start","seq":0,"time":1000,"data":{"turn":1}},
    {"type":"step/start","seq":1,"time":1001,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 2,
      "time": 1002,
      "data": {"turn":1,"step":1,"message":{"id":"sys-old","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"old prompt"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 3,
      "time": 1003,
      "data": {"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]},
      "surfaceOp": "append"
    },
    {
      "type": "system/message",
      "seq": 4,
      "time": 1004,
      "data": {"turn":1,"step":1,"message":{"id":"sys-clear","role":"system","source":{"kind":"system-prompt"},"content":[]}},
      "surfaceOp": {"op":"replace","startSeq":2,"endSeq":2},
      "sourceEventSeqs": [4]
    },
    {
      "type": "system/message",
      "seq": 5,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]}},
      "surfaceOp": {"op":"replace","startSeq":4,"endSeq":4},
      "sourceEventSeqs": [4]
    },
    {"type":"step/end","seq":6,"time":1006,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":7,"time":1007,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-header-version",
			Operation: "session",
			Accepted:  false,
			Input:     json.RawMessage(`{"header":{"version":3,"id":"s1","createdAt":1000,"isSeeded":false},"events":[],"restoreTime":2000}`),
			Expected:  json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-inbox-duplicate",
			Operation: "inbox",
			Accepted:  false,
			Input: json.RawMessage(`{
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1000,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    },
    {
      "type": "agent/inbox/spliced",
      "seq": 1,
      "time": 1001,
      "data": {"target":"next-step","start":0,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    }
  ]
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-inbox-span",
			Operation: "inbox",
			Accepted:  false,
			Input: json.RawMessage(`{
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1000,
      "data": {"target":"next-turn","start":1,"inserted":[{"id":"u2","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"followup"}]}]}
    },
    {
      "type": "agent/inbox/spliced",
      "seq": 1,
      "time": 1001,
      "data": {
        "target": "next-step",
        "start": 0,
        "inserted": [{"id":"u3","role":"user","source":{"kind":"tool-context","tag":{"tool":"echo"}},"content":[{"type":"text","text":"steering"}]}]
      }
    }
  ]
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-legacy-envelope",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]},
      "sessionId": "s1"
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
        "stream": [
          {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-missing-surface",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {"type":"user/message","seq":5,"time":1006,"data":{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}},
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
        "stream": [
          {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-replace-coverage",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {"type":"turn/start","seq":0,"time":1000,"data":{"turn":1}},
    {"type":"step/start","seq":1,"time":1001,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 2,
      "time": 1002,
      "data": {"turn":1,"step":1,"message":{"id":"sys-old","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"old prompt"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 3,
      "time": 1003,
      "data": {"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]},
      "surfaceOp": "append"
    },
    {
      "type": "system/message",
      "seq": 4,
      "time": 1004,
      "data": {"turn":1,"step":1,"message":{"id":"sys-clear","role":"system","source":{"kind":"system-prompt"},"content":[]}},
      "surfaceOp": {"op":"replace","startSeq":2,"endSeq":2},
      "sourceEventSeqs": [3]
    },
    {
      "type": "system/message",
      "seq": 5,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]}},
      "surfaceOp": {"op":"replace","startSeq":4,"endSeq":4},
      "sourceEventSeqs": [4]
    },
    {"type":"step/end","seq":6,"time":1006,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":7,"time":1007,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-seq-gap",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":3,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
        "stream": [
          {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "invalid-tool-error-success",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {
      "type": "request/header",
      "seq": 6,
      "time": 1007,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial"
      }
    },
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-tool",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
        },
        "stream": [
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}},
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"r1","role":"tool","source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"echo"}],"toolCallId":"c1"},
        "meta": {"tool":"echo","local":[true,null]},
        "error": {"name":"ToolError","code":"FAILED"}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":11,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"step/start","seq":12,"time":1012,"data":{"turn":1,"step":2}},
    {
      "type": "assistant/message",
      "seq": 13,
      "time": 1013,
      "data": {
        "turn": 1,
        "step": 2,
        "message": {"id":"a-final","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"echo"}]},
        "stream": [
          {"type":"chunk","time":1013,"chunk":{"type":"text-delta","index":0,"text":"echo"}},
          {"type":"chunk","time":1014,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ],
        "usage": {"inputTokens":4,"outputTokens":1,"totalTokens":5}
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":14,"time":1014,"data":{"turn":1,"step":2}},
    {"type":"turn/end","seq":15,"time":1015,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
		{
			ID:        "model-failure",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/attempt",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "stream": [
          {
            "type": "chunk",
            "time": 1010,
            "chunk": {
              "type": "finish",
              "reason": {"kind":"error","failure":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}
            }
          }
        ]
      }
    },
    {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
    {
      "type": "turn/end",
      "seq": 10,
      "time": 1010,
      "data": {"turn":1,"reason":{"kind":"error","error":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}}
    }
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/attempt",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "stream": [
            {
              "type": "chunk",
              "time": 1010,
              "chunk": {
                "type": "finish",
                "reason": {"kind":"error","failure":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}
              }
            }
          ]
        }
      },
      {"type":"step/end","seq":9,"time":1009,"data":{"turn":1,"step":1}},
      {
        "type": "turn/end",
        "seq": 10,
        "time": 1010,
        "data": {"turn":1,"reason":{"kind":"error","error":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}}
      }
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":11,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[4,5],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}
  ],
  "requestHeader": {"config":{"provider":"p","model":"m"}},
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {
          "time": 1010,
          "chunk": {
            "type": "finish",
            "reason": {"kind":"error","failure":{"message":"rate limit","code":"RATE_LIMIT","status":429,"providerRetryAfterMs":300,"requestId":"req-1"}}
          }
        }
      ]
    }
  ]
}`),
		},
		{
			ID:        "repair-mixed-assistant-order",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {
      "type": "request/header",
      "seq": 6,
      "time": 1007,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial"
      }
    },
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-tool",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [
            {"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},
            {"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}
          ]
        },
        "stream": [
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c2","name":"echo","arguments":"{broken"}}
  ],
  "restoreTime": 2000,
  "repair": true
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {
        "type": "request/header",
        "seq": 6,
        "time": 1007,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m"},
            "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
          },
          "reason": "initial"
        }
      },
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {
            "id": "a-tool",
            "role": "assistant",
            "source": {"kind":"model","provider":"p","model":"m"},
            "content": [
              {"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},
              {"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}
            ]
          },
          "stream": [
            {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
            {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}},
            {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
          ]
        },
        "surfaceOp": "append"
      },
      {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c2","name":"echo","arguments":"{broken"}}
    ]
  },
  "restoreSuffix": [
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-10",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
        },
        "error": {"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}
      },
      "surfaceOp": "append"
    },
    {
      "type": "tool/result",
      "seq": 11,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c2-11",
          "role": "tool",
          "toolCallId": "c2",
          "isError": true,
          "source": {"kind":"tool","callId":"c2"},
          "content": [
            {
              "type": "text",
              "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
            }
          ]
        },
        "error": {"name":"ToolOutcomeUnknownError","code":"TOOL_OUTCOME_UNKNOWN"}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":12,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":13,"time":1009,"data":{"turn":1,"reason":{"kind":"interrupted"}}},
    {"type":"session/end-seed","seq":14,"time":2000,"data":{}}
  ],
  "repair": [
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-10",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
        },
        "error": {"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}
      },
      "surfaceOp": "append"
    },
    {
      "type": "tool/result",
      "seq": 11,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c2-11",
          "role": "tool",
          "toolCallId": "c2",
          "isError": true,
          "source": {"kind":"tool","callId":"c2"},
          "content": [
            {
              "type": "text",
              "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
            }
          ]
        },
        "error": {"name":"ToolOutcomeUnknownError","code":"TOOL_OUTCOME_UNKNOWN"}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":12,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":13,"time":1009,"data":{"turn":1,"reason":{"kind":"interrupted"}}}
  ],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8,10,11],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {
      "id": "a-tool",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [
        {"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "},
        {"type":"tool-call","id":"c2","name":"echo","arguments":"{broken"}
      ]
    },
    {
      "id": "interrupted-tool-result-c1-10",
      "role": "tool",
      "toolCallId": "c1",
      "isError": true,
      "source": {"kind":"tool","callId":"c1"},
      "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
    },
    {
      "id": "interrupted-tool-result-c2-11",
      "role": "tool",
      "toolCallId": "c2",
      "isError": true,
      "source": {"kind":"tool","callId":"c2"},
      "content": [
        {
          "type": "text",
          "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
        }
      ]
    }
  ],
  "requestHeader": {
    "config": {"provider":"p","model":"m"},
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
  },
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
    "updates": []
  },
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
        {"time":1008,"chunk":{"type":"tool-call-delta","index":1,"id":"c2","name":"echo","argumentsDelta":"{broken"}},
        {"time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
      ]
    }
  ]
}`),
		},
		{
			ID:        "repair-tool-not-started",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {
      "type": "request/header",
      "seq": 6,
      "time": 1007,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial"
      }
    },
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-tool",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
        },
        "stream": [
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
        ]
      },
      "surfaceOp": "append"
    }
  ],
  "restoreTime": 2000,
  "repair": true
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {
        "type": "request/header",
        "seq": 6,
        "time": 1007,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m"},
            "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
          },
          "reason": "initial"
        }
      },
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {
            "id": "a-tool",
            "role": "assistant",
            "source": {"kind":"model","provider":"p","model":"m"},
            "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
          },
          "stream": [
            {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
            {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
          ]
        },
        "surfaceOp": "append"
      }
    ]
  },
  "restoreSuffix": [
    {
      "type": "tool/result",
      "seq": 9,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-9",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
        },
        "error": {"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":10,"time":1008,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":11,"time":1008,"data":{"turn":1,"reason":{"kind":"interrupted"}}},
    {"type":"session/end-seed","seq":12,"time":2000,"data":{}}
  ],
  "repair": [
    {
      "type": "tool/result",
      "seq": 9,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-9",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
        },
        "error": {"name":"ToolNotStartedError","code":"TOOL_NOT_STARTED"}
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":10,"time":1008,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":11,"time":1008,"data":{"turn":1,"reason":{"kind":"interrupted"}}}
  ],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8,9],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {
      "id": "a-tool",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
    },
    {
      "id": "interrupted-tool-result-c1-9",
      "role": "tool",
      "toolCallId": "c1",
      "isError": true,
      "source": {"kind":"tool","callId":"c1"},
      "content": [{"type":"text","text":"The tool call was interrupted before the qi recorded it as started. Retry it if it is still needed."}]
    }
  ],
  "requestHeader": {
    "config": {"provider":"p","model":"m"},
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
  },
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
    "updates": []
  },
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
        {"time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
      ]
    }
  ]
}`),
		},
		{
			ID:        "repair-tool-outcome-unknown",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {
      "type": "request/header",
      "seq": 6,
      "time": 1007,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial"
      }
    },
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-tool",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
        },
        "stream": [
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}}
  ],
  "restoreTime": 2000,
  "repair": true
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {
        "type": "request/header",
        "seq": 6,
        "time": 1007,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m"},
            "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
          },
          "reason": "initial"
        }
      },
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {
            "id": "a-tool",
            "role": "assistant",
            "source": {"kind":"model","provider":"p","model":"m"},
            "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
          },
          "stream": [
            {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
            {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
          ]
        },
        "surfaceOp": "append"
      },
      {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}}
    ]
  },
  "restoreSuffix": [
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-10",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [
            {
              "type": "text",
              "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
            }
          ]
        },
        "error": {"name":"ToolOutcomeUnknownError","code":"TOOL_OUTCOME_UNKNOWN"}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":11,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":12,"time":1009,"data":{"turn":1,"reason":{"kind":"interrupted"}}},
    {"type":"session/end-seed","seq":13,"time":2000,"data":{}}
  ],
  "repair": [
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1009,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "interrupted-tool-result-c1-10",
          "role": "tool",
          "toolCallId": "c1",
          "isError": true,
          "source": {"kind":"tool","callId":"c1"},
          "content": [
            {
              "type": "text",
              "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
            }
          ]
        },
        "error": {"name":"ToolOutcomeUnknownError","code":"TOOL_OUTCOME_UNKNOWN"}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":11,"time":1009,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":12,"time":1009,"data":{"turn":1,"reason":{"kind":"interrupted"}}}
  ],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8,10],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {
      "id": "a-tool",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
    },
    {
      "id": "interrupted-tool-result-c1-10",
      "role": "tool",
      "toolCallId": "c1",
      "isError": true,
      "source": {"kind":"tool","callId":"c1"},
      "content": [
        {
          "type": "text",
          "text": "The tool call was interrupted after it was recorded, but no result was durably recorded. Its outcome is unknown. Decide whether to retry from the tool semantics: retry only if the operation is read-only or idempotent; if it may have side effects, first verify external state or ask the user. Do not retry blindly."
        }
      ]
    }
  ],
  "requestHeader": {
    "config": {"provider":"p","model":"m"},
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
  },
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
    "updates": []
  },
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
        {"time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
      ]
    }
  ]
}`),
		},
		{
			ID:        "request-series-and-resume",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {"type":"request/header","seq":0,"time":1000,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/header","seq":1,"time":1001,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"resume","startsSeries":true}},
    {"type":"request/header","seq":2,"time":1002,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"series"}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {"type":"request/header","seq":0,"time":1000,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
      {"type":"request/header","seq":1,"time":1001,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"resume","startsSeries":true}},
      {"type":"request/header","seq":2,"time":1002,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"series"}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":3,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[],"replacements":[]},
  "messages": [],
  "requestHeader": {"config":{"provider":"p","model":"m"}},
  "requestContext": null,
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": []
}`),
		},
		{
			ID:        "request-tool-updates",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "request/header",
      "seq": 0,
      "time": 1000,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial",
        "startsSeries": true
      }
    },
    {
      "type": "request/header",
      "seq": 1,
      "time": 1001,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
          "adapterDefaults": {"maxTokens":true},
          "tools": [
            {"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}},
            {"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}
          ]
        },
        "reason": "change"
      }
    },
    {
      "type": "developer/message",
      "seq": 2,
      "time": 1002,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"dev-add","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"extra"}]},
        "headerSeq": 1
      },
      "surfaceOp": "append"
    },
    {"type":"request/context","seq":3,"time":1003,"data":{"provider":"p","model":"m","contextWindow":4096,"systemPromptUpdate":"in-history"}},
    {
      "type": "request/header",
      "seq": 4,
      "time": 1004,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
          "tools": [{"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}]
        },
        "reason": "change"
      }
    },
    {
      "type": "developer/message",
      "seq": 5,
      "time": 1005,
      "data": {
        "turn": 1,
        "step": 2,
        "message": {"id":"dev-remove","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-removal","toolName":"echo"}]}
      },
      "surfaceOp": "append"
    }
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "request/header",
        "seq": 0,
        "time": 1000,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m"},
            "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
          },
          "reason": "initial",
          "startsSeries": true
        }
      },
      {
        "type": "request/header",
        "seq": 1,
        "time": 1001,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
            "adapterDefaults": {"maxTokens":true},
            "tools": [
              {"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}},
              {"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}
            ]
          },
          "reason": "change"
        }
      },
      {
        "type": "developer/message",
        "seq": 2,
        "time": 1002,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {"id":"dev-add","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"extra"}]},
          "headerSeq": 1
        },
        "surfaceOp": "append"
      },
      {"type":"request/context","seq":3,"time":1003,"data":{"provider":"p","model":"m","contextWindow":4096,"systemPromptUpdate":"in-history"}},
      {
        "type": "request/header",
        "seq": 4,
        "time": 1004,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
            "tools": [{"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}]
          },
          "reason": "change"
        }
      },
      {
        "type": "developer/message",
        "seq": 5,
        "time": 1005,
        "data": {
          "turn": 1,
          "step": 2,
          "message": {"id":"dev-remove","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-removal","toolName":"echo"}]}
        },
        "surfaceOp": "append"
      }
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":6,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[2,5],"replacements":[]},
  "messages": [
    {"id":"dev-add","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-addition","toolName":"extra"}]},
    {"id":"dev-remove","role":"developer","source":{"kind":"tool-registry"},"content":[{"type":"tool-removal","toolName":"echo"}]}
  ],
  "requestHeader": {
    "config": {"provider":"p","model":"m","reasoningEffort":"high","temperature":0.2,"maxTokens":128,"stop":["END"]},
    "tools": [{"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}]
  },
  "requestContext": {"provider":"p","model":"m","contextWindow":4096,"systemPromptUpdate":"in-history"},
  "toolHistory": {
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
    "updates": [
      {"messageId":"dev-add","additions":[{"name":"extra","description":"extra tool","parameters":{"type":"object"},"deferLoading":true}]},
      {"messageId":"dev-remove","additions":[]}
    ]
  },
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": []
}`),
		},
		{
			ID:        "simple-reply",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
        "stream": [
          {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {"type":"request/header","seq":6,"time":1007,"data":{"header":{"config":{"provider":"p","model":"m"}},"reason":"initial"}},
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1010,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]},
          "stream": [
            {"type":"text-chunks","time0":1008,"index":0,"dt":[],"texts":["你好！"]},
            {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
          ]
        },
        "surfaceOp": "append"
      },
      {"type":"step/end","seq":9,"time":1011,"data":{"turn":1,"step":1}},
      {"type":"turn/end","seq":10,"time":1012,"data":{"turn":1,"reason":{"kind":"completed"}}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":11,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {"id":"a1","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"你好！"}]}
  ],
  "requestHeader": {"config":{"provider":"p","model":"m"}},
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [{"time":1008,"chunk":{"type":"text-delta","index":0,"text":"你好！"}},{"time":1009,"chunk":{"type":"finish","reason":{"kind":"stop"}}}]
    }
  ]
}`),
		},
		{
			ID:        "surface-clear-and-replace",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {"type":"turn/start","seq":0,"time":1000,"data":{"turn":1}},
    {"type":"step/start","seq":1,"time":1001,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 2,
      "time": 1002,
      "data": {"turn":1,"step":1,"message":{"id":"sys-old","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"old prompt"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 3,
      "time": 1003,
      "data": {"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]},
      "surfaceOp": "append"
    },
    {
      "type": "system/message",
      "seq": 4,
      "time": 1004,
      "data": {"turn":1,"step":1,"message":{"id":"sys-clear","role":"system","source":{"kind":"system-prompt"},"content":[]}},
      "surfaceOp": {"op":"replace","startSeq":2,"endSeq":2},
      "sourceEventSeqs": [2]
    },
    {
      "type": "system/message",
      "seq": 5,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]}},
      "surfaceOp": {"op":"replace","startSeq":4,"endSeq":4},
      "sourceEventSeqs": [4]
    },
    {"type":"step/end","seq":6,"time":1006,"data":{"turn":1,"step":1}},
    {"type":"turn/end","seq":7,"time":1007,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {"type":"turn/start","seq":0,"time":1000,"data":{"turn":1}},
      {"type":"step/start","seq":1,"time":1001,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 2,
        "time": 1002,
        "data": {"turn":1,"step":1,"message":{"id":"sys-old","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"old prompt"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 3,
        "time": 1003,
        "data": {"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]},
        "surfaceOp": "append"
      },
      {
        "type": "system/message",
        "seq": 4,
        "time": 1004,
        "data": {"turn":1,"step":1,"message":{"id":"sys-clear","role":"system","source":{"kind":"system-prompt"},"content":[]}},
        "surfaceOp": {"op":"replace","startSeq":2,"endSeq":2},
        "sourceEventSeqs": [2]
      },
      {
        "type": "system/message",
        "seq": 5,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]}},
        "surfaceOp": {"op":"replace","startSeq":4,"endSeq":4},
        "sourceEventSeqs": [4]
      },
      {"type":"step/end","seq":6,"time":1006,"data":{"turn":1,"step":1}},
      {"type":"turn/end","seq":7,"time":1007,"data":{"turn":1,"reason":{"kind":"completed"}}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":8,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[5,3],"replacements":[{"seq":4,"start":2,"end":2,"shadowedSeqs":[2]},{"seq":5,"start":4,"end":4,"shadowedSeqs":[4]}]},
  "messages": [
    {"id":"sys-new","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"new prompt"}]},
    {"id":"u-replace","role":"user","source":{"kind":"user","extension":{"original":true}},"content":[{"type":"text","text":"question"}]}
  ],
  "requestHeader": null,
  "requestContext": null,
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": []
}`),
		},
		{
			ID:        "tool-loop",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [
    {
      "type": "agent/inbox/spliced",
      "seq": 0,
      "time": 1001,
      "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
    },
    {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
    {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
    {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
    {
      "type": "system/message",
      "seq": 4,
      "time": 1005,
      "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
      "surfaceOp": "append"
    },
    {
      "type": "user/message",
      "seq": 5,
      "time": 1006,
      "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
      "surfaceOp": "append"
    },
    {
      "type": "request/header",
      "seq": 6,
      "time": 1007,
      "data": {
        "header": {
          "config": {"provider":"p","model":"m"},
          "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
        },
        "reason": "initial"
      }
    },
    {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
    {
      "type": "assistant/message",
      "seq": 8,
      "time": 1008,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {
          "id": "a-tool",
          "role": "assistant",
          "source": {"kind":"model","provider":"p","model":"m"},
          "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
        },
        "stream": [
          {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
          {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
        ]
      },
      "surfaceOp": "append"
    },
    {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}},
    {
      "type": "tool/result",
      "seq": 10,
      "time": 1010,
      "data": {
        "turn": 1,
        "step": 1,
        "message": {"id":"r1","role":"tool","source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"echo"}],"toolCallId":"c1"},
        "meta": {"tool":"echo","local":[true,null]}
      },
      "surfaceOp": "append",
      "sourceEventSeqs": [9]
    },
    {"type":"step/end","seq":11,"time":1011,"data":{"turn":1,"step":1}},
    {"type":"step/start","seq":12,"time":1012,"data":{"turn":1,"step":2}},
    {
      "type": "assistant/message",
      "seq": 13,
      "time": 1013,
      "data": {
        "turn": 1,
        "step": 2,
        "message": {"id":"a-final","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"echo"}]},
        "stream": [
          {"type":"chunk","time":1013,"chunk":{"type":"text-delta","index":0,"text":"echo"}},
          {"type":"chunk","time":1014,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
        ],
        "usage": {"inputTokens":4,"outputTokens":1,"totalTokens":5}
      },
      "surfaceOp": "append"
    },
    {"type":"step/end","seq":14,"time":1014,"data":{"turn":1,"step":2}},
    {"type":"turn/end","seq":15,"time":1015,"data":{"turn":1,"reason":{"kind":"completed"}}}
  ],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [
      {
        "type": "agent/inbox/spliced",
        "seq": 0,
        "time": 1001,
        "data": {"target":"next-turn","start":0,"inserted":[{"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]}]}
      },
      {"type":"turn/start","seq":1,"time":1002,"data":{"turn":1}},
      {"type":"agent/inbox/spliced","seq":2,"time":1003,"data":{"target":"next-turn","start":0,"removedCount":1,"inserted":[]}},
      {"type":"step/start","seq":3,"time":1004,"data":{"turn":1,"step":1}},
      {
        "type": "system/message",
        "seq": 4,
        "time": 1005,
        "data": {"turn":1,"step":1,"message":{"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]}},
        "surfaceOp": "append"
      },
      {
        "type": "user/message",
        "seq": 5,
        "time": 1006,
        "data": {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
        "surfaceOp": "append"
      },
      {
        "type": "request/header",
        "seq": 6,
        "time": 1007,
        "data": {
          "header": {
            "config": {"provider":"p","model":"m"},
            "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
          },
          "reason": "initial"
        }
      },
      {"type":"request/context","seq":7,"time":1007,"data":{"provider":"p","model":"m"}},
      {
        "type": "assistant/message",
        "seq": 8,
        "time": 1008,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {
            "id": "a-tool",
            "role": "assistant",
            "source": {"kind":"model","provider":"p","model":"m"},
            "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
          },
          "stream": [
            {"type":"chunk","time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
            {"type":"chunk","time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
          ]
        },
        "surfaceOp": "append"
      },
      {"type":"tool/call","seq":9,"time":1009,"data":{"turn":1,"step":1,"callId":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}},
      {
        "type": "tool/result",
        "seq": 10,
        "time": 1010,
        "data": {
          "turn": 1,
          "step": 1,
          "message": {"id":"r1","role":"tool","source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"echo"}],"toolCallId":"c1"},
          "meta": {"tool":"echo","local":[true,null]}
        },
        "surfaceOp": "append",
        "sourceEventSeqs": [9]
      },
      {"type":"step/end","seq":11,"time":1011,"data":{"turn":1,"step":1}},
      {"type":"step/start","seq":12,"time":1012,"data":{"turn":1,"step":2}},
      {
        "type": "assistant/message",
        "seq": 13,
        "time": 1013,
        "data": {
          "turn": 1,
          "step": 2,
          "message": {"id":"a-final","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"echo"}]},
          "stream": [
            {"type":"chunk","time":1013,"chunk":{"type":"text-delta","index":0,"text":"echo"}},
            {"type":"chunk","time":1014,"chunk":{"type":"finish","reason":{"kind":"stop"}}}
          ],
          "usage": {"inputTokens":4,"outputTokens":1,"totalTokens":5}
        },
        "surfaceOp": "append"
      },
      {"type":"step/end","seq":14,"time":1014,"data":{"turn":1,"step":2}},
      {"type":"turn/end","seq":15,"time":1015,"data":{"turn":1,"reason":{"kind":"completed"}}}
    ]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":16,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[4,5,8,10,13],"replacements":[]},
  "messages": [
    {"id":"sys1","role":"system","source":{"kind":"system-prompt"},"content":[{"type":"text","text":"请简洁回答。"}]},
    {"id":"u1","role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"你好"}]},
    {
      "id": "a-tool",
      "role": "assistant",
      "source": {"kind":"model","provider":"p","model":"m"},
      "content": [{"type":"tool-call","id":"c1","name":"echo","arguments":" { \"text\": \"echo\" } "}]
    },
    {"id":"r1","role":"tool","source":{"kind":"tool","callId":"c1"},"content":[{"type":"text","text":"echo"}],"toolCallId":"c1"},
    {"id":"a-final","role":"assistant","source":{"kind":"model","provider":"p","model":"m"},"content":[{"type":"text","text":"echo"}]}
  ],
  "requestHeader": {
    "config": {"provider":"p","model":"m"},
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]
  },
  "requestContext": {"provider":"p","model":"m"},
  "toolHistory": {
    "tools": [{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}],
    "updates": []
  },
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": [
    {
      "seq": 8,
      "expanded": [
        {"time":1008,"chunk":{"type":"tool-call-delta","index":0,"id":"c1","name":"echo","argumentsDelta":" { \"text\": \"echo\" } "}},
        {"time":1009,"chunk":{"type":"finish","reason":{"kind":"tool-calls"}}}
      ]
    },
    {
      "seq": 13,
      "expanded": [{"time":1013,"chunk":{"type":"text-delta","index":0,"text":"echo"}},{"time":1014,"chunk":{"type":"finish","reason":{"kind":"stop"}}}]
    }
  ]
}`),
		},
		{
			ID:        "unknown-ignorable",
			Operation: "session",
			Accepted:  true,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [{"type":"future/extension","seq":0,"time":1000,"data":{"opaque":[true,null]},"ignorable":true}],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{
  "accepted": true,
  "roundTrip": {
    "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
    "events": [{"type":"future/extension","seq":0,"time":1000,"data":{"opaque":[true,null]},"ignorable":true}]
  },
  "restoreSuffix": [{"type":"session/end-seed","seq":1,"time":2000,"data":{}}],
  "repair": [],
  "repairAgain": [],
  "surface": {"nodes":[],"replacements":[]},
  "messages": [],
  "requestHeader": null,
  "requestContext": null,
  "toolHistory": {"tools":[],"updates":[]},
  "inbox": {"next-turn":[],"next-step":[]},
  "streams": []
}`),
		},
		{
			ID:        "unknown-required",
			Operation: "session",
			Accepted:  false,
			Input: json.RawMessage(`{
  "header": {"version":4,"id":"s1","createdAt":1000,"isSeeded":false},
  "events": [{"type":"future/extension","seq":0,"time":1000,"data":{"opaque":[true,null]}}],
  "restoreTime": 2000
}`),
			Expected: json.RawMessage(`{"accepted":false}`),
		},
	}
}
