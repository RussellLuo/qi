package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"sync"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/prompt"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type requestSpy struct {
	adapter   scriptedAdapter
	resolve   func(context.Context, llm.ModelRef) (llm.ModelInfo, error)
	onRequest func(llm.Request)
}

func (a *requestSpy) ResolveModel(ctx context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	if a.resolve != nil {
		return a.resolve(ctx, ref)
	}

	return a.adapter.ResolveModel(ctx, ref)
}

func (a *requestSpy) Stream(ctx context.Context, r llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	if a.onRequest != nil {
		a.onRequest(r)
	}

	return a.adapter.Stream(ctx, r)
}

func driveRequest(t *testing.T, h *qi, index int, config Config) error {
	t.Helper()
	run := h.run(config)
	run.InvocationID = protocol.InvocationID(fmt.Sprintf("request-%d", index))
	return h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { return nil })
}

func headers(t *testing.T, s session.Session) []protocol.RequestHeaderData {
	t.Helper()
	result := []protocol.RequestHeaderData{}

	for _, e := range sessionEvents(t, s) {
		if e.Type == protocol.RequestHeader {
			result = append(result, protocol.EventPayload[protocol.RequestHeaderData](e))
		}
	}

	return result
}

func TestRequestMatchesCommittedSurfaceAndHookBinding(t *testing.T) {
	original := &scriptedAdapter{}
	selected := &requestSpy{
		adapter: scriptedAdapter{
			Scripts: []scriptedScript{
				{
					Chunks: toolChunks(protocol.ToolCall{
						ID:        "c",
						Name:      "echo",
						Arguments: `{"text":"hello"}`,
					}),
				},
				{Chunks: textChunks("done")},
			},
		},
	}
	h := newQi(t, original, nil)
	defer h.stop(t)

	if _, err := h.loop.coordinator.LLM.RegisterAdapter(h.owner, llm.AdapterRegistration{
		Route:    "second",
		Provider: "second",
		Adapter:  selected,
	}); err != nil {
		t.Fatal(err)
	}

	selected.resolve = func(ctx context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
		v, err := selected.adapter.ResolveModel(ctx, ref)
		effort := "high"
		max := int64(99)
		v.Defaults = protocol.CallConfig{ReasoningEffort: &effort, MaxTokens: &max}
		return v, err
	}
	changed := false
	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "binding",
		Request: func(_ context.Context, r agentmiddleware.Request) (agentmiddleware.Request, error) {
			if !changed {
				changed = true
				// Registration after PrepareRun is not part of this Run's candidates.
				if _, err := h.loop.coordinator.LLM.RegisterAdapter(h.owner, llm.AdapterRegistration{
					Route:    "late",
					Provider: "late",
					Adapter:  &scriptedAdapter{},
				}); err != nil {
					t.Fatal(err)
				}
			}

			r.Config.Provider = "second"
			r.Config.Model = "exact"

			return r, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	selected.onRequest = func(r llm.Request) {
		_, p, err := snapshot(context.Background(), h.current)
		if err != nil {
			t.Fatal(err)
		}

		if !sameJSON(r.Config, p.RequestHeader.Config) ||
			!sameJSON(r.Messages, p.Messages) ||
			!sameJSON(r.Tools, p.RequestHeader.Tools) ||
			!sameJSON(r.ToolHistory, p.ToolHistory) ||
			r.SessionID != h.current.ID() {
			t.Fatalf(
				"actual request drifted: config=%v messages=%v tools=%v history=%v requestHistory=%+v persistedHistory=%+v",
				sameJSON(r.Config, p.RequestHeader.Config),
				sameJSON(r.Messages, p.Messages),
				sameJSON(r.Tools, p.RequestHeader.Tools),
				sameJSON(r.ToolHistory, p.ToolHistory),
				r.ToolHistory,
				p.ToolHistory,
			)
		}

		if p.RequestContext.Provider != "second" || p.RequestContext.Model != "exact" {
			t.Fatal("stale adapter context")
		}

		if len(r.Messages) == 0 || r.Messages[0].Role != protocol.RoleSystem {
			t.Fatal("system is not in Surface request")
		}
		// Mutating adapter-owned input cannot corrupt its durable source.
		r.Messages[0].Content[0] = protocol.TextBlock{Text: "mutated"}
		r.Tools[0].Parameters[0] = '['
		*r.Config.MaxTokens = 7
	}
	if err := driveRequest(t, h, 1, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "declared"}}); err != nil {
		t.Fatal(err)
	}

	if len(original.Seen) != 0 || len(selected.adapter.Seen) != 2 {
		t.Fatal("hook did not select exact adapter")
	}

	hs := headers(t, h.current)
	if len(hs) != 1 ||
		hs[0].Reason != "initial" ||
		hs[0].Header.AdapterDefaults == nil ||
		!hs[0].Header.AdapterDefaults.ReasoningEffort ||
		!hs[0].Header.AdapterDefaults.MaxTokens ||
		*hs[0].Header.Config.MaxTokens != 99 {
		t.Fatalf("header/defaults = %+v", hs)
	}

	users := 0
	contexts := 0

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.UserMessage {
			users++
		}

		if e.Type == protocol.RequestContextEvent {
			contexts++
		}
	}

	if users != 1 || contexts != 1 {
		t.Fatalf("duplicate input/context: %d/%d", users, contexts)
	}
}

