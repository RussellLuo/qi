package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func belongsTo(entry, module string) bool {
	return entry == module || strings.HasPrefix(entry, module+"/")
}

// normalizeEntryMappings keeps the request unchanged for its v1alpha2 record.
// Only inline local --with mappings infer their module from the source go.mod.
// Explicit --replace keys retain their exact module identity.
func normalizeEntryMappings(r *runner, request *Request) (*Request, error) {
	normalized := *request
	normalized.Replacements = nil
	byModule := map[string]Replacement{}
	for _, replacement := range request.Replacements {
		if filepath.IsAbs(replacement.Target) && request.entryReplacements[replacement.Module] && replacement.Module != CoreModule {
			raw, err := r.run(replacement.Target, "go", "mod", "edit", "-json")
			if err != nil {
				return nil, fmt.Errorf("entry %s source: %w", replacement.Module, err)
			}
			var metadata struct{ Module struct{ Path string } }
			if err := json.Unmarshal(raw, &metadata); err != nil {
				return nil, err
			}
			if metadata.Module.Path == "" || !belongsTo(replacement.Module, metadata.Module.Path) {
				return nil, fmt.Errorf("entry %s does not belong to source module %s", replacement.Module, metadata.Module.Path)
			}
			replacement.Module = metadata.Module.Path
		}
		if old, ok := byModule[replacement.Module]; ok && old != replacement {
			return nil, fmt.Errorf("conflicting or overlapping source mappings for module %s", replacement.Module)
		}
		byModule[replacement.Module] = replacement
	}
	for _, replacement := range byModule {
		normalized.Replacements = append(normalized.Replacements, replacement)
	}
	sort.Slice(normalized.Replacements, func(i, j int) bool { return normalized.Replacements[i].Module < normalized.Replacements[j].Module })
	return &normalized, nil
}

// resolveRequest discovers package ownership before generating module requires.
// Go resolves unmapped published entries in isolated probes. Explicit mappings
// supply ownership directly; the final package graph must confirm that owner.
func resolveRequest(r *runner, request *Request, goVersion string) (Manifest, Resolution, map[string]string, error) {
	var m Manifest
	var resolution Resolution
	normalized, err := normalizeEntryMappings(r, request)
	if err != nil {
		return m, resolution, nil, err
	}
	var values []string
	for _, replacement := range normalized.Replacements {
		if filepath.IsAbs(replacement.Target) {
			values = append(values, replacement.Module+"="+replacement.Target)
		}
	}
	sources, err := resolveSources(r, m, values, true)
	if err != nil {
		return m, resolution, nil, err
	}
	// Resolve moving replacement targets once; every ownership/version probe
	// and the generated project use this same fixed replacement identity.
	probe := filepath.Join(r.dir, "entry-replacements")
	if err := writeFile(probe, "go.mod", []byte("module qi.generated/entries\n\ngo "+goVersion+"\n")); err != nil {
		return m, resolution, nil, err
	}
	var replacements []ResolvedReplacement
	targets := map[string]moduleInfo{}
	for _, replacement := range normalized.Replacements {
		resolved := ResolvedReplacement{Replacement: replacement}
		if !filepath.IsAbs(replacement.Target) {
			target, ok := targets[replacement.Target]
			if !ok {
				raw, err := r.run(probe, "go", "list", "-m", "-json", replacement.Target)
				if err != nil {
					return m, resolution, nil, fmt.Errorf("replacement for %s: %w", replacement.Module, err)
				}
				if err := json.Unmarshal(raw, &target); err != nil {
					return m, resolution, nil, err
				}
				targets[replacement.Target] = target
			}
			path, _, _ := strings.Cut(replacement.Target, "@")
			if target.Path != path || target.Version == "" {
				return m, resolution, nil, fmt.Errorf("replacement for %s resolved outside target module %s", replacement.Module, path)
			}
			resolved.Target, resolved.TargetVersion, resolved.QueryExecuted = path, target.Version, true
		}
		replacements = append(replacements, resolved)
	}
	// An inline remote mapping selects a package, too. Discover its original
	// owner before mapping the fork; installing the package path as a replace
	// key would invent a module and incorrectly use the fork's root factory.
	var discoveryMappings []ResolvedReplacement
	for _, replacement := range replacements {
		if !request.entryReplacements[replacement.Module] || filepath.IsAbs(replacement.Target) || replacement.Module == CoreModule {
			discoveryMappings = append(discoveryMappings, replacement)
		}
	}
	for i := range replacements {
		replacement := &replacements[i]
		if !request.entryReplacements[replacement.Module] || filepath.IsAbs(replacement.Target) || replacement.Module == CoreModule {
			continue
		}
		for _, selection := range normalized.Extensions {
			if selection.Module == replacement.Module {
				owner, err := discoverEntry(r, selection, discoveryMappings, goVersion, fmt.Sprintf("inline-entry-%d", i))
				if err != nil {
					return m, resolution, nil, err
				}
				replacement.Module = owner
				break
			}
		}
	}
	byModule := map[string]ResolvedReplacement{}
	for _, replacement := range replacements {
		if old, ok := byModule[replacement.Module]; ok && old != replacement {
			return m, resolution, nil, fmt.Errorf("conflicting or overlapping source mappings for module %s", replacement.Module)
		}
		byModule[replacement.Module] = replacement
	}
	replacements = nil
	normalized.Replacements = nil
	for _, replacement := range byModule {
		replacements = append(replacements, replacement)
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].Module < replacements[j].Module })
	for _, replacement := range replacements {
		normalized.Replacements = append(normalized.Replacements, replacement.Replacement)
	}
	owners := map[string]string{}
	for i, selection := range normalized.Extensions {
		entry := selection.Module
		var mapping *ResolvedReplacement
		for j := range replacements {
			candidate := &replacements[j]
			if belongsTo(entry, candidate.Module) && (mapping == nil || len(candidate.Module) > len(mapping.Module)) {
				mapping = candidate
			}
		}
		if mapping != nil {
			owners[entry] = mapping.Module
			if filepath.IsAbs(mapping.Target) {
				// A parent module mapping cannot consume a nested module's files.
				tail := strings.TrimPrefix(strings.TrimPrefix(entry, mapping.Module), "/")
				directory := mapping.Target
				if tail != "" {
					for _, part := range strings.Split(tail, "/") {
						directory = filepath.Join(directory, part)
						if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
							return m, resolution, nil, fmt.Errorf("entry %s crosses nested module at %s; supply that module's source mapping", entry, directory)
						}
					}
				}
			}
			continue
		}
		owner, err := discoverEntry(r, selection, replacements, goVersion, fmt.Sprintf("entry-%d", i))
		if err != nil {
			return m, resolution, nil, err
		}
		owners[entry] = owner
	}
	m, resolution, _, err = prepareRequest(normalized, owners)
	if err != nil {
		return m, resolution, nil, err
	}
	resolution.Replacements = replacements
	m, resolution, err = resolveVersions(r, m, resolution, goVersion)
	return m, resolution, sources, err
}

