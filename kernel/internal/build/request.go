package build

import (
	"fmt"
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

// parseLegacyCLI handles only the compatibility flags. The standard --with /
// --replace parser belongs to Gordis Run and is never duplicated here.
func parseLegacyCLI(args []string) (Options, error) {
	var options Options
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" && i == len(args)-1 {
			break
		}
		flag, value, supplied := strings.Cut(arg, "=")
		switch flag {
		case "--manifest", "-manifest", "--output", "-output", "--source", "-source":
		default:
			return options, fmt.Errorf("legacy --manifest cannot be mixed with --with, --replace or a core version; unexpected argument %q", arg)
		}
		key := strings.TrimLeft(flag, "-")
		if !supplied {
			i++
			if i == len(args) {
				return options, fmt.Errorf("--%s requires a value", key)
			}
			value = args[i]
		}
		if value == "" {
			return options, fmt.Errorf("--%s requires a nonempty value", key)
		}
		if key != "source" && seen[key] {
			return options, fmt.Errorf("duplicate --%s", key)
		}
		seen[key] = true
		switch key {
		case "manifest":
			options.Manifest = value
		case "output":
			options.Output = value
		case "source":
			options.Sources = append(options.Sources, value)
		}
	}
	if !seen["manifest"] {
		return options, fmt.Errorf("--source requires legacy --manifest; use --with or --replace")
	}
	if !seen["output"] {
		return options, fmt.Errorf("legacy --manifest requires --output")
	}
	return options, nil
}