func TestRejectAndPreparationCancellationAdmitNoMessages(t *testing.T) {
	for _, stage := range []string{"reject", "pre-step", "request", "prepare"} {
		t.Run(stage, func(t *testing.T) {
			spy := &requestSpy{}
			h := newQi(t, spy, nil)
			defer h.stop(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			middleware := agentmiddleware.Middleware{
				ID: "gate",
				PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
					if input.Prompt.Text != "You are a test assistant." {
						t.Fatal("pre-step ran before prompt assembly")
					}

					if stage == "reject" {
						input.Kind = "reject"
					}

					if stage == "pre-step" {
						cancel()
					}

					return input, nil
				},
				Request: func(_ context.Context, r agentmiddleware.Request) (agentmiddleware.Request, error) {
					if stage == "request" {
						cancel()
					}

					return r, nil
				},
			}
			if _, err := h.middleware.Register(h.owner, middleware); err != nil {
				t.Fatal(err)
			}

			if stage == "prepare" {
				spy.resolve = func(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
					cancel()
					return llm.ModelInfo{Ref: ref}, nil
				}
			}

			err := h.loop.Drive(
				ctx,
				h.run(Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}),
				func(contracts.DriverEvent) error { return nil },
			)
			if stage == "reject" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel error = %v", err)
			}

			starts := 0

			for _, e := range sessionEvents(t, h.current) {
				switch e.Type {
				case protocol.UserMessage, protocol.SystemMessage, protocol.RequestHeader, protocol.RequestContextEvent:
					t.Fatalf("unadmitted fact at %s: %s", stage, e.Type)
				case protocol.StepStarted:
					starts++
				}
			}

			want := 1
			if stage == "reject" || stage == "pre-step" {
				want = 0
			}

			if starts != want {
				t.Fatalf("step starts = %d", starts)
			}

			facts := sessionEvents(t, h.current)
			reason := protocol.EventPayload[protocol.TurnEndData](facts[len(facts)-1]).Reason.Kind
			if stage == "reject" && reason != "blocked" {
				t.Fatalf("reject reason=%s", reason)
			}

			if len(spy.adapter.Seen) != 0 {
				t.Fatal("model dispatched")
			}
		})
	}
}

