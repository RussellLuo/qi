package launcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type taskScope struct {
	ctx    context.Context
	result chan error
	fail   error
}

func (s taskScope) Go(_ string, fn func(context.Context) error) error {
	if s.fail != nil {
		return s.fail
	}

	go func() {
		s.result <- fn(s.ctx)
	}()

	return nil
}

func TestFiniteTaskReadyCancelClaimAndDrain(t *testing.T) {
	for _, cancelScope := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		parent, stopParent := context.WithCancel(ctx)
		scopeCtx, stopScope := context.WithCancel(ctx)
		result := make(chan error, 1)
		var checks, runs, begins atomic.Int32
		control := NewCoordinator(func(int) { begins.Add(1) })
		launch := &Context{Parent: parent, Control: control}
		var task Task
		entered, cleaning, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
		check := func(context.Context) error {
			checks.Add(1)
			return nil
		}
		run := func(ctx context.Context, begin func(int)) Completion {
			runs.Add(1)
			close(entered)
			<-ctx.Done()
			begin(1)
			close(cleaning)
			<-finish
			return Completion{Code: 1, CleanupError: ctx.Err()}
		}
		if err := task.Start(taskScope{ctx: scopeCtx, result: result}, launch, "task", check, run); err != nil {
			t.Fatal(err)
		}

		if err := task.CheckReady(ctx); err != nil || checks.Load() != 1 {
			t.Fatal(err)
		}

		if runs.Load() != 0 {
			t.Fatal("submitted before Ready")
		}

		if err := control.OpenReady(); err != nil {
			t.Fatal(err)
		}

		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		if cancelScope {
			stopScope()
		} else {
			stopParent()
		}

		select {
		case <-cleaning:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		if begins.Load() != 1 {
			t.Fatal("cleanup started before shutdown deadline")
		}

		drainCtx, drainCancel := context.WithCancel(context.Background())
		drainCancel()
		if err := task.Drain(drainCtx); !errors.Is(err, context.Canceled) {
			t.Fatal("drain completed before cleanup", err)
		}

		select {
		case <-control.Done():
			t.Fatal("reported before cleanup")
		default:
		}

		close(finish)
		if err := task.Drain(ctx); err != nil {
			t.Fatal(err)
		}

		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		completion := <-control.Done()
		if completion.Code != 1 || completion.CleanupError == nil || begins.Load() != 1 {
			t.Fatal(completion)
		}
		// Reactivation keeps the original check and completed drain; it cannot
		// repeat business work even if the old Scope has already stopped.
		if err := task.Start(
			taskScope{ctx: ctx, result: result},
			launch,
			"again",
			func(context.Context) error {
				t.Error("replaced check")
				return nil
			},
			run,
		); err != nil {
			t.Fatal(err)
		}

		if err := task.CheckReady(ctx); err != nil || checks.Load() != 2 || runs.Load() != 1 {
			t.Fatal(err)
		}

		if err := control.Claim(); !errors.Is(err, ErrClaimed) {
			t.Fatal(err)
		}

		stopParent()
		stopScope()
		cancel()
	}
}

func TestFiniteTaskStartupFailureNeverExecutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, scopeFailure := range []bool{true, false} {
		parent, stop := context.WithCancel(ctx)
		var task Task
		result := make(chan error, 1)
		control := NewCoordinator(nil)
		scope := taskScope{ctx: parent, result: result}
		if scopeFailure {
			scope.fail = errors.New("scope stopped")
		}

		err := task.Start(
			scope,
			&Context{Parent: parent, Control: control},
			"task",
			func(context.Context) error { return nil },
			func(context.Context, func(int)) Completion {
				t.Error("startup failure executed callback")
				return Completion{}
			},
		)
		if scopeFailure && err == nil {
			t.Fatal("Scope error lost")
		}

		control.Stop(errors.New("graph startup failed"))
		stop()
		if err := task.Drain(ctx); err != nil {
			t.Fatal(err)
		}

		if !scopeFailure {
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		}

		select {
		case <-control.Done():
			t.Fatal("startup failure fabricated completion")
		default:
		}
	}
}
