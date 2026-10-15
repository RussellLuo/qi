package agentloop_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestPublicLocalObservationAndRawHostFacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := setup(t, nil, nil, nil)
	mount(t, f.host, gordis.InstanceSpec{ID: "model", Plugin: "test.model"})
	_, c := f.caller(t, "observe")
	binding := events.Bind(c.scope)
	rawHost := make(chan contracts.DriverEvent, 128)
	appended := make(chan gordisbridge.SessionObservation, 128)
	if _, err := binding.On(gordisbridge.DriverEventTopic, func(_ context.Context, e contracts.DriverEvent) error { rawHost <- e; return nil }, events.Global()); err != nil {
		t.Fatal(err)
	}
	if _, err := binding.On(gordisbridge.SessionAppendedTopic, func(_ context.Context, e gordisbridge.SessionObservation) error { appended <- e; return nil }, events.Global()); err != nil {
		t.Fatal(err)
	}
	h, err := c.factory.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: "public-observe"}, Agent: agent.AgentOptions{Config: config()}})
	if err != nil {
		t.Fatal(err)
	}
	current := h.Session()
	a := h.Agent()
	factsSub, err := current.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer factsSub.Close()
	streams, err := a.SubscribeAssistant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer streams.Close()
	notices, err := a.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer notices.Close()
	watermark := current.Watermark()
	outside := protocol.NewSessionEvent("fixture/observation", map[string]string{"value": "outside Driver"})
	outside.Ignorable = true
	seq, err := current.Append(ctx, outside)
	if err != nil {
		t.Fatal(err)
	}
	invocation, source, err := a.Submit(ctx, agent.Input{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var raw []contracts.DriverEvent
	for e, err := range source {
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, e.Clone())
	}
	if err := a.WhenIdle(ctx); err != nil {
		t.Fatal(err)
	}
	through := current.Watermark()
	durable := []protocol.SessionEvent{}
	for e, err := range current.Events(ctx, watermark) {
		if err != nil {
			t.Fatal(err)
		}
		durable = append(durable, e)
	}
	notified := []protocol.SessionEvent{}
	for e, err := range factsSub.Events() {
		if err != nil {
			t.Fatal(err)
		}
		notified = append(notified, e)
		if e.Seq == through {
			break
		}
	}
	if !reflect.DeepEqual(durable, notified) || len(notified) == 0 || notified[0].Seq != seq || notified[0].Type != "fixture/observation" || factsSub.Dropped() != 0 {
		t.Fatal("full-source Session observation", durable, notified)
	}
	var live []protocol.AssistantStreamFrame
	instance := ""
	for e, err := range streams.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.Agent != a || e.InstanceID == "" {
			t.Fatal(e)
		}
		instance = e.InstanceID
		live = append(live, e.Frame)
		if e.Frame.Type == "end" {
			break
		}
	}
	var rawFrames []protocol.AssistantStreamFrame
	for _, e := range raw {
		if e.Stream != nil {
			rawFrames = append(rawFrames, *e.Stream)
		}
	}
	if !reflect.DeepEqual(live, rawFrames) || len(live) != 6 || streams.Dropped() != 0 {
		t.Fatal("raw stream differs", live, rawFrames)
	}
	end := live[len(live)-1]
	if end.Outcome == nil || end.Outcome.Kind != "committed" || end.Outcome.Seq == nil {
		t.Fatal(end)
	}
	matched := false
	for _, e := range durable {
		if e.Seq == *end.Outcome.Seq {
			matched = e.Type == protocol.AssistantMessage
		}
	}
	if !matched {
		t.Fatal("preview has no durable reference", end)
	}
	var hostRaw []contracts.DriverEvent
	for {
		select {
		case e := <-rawHost:
			if e.InvocationID != invocation {
				continue
			}
			hostRaw = append(hostRaw, e)
			if e.Type == "ended" {
				goto hostDone
			}
		case <-ctx.Done():
			t.Fatal("raw Host Driver ended missing")
		}
	}
hostDone:
	if !reflect.DeepEqual(hostRaw, raw) {
		t.Fatal("Host changed raw fields", hostRaw, raw)
	}
	outsideHint := false
	for !outsideHint {
		select {
		case e := <-appended:
			outsideHint = e.SessionID == current.ID() && e.Event.Seq == seq && e.Event.Type == outside.Type
		case <-ctx.Done():
			t.Fatal("non-Driver Host append hint missing")
		}
	}
	if err := h.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	inserted, claimed, idle, disposed := false, false, false, false
	for e, err := range notices.Events() {
		if err != nil {
			t.Fatal(err)
		}
		if e.InstanceID != instance {
			t.Fatal("incarnation mismatch", e)
		}
		if e.Inbox != nil {
			inserted = inserted || e.Inbox.Kind == "inserted"
			claimed = claimed || e.Inbox.Kind == "claimed"
		}
		if e.Status != nil {
			idle = idle || e.Status.Status == agent.StatusIdle
		}
		if e.Disposed != nil {
			disposed = e.Disposed.Snapshot.Disposed
		}
	}
	if !inserted || !claimed || !idle || !disposed || notices.Dropped() != 0 {
		t.Fatal(inserted, claimed, idle, disposed, notices.Dropped())
	}
	if _, err := c.service.Get(ctx, a.ID()); !errors.Is(err, agent.ErrNotFound) {
		t.Fatal("disposed Registry entry", err)
	}
	if err := f.host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	assertNoLeases(t, f.host)
}
