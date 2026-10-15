// Package build generates and validates a standalone Go application from an
// explicit request, then publishes its binary and auditable build inputs.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type Options struct {
	Manifest string
	Output   string
	Sources  []string
	Request  *Request
}

type commandRecord struct {
	Args     []string `json:"args"`
	Log      string   `json:"log"`
	ExitCode int      `json:"exitCode"`
}

type moduleInfo struct {
	Path      string      `json:"Path"`
	Version   string      `json:"Version,omitempty"`
	Main      bool        `json:"Main,omitempty"`
	Dir       string      `json:"Dir,omitempty"`
	GoMod     string      `json:"GoMod,omitempty"`
	Sum       string      `json:"Sum,omitempty"`
	GoModSum  string      `json:"GoModSum,omitempty"`
	GoVersion string      `json:"GoVersion,omitempty"`
	Replace   *moduleInfo `json:"Replace,omitempty"`
}

type sourceRecord struct {
	Module        string            `json:"module"`
	Directory     string            `json:"directory"`
	TargetVersion string            `json:"targetVersion"`
	Files         map[string]string `json:"files"`
	SHA256        string            `json:"sha256"`
}

type Record struct {
	APIVersion       string            `json:"apiVersion"`
	Manifest         Manifest          `json:"manifest"`
	ManifestSHA256   string            `json:"manifestSHA256"`
	LocalDevelopment bool              `json:"localDevelopment"`
	Sources          []sourceRecord    `json:"sources"`
	Modules          []moduleInfo      `json:"modules"`
	Environment      map[string]string `json:"environment"`
	GeneratedSHA256  map[string]string `json:"generatedSHA256"`
	BinarySHA256     string            `json:"binarySHA256"`
	Commands         []commandRecord   `json:"commands"`
}

// requestRecord uses a distinct API version for CLI query and resolution facts.
type requestRecord struct {
	APIVersion       string            `json:"apiVersion"`
	Request          *Request          `json:"request"`
	RequestSHA256    string            `json:"requestSHA256"`
	Resolved         Resolution        `json:"resolved"`
	LocalDevelopment bool              `json:"localDevelopment"`
	Sources          []sourceRecord    `json:"sources"`
	Modules          []moduleInfo      `json:"modules"`
	Environment      map[string]string `json:"environment"`
	GeneratedSHA256  map[string]string `json:"generatedSHA256"`
	BinarySHA256     string            `json:"binarySHA256"`
	Commands         []commandRecord   `json:"commands"`
}

type runner struct {
	ctx      context.Context
	dir      string
	env      []string
	commands []commandRecord
}

func nativeEnvironment() []string {
	override := map[string]string{
		"GOWORK":      "off",
		"GOFLAGS":     "",
		"GOTOOLCHAIN": "local",
		"GOOS":        runtime.GOOS,
		"GOARCH":      runtime.GOARCH,
		"CGO_ENABLED": "0",
	}
	var env []string

	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := override[key]; !ok {
			env = append(env, entry)
		}
	}

	for key, value := range override {
		env = append(env, key+"="+value)
	}

	return env
}

