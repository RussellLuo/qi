package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/RussellLuo/gordis"
	agentplugin "github.com/RussellLuo/qi/kernel/core/agentloop/plugin"

	launchcontract "github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/core/agentloop"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// Parser is compiled invocation metadata; it never activates a plugin.
type Parser struct{}

func (Parser) Help() string { return help }
func (Parser) Parse(args []string, tty bool) (launchcontract.Invocation, error) {
	startup, err := parse(args, tty)
	if err != nil {
		return nil, applicationError(err, jsonRequested(args))
	}
	return invocation{startup}, nil
}

type invocation struct{ startup startup }

func (i invocation) HelpRequested() bool { return i.startup.Help }
func (i invocation) Prepare(ctx context.Context, input launchcontract.Input) (launchcontract.Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if i.startup.Help {
		return nil, errors.New("help invocation must not be prepared")
	}
	task, err := i.startup.Input(ctx, input.Stdin)
	if err != nil {
		return nil, applicationError(err, i.startup.JSON)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		return nil, err
	}
	return &prepared{request: request{Task: task, SessionID: i.startup.SessionID, Resume: i.startup.Resume, JSON: i.startup.JSON}, cwd: cwd, outcomes: new(outcomes)}, nil
}

// Prepared state is private to this plugin and this invocation. The exact
// tracker bound before Agent activation is later consumed by the runner.
type prepared struct {
	request  request
	cwd      string
	outcomes *outcomes
	launchcontract.Task
}

func (p *prepared) WorkingDirectory() string { return p.cwd }
func (p *prepared) Bind(plugins []gordis.Plugin) ([]gordis.Plugin, error) {
	result := append([]gordis.Plugin(nil), plugins...)
	for i, plugin := range result {
		switch original := plugin.(type) {
		case *agentplugin.Plugin:
			copy := *original
			previous := copy.WrapDriver
			copy.WrapDriver = func(driver agentloop.Driver) agentloop.Driver {
				if previous != nil {
					driver = previous(driver)
				}
				return p.outcomes.Wrap(driver)
			}
			result[i] = &copy
		case launchcontract.HeaderCheckBinder:
			result[i] = original.WithHeaderCheck(func(header protocol.SessionHeader) error {
				if string(header.ID) == p.request.SessionID && p.request.Resume && header.CWD == nil {
					return applicationError(&ResumeError{SessionID: p.request.SessionID}, p.request.JSON)
				}
				return nil
			})
		}
	}
	return result, nil
}

// ResumeError must be detected before JSONL restoration can repair the log.
type ResumeError struct{ SessionID string }

func (e *ResumeError) Error() string {
	return fmt.Sprintf("session %q recorded no working directory, so it cannot be adopted", e.SessionID)
}

type outputError struct{ error }

func (e *outputError) Unwrap() error                { return e.error }
func (e *outputError) WriteError(w io.Writer) error { return writeJSONError(w, e.error) }
func applicationError(err error, json bool) error {
	if json {
		return &outputError{err}
	}
	return err
}