func TestHeaderReasonsContextAndHistoricalTools(t *testing.T) {
	spy := &requestSpy{
		adapter: scriptedAdapter{
			Scripts: []scriptedScript{
				{Chunks: textChunks("one")},
				{Chunks: textChunks("two")},
				{Chunks: textChunks("three")},
				{Chunks: textChunks("four")},
				{Chunks: textChunks("five")},
				{Chunks: textChunks("six")},
			},
		},
	}
	h := newQi(t, spy, nil)
	defer h.stop(t)

	window := 100
	spy.resolve = func(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
		return llm.ModelInfo{Ref: ref, ContextWindow: window}, nil
	}
	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	if err := driveRequest(t, h, 1, config); err != nil {
		t.Fatal(err)
	}

	if err := driveRequest(t, h, 2, config); err != nil {
		t.Fatal(err)
	}

	window = 200
	if err := driveRequest(t, h, 3, config); err != nil {
		t.Fatal(err)
	}

	config.Selection.Tools = []string{}
	if err := driveRequest(t, h, 4, config); err != nil {
		t.Fatal(err)
	}

	_, err := h.loop.coordinator.Tools.Register(h.owner, tools.Definition{
		Name:         "deferred",
		Description:  "later",
		DeferLoading: true,
		Parameters:   tools.JSONSchema(`{"type":"object"}`),
		Execute:      tools.JSONTool(func(context.Context, tools.Execution) (json.RawMessage, error) { return []byte(`null`), nil }),
	})
	// Tool callbacks use the explicit JSON adapter; declarations remain historical.
	if err != nil {
		t.Fatal(err)
	}

	config.Selection.Tools = []string{"deferred"}
	if err := driveRequest(t, h, 5, config); err != nil {
		t.Fatal(err)
	}
	// A new attachment using a fresh Loop records resume even with an equal header.
	h.loop = New(h.loop.coordinator)
	h.agent = &testAgentRuntime{sessionID: h.current.ID()}
	if err := driveRequest(t, h, 6, config); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, h.current)
	reasons := []string{}

	for _, d := range hs {
		reasons = append(reasons, d.Reason)
	}

	if !reflect.DeepEqual(reasons, []string{"initial", "change", "change", "resume"}) {
		t.Fatalf("reasons = %v", reasons)
	}

	if hs[0].Header.AdapterDefaults != nil || hs[1].Header.Tools != nil || !hs[2].Header.Tools[0].DeferLoading {
		t.Fatalf("optional defaults/tools/deferLoading lost: %+v", hs)
	}

	contexts, developers := 0, 0

	for _, e := range sessionEvents(t, h.current) {
		if e.Type == protocol.RequestContextEvent {
			contexts++
		}

		if e.Type == protocol.DeveloperMessage {
			developers++
			d := protocol.EventPayload[protocol.MessageData](e)
			if d.Message.Content[0].Kind() == protocol.BlockToolAddition && d.HeaderSeq == nil {
				t.Fatal("addition missing historical header")
			}
		}
	}

	if contexts != 2 || developers != 2 {
		t.Fatalf("contexts/developers = %d/%d", contexts, developers)
	}

	r := spy.adapter.Seen[4]
	if len(r.ToolHistory.Tools) != 1 ||
		r.ToolHistory.Tools[0].Name != "echo" ||
		len(r.ToolHistory.Updates) != 2 ||
		len(r.ToolHistory.Updates[1].Additions) != 1 ||
		r.ToolHistory.Updates[1].Additions[0].Name != "deferred" {
		t.Fatalf("history rebuilt from latest registry: %+v", r.ToolHistory)
	}
}

func TestSystemHistoryClearRestoreAndSeries(t *testing.T) {
	spy := &requestSpy{adapter: scriptedAdapter{Scripts: []scriptedScript{}}}

	for i := 0; i < 8; i++ {
		spy.adapter.Scripts = append(spy.adapter.Scripts, scriptedScript{Chunks: textChunks("ok")})
	}

	h := newQi(t, spy, nil)
	defer h.stop(t)

	text := "A"
	h.loop.coordinator.Prompts = prompt.NewRuntime()
	if _, err := h.loop.coordinator.Prompts.Register(h.owner, prompt.Section{
		ID:     "dynamic",
		Render: func(context.Context, prompt.Context) (string, error) { return text, nil },
	}); err != nil {
		t.Fatal(err)
	}

	spy.resolve = func(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
		return llm.ModelInfo{
			Ref:                ref,
			SystemPromptUpdate: "in-history",
			ToolUpdate:         true,
		}, nil
	}
	starts := false
	replace := false
	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "series",
		PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
			input.StartsRequestSeries = starts
			if replace {
				facts, p, err := snapshot(context.Background(), h.current)
				if err != nil {
					t.Fatal(err)
				}

				for _, seq := range p.Surface.Nodes {
					if facts[seq].Type == protocol.UserMessage {
						message := protocol.NewTextMessage(protocol.MessageID("compacted"), protocol.RoleUser, protocol.SourceUser, "compacted")
						e := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, message)
						e.SurfaceOp = &protocol.SurfaceOp{
							Op:       "replace",
							StartSeq: seq,
							EndSeq:   seq,
						}
						e.SourceEventSeqs = []protocol.Sequence{seq}
						if _, err := h.current.Append(context.Background(), e); err != nil {
							t.Fatal(err)
						}

						break
					}
				}
			}

			return input, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}

	for i, v := range []string{"A", "B", "", "C", "C", "C", "D", "D"} {
		text = v
		starts = i == 4
		replace = i == 5
		if err := driveRequest(t, h, i+1, config); err != nil {
			t.Fatal(err)
		}
	}

	for i, want := range [][]string{{"A"}, {"A", "B"}, {}, {"C"}, {"C"}, {"C"}, {"C", "D"}, {"C", "D"}} {
		actual := []string{}

		for _, m := range spy.adapter.Seen[i].Messages {
			if m.Role == protocol.RoleSystem {
				actual = append(actual, firstText([]protocol.Message{m}))
			}
		}

		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("request %d systems=%q want=%q", i, actual, want)
		}
	}

	hs := headers(t, h.current)
	reasons := []string{}

	for _, d := range hs {
		reasons = append(reasons, d.Reason)
	}

	if !reflect.DeepEqual(reasons, []string{"initial", "series", "series", "series"}) {
		t.Fatalf("series reasons=%v", reasons)
	}
}