func (r *runner) run(dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(r.ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, r.env
	// Separate stderr so JSON commands remain machine-readable.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	code := 0
	if err != nil {
		code = -1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}

	log := fmt.Sprintf("logs/%02d.log", len(r.commands)+1)
	r.commands = append(r.commands, commandRecord{args, log, code})
	if writeErr := writeFile(r.dir, log, append(append([]byte(nil), raw...), []byte(stderr.String())...)); writeErr != nil {
		return nil, writeErr
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s%s", strings.Join(args, " "), err, raw, stderr.String())
	}

	return raw, nil
}

func writeFile(root, name string, raw []byte) error {
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	return os.WriteFile(path, raw, 0644)
}

func jsonFile(root, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	return writeFile(root, name, append(raw, '\n'))
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeStream[T any](raw []byte) ([]T, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var values []T

	for {
		var value T
		if err := decoder.Decode(&value); err != nil {
			if err == io.EOF {
				return values, nil
			}

			return nil, err
		}

		values = append(values, value)
	}
}

func resolveSources(r *runner, m Manifest, values []string, ordinaryDependencies ...bool) (map[string]string, error) {
	sources := map[string]string{}
	versions := m.versions()

	for _, value := range values {
		module, directory, ok := strings.Cut(value, "=")
		if !ok || directory == "" {
			return nil, fmt.Errorf("source must be <selected-module>=<directory>: %q", value)
		}
		if _, ok := versions[module]; !ok && len(ordinaryDependencies) == 0 {
			return nil, fmt.Errorf("source module %s is not selected", module)
		}
		if _, ok := sources[module]; ok {
			return nil, fmt.Errorf("duplicate source mapping for %s", module)
		}

		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, err
		}

		absolute, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", module, err)
		}
		if info, err := os.Stat(filepath.Join(absolute, "go.mod")); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("source %s must contain a regular go.mod", module)
		}

		raw, err := r.run(absolute, "go", "mod", "edit", "-json")
		if err != nil {
			return nil, err
		}

		var metadata struct {
			Module  struct{ Path string }
			Replace []json.RawMessage
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, err
		}
		if metadata.Module.Path != module {
			if len(ordinaryDependencies) != 0 && strings.HasPrefix(module, metadata.Module.Path+"/") {
				return nil, fmt.Errorf("source module mismatch: replacement key %s is a package path; use module %s", module, metadata.Module.Path)
			}
			return nil, fmt.Errorf("source module mismatch: selected %s, go.mod declares %s", module, metadata.Module.Path)
		}
		// Replaces in dependency go.mod files are ignored by Go. Reject them
		// rather than silently resolving a different source than the developer used.
		if len(metadata.Replace) != 0 {
			return nil, fmt.Errorf(
				"source %s has go.mod replacements; remove them and declare dependencies explicitly with --replace (legacy: --source for selected modules)",
				module,
			)
		}

		sources[module] = absolute
	}

	return sources, nil
}

func checkModules(modules []moduleInfo, m Manifest, sources map[string]string) error {
	selected := m.versions()
	seen := map[string]bool{}

	for _, module := range modules {
		if module.Main {
			continue
		}

		if version, ok := selected[module.Path]; ok {
			seen[module.Path] = true
			if module.Version != version {
				return fmt.Errorf(
					"selected module %s resolved to %s, manifest requires %s; adjust manifest",
					module.Path,
					module.Version,
					version,
				)
			}
		}

		if module.Replace != nil {
			if directory, ok := sources[module.Path]; !ok || module.Replace.Path != directory || module.Replace.Version != "" {
				return fmt.Errorf("undeclared replacement for %s: %s", module.Path, module.Replace.Path)
			}
		} else if _, ok := sources[module.Path]; ok {
			return fmt.Errorf("missing explicit replacement for %s", module.Path)
		}
	}

	for module := range selected {
		if !seen[module] {
			return fmt.Errorf("selected module %s missing from resolved graph", module)
		}
	}

	return nil
}

type packageInfo struct {
	ImportPath string
	Name       string
	Dir        string
	Module     *moduleInfo

	GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
}

