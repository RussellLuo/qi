package build

import (
	"encoding/json"
	"fmt"

	builder "github.com/RussellLuo/gordis/build"
	"gopkg.in/yaml.v3"
)

// qiRecord encodes the existing Qi schemas from public build facts. The engine
// retains ownership of project/, logs/ and dependencies.json.
func qiRecord(request *Request, manifest Manifest, manifestRaw []byte) func(builder.Facts) (map[string][]byte, error) {
	// Capture intent now, rather than re-reading mutable argv or a manifest after
	// compilation. The unexported inline-mapping table is not part of the record.
	var raw []byte
	var captured Request
	var captureErr error
	if request != nil {
		raw, captureErr = json.MarshalIndent(request, "", "  ")
		if captureErr == nil {
			captureErr = json.Unmarshal(raw, &captured)
		}
	} else {
		raw = append([]byte{}, manifestRaw...)
	}
	return func(f builder.Facts) (map[string][]byte, error) {
		if captureErr != nil {
			return nil, captureErr
		}
		versions := map[string]string{}
		for _, r := range f.Plan.Requirements {
			versions[r.Path] = r.RequiredVersion
		}
		var sources []sourceRecord
		for _, input := range f.Inputs {
			sources = append(sources, sourceRecord{
				Module: input.Module, Directory: input.Directory,
				TargetVersion: versions[input.Module], Files: input.Files, SHA256: input.SHA256,
			})
		}
		// Both module envelopes deliberately use the field names returned by Go.
		moduleRaw, err := json.Marshal(f.Modules)
		if err != nil {
			return nil, err
		}
		var modules []moduleInfo
		if err := json.Unmarshal(moduleRaw, &modules); err != nil {
			return nil, err
		}
		var commands []commandRecord
		for _, c := range f.Commands {
			commands = append(commands, commandRecord{Args: c.Args, Log: c.Log, ExitCode: c.ExitCode})
		}
		files := map[string][]byte{}
		put := func(name string, value any) error {
			data, err := json.MarshalIndent(value, "", "  ")
			if err == nil {
				files[name] = append(data, '\n')
			}
			return err
		}
		if request == nil {
			manifest.builtinVersion = versions[BuiltinModule]
			record := Record{
				APIVersion: "qi.build-record/v1alpha1", Manifest: manifest, ManifestSHA256: digest(raw),
				LocalDevelopment: f.LocalDevelopment, Sources: sources, Modules: modules,
				Environment: f.Environment, GeneratedSHA256: f.GeneratedSHA256,
				BinarySHA256: f.BinarySHA256, Commands: commands,
			}
			if err := put("record.json", record); err != nil {
				return nil, err
			}
			files["manifest-input.yaml"] = raw
			files["manifest.yaml"], err = yaml.Marshal(manifest)
			return files, err
		}

		resolution, err := qiResolution(captured, f)
		if err != nil {
			return nil, err
		}
		record := requestRecord{
			APIVersion: "qi.build-record/v1alpha2", Request: &captured, RequestSHA256: digest(raw), Resolved: resolution,
			LocalDevelopment: f.LocalDevelopment, Sources: sources, Modules: modules,
			Environment: f.Environment, GeneratedSHA256: f.GeneratedSHA256,
			BinarySHA256: f.BinarySHA256, Commands: commands,
		}
		if err := put("record.json", record); err != nil {
			return nil, err
		}
		if err := put("resolved-build.json", resolution); err != nil {
			return nil, err
		}
		files["request.json"] = raw
		return files, nil
	}
}

func qiResolution(request Request, f builder.Facts) (Resolution, error) {
	resolution := Resolution{}
	owners := map[string]string{}
	requirements := map[string]builder.ResolvedRequirement{}
	graph := map[string]string{}
	for _, p := range f.Plan.Packages {
		owners[p.Path] = p.Module
	}
	for _, r := range f.Plan.Requirements {
		requirements[r.Path] = r
	}
	for _, m := range f.Modules {
		graph[m.Path] = m.Version
	}
	builtin := request.Core
	builtin.Module = BuiltinModule
	for _, s := range request.Extensions {
		if owners[s.Module] == BuiltinModule && s.ExplicitQuery {
			builtin.Query, builtin.ExplicitQuery = s.Query, true
		}
	}
	selections := append([]Selection{request.Core}, request.Extensions...)
	selections = append(selections, builtin)
	for i, s := range selections {
		module, entry := owners[s.Module], s.Module
		if i == 0 {
			module, entry = CoreModule, ""
		} else if i == len(selections)-1 {
			module, entry = BuiltinModule, BuiltinModule+"/bundle"
		}
		r, ok := requirements[module]
		if !ok {
			return resolution, fmt.Errorf("Qi record: missing requirement for %s", s.Module)
		}
		s.Module, s.Query = module, r.Query.Value
		resolution.Selections = append(resolution.Selections, ResolvedSelection{
			Selection: s, Entry: entry, RequiredVersion: r.RequiredVersion, FinalVersion: graph[module],
			QueryExecuted: r.QueryExecuted, DevelopmentPlaceholder: r.DevelopmentPlaceholder,
		})
	}
	for _, r := range f.Replacements {
		target := r.Source.Directory
		if target == "" {
			target = r.Source.Module
		}
		resolution.Replacements = append(resolution.Replacements, ResolvedReplacement{
			Replacement:   Replacement{Module: r.Module, Version: r.Version, Target: target},
			TargetVersion: r.TargetVersion, QueryExecuted: r.QueryExecuted, Used: r.Used,
		})
	}
	return resolution, nil
}
