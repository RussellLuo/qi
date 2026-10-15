// Package cli owns Profile launcher syntax and process input adaptation.
// Application capabilities and composition are supplied by the assembly.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/RussellLuo/qi/kernel/boot/internal/bundles"
	"github.com/RussellLuo/qi/kernel/boot/internal/launcher"
	"github.com/RussellLuo/qi/kernel/boot/internal/processcontrol"
	privateprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

const Help = `Usage:
  qi --help
  qi --profile <path> [-- application arguments...]
  qi --profile <path> -- --help

Launcher options:
  --profile <path>   explicit Profile YAML file (required to run)
  -h, --help        show launcher help without reading configuration

The Profile declares the application graph. No default file or graph is selected.
Arguments after the first -- belong to its registered argument consumer.
Use qi --profile <path> -- --help to discover the application's options.
The launcher does not load .env files. Applications supply their own credentials.
`

// AssemblyInput carries launcher facts, never plugin configuration overrides.
type AssemblyInput struct {
	ProfilePath string
	Streams     launch.Input
}

// Application selects the trusted capabilities once, before parsing argv.
// Assemble may bind those registrations but must not replace the selected set.
type Application struct {
	BundleCatalog profile.BundleCatalog
	Registrations []registration.PluginRegistration
	Assemble      func(profile.Profile, AssemblyInput) ([]registration.PluginRegistration, launcher.Policy, launcher.Hooks, error)
}

// Normalize parses only the prefix before the first --. Application argv stays
// an array of unmodified strings; no stdin, environment or source is read here.
func Normalize(arguments []string, streams launch.Input, app Application) (*launcher.Options, error) {
	if len(arguments) == 0 {
		return nil, writeHelp(streams.Stdout)
	}

	prefix, args := arguments, []string(nil)

	for i, arg := range arguments {
		if arg == "--" {
			prefix, args = arguments[:i], append([]string(nil), arguments[i+1:]...)
			break
		}
	}

	flags := flag.NewFlagSet("qi", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	path := flags.String("profile", "", "explicit Profile file")
	help := flags.Bool("help", false, "launcher help")
	flags.BoolVar(help, "h", false, "launcher help")
	if err := flags.Parse(prefix); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q; use --profile <path> and put application arguments after --", flags.Arg(0))
	}
	if *help {
		if len(prefix) != 1 || len(args) != 0 {
			return nil, errors.New("launcher help must be used alone; use --profile <path> -- --help for application help")
		}

		return nil, writeHelp(streams.Stdout)
	}

	if strings.TrimSpace(*path) == "" {
		return nil, errors.New("--profile requires a non-empty file path")
	}

	options := &launcher.Options{
		Registrations: append([]registration.PluginRegistration(nil), app.Registrations...),
		BundleCatalog: app.BundleCatalog,
		Args:          args,
		Input:         streams,
		LoadSource: func() (privateprofile.ResolvedProfile, error) {
			resolved, err := bundles.LoadWith(*path, app.BundleCatalog)
			if err != nil {
				return resolved, fmt.Errorf("read runtime profile: %w", err)
			}

			return resolved, nil
		},
		ApplicationHelp: func(parser launch.Parser) string { return applicationHelp(*path, parser) },
		HelpHint:        "View application help: " + invocationPrefix(*path) + " -- --help",
	}
	if app.Assemble != nil {
		options.Assemble = func(p profile.Profile) ([]registration.PluginRegistration, launcher.Policy, launcher.Hooks, error) {
			return app.Assemble(p, AssemblyInput{ProfilePath: *path, Streams: streams})
		}
	}

	return options, nil
}

// Quote paths as one shell argument, including spaces and literal apostrophes.
func invocationPrefix(path string) string {
	if strings.ContainsAny(path, " \t\r\n'\"\\$`;&|<>()*?[]{}!#~") {
		path = "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
	}

	return "qi --profile " + path
}

func applicationHelp(path string, parser launch.Parser) string {
	prefix := invocationPrefix(path)
	if parser == nil {
		return fmt.Sprintf(
			"Profile: %s\nUsage: %s\n\nThis Profile does not accept application arguments.\nRun until a stop signal; stdin is not read.\n",
			path,
			prefix,
		)
	}
	// The consumer owns options and examples; only its path placeholders are
	// filled by the CLI, without copying any consumer-specific flag definitions.
	help := strings.NewReplacer("<path>", strings.TrimPrefix(prefix, "qi --profile "),
		"<persistent-profile-path>", strings.TrimPrefix(prefix, "qi --profile ")).Replace(parser.Help())

	return fmt.Sprintf("Profile: %s\nInvocation: %s -- [application arguments...]\n\n%s", path, prefix, help)
}

func writeHelp(out io.Writer) error {
	n, err := io.WriteString(out, Help)
	if err == nil && n != len(Help) {
		err = io.ErrShortWrite
	}

	return err
}

// Work is shared by the executable and explicit test applications.
func Work(ctx context.Context, begin func(int), argv []string, streams launch.Input, stdinIsTTY bool, app Application) (int, error) {
	options, err := Normalize(argv, streams, app)
	if err != nil {
		fmt.Fprintln(streams.Stderr, "qi:", err)
		return 1, nil
	}

	if options == nil {
		return 0, nil
	}

	options.StdinIsTTY = stdinIsTTY

	return launcher.Work(ctx, begin, *options)
}

// Run adapts OS signals and terminal input to the existing process supervisor.
func Run(argv []string, streams launch.Input, app Application) int {
	signal.Ignore(syscall.SIGPIPE)
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	isTTY := false
	if file, ok := streams.Stdin.(*os.File); ok {
		isTTY = IsTerminal(file)
	}

	return processcontrol.RunProcess(signals, os.Exit, func(ctx context.Context, begin func(int)) (int, error) {
		return Work(ctx, begin, argv, streams, isTTY, app)
	})
}