func sourceInputs(packages []packageInfo, m Manifest, sources map[string]string) ([]sourceRecord, error) {
	inputs := map[string]map[string]string{}

	for module := range sources {
		inputs[module] = map[string]string{}
	}

	add := func(module, name string) error {
		root := sources[module]
		absolute, err := filepath.EvalSymlinks(name)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, absolute)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("source input %s is outside mapping for %s", name, module)
		}

		raw, err := os.ReadFile(absolute)
		if err != nil {
			return err
		}

		inputs[module][filepath.ToSlash(rel)] = digest(raw)

		return nil
	}
	selected := map[string]string{}

	for _, p := range m.Plugins {
		selected[p.Package] = p.Module
	}

	for _, p := range packages {
		if module, ok := selected[p.ImportPath]; ok && (p.Module == nil || p.Module.Path != module) {
			return nil, fmt.Errorf("selected package %s resolved outside module %s", p.ImportPath, module)
		}
		if p.Module == nil {
			continue
		}

		if _, ok := sources[p.Module.Path]; !ok {
			continue
		}

		for _, files := range [][]string{
			p.GoFiles,
			p.CgoFiles,
			p.CFiles,
			p.CXXFiles,
			p.MFiles,
			p.HFiles,
			p.FFiles,
			p.SFiles,
			p.SwigFiles,
			p.SwigCXXFiles,
			p.SysoFiles,
			p.EmbedFiles,
		} {
			for _, file := range files {
				if err := add(p.Module.Path, filepath.Join(p.Dir, file)); err != nil {
					return nil, err
				}
			}
		}
	}

	var records []sourceRecord

	for module, directory := range sources {
		for _, name := range []string{"go.mod", "go.sum"} {
			file := filepath.Join(directory, name)
			if _, err := os.Stat(file); os.IsNotExist(err) && name == "go.sum" {
				continue
			}

			if err := add(module, file); err != nil {
				return nil, err
			}
		}

		raw, _ := json.Marshal(inputs[module]) // String maps have deterministic JSON key order.
		records = append(records, sourceRecord{module, directory, m.versions()[module], inputs[module], digest(raw)})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Module < records[j].Module })

	return records, nil
}

