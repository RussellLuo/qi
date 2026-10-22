package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/protocol"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const ID = "test/tool-process/你好"

// ProfilePlugin uses the launcher's ASCII Plugin ID grammar while sharing the
// same business implementation as the protocol identity fixture.
type ProfilePlugin struct{ Plugin }

const ProfileID = "test/tool-process/profile"

func (*ProfilePlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: ProfileID, Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return new(ProfilePlugin) }}
}

type Plugin struct {
	Mode      string `json:"mode"`
	Directory string `json:"directory"`
	Prefix    string `json:"prefix"`
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: ID, Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()}, New: func() gordis.Plugin { return new(Plugin) }}
}

func (p *Plugin) Validate() error {
	if p.Mode == "validate" {
		return errors.New("fixture validation failed")
	}
	return nil
}

func (p *Plugin) mark(name string) error {
	if p.Directory == "" {
		return nil
	}
	return os.WriteFile(filepath.Join(p.Directory, name), []byte("done"), 0600)
}

func (p *Plugin) Activate(ctx context.Context, scope *gordis.Scope) error {
	if p.Mode == "empty-environment" && os.Getenv("QI_PT02_AMBIENT") != "" {
		return errors.New("fixture inherited ambient host environment")
	}
	stopping, fail := make(chan struct{}), make(chan struct{})
	if err := scope.OnStop("release waiting Tool", func(context.Context) error { close(stopping); return p.mark("stopping") }); err != nil {
		return err
	}
	if err := scope.Defer("fixture cleanup", func(context.Context) error {
		if p.Mode == "waitstop" || strings.HasPrefix(p.Mode, "barrier-") {
			if _, err := os.Stat(filepath.Join(p.Directory, "exited")); err != nil {
				return fmt.Errorf("worker not drained: %w", err)
			}
		}
		if err := p.mark("cleaned"); err != nil {
			return err
		}
		if p.Mode == "cleanup-error" {
			return errors.New("fixture cleanup failed")
		}
		return nil
	}); err != nil {
		return err
	}
	if p.Mode == "activate" {
		return errors.New("fixture activation failed")
	}
	if p.Mode == "background" {
		if err := scope.AfterReady("fixture failure", func(ctx context.Context) error {
			select {
			case <-fail:
				return errors.New("fixture background failure")
			case <-ctx.Done():
				return ctx.Err()
			}
		}); err != nil {
			return err
		}
	}
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	d := tools.Definition{
		Name: "greet", Description: "Greet in an ordinary Plugin child process.",
		Parameters: tools.JSONSchema(`{"type":"object","properties":{"name":{"type":"string","minLength":1}},"required":["name"],"additionalProperties":false}`),
		Output:     tools.JSONSchema(`{"type":"object","properties":{"pid":{"type":"integer"},"greeting":{"type":"string"}},"required":["pid","greeting"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, e tools.Execution) (tools.ExecutionResult, error) {
			if p.Mode != "semantic" && (e.CWD != "" || e.Identity != (tools.ExecutionIdentity{})) {
				return tools.ExecutionResult{}, errors.New("unexpected business context")
			}
			if p.Mode == "semantic" {
				value, _ := json.Marshal(e)
				return tools.ExecutionResult{Value: value, Meta: json.RawMessage(` {"n":900719925474099312345} `), Content: []protocol.ContentBlock{protocol.TextBlock{Text: "结果"}, protocol.ReasoningBlock{Text: "思考"}, protocol.ToolAdditionBlock{ToolName: "added"}, protocol.ToolRemovalBlock{ToolName: "removed"}}, AdditionalContexts: []protocol.Message{protocol.NewTextMessage("context", protocol.RoleUser, "fixture", "extra")}, ConcludesTurn: true}, nil
			}
			if err := p.mark("started"); err != nil {
				return tools.ExecutionResult{}, err
			}
			switch p.Mode {
			case "barrier-cancel", "barrier-timeout":
				<-ctx.Done()
				if err := p.mark("cancelled"); err != nil {
					return tools.ExecutionResult{}, err
				}
				// Deliberately outlive both cancellation and OnStop. Only the test
				// releases this real worker; cleanup must wait for its owner lease.
				for {
					if _, err := os.Stat(filepath.Join(p.Directory, "release")); err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if err := p.mark("exited"); err != nil {
					return tools.ExecutionResult{}, err
				}
				return tools.ExecutionResult{}, ctx.Err()
			case "error", "abort-error":
				return tools.ExecutionResult{}, errors.New("business failed")
			case "cancelled":
				return tools.ExecutionResult{}, context.Canceled
			case "timeout":
				<-ctx.Done()
				return tools.ExecutionResult{}, ctx.Err()
			case "panic":
				panic("fixture panic")
			case "waitstop":
				<-ctx.Done()
				if err := p.mark("cancelled"); err != nil {
					return tools.ExecutionResult{}, err
				}
				<-stopping
				if err := p.mark("exited"); err != nil {
					return tools.ExecutionResult{}, err
				}
			case "background":
				close(fail)
			}
			var args struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(e.Arguments, &args); err != nil {
				return tools.ExecutionResult{}, err
			}
			value, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "greeting": p.Prefix + args.Name})
			result := tools.ExecutionResult{Value: value, Content: []protocol.ContentBlock{protocol.TextBlock{Text: string(value)}}}
			switch p.Mode {
			case "meta":
				result.Meta = json.RawMessage(`{}`)
			case "contexts":
				result.AdditionalContexts = []protocol.Message{protocol.NewTextMessage("context", protocol.RoleUser, "fixture", "extra")}
			case "concludes":
				result.ConcludesTurn = true
			case "content":
				result.Content = []protocol.ContentBlock{protocol.TextBlock{Text: "different"}}
			case "multiple":
				result.Content = append(result.Content, protocol.TextBlock{Text: "extra"})
			case "invalid-json":
				result.Value = json.RawMessage(`broken`)
			case "wrong-output":
				result.Value = json.RawMessage(`123`)
				result.Content = []protocol.ContentBlock{protocol.TextBlock{Text: "123"}}
			case "oversize":
				result.Value, _ = json.Marshal(map[string]any{"pid": os.Getpid(), "greeting": strings.Repeat("x", (128 * 1024))})
				result.Content = []protocol.ContentBlock{protocol.TextBlock{Text: string(result.Value)}}
			}
			return result, nil
		},
	}
	switch p.Mode {
	case "semantic":
		d.Output = tools.JSONSchema(`{}`)
		d.DeferLoading = true
		d.Timeout = -time.Nanosecond
	case "deferred":
		d.DeferLoading = true
	case "large-timeout":
		d.Timeout = (30 * time.Second) + time.Millisecond
	case "fractional-timeout":
		d.Timeout = time.Nanosecond
	case "negative-timeout":
		d.Timeout = -time.Nanosecond
	case "timeout":
		d.Timeout = 20 * time.Millisecond
	case "barrier-timeout":
		d.Timeout = 200 * time.Millisecond
	case "abort-error":
		d.FailureMode = tools.ToolFailureAbort
	case "schema":
		d.Parameters = tools.JSONSchema(`{"type":42}`)
	}
	if p.Mode == "middleware" {
		_, err = runtime.Bind(scope).RegisterMiddleware(tools.Middleware{ID: "child", Pre: func(_ context.Context, e tools.Execution) (tools.Execution, error) { return e, nil }})
		return err
	}
	if p.Mode == "late-register" {
		if err := scope.AfterReady("late registration", func(ctx context.Context) error {
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if _, err := os.Stat(filepath.Join(p.Directory, "mutate")); err != nil {
						continue
					}
					other := d
					other.Name = "late"
					_, err := runtime.Bind(scope).Register(other)
					if err == nil {
						return errors.New("late registration unexpectedly accepted")
					}
					if err := p.mark("rejected"); err != nil {
						return err
					}
					<-ctx.Done()
					return nil
				}
			}
		}); err != nil {
			return err
		}
	}
	definitions := []tools.Definition{d}
	if p.Mode == "batch" {
		other := d
		other.Name = "other"
		definitions = append(definitions, other)
	}
	_, err = runtime.Bind(scope).RegisterBatch(definitions)
	if err == nil && p.Mode == "activate-after-register" {
		return errors.New("fixture failed after registration")
	}
	return err
}