// discoverEntry asks Go for the package's actual module, including nested modules.
func discoverEntry(r *runner, selection Selection, replacements []ResolvedReplacement, goVersion, name string) (string, error) {
	entry := selection.Module
	probe := filepath.Join(r.dir, name)
	if err := writeFile(probe, "go.mod", []byte("module qi.generated/entry\n\ngo "+goVersion+"\n")); err != nil {
		return "", err
	}
	for _, replacement := range replacements {
		left, target := replacement.Module, replacement.Target
		if replacement.Version != "" {
			left += "@" + replacement.Version
		}
		if replacement.TargetVersion != "" {
			target += "@" + replacement.TargetVersion
		}
		if _, err := r.run(probe, "go", "mod", "edit", "-replace="+left+"="+target); err != nil {
			return "", err
		}
	}
	if _, err := r.run(probe, "go", "get", entry+"@"+selection.Query); err != nil {
		return "", fmt.Errorf("entry %s module discovery: %w", entry, err)
	}
	// A root selection may acquire its factory from a remote replacement even
	// when the original module has no root Go files. Preserve that old use.
	modulesRaw, err := r.run(probe, "go", "list", "-m", "-json", "all")
	if err != nil {
		return "", err
	}
	modules, err := decodeStream[moduleInfo](modulesRaw)
	if err != nil {
		return "", err
	}
	for _, module := range modules {
		if module.Path == entry {
			return module.Path, nil
		}
	}
	raw, err := r.run(probe, "go", "list", "-find", "-json", entry)
	if err != nil {
		return "", fmt.Errorf("entry %s module discovery: %w", entry, err)
	}
	var pkg packageInfo
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", err
	}
	if pkg.Module == nil || !belongsTo(entry, pkg.Module.Path) || pkg.Name == "main" {
		return "", fmt.Errorf("entry %s must be an importable package owned by a Go module", entry)
	}
	return pkg.Module.Path, nil
}
