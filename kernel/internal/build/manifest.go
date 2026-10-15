// Package build owns static assembly generation. It never imports extensions.
package build

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const CoreModule = "github.com/RussellLuo/qi/kernel"
const BuiltinModule = "github.com/RussellLuo/qi/builtin"
const APIVersion = "qi.build/v1alpha1"
const generatedModule = "example.com/qi-compiled"

type Core struct {
	Version string `yaml:"version" json:"version"`
}

type Plugin struct {
	Module    string `yaml:"module" json:"module"`
	Version   string `yaml:"version" json:"version"`
	Package   string `yaml:"package" json:"package"`
	Execution string `yaml:"execution" json:"execution"`
}

type Manifest struct {
	builtinVersion string // derived dependency; never a manifest input or protocol field

	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Core       Core     `yaml:"core" json:"core"`
	Plugins    []Plugin `yaml:"plugins" json:"plugins"`
}

var fixedVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+incompatible)?$`)
var importPath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~/-]*$`)

func validPath(path string) bool {
	if !importPath.MatchString(path) || strings.Contains(path, "//") || strings.HasSuffix(path, "/") {
		return false
	}

	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}

	return true
}

// Parse is strict and normalizes package defaults and selection order only.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("build manifest: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("build manifest must contain one document")
	}
	if m.APIVersion != APIVersion {
		return m, fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if !fixedVersion.MatchString(m.Core.Version) {
		return m, fmt.Errorf("core.version must be a fixed Go version, got %q", m.Core.Version)
	}

	versions := map[string]string{CoreModule: m.Core.Version}
	packages := map[string]bool{}

	for i := range m.Plugins {
		p := &m.Plugins[i]
		if !validPath(p.Module) || !strings.Contains(strings.Split(p.Module, "/")[0], ".") || p.Module == generatedModule {
			return m, fmt.Errorf("invalid plugin module %q", p.Module)
		}
		if !fixedVersion.MatchString(p.Version) {
			return m, fmt.Errorf("%s: version must be fixed, got %q", p.Module, p.Version)
		}
		if p.Execution != "local" {
			return m, fmt.Errorf("%s: execution %q is unsupported; only local is supported", p.Module, p.Execution)
		}
		if p.Package == "" {
			p.Package = p.Module
		}

		if !validPath(p.Package) || (p.Package != p.Module && !strings.HasPrefix(p.Package, p.Module+"/")) {
			return m, fmt.Errorf("package %q must belong to module %s", p.Package, p.Module)
		}
		if old, ok := versions[p.Module]; ok && old != p.Version {
			return m, fmt.Errorf("conflicting versions for %s: %s and %s", p.Module, old, p.Version)
		}

		versions[p.Module] = p.Version
		if packages[p.Package] {
			return m, fmt.Errorf("duplicate selected package %s", p.Package)
		}

		packages[p.Package] = true
	}

	sort.Slice(m.Plugins, func(i, j int) bool { return m.Plugins[i].Package < m.Plugins[j].Package })

	return m, nil
}

func (m Manifest) versions() map[string]string {
	versions := map[string]string{CoreModule: m.Core.Version}
	if m.builtinVersion != "" {
		versions[BuiltinModule] = m.builtinVersion
	}

	for _, p := range m.Plugins {
		versions[p.Module] = p.Version
	}

	return versions
}
