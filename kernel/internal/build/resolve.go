package build

import (
	"fmt"
	"path/filepath"
	"strings"
)

// prepareRequest separates local mappings from remote queries before any Go
// resolution. Fixed local requirements never need a published module version.
func prepareRequest(request *Request, ownership ...map[string]string) (Manifest, Resolution, []string, error) {
	m := Manifest{APIVersion: APIVersion}
	resolution := Resolution{}
	replacements := map[string]Replacement{}
	var sources []string

	for _, replacement := range request.Replacements {
		replacements[replacement.Module] = replacement
		if filepath.IsAbs(replacement.Target) {
			sources = append(sources, replacement.Module+"="+replacement.Target)
		}

		resolution.Replacements = append(resolution.Replacements, ResolvedReplacement{Replacement: replacement})
	}

	// The default builtin follows the same requested query, independently resolved.
	selections := append([]Selection{request.Core}, request.Extensions...)
	builtin := request.Core
	builtin.Module = BuiltinModule
	for _, selected := range request.Extensions {
		module := selected.Module
		if len(ownership) != 0 && ownership[0][module] != "" {
			module = ownership[0][module]
		}
		if module == BuiltinModule && selected.ExplicitQuery {
			builtin.Query, builtin.ExplicitQuery = selected.Query, true
		}
	}
	selections = append(selections, builtin)
	entries := make([]string, len(selections))
	shared := map[string]Selection{}
	for i := range selections {
		selection := &selections[i]
		if i != 0 && i != len(selections)-1 {
			entries[i] = selection.Module
			if len(ownership) != 0 && ownership[0][selection.Module] != "" {
				selection.Module = ownership[0][selection.Module]
			}
		}
		old, ok := shared[selection.Module]
		if ok && old.ExplicitQuery && selection.ExplicitQuery && old.Query != selection.Query {
			return m, resolution, nil, fmt.Errorf("conflicting queries for module %s (entry %s): %s and %s", selection.Module, entries[i], old.Query, selection.Query)
		}
		if !ok || selection.ExplicitQuery {
			shared[selection.Module] = *selection
		}
	}
	for i, selection := range selections {
		effective := shared[selection.Module]
		selection.Query = effective.Query
		replacement := replacements[selection.Module]
		local := filepath.IsAbs(replacement.Target)
		version := developmentVersion(selection.Module)

		// An implicit local choice needs only a valid require version, not a query.
		// Explicit moving queries still resolve the published module identity.
		placeholder := local && !effective.ExplicitQuery
		query := !placeholder && !(local && fixedVersion.MatchString(effective.Query))
		if !query && effective.ExplicitQuery {
			version = selection.Query
		}

		if !query && replacement.Version != "" && replacement.Version != version {
			return m, resolution, nil, fmt.Errorf("replacement version %s does not cover selected %s@%s", replacement.Version, selection.Module, version)
		}

		entry := entries[i]
		if i == len(selections)-1 {
			entry = BuiltinModule + "/bundle"
		}
		if i == 0 {
			m.Core.Version = version
		} else if i == len(selections)-1 {
			m.builtinVersion = version
		} else {
			m.Plugins = append(m.Plugins, Plugin{selection.Module, version, entry, "local"})
		}

		resolution.Selections = append(resolution.Selections, ResolvedSelection{
			Selection:              selection,
			RequiredVersion:        version,
			Entry:                  entry,
			QueryExecuted:          query,
			DevelopmentPlaceholder: placeholder,
		})
	}

	return m, resolution, sources, nil
}

