package build

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Request preserves user intent separately from query resolution and the final graph.
type Request struct {
	Args            []string      `json:"args"`
	CWD             string        `json:"cwd"`
	Core            Selection     `json:"core"`
	CoreQuerySource string        `json:"coreQuerySource"`
	Extensions      []Selection   `json:"extensions"`
	Replacements    []Replacement `json:"replacements"`

	// Inline --with mappings may name a package; --replace keys name modules.
	entryReplacements map[string]bool
}

type Selection struct {
	// Request keeps the selected input path; Resolution records its module
	// owner separately from ResolvedSelection.Entry, without changing JSON keys.
	Module        string `json:"module"`
	Query         string `json:"query"`
	ExplicitQuery bool   `json:"explicitQuery"`
}

type Replacement struct {
	Module  string `json:"module"`
	Version string `json:"version,omitempty"`
	Target  string `json:"target"`
}

type Resolution struct {
	Selections   []ResolvedSelection   `json:"selections"`
	Replacements []ResolvedReplacement `json:"replacements"`
}

type ResolvedSelection struct {
	Selection
	RequiredVersion        string `json:"requiredVersion"`
	Entry                  string `json:"entry,omitempty"`
	QueryExecuted          bool   `json:"queryExecuted"`
	DevelopmentPlaceholder bool   `json:"developmentPlaceholder"`
	FinalVersion           string `json:"finalVersion,omitempty"`
}

type ResolvedReplacement struct {
	Replacement
	TargetVersion string `json:"targetVersion,omitempty"`
	QueryExecuted bool   `json:"queryExecuted"`
	Used          bool   `json:"used"`
}

func defaultOutput() string {
	if runtime.GOOS == "windows" {
		return "qi.exe"
	}

	return "qi"
}

func parseSelection(value string) (Selection, string, error) {
	left, target, replaced := strings.Cut(value, "=")
	module, query, explicit := strings.Cut(left, "@")
	if !validPath(module) ||
		!strings.Contains(strings.Split(module, "/")[0], ".") ||
		module == generatedModule ||
		(explicit && (query == "" || strings.Contains(query, "@"))) ||
		(replaced && target == "") {
		return Selection{}, "", fmt.Errorf("invalid module selection %q; expected module[@version][=replacement]", value)
	}

	if !explicit {
		query = "latest"
	}

	return Selection{module, query, explicit}, target, nil
}

