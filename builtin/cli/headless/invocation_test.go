package headless_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/builtin/cli/headless"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	sessionruntime "github.com/RussellLuo/qi/kernel/core/session/plugin"
)

type noInput struct{}

func (noInput) Read([]byte) (int, error) { panic("pure help/cancelled Prepare read stdin") }
func TestInvocationMetadataSeparatesHelpInputAndApplicationErrors(t *testing.T) {
	parser := headless.Parser{}
	call, help, err := launchcontract.Parse(parser, []string{"--json", "--help"}, false)
	if err != nil || call != nil || !strings.Contains(help, "Usage: qi --profile") {
		t.Fatal(call, help, err)
	}
	_, _, err = launchcontract.Parse(parser, []string{"--json", "--bogus"}, true)
	var projection launchcontract.OutputError
	if !errors.As(err, &projection) {
		t.Fatal("parser lost JSON application error", err)
	}
	_, _, err = launchcontract.Parse(parser, []string{"--session-id", "--json"}, true)
	if errors.As(err, &projection) {
		t.Fatal("option value incorrectly enabled JSON", err)
	}
	call, _, err = launchcontract.Parse(parser, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := call.Prepare(ctx, launchcontract.Input{Stdin: noInput{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	call, _, err = launchcontract.Parse(parser, []string{"--json"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call.Prepare(context.Background(), launchcontract.Input{Stdin: strings.NewReader("\xff")}); !errors.As(err, &projection) {
		t.Fatal("stdin failure lost application classification", err)
	}
}

type borrowedInput struct {
	started, release, finished chan struct{}
	closed                     bool
}

func (r *borrowedInput) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.finished)
	return 0, io.EOF
}
func (r *borrowedInput) Close() error { r.closed = true; return nil }

func TestPrepareCancelsBlockedBorrowedInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--json", "-"}} {
		call, _, err := launchcontract.Parse(headless.Parser{}, args, false)
		if err != nil {
			t.Fatal(err)
		}
		input := &borrowedInput{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := call.Prepare(ctx, launchcontract.Input{Stdin: input}); done <- err }()
		select {
		case <-input.started:
		case <-time.After(time.Second):
			t.Fatal("read did not start")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Prepare did not cancel")
		}
		if input.closed {
			t.Fatal("closed borrowed stream")
		}
		close(input.release)
		<-input.finished
	}
}

type brokenInput struct{}

func (brokenInput) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestPrepareProjectsReadFailure(t *testing.T) {
	call, _, err := launchcontract.Parse(headless.Parser{}, []string{"--json", "-"}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.Prepare(context.Background(), launchcontract.Input{Stdin: brokenInput{}})
	var projection launchcontract.OutputError
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.As(err, &projection) {
		t.Fatal(err)
	}
}

func TestPreparedHeaderBindingPrecedesRecoveryAndPreservesPrototype(t *testing.T) {
	original := &sessionruntime.Plugin{}
	priorCalls := 0
	original.HeaderCheck = func(protocol.SessionHeader) error { priorCalls++; return io.ErrUnexpectedEOF }
	call, _, err := launchcontract.Parse(headless.Parser{}, []string{"--json", "--session-id", "old", "hello"}, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := call.Prepare(context.Background(), launchcontract.Input{Stdin: noInput{}})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := launchcontract.Bind(prepared, []gordis.Plugin{original})
	if err != nil || bound[0] == original {
		t.Fatal(bound, err)
	}
	store := bound[0].(*sessionruntime.Plugin)
	err = store.HeaderCheck(protocol.SessionHeader{ID: "old"})
	var admission *headless.ResumeError
	var projection launchcontract.OutputError
	if !errors.As(err, &admission) || !errors.As(err, &projection) || priorCalls != 0 {
		t.Fatal("rejected historical header reached recovery hook", err, priorCalls)
	}
	cwd := "/test"
	if err := store.HeaderCheck(protocol.SessionHeader{ID: "old", CWD: &cwd}); !errors.Is(err, io.ErrUnexpectedEOF) || priorCalls != 1 {
		t.Fatal("binding lost prior hook", err, priorCalls)
	}
	if err := original.HeaderCheck(protocol.SessionHeader{ID: "old"}); !errors.Is(err, io.ErrUnexpectedEOF) || priorCalls != 2 {
		t.Fatal("prototype mutated", err, priorCalls)
	}
}

func TestPreparedBindingsAreDetachedAndKeepDriverWrapper(t *testing.T) {
	calls := 0
	original := &agentplugin.Plugin{WrapDriver: func(driver agentloop.Driver) agentloop.Driver { calls++; return driver }}
	parse := func() launchcontract.Prepared {
		call, err := (headless.Parser{}).Parse([]string{"hello"}, true)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := call.Prepare(context.Background(), launchcontract.Input{Stdin: noInput{}})
		if err != nil {
			t.Fatal(err)
		}
		return prepared
	}
	first, second := parse(), parse()
	a, err := first.Bind([]gordis.Plugin{original})
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Bind([]gordis.Plugin{original})
	if err != nil {
		t.Fatal(err)
	}
	if a[0] == original || b[0] == original || a[0] == b[0] || !reflect.DeepEqual(original.Spec().Requires, a[0].Spec().Requires) {
		t.Fatal("bound prototype or service requirements changed")
	}
	a[0].(*agentplugin.Plugin).WrapDriver(nil)
	b[0].(*agentplugin.Plugin).WrapDriver(nil)
	original.WrapDriver(nil)
	if calls != 3 {
		t.Fatal("lost original driver wrapper", calls)
	}
}
