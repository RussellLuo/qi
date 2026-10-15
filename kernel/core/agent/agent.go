// Package agent owns input admission and activity lifetimes. Execution dependencies are borrowed.
package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sync"
	"sync/atomic"

	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/contracts"
	"github.com/RussellLuo/qi/kernel/core/internal/observation"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

type submission struct {
	invocation protocol.InvocationID
	output     *subscription
}

type loopAgent struct {
	contracts.AssistantStreamState
	driver               agentloop.Driver
	observer             AgentObserver
	initialNotifications []AgentNotification
	initialDropped       uint64
	initialWake          bool
	notifications        map[*observation.Subscription[AgentNotification]]struct{}
	streams              map[*observation.Subscription[contracts.AssistantStreamObservation]]struct{}
	session              session.Session
	id                   ID
	sessionID            protocol.SessionID
	parentID             ID
	config               Config
	cleanupResources     Cleanup
	reservation          contracts.SessionReservation
	requestState         *agentloop.RequestState
	ctx                  context.Context
	stop                 context.CancelCauseFunc
	done                 chan struct{}
	shutdownOnce         sync.Once
	errorReported        atomic.Bool // worker-owned: native Loop error notification suppresses fallback
	cleanupErr           error       // published by closing done
	shutdownErr          error       // written before cleanup, under shutdownOnce
	inbox                chan submission
	// transitions serializes input admission, claims, and cancellation. When
	// both locks are needed, acquire transitions before mu.
	transitions sync.Mutex
	// mu protects state read by callers independently of input transitions.
	mu               sync.Mutex
	ready            bool
	disposed         bool
	status           Status
	active           protocol.InvocationID
	activeStop       context.CancelCauseFunc
	idle             chan struct{}
	nextRun          uint64
	invocationPrefix string
	observation      *subscription
	inputInbox       *session.Inbox
	pendingOutputs   map[protocol.MessageID]*submission
	activeOutput     *submission
	activityCtx      context.Context
	wakeRequested    bool
}

func (a *loopAgent) ID() ID { return a.id }

func (a *loopAgent) SessionID() protocol.SessionID { return a.sessionID }

func (a *loopAgent) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.status
}

func (a *loopAgent) Disposed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.disposed
}

func (a *loopAgent) Submit(ctx context.Context, input Input) (protocol.InvocationID, iter.Seq2[contracts.DriverEvent, error], error) {
	if input.Text == "" {
		return "", nil, errors.New("agent input text is required")
	}

	a.transitions.Lock()
	defer a.transitions.Unlock()

	// Keep the queued message and its output subscription in one admission
	// transition; the worker uses the message ID to recover this submission.
	if err := a.admit(ctx); err != nil {
		return "", nil, err
	}

	invocation := a.newInvocation()
	message := protocol.NewTextMessage(protocol.MessageID(invocation.String()+"/user-1"), protocol.RoleUser, protocol.SourceUser, input.Text)
	result := newSubscription(ctx)
	request := &submission{
		invocation: invocation,
		output:     result,
	}
	if err := a.send(ctx, message, contracts.NextTurn, true); err != nil {
		result.detach()
		result.stop()
		return "", nil, err
	}
	// The worker cannot claim until the admission transition releases its gate.
	a.pendingOutputs[message.ID] = request
	a.mu.Lock()
	if a.active == "" {
		a.active = invocation
	}

	a.observation = result
	a.mu.Unlock()
	return invocation, result.stream, nil
}

func (a *loopAgent) newInvocation() protocol.InvocationID {
	a.nextRun++
	return protocol.InvocationID(fmt.Sprintf("%s-run-%d", a.invocationPrefix, a.nextRun))
}

func (a *loopAgent) admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.disposed {
		return ErrClosed
	}
	if !a.ready {
		return ErrInitializing
	}

	return nil
}

func (a *loopAgent) Send(ctx context.Context, message protocol.Message, target contracts.InboxTarget, wakeup bool) error {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	if err := a.admit(ctx); err != nil {
		return err
	}

	return a.send(ctx, message, target, wakeup)
}

func (a *loopAgent) Followup(ctx context.Context, m protocol.Message) error {
	return a.Send(ctx, m, contracts.NextTurn, true)
}

func (a *loopAgent) Steer(ctx context.Context, m protocol.Message) error {
	return a.Send(ctx, m, contracts.NextStep, true)
}

func (a *loopAgent) Inject(ctx context.Context, m protocol.Message) error {
	return a.Send(ctx, m, contracts.NextStep, false)
}

