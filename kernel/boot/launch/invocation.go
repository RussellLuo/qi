// Package launch defines invocation capabilities independently of a
// running Host. Implementations belong to plugins; process ownership stays with
// the launcher. None of these values are public Profile configuration.
package launch

import (
	"context"
	"io"

	"github.com/RussellLuo/gordis"
)

// CarrierID is reserved for the non-Profile launch context provider.
const CarrierID = "qi.launch-context"

// Parser.Help may use <path> and <persistent-profile-path> placeholders in
// examples; the CLI fills them with the explicitly selected Profile path.
// Parser.Help and Parse must be pure: no environment/config loading, stdin,
// filesystem writes, Host creation, credentials or process execution.
type Parser interface {
	Help() string
	Parse(args []string, stdinIsTTY bool) (Invocation, error)
}

// Invocation separates pure parsing/help from cancellable input preparation.
// Prepare runs under process supervision, before any plugin activation.
type Invocation interface {
	HelpRequested() bool
	Prepare(context.Context, Input) (Prepared, error)
}

type Input struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Prepared binds typed, run-local callbacks (e.g. HeaderCheck/Driver wrappers)
// before activation. Bind must preserve registered Specs and leave its input
// prototypes untouched. A plugin retains its own concrete prepared type.
type Prepared interface {
	Bind([]gordis.Plugin) ([]gordis.Plugin, error)
}

// Context is published only by a trusted internal provider carrier. Parent is
// the process/task cancellation context, not a temporary Mount deadline.
type Context struct {
	Parent     context.Context
	Invocation Prepared
	Stdout     io.Writer
	Stderr     io.Writer
	CWD        string
	Control    ProcessControl
}

// ProcessControl cannot dispose a Host or exit a process. Claim is a global,
// irreversible execution permit: reactivation must never resubmit a task.
// BeginShutdown arms the existing process supervisor before task cleanup.
// Complete reports only after output and task cleanup, never Host cleanup.
type ProcessControl interface {
	Ready() <-chan struct{}
	Claim() error
	BeginShutdown(int)
	Complete(code int, cleanupErr error) error
}

// ReadyCheck runs after the entire graph/management backend is mounted, before
// opening Ready. It may validate resolved services but cannot submit a task.
type ReadyCheck interface{ CheckReady(context.Context) error }

// WorkingDirectory supplies the physical cwd resolved during input preparation.
type WorkingDirectory interface{ WorkingDirectory() string }

// OutputError carries an application error projection without making the
// launcher know the consumer's output format. Bootstrap errors do not implement it.
type OutputError interface{ WriteError(io.Writer) error }

// InvocationDrain waits for the consumer's Scope-owned task after process
// cancellation, before the launcher revokes any of its live dependencies.
type InvocationDrain interface{ Drain(context.Context) error }
