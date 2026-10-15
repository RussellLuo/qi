package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agentmiddleware"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

func assertAbortedActivity(t *testing.T, h *inboxQi, wantSteps int, cause CancelCause) {
	t.Helper()
	counts := map[protocol.SessionEventKind]int{}

	for _, e := range sessionEvents(t, h.current) {
		counts[e.Type]++
		if e.Type == protocol.TurnEnded {
			r := protocol.EventPayload[protocol.TurnEndData](e).Reason
			if r.Kind != "aborted" || r.Reason == nil || *r.Reason != cause {
				t.Fatal(r)
			}
		}
	}

	if counts[protocol.TurnStarted] != 1 ||
		counts[protocol.TurnEnded] != 1 ||
		counts[protocol.StepStarted] != wantSteps ||
		counts[protocol.StepEnded] != wantSteps {
		t.Fatal(counts)
	}
}

func TestDisposeAndServiceCloseDrainWithoutObservers(t *testing.T) {
	for _, disk := range []bool{false} {
		for _, closeService := range []bool{false, true} {
			t.Run(fmt.Sprintf("jsonl=%t/serviceClose=%t", disk, closeService), func(t *testing.T) {
				h := newInboxQi(t, disk, nil)
				entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				if _, err := h.tools.Register(newTestOwner("drain", 1), tools.Definition{
					Name: "schedule",
					Execute: func(ctx context.Context, _ tools.Execution) (tools.ExecutionResult, error) {
						close(entered)
						<-ctx.Done()
						close(canceled)
						<-release

						return tools.ExecutionResult{
							Value:              json.RawMessage(`{}`),
							Content:            []protocol.ContentBlock{protocol.TextBlock{Text: "finished after disposal"}},
							AdditionalContexts: []protocol.Message{inboxUser("late-context")},
						}, nil
					},
				}); err != nil {
					t.Fatal(err)
				}

				if _, _, err := h.a.Submit(context.Background(), Input{Text: "unobserved"}); err != nil {
					t.Fatal(err)
				}

				_ = nextInboxRequest(t, h)
				h.llm.release <- schedulingCalls()
				waitTestSignal(t, entered)
				done := make(chan error, 1)
				go func() {
					if closeService {
						h.service.Close()
						done <- nil
					} else {
						done <- h.service.Delete(context.Background(), h.a.ID())
					}
				}()
				waitTestSignal(t, canceled)
				if err := h.a.Followup(context.Background(), inboxUser("rejected")); !errors.Is(err, ErrClosed) {
					t.Fatal("admission remained open", err)
				}

				if _, err := h.current.Attach(); !errors.Is(err, session.ErrAttached) {
					t.Fatal("detached while draining", err)
				}

				select {
				case <-done:
					t.Fatal("dispose before drain")
				default:
				}

				close(release)

				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("dispose blocked")
				}

				assertAbortedActivity(t, h, 1, CancelCause{Kind: "disposed"})
				results := 0

				for _, e := range sessionEvents(t, h.current) {
					if e.Type == protocol.ToolResulted {
						results++
					}
				}

				if results != 2 || !reflect.DeepEqual(inboxPending(t, h.a, contracts.NextStep), []string{"late-context"}) {
					t.Fatal("lost disposal settlement", results)
				}

				reservation, err := h.current.Attach()
				if err != nil {
					t.Fatal("attachment leaked", err)
				}

				_ = reservation.Close()
			})
		}
	}
}

