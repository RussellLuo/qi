package launch

import "errors"

// Parse uses only compiled metadata; the caller supplies a planner-selected
// consumer. A graph without one rejects application argv without touching stdin.
func Parse(parser Parser, args []string, stdinIsTTY bool) (Invocation, string, error) {
	if parser == nil {
		if len(args) != 0 {
			return nil, "", errors.New("Profile has no CLI argument consumer")
		}

		return nil, "", nil
	}

	invocation, err := parser.Parse(append([]string(nil), args...), stdinIsTTY)
	if err != nil {
		return nil, "", err
	}
	if invocation == nil {
		return nil, "", errors.New("CLI parser returned no invocation")
	}
	if invocation.HelpRequested() {
		return nil, parser.Help(), nil
	}

	return invocation, "", nil
}
