package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestAttachedRunRejectsMissingPortsAndForeignOwnershipBeforeWrites(t *testing.T) {
	h := newQi(t, &scriptedAdapter{}, nil)
	defer h.stop(t)
	base := h.run(Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}})
	for _, field := range []string{"agent", "session", "attachment", "inbox", "request-state", "identity", "foreign-token"} {
		t.Run(field, func(t *testing.T) {
			run := base
			switch field {
			case "agent":
				run.Agent = nil
			case "session":
				run.Attached.Session = nil
			case "attachment":
				run.Attached.Reservation = nil
			case "inbox":
				run.Attached.Inbox = nil
			case "request-state":
				run.Attached.RequestState = nil
			case "identity":
				run.Agent = &testAgentRuntime{sessionID: "other"}
			case "foreign-token":
				other, err := session.Restore(h.current.Header(), nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				reservation, err := other.Attach()
				if err != nil {
					t.Fatal(err)
				}
				defer reservation.Close()
				run.Attached.Reservation = reservation
			}
			before := h.current.Watermark()
			err := h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { t.Error("invalid dependency emitted a fact"); return nil })
			if err == nil || h.current.Watermark() != before {
				t.Fatal("invalid dependency admitted or wrote history", err)
			}
			if field == "foreign-token" && !errors.Is(err, session.ErrAttached) {
				t.Fatal("foreign attachment token not checked", err)
			}
		})
	}
}