func (a *loopAgent) send(ctx context.Context, m protocol.Message, target contracts.InboxTarget, wakeup bool) error {
	if target != contracts.NextTurn && target != contracts.NextStep {
		return errors.New("invalid inbox target")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Role != protocol.RoleUser {
		return errors.New("inbox requires a user message")
	}

	a.mu.Lock()
	afterAbort := wakeup && a.status == StatusRunning && a.activityCtx != nil && a.activityCtx.Err() != nil
	a.mu.Unlock()
	if afterAbort {
		target = contracts.NextTurn
	}

	if _, err := a.inputInbox.Splice(ctx, target, int(^uint(0)>>1), 0, []protocol.Message{m}, false); err != nil {
		return err
	}

	a.notifyInbox("inserted", m, 0)
	if wakeup {
		a.mu.Lock()
		if a.status == StatusIdle {
			a.status = StatusRunning
			a.idle = make(chan struct{})
			a.activityCtx, a.activeStop = context.WithCancelCause(a.ctx)
			a.mu.Unlock()
			a.notifyStatus(StatusRunning)
			a.inbox <- submission{}
		} else {
			if afterAbort && !a.disposed && contracts.CancellationCause(context.Cause(a.activityCtx)).Kind != "disposed" {
				a.wakeRequested = true
			}

			a.mu.Unlock()
		}
	}

	return nil
}

// Splice edits pending input without waking work. Every explicit removal is
// discarded; insertions become visible only after the normalized commit.
func (a *loopAgent) Splice(
	ctx context.Context,
	target contracts.InboxTarget,
	start, count int,
	inserted []protocol.Message,
) ([]protocol.Message, error) {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	if err := a.admit(ctx); err != nil {
		return nil, err
	}

	removed, err := a.inputInbox.Splice(ctx, target, start, count, inserted, true)
	if err != nil {
		return nil, err
	}

	for _, m := range removed {
		a.discard(m, CancelRequested)
	}

	for _, m := range inserted {
		a.notifyInbox("inserted", m, 0)
	}

	return removed, nil
}

func (a *loopAgent) discard(m protocol.Message, cause CancelCause) {
	a.notifyInbox("discarded", m, 0)
	if request := a.pendingOutputs[m.ID]; request != nil {
		request.output.finish(contracts.ActivityCancellation{Cause: cause})
		delete(a.pendingOutputs, m.ID)
	}
}

func (a *loopAgent) Pending(ctx context.Context) (contracts.InboxState, error) {
	return a.inputInbox.Pending(ctx)
}

func (a *loopAgent) EnqueueContexts(ctx context.Context, messages []protocol.Message) error {
	if len(messages) == 0 {
		return nil
	}
	// This is result settlement for already admitted work, not new input
	// admission. Disposal closes Send/Splice but must still accept a drained
	// tool's contexts before the activity releases its Session reservation.
	a.transitions.Lock()
	defer a.transitions.Unlock()

	_, err := a.inputInbox.Splice(ctx, contracts.NextStep, int(^uint(0)>>1), 0, messages, false)
	if err == nil {
		for _, m := range messages {
			a.notifyInbox("inserted", m, 0)
		}
	}

	return err
}

func (a *loopAgent) AgentError(event contracts.AgentErrorObservation) {
	a.errorReported.Store(true)
	event.InstanceID = a.InstanceID()
	a.notify(AgentNotification{InstanceID: event.InstanceID, Error: &event})
}

// Append and Claim share admission's gate, including clear-before-abort.
func (a *loopAgent) Append(ctx context.Context, e protocol.SessionEvent) (protocol.Sequence, error) {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	return a.inputInbox.Append(ctx, e)
}

func (a *loopAgent) Claim(ctx context.Context, target contracts.InboxTarget, turn protocol.TurnID) ([]protocol.Message, error) {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	messages, err := a.inputInbox.Claim(ctx, target, turn)
	if err != nil {
		return nil, err
	}

	for _, m := range messages {

		a.notifyInbox("claimed", m, turn)
	}

	return messages, nil
}

func (a *loopAgent) notifyInbox(kind string, m protocol.Message, turn protocol.TurnID) {
	{
		event := contracts.AgentInboxObservation{
			Agent:      a,
			Kind:       kind,
			Message:    m.Clone(),
			Turn:       turn,
			InstanceID: a.InstanceID(),
		}
		a.notify(AgentNotification{InstanceID: event.InstanceID, Inbox: &event})
	}
}

func (a *loopAgent) clear(ctx context.Context, cause CancelCause) error {
	return a.inputInbox.Clear(ctx, func(m protocol.Message) { a.discard(m, cause) })
}

func (a *loopAgent) Configure(ctx context.Context, config Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateConfig(config); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.disposed {
		return ErrClosed
	}
	if !a.ready {
		return ErrInitializing
	}

	a.config = cloneConfig(config)

	return nil
}

func (a *loopAgent) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()

	return cloneConfig(a.config)
}

