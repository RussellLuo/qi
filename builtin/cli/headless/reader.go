package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/RussellLuo/qi/kernel/core/gordisbridge"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/session"
)

// ObserveEvents provides lossy wakeups only. Reading the Store remains the
// authority, so polling and the final watermark barrier survive dropped hints.
type observeEvents func(func(context.Context, gordisbridge.SessionObservation) error) (func(), error)
type readerFinish struct {
	through protocol.Sequence
	text    *string
}
type projectionReader struct {
	finish chan readerFinish
	done   chan struct{}
	err    error
	stop   func()
}

func startReader(ctx context.Context, current session.Session, after protocol.Sequence, cwd string, sink io.Writer, observe observeEvents, cancel func()) (*projectionReader, error) {
	r := &projectionReader{finish: make(chan readerFinish), done: make(chan struct{}), stop: func() {}}
	hint := make(chan struct{}, 1)
	if observe != nil {
		stop, err := observe(func(_ context.Context, o gordisbridge.SessionObservation) error {
			if o.SessionID == current.ID() {
				select {
				case hint <- struct{}{}:
				default:
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		r.stop = stop
	}
	ready := make(chan error, 1)
	go func() {
		defer close(r.done)
		p := newProjector(sink)
		cursor := after
		readThrough := func(through protocol.Sequence) error {
			for event, err := range current.Events(ctx, cursor) {
				if err != nil {
					return err
				}
				if event.Seq > through {
					break
				}
				if event.Seq != cursor+1 {
					return fmt.Errorf("headless projection cannot read seq %d", cursor+1)
				}
				if err := p.push(event); err != nil {
					return fmt.Errorf("write JSON projection at seq %d: %w", event.Seq, err)
				}
				// Filtered events also advance; a multi-line event is acknowledged only
				// after all its application lines succeeded. Failure ends this worker.
				cursor = event.Seq
			}
			if cursor != through {
				return fmt.Errorf("headless projection stopped at %d before watermark %d", cursor, through)
			}
			return nil
		}
		r.err = p.session(current.ID(), cwd)
		ready <- r.err
		if r.err != nil {
			cancel()
			return
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case finish := <-r.finish:
				r.err = readThrough(finish.through)
				if r.err == nil && finish.text != nil {
					r.err = p.final(*finish.text)
				}
				if r.err != nil {
					cancel()
				}
				return
			case <-hint:
			case <-ticker.C:
			}
			if r.err = readThrough(current.Watermark()); r.err != nil {
				cancel()
				return
			}
		}
	}()
	if err := <-ready; err != nil {
		<-r.done
		r.stop()
		return nil, err
	}
	return r, nil
}
func (r *projectionReader) close(through protocol.Sequence, text *string) error {
	select {
	case r.finish <- readerFinish{through, text}:
	case <-r.done:
	}
	<-r.done
	r.stop()
	return r.err
}