// Build delivers output and output.build only after compilation and pure
// Application validation. It refuses existing paths and never edits inputs.
func Build(ctx context.Context, options Options) (string, error) {
	if options.Output == "" || (options.Manifest == "" && options.Request == nil) || (options.Manifest != "" && options.Request != nil) {
		return "", fmt.Errorf("output and exactly one build input are required")
	}

	output, err := filepath.Abs(options.Output)
	if err != nil {
		return "", err
	}

	recordPath := output + ".build"

	for _, path := range []string{output, recordPath} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("refusing existing or inaccessible output %s", path)
		}
	}

	var raw []byte
	var m Manifest
	var resolution Resolution
	if options.Request != nil {
		raw, err = json.MarshalIndent(options.Request, "", "  ")
	} else {
		raw, err = os.ReadFile(options.Manifest)
		if err == nil {
			m, err = Parse(raw)
		}
	}
	if err != nil {
		return "", err
	}

	work, err := os.MkdirTemp("", "qi-build-")
	if err != nil {
		return "", err
	}

	defer os.RemoveAll(work)

	r := &runner{
		ctx: ctx,
		dir: work,
		env: nativeEnvironment(),
	}
	// Capture the native toolchain before isolated ownership/version probes.
	envRaw, err := r.run(
		work,
		"go",
		"env",
		"-json",
		"GOVERSION",
		"GOOS",
		"GOARCH",
		"CGO_ENABLED",
		"GOWORK",
		"GOFLAGS",
		"GOTOOLCHAIN",
		"GOEXPERIMENT",
		"GOAMD64",
		"GOARM64",
	)
	if err != nil {
		return "", err
	}

	var environment map[string]string
	if err := json.Unmarshal(envRaw, &environment); err != nil {
		return "", err
	}

	goVersion := strings.TrimPrefix(environment["GOVERSION"], "go")
	if !regexpGoVersion(goVersion) {
		return "", fmt.Errorf("unsupported Go toolchain %s", environment["GOVERSION"])
	}

	var replacements []Replacement
	var sources map[string]string
	if options.Request != nil {
		m, resolution, sources, err = resolveRequest(r, options.Request, goVersion)
		if err != nil {
			return "", err
		}
		for _, resolved := range resolution.Replacements {
			replacement := resolved.Replacement
			if resolved.TargetVersion != "" {
				replacement.Target += "@" + resolved.TargetVersion
			}
			replacements = append(replacements, replacement)
		}
	} else {
		m.builtinVersion = developmentVersion(BuiltinModule)
		sources, err = resolveSources(r, m, options.Sources)
		if err != nil {
			return "", err
		}
		// Resolve the default builtin's fixed query independently of kernel.
		legacy := &Request{Core: Selection{Module: CoreModule, Query: m.Core.Version, ExplicitQuery: true}}
		for module, directory := range sources {
			legacy.Replacements = append(legacy.Replacements, Replacement{Module: module, Target: directory})
		}
		defaults, resolved, _, prepareErr := prepareRequest(legacy)
		if prepareErr != nil {
			return "", prepareErr
		}
		// Legacy core.version already is the selected fixed requirement; it is not a moving query.
		for i := range resolved.Selections {
			if resolved.Selections[i].Module == CoreModule {
				resolved.Selections[i].QueryExecuted = false
			}
		}
		defaults, _, err = resolveVersions(r, defaults, resolved, goVersion)
		if err != nil {
			return "", err
		}
		m.builtinVersion = defaults.builtinVersion

	}
	files, err := generate(m, sources, goVersion, replacements...)
	if err != nil {
		return "", err
	}

	project := filepath.Join(work, "project")

	for name, content := range files {
		if err := writeFile(project, name, content); err != nil {
			return "", err
		}
	}

	for _, args := range [][]string{{"go", "mod", "tidy"}, {"go", "mod", "download", "all"}, {"go", "mod", "verify"}} {
		if _, err := r.run(project, args...); err != nil {
			return "", err
		}
	}

	// Validate what Go actually resolved, then hash the local files used by
	// that dependency graph rather than every file in each source directory.
	moduleRaw, err := r.run(project, "go", "list", "-mod=readonly", "-m", "-json", "all")
	if err != nil {
		return "", err
	}

	modules, err := decodeStream[moduleInfo](moduleRaw)
	if err != nil {
		return "", err
	}
	if options.Request == nil {
		if err := checkModules(modules, m, sources); err != nil {
			return "", err
		}
	} else {
		if err := checkResolution(modules, &resolution); err != nil {
			return "", err
		}
	}

	dependencyRaw, err := r.run(project, "go", "list", "-mod=readonly", "-deps", "-json", ".")
	if err != nil {
		return "", err
	}

	packages, err := decodeStream[packageInfo](dependencyRaw)
	if err != nil {
		return "", err
	}

	if options.Request != nil {
		for _, selected := range m.Plugins {
			found := false
			for _, pkg := range packages {
				if pkg.ImportPath != selected.Package {
					continue
				}
				found = true
				if pkg.Name == "main" || pkg.Module == nil || pkg.Module.Path != selected.Module {
					return "", fmt.Errorf("entry %s must be an importable package owned by module %s", selected.Package, selected.Module)
				}
			}
			if !found {
				return "", fmt.Errorf("entry %s missing from module %s", selected.Package, selected.Module)
			}
		}
	}
	inputs, err := sourceInputs(packages, m, sources)
	if err != nil {
		return "", err
	}

	candidate := filepath.Join(work, "candidate")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}

	if _, err := r.run(project, "go", "build", "-mod=readonly", "-trimpath", "-o", candidate, "."); err != nil {
		return "", err
	}
	if _, err := r.run(work, candidate, "--help"); err != nil {
		return "", fmt.Errorf("compiled Application validation failed: %w", err)
	}

	// Rehash after compilation so the record cannot describe inputs that
	// changed while the binary was being built.
	current, err := sourceInputs(packages, m, sources)
	if err != nil {
		return "", err
	}

	before, _ := json.Marshal(inputs)
	after, _ := json.Marshal(current)
	if string(before) != string(after) {
		return "", fmt.Errorf("local source inputs changed during build; retry with a fixed snapshot")
	}
	// Save final generated module files (including complete transitive sums).
	generatedHashes := map[string]string{}

	for _, name := range []string{"go.mod", "go.sum", "main.go", "internal/compiledplugins/plugins_gen.go"} {
		content, err := os.ReadFile(filepath.Join(project, name))
		if err != nil {
			return "", err
		}

		generatedHashes[name] = digest(content)
	}

	binary, err := os.ReadFile(candidate)
	if err != nil {
		return "", err
	}

	record := Record{
		"qi.build-record/v1alpha1",
		m,
		digest(raw),
		len(sources) != 0,
		inputs,
		modules,
		environment,
		generatedHashes,
		digest(binary),
		r.commands,
	}
	if options.Request == nil {
		if err := jsonFile(work, "record.json", record); err != nil {
			return "", err
		}
		normalized, err := yamlManifest(m)
		if err != nil {
			return "", err
		}
		if err := writeFile(work, "manifest.yaml", normalized); err != nil {
			return "", err
		}
		if err := writeFile(work, "manifest-input.yaml", raw); err != nil {
			return "", err
		}
	} else {
		// The CLI record does not impersonate a user-supplied legacy manifest.
		v2 := requestRecord{
			APIVersion:       "qi.build-record/v1alpha2",
			Request:          options.Request,
			RequestSHA256:    digest(raw),
			Resolved:         resolution,
			LocalDevelopment: len(sources) != 0,
			Sources:          inputs,
			Modules:          modules,
			Environment:      environment,
			GeneratedSHA256:  generatedHashes,
			BinarySHA256:     digest(binary),
			Commands:         r.commands,
		}
		if err := jsonFile(work, "record.json", v2); err != nil {
			return "", err
		}
		if err := writeFile(work, "request.json", raw); err != nil {
			return "", err
		}
		if err := jsonFile(work, "resolved-build.json", resolution); err != nil {
			return "", err
		}
	}

	if err := writeFile(work, "dependencies.json", dependencyRaw); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Publish only after validation and all record files are complete.
	if err := publish(work, candidate, output, recordPath); err != nil {
		return "", err
	}

	return recordPath, nil
}