func TestUnknownOrLateBindingFailsBeforeAdmission(t *testing.T) {
	for _, provider := range []string{"unknown", "late"} {
		t.Run(provider, func(t *testing.T) {
			h := newQi(t, &scriptedAdapter{}, nil)
			defer h.stop(t)

			if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
				ID: "missing",
				Request: func(_ context.Context, r agentmiddleware.Request) (agentmiddleware.Request, error) {
					if _, err := h.loop.coordinator.LLM.RegisterAdapter(newTestOwner("late", 2), llm.AdapterRegistration{
						Route:    "late",
						Provider: "late",
						Adapter:  &scriptedAdapter{},
					}); err != nil {
						t.Fatal(err)
					}

					r.Config.Provider = provider

					return r, nil
				},
			}); err != nil {
				t.Fatal(err)
			}

			if err := driveRequest(t, h, 1, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}); err == nil {
				t.Fatal("absent snapshot binding accepted")
			}

			for _, e := range sessionEvents(t, h.current) {
				if e.Type == protocol.UserMessage || e.Type == protocol.SystemMessage || e.Type == protocol.RequestHeader {
					t.Fatal("failed prepare admitted facts")
				}
			}
		})
	}
}

func TestStartsSeriesOnInitialResumeAndChangedHeaders(t *testing.T) {
	spy := &requestSpy{
		adapter: scriptedAdapter{
			Scripts: []scriptedScript{{Chunks: textChunks("one")}, {Chunks: textChunks("two")}, {Chunks: textChunks("three")}},
		},
	}
	h := newQi(t, spy, nil)
	defer h.stop(t)

	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "new-series",
		PreStep: func(_ context.Context, input agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
			input.StartsRequestSeries = true
			return input, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	if err := driveRequest(t, h, 1, config); err != nil {
		t.Fatal(err)
	}

	h.loop = New(h.loop.coordinator)
	h.agent = &testAgentRuntime{sessionID: h.current.ID()}
	if err := driveRequest(t, h, 2, config); err != nil {
		t.Fatal(err)
	}

	temperature := 0.0
	config.LLM.Temperature = &temperature
	if err := driveRequest(t, h, 3, config); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, h.current)
	if len(hs) != 3 {
		t.Fatalf("headers=%+v", hs)
	}

	for i, reason := range []string{"initial", "resume", "change"} {
		if hs[i].Reason != reason || !hs[i].StartsSeries {
			t.Fatalf("header %d=%+v", i, hs[i])
		}
	}
}