func (a *loopAgent) Cancel(ctx context.Context, invocation protocol.InvocationID, cause CancelCause) error {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := contracts.ValidateCancelCause(cause); err != nil {
		return err
	}

	a.mu.Lock()
	active := a.active == invocation && a.activeStop != nil
	a.mu.Unlock()
	// Before the worker selects its first turn, the submitted input is pending.
	if !active {
		for _, request := range a.pendingOutputs {
			if request.invocation == invocation {
				active = true
				break
			}
		}
	}

	if !active {
		return fmt.Errorf("%w: %s", ErrNotActive, invocation)
	}

	return a.cancelActivity(ctx, cause, CancelOptions{})
}

func (a *loopAgent) CancelActivity(cause protocol.AgentCancelCause, options ...CancelOptions) error {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	if err := contracts.ValidateCancelCause(cause); err != nil {
		return err
	}

	option := CancelOptions{}
	if len(options) > 0 {
		option = options[0]
	}

	return a.cancelActivity(context.Background(), cause, option)
}

func (a *loopAgent) cancelActivity(ctx context.Context, cause CancelCause, option CancelOptions) error {
	if !option.KeepInbox {
		if err := a.clear(ctx, cause); err != nil {
			return err
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !option.KeepInbox {
		a.wakeRequested = false
	}

	if a.activeStop != nil {
		a.activeStop(contracts.ActivityCancellation{Cause: cause})
	}

	return nil
}

func (a *loopAgent) WaitIdle(ctx context.Context) error { return a.WhenIdle(ctx) }

func (a *loopAgent) WhenIdle(ctx context.Context) error {
	for {
		a.mu.Lock()
		idle := a.idle
		a.mu.Unlock()

		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}

		a.mu.Lock()
		quiet := a.status == StatusIdle
		a.mu.Unlock()
		if quiet {
			return nil
		}
	}
}

func (a *loopAgent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()

	state := State{
		Status:           a.status,
		ActiveInvocation: a.active,
		Disposed:         a.disposed,
	}
	if a.observation != nil {
		state.Dropped = a.observation.dropped.Load()
	}

	return state
}

func (a *loopAgent) shutdown() {
	a.shutdownOnce.Do(a.stopAdmission)
}

func (a *loopAgent) stopAdmission() {
	a.transitions.Lock()
	defer a.transitions.Unlock()

	a.mu.Lock()
	a.disposed = true
	a.mu.Unlock()
	cause := contracts.ActivityCancellation{Cause: protocol.AgentCancelCause{Kind: "disposed"}}
	// Keep the same clear-before-abort ordering as explicit cancellation. If
	// storage is unavailable, its committed pending facts remain for recovery.
	a.shutdownErr = a.clear(context.Background(), cause.Cause)
	a.mu.Lock()
	if a.activeStop != nil {
		a.activeStop(cause)
	}

	a.stop(cause)
	a.mu.Unlock()

	for id, request := range a.pendingOutputs {
		request.output.finish(ErrClosed)
		delete(a.pendingOutputs, id)
	}
}

func (a *loopAgent) notifyStatus(status Status) {
	event := contracts.AgentStatusObservation{Agent: a, Status: status, InstanceID: a.InstanceID()}
	a.notify(AgentNotification{InstanceID: event.InstanceID, Status: &event})
}

func (a *loopAgent) cleanup() {
	// Handle disposal and initialization rollback share this drain and release path.
	a.shutdown()
	a.mu.Lock()
	cleanupResources := a.cleanupResources
	a.cleanupResources = nil
	a.disposed = true
	// cleanup runs after Driver quiescence; no later Run can use this
	// attachment's request history. Failed initialization takes the same path.
	a.requestState = nil
	a.initialNotifications = nil
	a.mu.Unlock()
	if cleanupResources != nil {
		a.cleanupErr = errors.Join(a.cleanupErr, callCleanup(context.Background(), cleanupResources))
	}

	if a.reservation != nil {
		a.cleanupErr = errors.Join(a.cleanupErr, callCleanup(context.Background(), func(context.Context) error { return a.reservation.Close() }))
	}

	a.cleanupErr = errors.Join(a.shutdownErr, a.cleanupErr)
	close(a.done)
}

func cloneConfig(config Config) Config {
	config.Loop.LLM = config.Loop.LLM.Clone()
	config.Loop.Selection = config.Loop.Selection.Clone()
	return config
}