// Each await boundary uses the actual Agent activity signal, even if the hook
// ignores cancellation and returns a successful decision after it is released.
func TestCancelAwaitBoundariesFirstWins(t *testing.T) {
	for _, disk := range []bool{false} {
		for _, stage := range []string{"pre-step", "request", "model", "retry", "turn-stopping", "rejected-turn-stopping"} {
			t.Run(fmt.Sprintf("jsonl=%t/%s", disk, stage), func(t *testing.T) {
				h := newInboxQi(t, disk, nil)
				entered, release := make(chan struct{}), make(chan struct{})
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				barrier := func(ctx context.Context) {
					close(entered)
					<-ctx.Done()
					<-release
				}
				mw := agentmiddleware.Middleware{ID: "cancel-barrier"}

				switch stage {
				case "pre-step":
					mw.PreStep = func(ctx context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
						barrier(ctx)
						return in, nil
					}
				case "request":
					mw.Request = func(ctx context.Context, in agentmiddleware.Request) (agentmiddleware.Request, error) {
						barrier(ctx)
						return in, nil
					}
				case "retry":
					mw.RequestError = func(
						ctx context.Context,
						_ agentmiddleware.RequestError,
						_ *agentmiddleware.RequestErrorAction,
					) (*agentmiddleware.RequestErrorAction, error) {
						barrier(ctx)
						return &agentmiddleware.RequestErrorAction{Kind: "retry"}, nil
					}
				case "turn-stopping", "rejected-turn-stopping":
					mw.TurnStopping = func(ctx context.Context, _ agentmiddleware.TurnStop) error {
						barrier(ctx)
						return nil
					}
					if stage == "rejected-turn-stopping" {
						mw.PreStep = func(_ context.Context, in agentmiddleware.StepInput) (agentmiddleware.StepInput, error) {
							in.Kind = "reject"
							return in, nil
						}
					}
				case "model":
					h.llm.canceled, h.llm.cancelRelease = entered, release
				}

				if stage != "model" {
					if _, err := h.middleware.Register(newTestOwner("boundary", 1), mw); err != nil {
						t.Fatal(err)
					}
				}

				if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
					t.Fatal(err)
				}

				if stage == "model" || stage == "retry" || stage == "turn-stopping" {
					_ = nextInboxRequest(t, h)
					if stage == "retry" {
						h.llm.release <- []protocol.StreamChunk{
							{
								Type: protocol.ChunkFinish,
								Reason: protocol.FinishReason{
									Kind:    protocol.FinishError,
									Failure: &protocol.LlmFailure{Code: "UNKNOWN", Message: "retry barrier"},
								},
							},
						}
					}

					if stage == "turn-stopping" {
						h.llm.release <- inboxStop()
					}
				}

				if stage != "model" {
					waitTestSignal(t, entered)
				}

				cause := CancelCause{Kind: "hook", Reason: "first"}
				if err := h.a.CancelActivity(cause); err != nil {
					t.Fatal(err)
				}

				if err := h.a.CancelActivity(CancelCause{Kind: "parent"}); err != nil {
					t.Fatal(err)
				}

				if stage == "model" {
					waitTestSignal(t, entered)
				}

				close(release)
				awaitTestAgentIdle(t, h.a)
				wantSteps := 1
				if stage == "pre-step" || stage == "rejected-turn-stopping" {
					wantSteps = 0
				}

				assertAbortedActivity(t, h, wantSteps, cause)
				if len(h.llm.requests) != 0 {
					t.Fatal("dispatched after cancellation")
				}
			})
		}
	}
}

func TestCancelStartedToolDrainsActualResultAndContexts(t *testing.T) {
	for _, disk := range []bool{false} {
		for _, keep := range []bool{false, true} {
			t.Run(fmt.Sprintf("jsonl=%t/keep=%t", disk, keep), func(t *testing.T) {
				h := newInboxQi(t, disk, nil)
				entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				calls := 0
				if _, err := h.tools.Register(newTestOwner("draining-tool", 1), tools.Definition{
					Name: "schedule",
					Execute: func(ctx context.Context, _ tools.Execution) (tools.ExecutionResult, error) {
						calls++
						close(entered)
						<-ctx.Done()
						close(canceled)
						<-release

						return tools.ExecutionResult{
							Value:              json.RawMessage(`{}`),
							Content:            []protocol.ContentBlock{protocol.TextBlock{Text: "actual late result"}},
							AdditionalContexts: []protocol.Message{inboxUser("late-context")},
						}, nil
					},
				}); err != nil {
					t.Fatal(err)
				}

				if err := h.a.Followup(context.Background(), inboxUser("initial")); err != nil {
					t.Fatal(err)
				}

				_ = nextInboxRequest(t, h)
				h.llm.release <- schedulingCalls()
				waitTestSignal(t, entered)
				if err := h.a.Steer(context.Background(), inboxUser("pending")); err != nil {
					t.Fatal(err)
				}

				if err := h.a.CancelActivity(CancelRequested, CancelOptions{KeepInbox: keep}); err != nil {
					t.Fatal(err)
				}

				waitTestSignal(t, canceled)
				waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				if h.a.WhenIdle(waitCtx) == nil {
					t.Fatal("idle before actual worker exit")
				}

				for _, e := range sessionEvents(t, h.current) {
					if e.Type == protocol.ToolResulted || e.Type == protocol.StepEnded || e.Type == protocol.TurnEnded {
						t.Fatal("settled before worker exit", e)
					}
				}

				close(release)
				awaitTestAgentIdle(t, h.a)
				assertAbortedActivity(t, h, 1, CancelRequested)
				wantPending := []string{"late-context"}
				if keep {
					wantPending = []string{"pending", "late-context"}
				}

				if calls != 1 || !reflect.DeepEqual(inboxPending(t, h.a, contracts.NextStep), wantPending) || len(h.llm.requests) != 0 {
					t.Fatal(calls, inboxPending(t, h.a, contracts.NextStep))
				}

				results := 0

				for _, e := range sessionEvents(t, h.current) {
					if e.Type == protocol.ToolResulted {
						results++
						d := protocol.EventPayload[protocol.ToolResultData](e)
						if results == 1 &&
							(d.Error != nil || *d.Message.IsError || d.Message.Content[0].(protocol.TextBlock).Text != "actual late result") {
							t.Fatal(d)
						}

						if results == 2 && (d.Error == nil || d.Error.Code != "ABORTED_BEFORE_DISPATCH") {
							t.Fatal(d)
						}
					}
				}

				if results != 2 {
					t.Fatal(results)
				}
			})
		}
	}
}