func TestAdapterDefaultsAreResolvedAgainOnEveryStep(t *testing.T) {
	spy := &requestSpy{
		adapter: scriptedAdapter{
			Scripts: []scriptedScript{
				{
					Chunks: toolChunks(protocol.ToolCall{
						ID:        "c",
						Name:      "echo",
						Arguments: `{"text":"hello"}`,
					}),
				},
				{Chunks: textChunks("done")},
			},
		},
	}
	resolutions := 0
	spy.resolve = func(_ context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
		resolutions++
		effort := fmt.Sprintf("effort-%d", resolutions)
		max := int64(resolutions * 100)
		return llm.ModelInfo{Ref: ref, Defaults: protocol.CallConfig{ReasoningEffort: &effort, MaxTokens: &max}}, nil
	}
	h := newQi(t, spy, nil)
	defer h.stop(t)

	if _, err := h.middleware.Register(h.owner, agentmiddleware.Middleware{
		ID: "proposal",
		Request: func(_ context.Context, r agentmiddleware.Request) (agentmiddleware.Request, error) {
			if r.Config.ReasoningEffort != nil || r.Config.MaxTokens != nil {
				t.Fatal("resolved adapter defaults leaked into next proposal")
			}

			return r, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := driveRequest(t, h, 1, Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, h.current)
	if resolutions != 2 ||
		len(hs) != 2 ||
		hs[1].Reason != "change" ||
		*hs[1].Header.Config.MaxTokens != 200 ||
		*hs[1].Header.Config.ReasoningEffort != "effort-2" {
		t.Fatalf("defaults were not re-resolved: %+v", hs)
	}
}

func TestResumeRestoresOnlyExplicitEffortForTheSameModel(t *testing.T) {
	for _, derived := range []bool{false, true} {
		t.Run(fmt.Sprint(derived), func(t *testing.T) {
			h := newQi(t, &scriptedAdapter{}, nil)
			defer h.stop(t)

			effort := "persisted"
			max := int64(10)
			header := protocol.EpochHeader{
				Config: protocol.CallConfig{
					Provider:        "fake",
					Model:           "model",
					ReasoningEffort: &effort,
					MaxTokens:       &max,
				},
			}
			if derived {
				header.AdapterDefaults = &protocol.AdapterDefaults{ReasoningEffort: true, MaxTokens: true}
			}

			if _, err := h.current.Append(
				context.Background(),
				protocol.NewSessionEvent(protocol.RequestHeader, protocol.RequestHeaderData{Header: header, Reason: "initial"}),
			); err != nil {
				t.Fatal(err)
			}

			for _, model := range []string{"model", "other"} {
				proposal, err := requestProposal(context.Background(), h.current, protocol.CallConfig{Provider: "fake", Model: model}, false)
				if err != nil {
					t.Fatal(err)
				}

				wantEffort := !derived && model == "model"
				if (proposal.ReasoningEffort != nil) != wantEffort || proposal.MaxTokens != nil {
					t.Fatalf("resume proposal=%+v", proposal)
				}
			}
		})
	}
}

// Full replay is retained as an independent oracle for request tests.
func snapshot(ctx context.Context, current session.Session) ([]protocol.SessionEvent, session.Projection, error) {
	events := []protocol.SessionEvent{}

	for event, err := range current.Events(ctx, -1) {
		if err != nil {
			return nil, session.Projection{}, err
		}

		events = append(events, event)
	}

	p, err := session.Fold(events)

	return events, p, err
}

func TestRecreatedSessionRequestHeader(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{Chunks: textChunks("one")},
			{Chunks: textChunks("two")},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	if err := driveRequest(t, h, 1, config); err != nil {
		t.Fatal(err)
	}

	if err := h.reservation.Close(); err != nil {
		t.Fatal(err)
	}
	h.reservation = nil
	if err := h.store.Delete(ctx, h.current.ID()); err != nil {
		t.Fatal(err)
	}

	current, err := h.store.Create(ctx, session.CreateRequest{ID: h.current.ID()})
	if err != nil {
		t.Fatal(err)
	}

	h.current = current
	h.agent = &testAgentRuntime{sessionID: current.ID()}
	if err := driveRequest(t, h, 2, config); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, current)
	if len(hs) != 1 || hs[0].Reason != "initial" {
		t.Fatalf("new Session request headers = %+v", hs)
	}
}

func TestSameLoopReattachRequestHeader(t *testing.T) {
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{Chunks: textChunks("one")},
			{Chunks: textChunks("two")},
			{Chunks: textChunks("three")},
			{Chunks: textChunks("four")},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	for attachment := 0; attachment < 2; attachment++ {
		reservation, err := h.current.Attach()
		if err != nil {
			t.Fatal(err)
		}

		h.agent = &testAgentRuntime{sessionID: h.current.ID()}
		h.reservation = reservation
		for turn := 0; turn < 2; turn++ {
			run := h.run(config)
			run.Attached.Reservation = reservation
			if err := h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { return nil }); err != nil {
				_ = reservation.Close()
				t.Fatal(err)
			}
		}

		if err := reservation.Close(); err != nil {
			t.Fatal(err)
		}
		h.reservation = nil
	}

	reasons := []string{}
	for _, header := range headers(t, h.current) {
		reasons = append(reasons, header.Reason)
	}

	if !reflect.DeepEqual(reasons, []string{"initial", "resume"}) {
		t.Fatalf("reattached Session request headers = %v", reasons)
	}
}

func TestRequestStateSurvivesLoopReplacementWithinAttachment(t *testing.T) {
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{Chunks: textChunks("one")},
			{Chunks: textChunks("two")},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	if err := driveRequest(t, h, 1, config); err != nil {
		t.Fatal(err)
	}

	h.loop = New(h.loop.coordinator)
	if err := driveRequest(t, h, 2, config); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, h.current)
	if len(hs) != 1 || hs[0].Reason != "initial" {
		t.Fatalf("same attachment after Loop replacement = %+v", hs)
	}
}

