package headless

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/RussellLuo/qi/kernel/core/agent"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type runner struct {
	Sessions       session.Store
	Factory        agent.Factory
	outcomes       *outcomes
	Observe        observe
	ObserveEvents  observeEvents
	Stdout, Stderr io.Writer
	CWD            string
	Config         agent.Config
	// BeginShutdown arms the launcher's cleanup deadline before detach/drain.
	BeginShutdown func(int)
}
type request struct {
	Task, SessionID string
	Resume          bool
	JSON            bool
}
type result struct {
	SessionID         protocol.SessionID
	FirstSeq, Through protocol.Sequence
	Text              string
	Reason            *protocol.TurnEndReason
	// CleanupError is disposal failure, separate from the durable task outcome.
	CleanupError error
}

func (r result) ExitCode() int {
	if r.Reason != nil && r.Reason.Kind == "completed" {
		return 0
	}
	return 1
}

func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (r runner) Run(ctx context.Context, request request) (result result, runErr error) {
	if r.Sessions == nil || r.Factory == nil || r.outcomes == nil || r.Stdout == nil || r.Stderr == nil {
		return result, errors.New("headless runner requires services, outcomes and output streams")
	}
	if blank(request.Task) {
		return result, errors.New(taskRequired)
	}
	if request.Resume && blank(request.SessionID) {
		return result, errors.New("--session-id requires a non-empty session id")
	}
	var handle agent.Handle
	var err error
	if request.Resume {
		handle, err = r.Factory.Resume(ctx, agent.ResumeOptions{SessionID: protocol.SessionID(request.SessionID), Agent: agent.AgentOptions{Config: r.Config}})
		if errors.Is(err, session.ErrResumeUnsupported) {
			return result, fmt.Errorf("--session-id cannot resume history with this Session Store; omit --session-id to start a new Session: %w", err)
		}
		if errors.Is(err, session.ErrNotFound) {
			return result, fmt.Errorf("session %q does not exist; omit --session-id to start a new Session", request.SessionID)
		}
	} else {
		id, e := uuid()
		if e != nil {
			return result, e
		}
		sid := protocol.SessionID("session-" + id)
		header := protocol.SessionHeader{Version: protocol.SessionFormatVersion, ID: sid, CreatedAt: time.Now().UnixMilli(), CWD: &r.CWD}
		handle, err = r.Factory.Create(ctx, agent.CreateOptions{Session: session.CreateRequest{ID: sid, Header: &header}, Agent: agent.AgentOptions{Config: r.Config}})
	}
	if err != nil {
		return result, err
	}
	current := handle.Session()
	result.SessionID = current.ID()
	// Install cleanup immediately after owned acquisition, including admission rejection.
	defer func() {
		if r.BeginShutdown != nil {
			code := result.ExitCode()
			if runErr != nil {
				code = 1
			}
			r.BeginShutdown(code)
		}
		if handle != nil {
			result.CleanupError = handle.Dispose(context.WithoutCancel(ctx))
		}
	}()
	if err = adoptable(ctx, current, r.CWD); err != nil {
		return result, err
	}
	currentAgent := handle.Agent()
	// Handle disposal drains activity before detaching; the launcher closes Host.
	beginCleanup := func() {
		if r.BeginShutdown != nil {
			code := result.ExitCode()
			if runErr != nil {
				code = 1
			}
			r.BeginShutdown(code)
		}
	}

	cancel := func() { _ = currentAgent.CancelActivity(protocol.AgentCancelCause{Kind: "parent"}) }
	failOutput := func() {
		if r.BeginShutdown != nil {
			r.BeginShutdown(1)
		}
		cancel()
	}
	stopParent := context.AfterFunc(ctx, cancel)
	defer stopParent()
	if err = currentAgent.WhenIdle(ctx); err != nil {
		cancel()
		return result, err
	}
	if err = adoptable(ctx, current, r.CWD); err != nil {
		return result, err
	}
	result.FirstSeq = current.Watermark()
	mark := r.outcomes.Mark()
	reasoning := &reasoning{writer: r.Stderr, newline: true, cancel: failOutput}
	if r.Observe != nil && !request.JSON {
		dispose, err := r.Observe(func(_ context.Context, o contracts.AssistantStreamObservation) error {
			if o.Agent == currentAgent {
				reasoning.frame(o.Frame)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		defer func() { beginCleanup(); dispose() }()
	}
	var reader *projectionReader
	if request.JSON {
		reader, err = startReader(context.WithoutCancel(ctx), current, result.FirstSeq, r.CWD, r.Stdout, r.ObserveEvents, failOutput)
		if err != nil {
			return result, err
		}
		defer func() {
			if reader != nil {
				if runErr != nil && r.BeginShutdown != nil {
					r.BeginShutdown(1)
				}
				runErr = errors.Join(runErr, reader.close(current.Watermark(), nil))
			}
		}()
	}
	messageID, err := uuid()
	if err != nil {
		return result, err
	}
	message := protocol.NewTextMessage(protocol.MessageID("message-"+messageID), protocol.RoleUser, protocol.SourceUser, request.Task)
	if err = currentAgent.Followup(ctx, message); err != nil {
		return result, err
	}
	// Parent cancellation is explicit. Settlement reads and idle wait survive it.
	if err = currentAgent.WhenIdle(context.WithoutCancel(ctx)); err != nil {
		return result, err
	}
	if err = reasoning.finish(); err != nil {
		return result, fmt.Errorf("write reasoning: %w", err)
	}
	result.Through = current.Watermark()
	if err = r.outcomes.Check(context.WithoutCancel(ctx), current, mark); err != nil {
		return result, err
	}
	result.Text, result.Reason, err = summarize(context.WithoutCancel(ctx), current, result.FirstSeq, result.Through)
	if err != nil {
		return result, err
	}
	if result.Reason != nil && result.Reason.Kind == "error" && result.Reason.Error != nil {
		detail := result.Reason.Error
		if err = write(r.Stderr, fmt.Sprintf("qi: %s: %s\n", detail.Code, detail.Message)); err != nil {
			return result, err
		}
	}
	if reader != nil {
		err = reader.close(result.Through, &result.Text)
		reader = nil
	} else {
		err = write(r.Stdout, result.Text+"\n")
	}
	if err != nil {
		failOutput()
		return result, fmt.Errorf("write headless output: %w", err)
	}
	// Output and the durable outcome are confirmed; disposal starts here, before
	// observers, the attachment or any Host generation can be released.
	if r.BeginShutdown != nil {
		r.BeginShutdown(result.ExitCode())
	}

	return result, nil
}
func write(w io.Writer, text string) error {
	n, err := io.WriteString(w, text)
	if err == nil && n != len(text) {
		return io.ErrShortWrite
	}
	return err
}

func adoptable(ctx context.Context, current session.Session, cwd string) error {
	h := current.Header()
	preset := h.AgentPreset
	for event, err := range current.Events(ctx, -1) {
		if err != nil {
			return err
		}
		if event.Type != "agent-preset/selected" {
			continue
		}
		var data struct {
			AgentPreset *string `json:"agentPreset"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.AgentPreset == nil || *data.AgentPreset == "" {
			return fmt.Errorf("session %q records a malformed agent-preset/selected event and cannot be adopted", current.ID())
		}
		preset = data.AgentPreset
	}
	if preset != nil {
		return fmt.Errorf("session %q runs under agent preset %q, which headless does not compose", current.ID(), *preset)
	}
	if (h.Origin != nil && *h.Origin == "subagent") || h.ParentSession != nil {
		return fmt.Errorf("session %q is a subagent or forked session and cannot be driven directly", current.ID())
	}
	if h.CWD == nil {
		return fmt.Errorf("session %q recorded no working directory, so it cannot be adopted", current.ID())
	}
	if *h.CWD != cwd {
		return fmt.Errorf("session %q was recorded in %q, not %q", current.ID(), *h.CWD, cwd)
	}
	return nil
}
func summarize(ctx context.Context, current session.Session, after, through protocol.Sequence) (string, *protocol.TurnEndReason, error) {
	text := ""
	var reason *protocol.TurnEndReason
	started := false
	cursor := after
	for event, err := range current.Events(ctx, after) {
		if err != nil {
			return "", nil, err
		}
		if event.Seq > through {
			break
		}
		if event.Seq != cursor+1 {
			return "", nil, fmt.Errorf("headless summary cannot read seq %d", cursor+1)
		}
		cursor = event.Seq
		if event.Type == protocol.TurnStarted {
			started = true
			continue
		}
		if !started {
			continue
		}
		switch event.Type {
		case protocol.AssistantMessage:
			message := protocol.EventPayload[protocol.AssistantMessageData](event).Message
			joined := ""
			for _, block := range message.Content {
				if b, ok := block.(protocol.TextBlock); ok {
					joined += b.Text
				}
			}
			if joined != "" {
				text = joined
			}
		case protocol.TurnEnded:
			data := protocol.EventPayload[protocol.TurnEndData](event)
			reason = &data.Reason
		}
	}
	if cursor != through {
		return "", nil, fmt.Errorf("headless summary stopped at %d before watermark %d", cursor, through)
	}
	return text, reason, nil
}

// A decorator needs only the execution port; Run supplies its Session resources.
var _ agentloop.Driver = (*outcomeDriver)(nil)
