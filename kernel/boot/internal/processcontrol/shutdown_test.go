package processcontrol

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestShutdownOutcomes(t *testing.T) {
	expected := map[string][]string{
		"normal":          {"dispose", "complete:0"},
		"coalesce":        {"dispose", "complete:0"},
		"dispose_error":   {"dispose", "force:0"},
		"interrupt":       {"dispose", "force:130"},
		"timeout_zero":    {"dispose", "force:0"},
		"timeout_one":     {"dispose", "force:1"},
		"repeat_term":     {"dispose", "force:0"},
		"repeat_int":      {"dispose", "force:130"},
		"normal_repeat":   {"dispose", "force:130"},
		"interrupt_error": {"dispose", "force:130"},
	}

	for _, name := range []string{
		"normal",
		"coalesce",
		"dispose_error",
		"interrupt",
		"timeout_zero",
		"timeout_one",
		"repeat_term",
		"repeat_int",
		"normal_repeat",
		"interrupt_error",
	} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			called := make(chan string, 2)
			var mu sync.Mutex
			trace := []string{}
			add := func(s string) {
				mu.Lock()
				trace = append(trace, s)
				mu.Unlock()
			}
			failure := name == "dispose_error" || name == "interrupt_error"
			grace := time.Second
			if name == "timeout_zero" || name == "timeout_one" {
				grace = 40 * time.Millisecond
			}

			s := NewProcessShutdown(func() error {
				add("dispose")
				<-release
				if failure {
					return errors.New("dispose failed")
				}

				return nil
			},
				func(code int) {
					called <- exitLabel("force", code)
				}, func(code int) {
					called <- exitLabel("complete", code)
				}, grace)

			switch name {
			case "interrupt", "interrupt_error", "repeat_term":
				s.Interrupt(130)
			case "repeat_int":
				s.Interrupt(0)
			case "timeout_one":
				s.Shutdown(1)
			default:
				s.Shutdown(0)
			}
			// Wait until disposal starts before escalation.
			deadline := time.Now().Add(time.Second)

			for {
				mu.Lock()
				started := len(trace) > 0
				mu.Unlock()
				if started {
					break
				}

				if time.Now().After(deadline) {
					t.Fatal("dispose did not start")
				}

				time.Sleep(time.Millisecond)
			}

			switch name {
			case "coalesce":
				if s.Shutdown(1) != s.Shutdown(130) {
					t.Fatal("shutdown did not coalesce")
				}
			case "repeat_term":
				s.Interrupt(0)
			case "repeat_int", "normal_repeat":
				s.Interrupt(130)
			}

			if name != "timeout_zero" && name != "timeout_one" {
				close(release)
			}

			select {
			case label := <-called:
				add(label)
			case <-time.After(time.Second):
				t.Fatal("exit callback missing")
			}

			if name == "timeout_zero" || name == "timeout_one" {
				close(release)
			}

			<-s.done

			select {
			case label := <-called:
				t.Fatalf("duplicate exit: %s", label)
			default:
			}

			if !slices.Equal(trace, expected[name]) {
				t.Fatalf("got %v want %v", trace, expected[name])
			}
		})
	}
}

func exitLabel(kind string, code int) string {
	switch code {
	case 0:
		return kind + ":0"
	case 1:
		return kind + ":1"
	default:
		return kind + ":130"
	}
}

func TestShutdownConcurrentRequestsDisposeOnce(t *testing.T) {
	release := make(chan struct{})
	var count int
	s := NewProcessShutdown(
		func() error {
			count++
			<-release
			return nil
		},
		func(int) { t.Error("unexpected force") },
		func(int) {},
		time.Second,
	)
	var requests sync.WaitGroup

	for i := 0; i < 100; i++ {
		requests.Go(func() { s.Shutdown(0) })
	}

	requests.Wait()
	close(release)
	<-s.done
	if count != 1 {
		t.Fatalf("dispose count=%d", count)
	}
}

// Report a Host cleanup failure even when the consumer armed its deadline with zero.
func TestRunProcessReportsCleanupFailureAfterBeginSuccess(t *testing.T) {
	forced := make(chan int, 1)
	code := RunProcess(nil, func(code int) { forced <- code }, func(_ context.Context, begin func(int)) (int, error) {
		begin(0)
		return 0, errors.New("Host cleanup failed")
	})
	if code != 1 {
		t.Fatal("cleanup failure exited successfully", code)
	}
	if code := <-forced; code != 1 {
		t.Fatal("forced callback exited successfully", code)
	}
}