func TestRequestStateInitialGenerationUsesCurrentSurface(t *testing.T) {
	ctx := context.Background()
	adapter := &scriptedAdapter{
		Scripts: []scriptedScript{
			{Chunks: textChunks("one")},
			{Chunks: textChunks("two")},
		},
	}
	h := newQi(t, adapter, nil)
	defer h.stop(t)

	config := Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}}
	if err := driveRequest(t, h, 1, config); err != nil {
		t.Fatal(err)
	}

	for _, fact := range sessionEvents(t, h.current) {
		if fact.Type != protocol.UserMessage {
			continue
		}

		message := protocol.NewTextMessage("replacement", protocol.RoleUser, protocol.SourceUser, "compacted")
		event := protocol.NewMessageEvent(protocol.UserMessage, 0, 0, message)
		event.SurfaceOp = &protocol.SurfaceOp{Op: "replace", StartSeq: fact.Seq, EndSeq: fact.Seq}
		event.SourceEventSeqs = []protocol.Sequence{fact.Seq}
		if _, err := h.current.Append(ctx, event); err != nil {
			t.Fatal(err)
		}

		break
	}

	h.agent = &testAgentRuntime{sessionID: h.current.ID()}
	if err := driveRequest(t, h, 2, config); err != nil {
		t.Fatal(err)
	}

	hs := headers(t, h.current)
	if len(hs) != 2 || hs[1].Reason != "resume" || hs[1].StartsSeries {
		t.Fatalf("new attachment inherited old Surface generation = %+v", hs)
	}

	_, projection, err := snapshot(ctx, h.current)
	if err != nil || len(projection.Surface.Replacements) != 1 || h.agent.requestState.generation != 1 {
		t.Fatalf("current Surface generation = %+v, state = %+v, error = %v", projection.Surface, h.agent.requestState, err)
	}
}

func TestRunRejectsMissingRequestState(t *testing.T) {
	h := newQi(t, &scriptedAdapter{}, nil)
	defer h.stop(t)
	run := h.run(Config{})
	run.Attached.RequestState = nil
	before := h.current.Watermark()
	if err := h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { return nil }); err == nil || h.current.Watermark() != before {
		t.Fatal("missing state was admitted", err)
	}
}

func TestSharedLoopRequestStateIsolatedAcrossConcurrentAttachments(t *testing.T) {
	adapter := &scriptedAdapter{}
	for range 8 {
		adapter.Scripts = append(adapter.Scripts, scriptedScript{Chunks: textChunks("done")})
	}

	h := newQi(t, adapter, nil)
	defer h.stop(t)

	second, err := h.store.Create(context.Background(), session.CreateRequest{ID: "session-2"})
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	for _, current := range []session.Session{h.current, second} {
		workers.Go(func() {
			for attachment := 0; attachment < 2; attachment++ {
				reservation, err := current.Attach()
				if err != nil {
					t.Error(err)
					return
				}

				agent := &testAgentRuntime{sessionID: current.ID()}
				for turn := 0; turn < 2; turn++ {
					run := Run{
						Agent:        agent,
						Attached:     AttachedRun{Session: current, Reservation: reservation, Inbox: session.NewInbox(current), RequestState: &agent.requestState},
						InvocationID: protocol.InvocationID(fmt.Sprintf("%s-%d-%d", current.ID(), attachment, turn)),
						Config:       Config{LLM: protocol.CallConfig{Provider: "fake", Model: "model"}},
					}
					message := protocol.NewTextMessage(protocol.MessageID(fmt.Sprint(attachment, "-", turn)), protocol.RoleUser, protocol.SourceUser, "hello")
					if _, err := session.NewInbox(current).Splice(context.Background(), contracts.NextTurn, 0, 0, []protocol.Message{message}, false); err != nil {
						t.Error(err)
						return
					}
					if err := h.loop.Drive(context.Background(), run, func(contracts.DriverEvent) error { return nil }); err != nil {
						t.Error(err)
						_ = reservation.Close()
						return
					}
				}

				if err := reservation.Close(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}

	workers.Wait()
	for _, current := range []session.Session{h.current, second} {
		hs := headers(t, current)
		if len(hs) != 2 || hs[0].Reason != "initial" || hs[1].Reason != "resume" || current.LastTurn() != 4 {
			t.Fatalf("Session %s requests = %+v, last Turn = %d", current.ID(), hs, current.LastTurn())
		}
	}
}
