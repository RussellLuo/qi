// Package processcontrol owns process signals and the bounded cleanup deadline.
package processcontrol

import (
	"context"
	"os"
	"sync"
	"syscall"
	"time"
)

const ShutdownGrace = 5 * time.Second

// ProcessShutdown owns process completion, never the lifetime of a generation.
// A forced exit is not evidence that dispose or durable settlement completed.
type ProcessShutdown struct {
	mu                         sync.Mutex
	pending, completed, forced bool
	done                       chan struct{}
	timer                      *time.Timer
	dispose                    func() error
	forceExit, complete        func(int)
	grace                      time.Duration
}

func NewProcessShutdown(dispose func() error, forceExit, complete func(int), grace time.Duration) *ProcessShutdown {
	return &ProcessShutdown{
		done:      make(chan struct{}),
		dispose:   dispose,
		forceExit: forceExit,
		complete:  complete,
		grace:     grace,
	}
}

// Shutdown calls coalesce and keep the first request's exit code.
func (s *ProcessShutdown) Shutdown(code int) <-chan struct{} {
	s.start(code, false)
	return s.done
}

// Interrupt starts disposal, or immediately escalates with this signal's code.
func (s *ProcessShutdown) Interrupt(code int) {
	s.start(code, true)
}

func (s *ProcessShutdown) start(code int, interrupt bool) {
	s.mu.Lock()
	if s.pending {
		s.mu.Unlock()
		if interrupt {
			s.finish(code, true, false)
		}

		return
	}

	s.pending = true
	s.timer = time.AfterFunc(s.grace, func() { s.finish(code, true, true) })
	s.mu.Unlock()
	go func() {
		err := s.dispose()
		s.finish(code, interrupt || err != nil, false)
		close(s.done)
	}()
}

func (s *ProcessShutdown) finish(code int, force, timeout bool) {
	s.mu.Lock()
	if s.forced || (s.completed && (timeout || !force)) {
		s.mu.Unlock()
		return
	}

	callback := s.complete
	if force {
		s.forced = true
		callback = s.forceExit
	} else {
		s.completed = true
	}

	s.timer.Stop()
	s.mu.Unlock()
	callback(code)
}

// RunProcess bounds cleanup after work requests shutdown. The work owns all
// resource release; signals cancel its parent but do not release live resources.
// forceExit is supplied only at the executable boundary (or by tests).
func RunProcess(signals <-chan os.Signal, forceExit func(int), work func(context.Context, func(int)) (int, error)) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	exit := make(chan int, 2)
	var disposeErr error
	// begin(0) can arm cleanup before the Host has closed. Once work finishes,
	// its cleanup error must turn that provisional success into a failure.
	// Reading after done closes keeps timeout/signal callbacks race-free.
	finalCode := func(code int) int {
		select {
		case <-done:
			if code == 0 && disposeErr != nil {
				return 1
			}
		default:
		}
		return code
	}
	shutdown := NewProcessShutdown(func() error {
		<-done
		return disposeErr
	},
		func(code int) {
			code = finalCode(code)
			forceExit(code)
			exit <- code
		}, func(code int) {
			exit <- finalCode(code)
		}, ShutdownGrace)
	go func() {
		code, err := work(ctx, func(code int) { shutdown.Shutdown(code) })
		disposeErr = err
		close(done)
		shutdown.Shutdown(code)
	}()

	for {
		select {
		case sig, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			code := 0
			if sig == syscall.SIGINT {
				code = 130
			}
			// Arm the deadline before cancellation can block in any native port.
			shutdown.Interrupt(code)
			cancel()
		case code := <-exit:
			return code
		}
	}
}
