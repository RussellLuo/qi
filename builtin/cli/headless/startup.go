// Task input follows the frozen one-shot headless contract.
package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const help = `Usage: qi --profile <path> -- [options] [task...]

Answer one task and exit; the answer goes to stdout and diagnostics to stderr.

Options:
  --session-id <id>  resume an exact Session history; requires Store history opening
  --json             emit committed application events as newline-delimited JSON
  -h, --help         show application help without preparing input or starting Host

Multiple task words are joined by spaces; '-' reads complete stdin.
Without a task, non-terminal stdin supplies the task.

Examples:
  qi --profile <path> -- "say hello"
  echo "say hello" | qi --profile <path> --
  qi --profile <persistent-profile-path> -- --session-id session-… "continue"
`

const taskRequired = `a task is required, for example: qi --profile <path> -- "say hello"`

type startup struct {
	Task      string
	ReadStdin bool
	SessionID string
	Resume    bool
	JSON      bool
	Help      bool
}

// jsonRequested matches the reference raw-argv scan, including option values.
func jsonRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			return false
		case "--json":
			return true
		case "--session-id":
			i++
		}
	}
	return false
}

// Parse accepts interleaved long options, last repeated value wins. Like the
// reference parser, help bypasses unknown-option and action validation.
func parse(args []string, stdinIsTTY bool) (startup, error) {
	var s startup
	var words []string
	var grammarErr error
	options := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if options {
			switch {
			case arg == "--":
				options = false
				continue
			case arg == "--json":
				s.JSON = true
				continue
			case arg == "-h" || arg == "--help":
				s.Help = true
				continue
			case arg == "--session-id":
				if i+1 >= len(args) {
					return s, errors.New("option '--session-id <id>' argument missing")
				}
				i++
				s.SessionID = args[i]
				s.Resume = true
				continue
			case strings.HasPrefix(arg, "--session-id="):
				s.SessionID = strings.TrimPrefix(arg, "--session-id=")
				s.Resume = true
				continue
			case strings.HasPrefix(arg, "-") && arg != "-":
				if grammarErr == nil {
					grammarErr = fmt.Errorf("unknown option '%s'", arg)
				}
				continue
			}
		}
		words = append(words, arg)
	}
	if s.Help {
		return s, nil
	}
	if grammarErr != nil {
		return s, grammarErr
	}
	if len(words) > 1 {
		for _, word := range words {
			if word == "-" {
				return s, errors.New("`-` must be the only task argument")
			}
		}
	}
	s.Task = strings.Join(words, " ")
	if len(words) > 0 && blank(s.Task) {
		return s, errors.New(taskRequired)
	}
	if len(words) == 0 && stdinIsTTY {
		return s, errors.New(taskRequired)
	}
	if s.Resume && blank(s.SessionID) {
		return s, errors.New("--session-id requires a non-empty session id")
	}
	s.ReadStdin = len(words) == 0 || s.Task == "-"
	return s, nil
}

func (s startup) Input(ctx context.Context, stdin io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	task := s.Task
	if s.ReadStdin {
		if stdin == nil {
			return "", errors.New("stdin is required")
		}
		// The stream is borrowed. Cancellation returns without closing it;
		// an uninterruptible Read can remain until the process exits.
		type result struct {
			raw []byte
			err error
		}
		done := make(chan result, 1)
		go func() { raw, err := io.ReadAll(stdin); done <- result{raw, err} }()
		var raw []byte
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case read := <-done:
			if read.err != nil {
				return "", fmt.Errorf("read stdin: %w", read.err)
			}
			raw = read.raw
		}
		if !utf8.Valid(raw) {
			return "", errors.New("stdin must be valid UTF-8")
		}
		task = string(raw)
	}
	if blank(task) {
		return "", errors.New(taskRequired)
	}
	return task, nil
}

// ECMAScript String.trim whitespace, used by the reference for task/id emptiness.
func blank(value string) bool {
	return strings.TrimFunc(value, func(r rune) bool {
		return r >= '\t' && r <= '\r' || r == ' ' || r == 0xA0 || r == 0x1680 || r >= 0x2000 && r <= 0x200A || r == 0x2028 || r == 0x2029 || r == 0x202F || r == 0x205F || r == 0x3000 || r == 0xFEFF
	}) == ""
}