// resolveVersions delegates query canonicalization to Go. The probe has no
// application imports or implementation dependencies. All selected queries,
// including the core choice, are resolved together; the generated project then
// applies Go's MVS to their requirements without imposing legacy equality.
func resolveVersions(r *runner, m Manifest, resolution Resolution, goVersion string) (Manifest, Resolution, error) {
	probe := filepath.Join(r.dir, "published-query")
	if err := writeFile(probe, "go.mod", []byte("module qi.generated/publishedquery\n\ngo "+goVersion+"\n")); err != nil {
		return m, resolution, err
	}
	resolve := func(queries []string) (map[string]moduleInfo, error) {
		resolved := map[string]moduleInfo{}
		if len(queries) == 0 {
			return resolved, nil
		}
		raw, err := r.run(probe, append([]string{"go", "list", "-m", "-json"}, queries...)...)
		if err != nil {
			return nil, fmt.Errorf("module version resolution failed: %w", err)
		}
		modules, err := decodeStream[moduleInfo](raw)
		if err != nil {
			return nil, err
		}
		if len(modules) != len(queries) {
			return nil, fmt.Errorf("Go returned incomplete query results")
		}
		for i, module := range modules {
			path, _, _ := strings.Cut(queries[i], "@")
			if module.Path != path || module.Version == "" {
				return nil, fmt.Errorf("module query %s: Go resolved %s without the requested identity/version", queries[i], module.Path)
			}
			resolved[queries[i]] = module
		}
		return resolved, nil
	}
	var queries []string
	seen := map[string]bool{}

	for _, selection := range resolution.Selections {
		query := selection.Module + "@" + selection.Query
		if selection.QueryExecuted && !seen[query] {
			queries = append(queries, query)
			seen[query] = true
		}
	}
	// Query published identities without replacements: explicit latest on a
	// local source must not become Go's synthetic zero-time development version.

	resolved, err := resolve(queries)
	if err != nil {
		return m, resolution, err
	}

	for i := range resolution.Selections {
		selection := &resolution.Selections[i]
		if selection.QueryExecuted {
			selection.RequiredVersion = resolved[selection.Module+"@"+selection.Query].Version
		}

		if selection.Module == BuiltinModule {
			m.builtinVersion = selection.RequiredVersion
		}
		if selection.Module == CoreModule && selection.Entry == "" {
			m.Core.Version = selection.RequiredVersion
		} else {
			for j := range m.Plugins {
				if m.Plugins[j].Package == selection.Entry {
					m.Plugins[j].Version = selection.RequiredVersion
				}
			}
		}

		for _, replacement := range resolution.Replacements {
			if replacement.Module == selection.Module && replacement.Version != "" && replacement.Version != selection.RequiredVersion {
				return m, resolution, fmt.Errorf(
					"replacement version %s does not cover selected %s@%s",
					replacement.Version,
					selection.Module,
					selection.RequiredVersion,
				)
			}
		}
	}

	return m, resolution, nil
}

// checkResolution records adjustments and validates actual replacement facts.
func checkResolution(modules []moduleInfo, resolution *Resolution) error {
	graph := map[string]moduleInfo{}
	declared := map[string]ResolvedReplacement{}

	for _, replacement := range resolution.Replacements {
		declared[replacement.Module] = replacement
	}

	for _, module := range modules {
		graph[module.Path] = module
		if module.Replace != nil {
			replacement, ok := declared[module.Path]
			if !ok ||
				(replacement.Version != "" && replacement.Version != module.Version) ||
				module.Replace.Path != replacement.Target ||
				module.Replace.Version != replacement.TargetVersion {
				return fmt.Errorf("undeclared replacement for %s", module.Path)
			}
		}
	}

	for i := range resolution.Selections {
		selected := &resolution.Selections[i]
		module, ok := graph[selected.Module]
		if !ok {
			return fmt.Errorf("selected module %s missing from resolved graph", selected.Module)
		}

		selected.FinalVersion = module.Version
	}

	for i := range resolution.Replacements {
		replacement := &resolution.Replacements[i]
		module, ok := graph[replacement.Module]
		replacement.Used = ok &&
			module.Replace != nil &&
			module.Replace.Path == replacement.Target &&
			module.Replace.Version == replacement.TargetVersion
		if ok && (replacement.Version == "" || replacement.Version == module.Version) && !replacement.Used {
			return fmt.Errorf("missing explicit replacement for %s", replacement.Module)
		}
	}

	return nil
}
