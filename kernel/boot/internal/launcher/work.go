package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"

	hostruntime "github.com/RussellLuo/qi/kernel/boot/internal/hostruntime"
	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	appregistration "github.com/RussellLuo/qi/kernel/boot/internal/registration"
)

// Options is a normalized invocation. The product CLI discovers its parser
// from Profile metadata; explicit Parser/ConsumerType are fixture adapters.
type Options struct {
	BundleCatalog appprofile.BundleCatalog
	Parser        Parser
	ConsumerType  string
	Args          []string
	StdinIsTTY    bool
	Input         Input
	Load          func() (appprofile.Profile, error) // complete-profile fixture adapter
	LoadSource    func() (appprofile.ResolvedProfile, error)
	Registrations []appregistration.PluginRegistration
	Policy        Policy
	Hooks         Hooks
	Assemble      func(appprofile.Profile) ([]appregistration.PluginRegistration, Policy, Hooks, error)

	// ApplicationHelp wraps pure Parser help with the selected file's invocation.
	// A nil parser represents a resident graph, whose only help exception is --help.
	ApplicationHelp func(Parser) string
	HelpHint        string
}

// Work resolves syntax and pure planning before starting the Host. Credentials
// and environment preparation belong to the application, outside this launcher.
func Work(ctx context.Context, begin func(int), options Options) (code int, cleanupErr error) {
	code = 1
	defer func() { begin(code) }()

	diagnose := func(err error) {
		var output OutputError
		if errors.As(err, &output) {
			err = errors.Join(err, output.WriteError(options.Input.Stdout))
		}

		fmt.Fprintf(options.Input.Stderr, "qi: %s\n", err)
	}
	registrations := options.Registrations
	bundleCatalog := options.BundleCatalog
	var resolved appprofile.ResolvedProfile
	load := func() (appprofile.Profile, error) {
		var err error
		if options.LoadSource != nil {
			resolved, err = options.LoadSource()
		} else {
			var profile appprofile.Profile
			profile, err = options.Load()
			if err == nil {
				resolved, err = appprofile.Compose(appprofile.SourceFromProfile(profile), bundleCatalog)
			}
		}

		if err == nil {
			err = appregistration.ValidateSourceTypes(registrations, resolved)
		}

		return resolved.Profile.Clone(), err
	}
	var profile appprofile.Profile
	loaded := false
	parser := options.Parser
	if parser == nil {
		// Production applications discover their argument consumer from the
		// Profile. An explicit fixture parser can parse before source loading.
		var err error
		profile, err = load()
		if err != nil {
			diagnose(err)
			return code, nil
		}

		loaded = true
		parser, err = appregistration.InvocationParser(registrations, profile)
		if err != nil {
			diagnose(err)
			return code, nil
		}
	}

	var invocation Invocation
	var help string
	var err error
	if parser == nil && options.ApplicationHelp != nil && len(options.Args) == 1 && options.Args[0] == "--help" {
		help = options.ApplicationHelp(nil)
	} else {
		invocation, help, err = Parse(parser, options.Args, options.StdinIsTTY)
		if err == nil && parser != nil && invocation == nil && options.ApplicationHelp != nil {
			help = options.ApplicationHelp(parser)
		}
	}

	if err != nil {
		diagnose(err)
		if options.HelpHint != "" {
			fmt.Fprintln(options.Input.Stderr, options.HelpHint)
		}

		return code, nil
	}

	if help != "" {
		n, err := io.WriteString(options.Input.Stdout, help)
		if err == nil && n != len(help) {
			err = io.ErrShortWrite
		}

		if err != nil {
			diagnose(err)
			return code, nil
		}

		return 0, nil
	}

	var prepared Prepared
	if invocation != nil && !loaded {
		prepared, err = invocation.Prepare(ctx, options.Input)
		if err != nil {
			diagnose(err)
			return code, nil
		}
	}

	if !loaded {
		profile, err = load()
		if err != nil {
			diagnose(err)
			return code, nil
		}
	}

	if options.Assemble != nil {
		registrations, options.Policy, options.Hooks, err = options.Assemble(profile)
		if err != nil {
			diagnose(err)
			return code, nil
		}
	}

	// Plan and validate the execution graph without mounting services. The
	// resulting candidate carries the frozen source into Host bootstrap.
	var candidate *appregistration.Candidate
	if options.Policy.Plan != nil {
		candidate, err = options.Policy.Plan(registrations, resolved, bundleCatalog)
	} else {
		candidate, err = appregistration.Plan(registrations, profile)
	}

	if err == nil && options.ConsumerType != "" {
		declared := false

		for _, instance := range candidate.Profile.Instances {
			if instance.ID == candidate.ConsumerID && instance.Plugin == options.ConsumerType {
				declared = true
			}
		}

		if !declared {
			err = fmt.Errorf("Profile must declare %s", options.ConsumerType)
		}
	}

	if err != nil {
		diagnose(err)
		return code, nil
	}

	frozenSource := resolved.Clone()
	candidate.Source = &frozenSource
	candidate.Bundles = bundleCatalog
	if invocation != nil && loaded {
		prepared, err = invocation.Prepare(ctx, options.Input)
		if err != nil {
			diagnose(err)
			return code, nil
		}
	}

	code, cleanupErr, err = Execute(ctx, begin, candidate, prepared, options.Input, options.Policy, options.Hooks)
	if err != nil {
		diagnose(err)
		var cleanup *hostruntime.CleanupError
		if errors.As(err, &cleanup) {
			cleanupErr = cleanup
		}

		return code, cleanupErr
	}

	if cleanupErr != nil {
		fmt.Fprintf(options.Input.Stderr, "qi: %s\n", cleanupErr)
	}

	return code, cleanupErr
}
