package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Decode the original input unchanged and compute repair with production Go.
func TestSessionProjectionAndRecovery(t *testing.T) {
	cases := projectionCases()

	for _, c := range cases {
		if c.Operation != "session" && c.Operation != "inbox" {
			continue
		}

		t.Run(c.ID, func(t *testing.T) {
			var input struct {
				Header      json.RawMessage   `json:"header"`
				Events      []json.RawMessage `json:"events"`
				Repair      bool              `json:"repair"`
				Create      bool              `json:"create"`
				RestoreTime int64             `json:"restoreTime"`
			}
			if err := json.Unmarshal(c.Input, &input); err != nil {
				t.Fatal(err)
			}

			result := map[string]any{"accepted": true}
			evaluate := func() (Projection, error) {
				var header protocol.SessionHeader
				if input.Header != nil {
					h, err := protocol.DecodeSessionHeader(input.Header)
					if err != nil {
						return Projection{}, err
					}

					b, err := protocol.EncodeSessionHeader(h)
					if err != nil {
						return Projection{}, err
					}

					assertSameJSON(t, input.Header, b)
					header = h
				}

				events := []protocol.SessionEvent{}

				for _, raw := range input.Events {
					e, err := protocol.DecodeSessionEvent(raw)
					if err != nil {
						return Projection{}, err
					}

					b, err := protocol.EncodeSessionEvent(e)
					if err != nil {
						return Projection{}, err
					}

					assertSameJSON(t, raw, b)
					events = append(events, e)
				}

				if _, err := Fold(events); err != nil {
					return Projection{}, err
				}

				original := cloneEvents(events)
				result["repair"] = []protocol.SessionEvent{}
				if input.Repair {
					closers, err := InterruptedTurnClosers(events)
					if err != nil {
						return Projection{}, err
					}

					var expected struct {
						Repair      json.RawMessage `json:"repair"`
						RepairAgain json.RawMessage `json:"repairAgain"`
					}
					if err := json.Unmarshal(c.Expected, &expected); err != nil {
						return Projection{}, err
					}

					encoded, err := json.Marshal(closers)
					if err != nil {
						return Projection{}, err
					}

					assertSameJSON(t, expected.Repair, encoded)
					result["repair"] = closers
					events = append(events, closers...)
					again, err := InterruptedTurnClosers(events)
					if err != nil {
						return Projection{}, err
					}

					encoded, err = json.Marshal(again)
					if err != nil {
						return Projection{}, err
					}

					assertSameJSON(t, expected.RepairAgain, encoded)
				}

				if input.Header != nil {
					result["roundTrip"] = map[string]any{"header": header, "events": original}
					// Use the same production recovery appender as JSONL/Attach.
					current := &LiveSession{
						id:     header.ID,
						header: header,
						events: original,
					}

					for _, e := range original {
						if e.Type == protocol.TurnStarted ||
							e.Type == protocol.TurnEnded ||
							e.Type == protocol.StepStarted ||
							e.Type == protocol.StepEnded ||
							e.Type == protocol.SessionEndSeed {
							if err := current.state.apply(e); err != nil {
								return Projection{}, err
							}
						}
					}

					err := recoverPrefix(original, func(e protocol.SessionEvent) error {
						if e.Type == protocol.SessionEndSeed {
							e.Time = input.RestoreTime
							if e.Time == 0 {
								e.Time = 2000
							}
						}

						_, err := current.appendLocked(e)

						return err
					}, !input.Create)
					if err != nil {
						return Projection{}, err
					}

					result["restoreSuffix"] = current.events[len(original):]
					again, err := InterruptedTurnClosers(events)
					if err != nil {
						return Projection{}, err
					}

					result["repairAgain"] = again
					streams := []any{}

					for _, e := range events {
						var records []protocol.AssistantStreamRecord

						switch e.Type {
						case protocol.AssistantMessage:
							records = protocol.EventPayload[protocol.AssistantMessageData](e).Stream
						case protocol.AssistantAttempt:
							records = protocol.EventPayload[protocol.AttemptData](e).Stream
						default:
							continue
						}

						expanded, err := protocol.ExpandAssistantStream(records)
						if err != nil {
							return Projection{}, err
						}

						streams = append(streams, map[string]any{"seq": e.Seq, "expanded": expanded})
					}

					result["streams"] = streams
				}

				return Fold(events)
			}
			actual, err := evaluate()
			if !c.Accepted {
				if err == nil {
					t.Fatal("accepted invalid fixture")
				}

				checkFixtureResult(t, c, map[string]any{"accepted": false})
				return
			}

			if err != nil {
				t.Fatal(err)
			}

			if c.Operation == "inbox" {
				result = map[string]any{"accepted": true, "inbox": actual.Inbox}
			} else {
				for key, value := range map[string]any{
					"surface":        actual.Surface,
					"messages":       actual.Messages,
					"requestHeader":  actual.RequestHeader,
					"requestContext": actual.RequestContext,
					"toolHistory":    actual.ToolHistory,
					"inbox":          actual.Inbox,
				} {
					result[key] = value
				}
			}

			checkFixtureResult(t, c, result)
		})
	}
}

func assertSameJSON(t *testing.T, expected, actual []byte) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(expected, &a); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(actual, &b); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON differs:\nexpected: %s\nactual: %s", expected, actual)
	}
}

func checkFixtureResult(t *testing.T, c projectionCase, result any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, c.Expected, encoded)
}