// parseCLI accepts flags and the core positional query in any order.
func parseCLI(args []string) (Options, error) {
	options := Options{Output: defaultOutput()}
	cwd, err := os.Getwd()
	if err != nil {
		return options, err
	}

	request := &Request{
		Args:              append([]string{}, args...),
		CWD:               cwd,
		entryReplacements: map[string]bool{},
	}
	var with, replace []string
	var positional string
	var positionalSet bool
	var manifestSet, outputSet bool

	// Collect raw CLI values before resolving version precedence and mappings.
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			for _, value := range args[i+1:] {
				if positionalSet {
					return options, fmt.Errorf("at most one core version positional argument is allowed")
				}

				positional, positionalSet = value, true
			}

			break
		}

		if !strings.HasPrefix(arg, "-") {
			if positionalSet {
				return options, fmt.Errorf("at most one core version positional argument is allowed")
			}

			positional, positionalSet = arg, true
			continue
		}

		flag, value, supplied := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		flag = strings.TrimPrefix(flag, "-")

		switch flag {
		case "manifest", "output", "source", "with", "replace":
		default:
			return options, fmt.Errorf("unknown option %s", arg)
		}

		if !supplied {
			i++
			if i == len(args) {
				return options, fmt.Errorf("--%s requires a value", flag)
			}

			value = args[i]
		}

		if value == "" {
			return options, fmt.Errorf("--%s requires a nonempty value", flag)
		}

		switch flag {
		case "manifest":
			if manifestSet {
				return options, fmt.Errorf("duplicate --manifest")
			}
			manifestSet, options.Manifest = true, value
		case "output":
			if outputSet {
				return options, fmt.Errorf("duplicate --output")
			}
			outputSet, options.Output = true, value
		case "source":
			options.Sources = append(options.Sources, value)
		case "with":
			with = append(with, value)
		case "replace":
			replace = append(replace, value)
		}
	}

	// The legacy manifest supplies its own selections; CLI queries cannot mix in.
	if manifestSet {
		if len(with)+len(replace) != 0 || positionalSet {
			return options, fmt.Errorf("--manifest cannot be mixed with --with, --replace or a core version")
		}

		if !outputSet {
			return options, fmt.Errorf("legacy --manifest requires --output")
		}

		return options, nil
	}

	if len(options.Sources) != 0 {
		return options, fmt.Errorf("--source requires legacy --manifest; use --with or --replace")
	}

	// Start with environment/default, then apply positional and explicit --with.
	query, source := os.Getenv("QI_VERSION"), "QI_VERSION"
	explicit := query != ""
	if !explicit {
		query, source = "latest", "default"
	}

	if positionalSet {
		query, source, explicit = positional, "positional", true
	}

	request.Core = Selection{CoreModule, query, explicit}
	request.CoreQuerySource = source

	seen := map[string]bool{}
	replacements := map[string]Replacement{}

	// --with may carry a mapping; --replace may repeat it only if identical.
	// Both forms share this table so overlapping version mappings are rejected.
	addReplacement := func(module, version, target string, duplicateAllowed bool) error {
		normalized, err := normalizeReplacementTarget(target)
		if err != nil {
			return fmt.Errorf("replacement %s: %w", module, err)
		}

		replacement := Replacement{module, version, normalized}
		if old, ok := replacements[module]; ok {
			if duplicateAllowed && old == replacement {
				return nil
			}

			return fmt.Errorf("duplicate, conflicting or overlapping replacement for %s", module)
		}

		replacements[module] = replacement
		return nil
	}

	// Only --with selects Extension factories. A replacement alone adds no import.
	for _, value := range with {
		selected, target, err := parseSelection(value)
		if err != nil {
			return options, err
		}

		if seen[selected.Module] {
			return options, fmt.Errorf("duplicate --with package %s", selected.Module)
		}

		seen[selected.Module] = true
		if selected.Module == CoreModule {
			if selected.ExplicitQuery {
				if positionalSet && positional != selected.Query {
					return options, fmt.Errorf("conflicting core versions %s and %s", positional, selected.Query)
				}

				request.Core, request.CoreQuerySource = selected, "with"
			}
		} else {
			request.Extensions = append(request.Extensions, selected)
		}

		if target != "" {
			request.entryReplacements[selected.Module] = true
			if err := addReplacement(selected.Module, "", target, false); err != nil {
				return options, err
			}
		}
	}

	replaceSeen := map[string]bool{}

	for _, value := range replace {
		selected, target, err := parseSelection(value)
		if err != nil {
			return options, err
		}

		if target == "" {
			return options, fmt.Errorf("--replace requires module[@version]=replacement")
		}

		if replaceSeen[selected.Module] {
			return options, fmt.Errorf("duplicate --replace key %s", selected.Module)
		}

		replaceSeen[selected.Module] = true
		version := ""
		if selected.ExplicitQuery {
			version = selected.Query
			if !fixedVersion.MatchString(version) {
				return options, fmt.Errorf("--replace left version must be fixed: %s", version)
			}
		}

		if err := addReplacement(selected.Module, version, target, true); err != nil {
			return options, err
		}
	}

	// Canonical ordering makes recorded requests independent of map iteration.
	sort.Slice(request.Extensions, func(i, j int) bool {
		return request.Extensions[i].Module < request.Extensions[j].Module
	})

	for _, replacement := range replacements {
		request.Replacements = append(request.Replacements, replacement)
	}

	sort.Slice(request.Replacements, func(i, j int) bool {
		return request.Replacements[i].Module < request.Replacements[j].Module
	})
	options.Request = request
	return options, nil
}

// A module target is stored as path@query; local directories are absolute.
// Existing bare relative directories remain compatible with the local CLI.
func normalizeReplacementTarget(target string) (string, error) {
	local := filepath.IsAbs(target) ||
		strings.HasPrefix(target, "."+string(filepath.Separator)) ||
		strings.HasPrefix(target, ".."+string(filepath.Separator)) ||
		target == "." ||
		target == ".."
	if !local {
		info, err := os.Stat(target)
		local = err == nil && info.IsDir()
	}

	if !local {
		remote, nested, err := parseSelection(target)
		if err != nil || nested != "" {
			return "", fmt.Errorf("invalid remote replacement %q; expected module[@query] or ./directory", target)
		}

		return remote.Module + "@" + remote.Query, nil
	}

	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}

	return filepath.EvalSymlinks(absolute)
}

func developmentVersion(module string) string {
	last := module[strings.LastIndex(module, "/")+1:]
	if regexp.MustCompile(`^v([2-9]|[1-9][0-9]+)$`).MatchString(last) {
		return last + ".0.0"
	}

	return "v0.0.0"
}
