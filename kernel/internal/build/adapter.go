package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	builder "github.com/RussellLuo/gordis/build"
)

// prepareCLI applies Qi policy to arguments already parsed by the public CLI.
func prepareCLI(a builder.Arguments) (builder.Request, builder.Adapter, error) {
	if len(a.Positionals) > 1 {
		return builder.Request{}, builder.Adapter{}, fmt.Errorf("at most one core version positional argument is allowed")
	}
	query, source := os.Getenv("QI_VERSION"), "QI_VERSION"
	explicit := query != ""
	if !explicit {
		query, source = "latest", "default"
	}
	if len(a.Positionals) == 1 {
		query, source, explicit = a.Positionals[0], "positional", true
	}
	r := &Request{
		Args: append([]string{}, a.Args...), CWD: a.Request.Dir,
		Core: Selection{CoreModule, query, explicit}, CoreQuerySource: source,
		entryReplacements: map[string]bool{},
	}
	mappings := map[string]Replacement{}
	add := func(path, version string, s builder.Source) error {
		target := s.Module + "@" + s.Query
		if s.Directory != "" {
			target = s.Directory
			if !filepath.IsAbs(target) {
				target = filepath.Join(r.CWD, target)
			}
			var err error
			target, err = filepath.EvalSymlinks(target)
			if err != nil {
				return fmt.Errorf("replacement %s: %w", path, err)
			}
		}
		replacement := Replacement{path, version, target}
		if old, ok := mappings[path]; ok && old != replacement {
			return fmt.Errorf("conflicting or overlapping replacement for %s", path)
		}
		mappings[path] = replacement
		return nil
	}
	for _, p := range a.Request.Packages {
		selected := Selection{p.Path, p.Query.Value, p.Query.Explicit}
		if p.Path == CoreModule {
			if selected.ExplicitQuery {
				if len(a.Positionals) == 1 && a.Positionals[0] != selected.Query {
					return builder.Request{}, builder.Adapter{}, fmt.Errorf("conflicting core versions %s and %s", a.Positionals[0], selected.Query)
				}
				r.Core, r.CoreQuerySource = selected, "with"
			}
		} else {
			r.Extensions = append(r.Extensions, selected)
		}
		if p.Source != nil {
			r.entryReplacements[p.Path] = true
			if err := add(p.Path, "", *p.Source); err != nil {
				return builder.Request{}, builder.Adapter{}, err
			}
		}
	}
	for _, replacement := range a.Request.Replacements {
		if err := add(replacement.Module, replacement.Version, replacement.Source); err != nil {
			return builder.Request{}, builder.Adapter{}, err
		}
	}
	for _, replacement := range mappings {
		r.Replacements = append(r.Replacements, replacement)
	}
	sort.Slice(r.Replacements, func(i, j int) bool { return r.Replacements[i].Module < r.Replacements[j].Module })
	return prepareOptions(Options{Output: a.Request.Output, Request: r})
}

// Build always uses the public engine, including callers of the legacy API.
func Build(ctx context.Context, options Options) (string, error) {
	request, adapter, err := prepareOptions(options)
	if err != nil {
		return "", err
	}
	result, err := builder.Build(ctx, request, adapter)
	return result.RecordPath, err
}