func publish(work, candidate, output, recordPath string) (err error) {
	if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	// A same-filesystem staging file permits an exclusive link with no clobber.
	staged, err := os.CreateTemp(filepath.Dir(output), ".qi-build-")
	if err != nil {
		return err
	}

	defer os.Remove(staged.Name())
	defer staged.Close()

	input, err := os.Open(candidate)
	if err != nil {
		return err
	}

	defer input.Close()

	if _, err = io.Copy(staged, input); err != nil {
		return err
	}
	if err = staged.Chmod(0755); err != nil {
		return err
	}
	if err = staged.Close(); err != nil {
		return err
	}
	if err = os.Mkdir(recordPath, 0755); err != nil {
		return fmt.Errorf("refusing record output: %w", err)
	}

	defer func() {
		if err != nil {
			os.RemoveAll(recordPath)
		}
	}()

	names := []string{"project", "logs", "record.json", "dependencies.json"}
	if _, statErr := os.Stat(filepath.Join(work, "request.json")); statErr == nil {
		names = append(names, "request.json", "resolved-build.json")
	} else {
		names = append(names, "manifest.yaml", "manifest-input.yaml")
	}
	for _, name := range names {
		if err = os.Rename(filepath.Join(work, name), filepath.Join(recordPath, name)); err != nil {
			// Temporary storage may be on another filesystem; copy only records.
			if err = copyTree(filepath.Join(work, name), filepath.Join(recordPath, name)); err != nil {
				return err
			}
		}
	}

	if err = os.Link(staged.Name(), output); err != nil {
		return fmt.Errorf("deliver binary without overwrite: %w", err)
	}

	return nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, rel), 0755)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(target, rel), raw, 0644)
	})
}
