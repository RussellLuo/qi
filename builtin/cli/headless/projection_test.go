package headless

import (
	"bytes"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestProjectionAndOutputBounds(t *testing.T) {
	cases := projectionCases()
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			var out bytes.Buffer
			if c.Kind == "bound" {
				payload, ok := parseJSON(string(c.Payload))
				if !ok {
					t.Fatal("bad bound input")
				}
				out.WriteString(boundJSONLine(payload.(jsonObject), c.MaxStringBytes, c.MaxEventBytes) + "\n")
			} else {
				p := newProjector(&out)
				if err := p.session(protocol.SessionID(c.SessionID), c.CWD); err != nil {
					t.Fatal(err)
				}
				for _, raw := range c.Events {
					event, err := protocol.DecodeSessionEvent(raw)
					if err != nil {
						t.Fatalf("invalid test event: %v", err)
					}
					if err := p.push(event); err != nil {
						t.Fatal(err)
					}
				}

				if err := p.final(c.Final); err != nil {
					t.Fatal(err)
				}
			}
			// Exact bytes also verify JSON.stringify escaping and the newline cap.
			if out.String() != c.Want {
				t.Fatalf("output mismatch:\nGo: %.1500s\nDS: %.1500s", out.String(), c.Want)
			}
		})
	}
}