func prepareOptions(options Options) (builder.Request, builder.Adapter, error) {
	var request builder.Request
	var adapter builder.Adapter
	if options.Output == "" || (options.Manifest == "") == (options.Request == nil) {
		return request, adapter, fmt.Errorf("output and exactly one build input are required")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return request, adapter, err
	}
	request = builder.Request{ModulePath: generatedModule, Dir: cwd, Output: options.Output}
	var manifest Manifest
	var raw []byte
	var extensions []Selection
	if r := options.Request; r != nil {
		request.Dir = r.CWD
		coreQuery := builder.Query{Value: r.Core.Query, Explicit: r.Core.ExplicitQuery}
		builtinQuery := coreQuery
		for _, p := range r.Extensions {
			if (p.Module == BuiltinModule || strings.HasPrefix(p.Module, BuiltinModule+"/")) && p.ExplicitQuery {
				// An explicit builtin selection overrides the inherited default.
				builtinQuery = builder.Query{Value: p.Query, Explicit: true}
			}
		}
		request.Modules = []builder.ModuleRequirement{{Path: CoreModule, Query: coreQuery}, {Path: BuiltinModule, Query: builtinQuery}}
		request.Packages = bootstrapPackages(coreQuery, builtinQuery)
		extensions = r.Extensions
		for _, p := range extensions {
			query := builder.Query{Value: p.Query, Explicit: p.ExplicitQuery}
			if !p.ExplicitQuery && (p.Module == BuiltinModule || strings.HasPrefix(p.Module, BuiltinModule+"/")) {
				query.Value = builtinQuery.Value
			}
			request.Packages = append(request.Packages, builder.Package{Path: p.Module, Query: query})
		}
		for _, replacement := range r.Replacements {
			source := publicSource(replacement.Target)
			if r.entryReplacements[replacement.Module] && replacement.Module != CoreModule {
				for i := range request.Packages {
					if request.Packages[i].Path == replacement.Module {
						request.Packages[i].Source = &source
					}
				}
			} else {
				request.Replacements = append(request.Replacements, builder.Replacement{Module: replacement.Module, Version: replacement.Version, Source: source})
			}
		}
	} else {
		raw, err = os.ReadFile(options.Manifest)
		if err == nil {
			manifest, err = Parse(raw)
		}
		if err != nil {
			return request, adapter, err
		}
		query := builder.Query{Value: manifest.Core.Version, Explicit: true}
		request.Packages = bootstrapPackages(query, query)
		manifest.builtinVersion = manifest.Core.Version
		for module, version := range manifest.versions() {
			request.Modules = append(request.Modules, builder.ModuleRequirement{Path: module, Query: builder.Query{Value: version, Explicit: true}, Exact: true})
		}
		sort.Slice(request.Modules, func(i, j int) bool { return request.Modules[i].Path < request.Modules[j].Path })
		for _, p := range manifest.Plugins {
			extensions = append(extensions, Selection{p.Package, p.Version, true})
			request.Packages = append(request.Packages, builder.Package{Path: p.Package, Query: builder.Query{Value: p.Version, Explicit: true}})
		}
		seen := map[string]bool{}
		for _, value := range options.Sources {
			module, directory, ok := strings.Cut(value, "=")
			if !ok || directory == "" {
				return request, adapter, fmt.Errorf("source must be <selected-module>=<directory>: %q", value)
			}
			if _, ok := manifest.versions()[module]; !ok {
				return request, adapter, fmt.Errorf("source module %s is not selected", module)
			}
			if seen[module] {
				return request, adapter, fmt.Errorf("duplicate source mapping for %s", module)
			}
			seen[module] = true
			request.Replacements = append(request.Replacements, builder.Replacement{Module: module, Source: builder.Source{Directory: directory}})
		}
	}

	adapter = builder.Adapter{
		Generate: func(plan builder.Plan) (builder.Project, error) {
			m := Manifest{}
			for _, selected := range extensions {
				m.Plugins = append(m.Plugins, Plugin{Package: selected.Module})
			}
			if options.Request == nil {
				owners := map[string]string{}
				for _, p := range plan.Packages {
					owners[p.Path] = p.Module
				}
				for _, p := range manifest.Plugins {
					if owners[p.Package] != p.Module {
						return builder.Project{}, fmt.Errorf("legacy package %s must belong to module %s, resolved %s", p.Package, p.Module, owners[p.Package])
					}
				}
			}
			files, err := generateApplication(m)
			return builder.Project{Files: files}, err
		},
		Validate: builder.Validation{Args: []string{"--help"}},
		Record:   qiRecord(options.Request, manifest, raw),
	}
	return request, adapter, nil
}

func bootstrapPackages(core, builtin builder.Query) []builder.Package {
	return []builder.Package{
		{Path: CoreModule + "/boot", Query: core},
		{Path: CoreModule + "/boot/application", Query: core},
		{Path: CoreModule + "/boot/launch", Query: core},
		{Path: BuiltinModule + "/bundle", Query: builtin},
	}
}

func publicSource(target string) builder.Source {
	if filepath.IsAbs(target) {
		return builder.Source{Directory: target}
	}
	module, query, _ := strings.Cut(target, "@")
	return builder.Source{Module: module, Query: query}
}
