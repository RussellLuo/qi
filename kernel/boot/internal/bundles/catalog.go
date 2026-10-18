// Package bundles constructs immutable catalogs from explicitly supplied layers.
// Executable artifacts remain the responsibility of plugins/packages/controller.
package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
)

type Descriptor struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Digest string `json:"digest"`
}

type entry struct {
	definition appprofile.BundleDefinition
	descriptor Descriptor
}

// Catalog has no mutation methods; all returned definitions are detached copies.
type Catalog struct {
	entries map[string]entry
}

// NewCatalogWith constructs only explicitly supplied immutable YAML layers.
func NewCatalogWith(layers ...[]byte) (*Catalog, error) {
	definitions := make([]appprofile.BundleDefinition, 0, len(layers))

	for _, raw := range layers {
		definition, err := appprofile.ParseBundleDefinition(raw)
		if err != nil {
			return nil, err
		}

		definitions = append(definitions, definition)
	}

	catalog, err := NewCatalogFromDefinitions(definitions...)
	if err != nil {
		return nil, err
	}
	// Preserve the raw resource digest used by existing saved diagnostics.
	for i, raw := range layers {
		entry := catalog.entries[definitions[i].ID]
		digest := sha256.Sum256(raw)
		entry.descriptor.Digest = "sha256-" + hex.EncodeToString(digest[:])
		catalog.entries[definitions[i].ID] = entry
	}

	return catalog, nil
}

// NewCatalogFromDefinitions creates only the explicitly supplied layers.
// JSON round-tripping through the source parser validates programmatic values
// and gives every frozen definition a deterministic content digest.
func NewCatalogFromDefinitions(definitions ...appprofile.BundleDefinition) (*Catalog, error) {
	catalog := &Catalog{entries: map[string]entry{}}

	for _, value := range definitions {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("bundle definition: %w", err)
		}

		definition, err := appprofile.ParseBundleDefinition(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := catalog.entries[definition.ID]; exists {
			return nil, fmt.Errorf("duplicate bundle %q", definition.ID)
		}

		digest := sha256.Sum256(raw)
		catalog.entries[definition.ID] = entry{
			definition: definition,
			descriptor: Descriptor{
				ID:     definition.ID,
				Title:  definition.Title,
				Digest: "sha256-" + hex.EncodeToString(digest[:]),
			},
		}
	}

	return catalog, nil
}

func (c *Catalog) Lookup(id string) (appprofile.BundleDefinition, string, bool) {
	if c == nil {
		return appprofile.BundleDefinition{}, "", false
	}

	entry, ok := c.entries[id]
	if !ok {
		return appprofile.BundleDefinition{}, "", false
	}

	return entry.definition.Clone(), entry.descriptor.Digest, true
}

func (c *Catalog) Snapshot() []Descriptor {
	result := []Descriptor{}
	if c != nil {
		for _, entry := range c.entries {
			result = append(result, entry.descriptor)
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })

	return result
}

// LoadWith uses the caller-selected catalog without path lookup or fallback.
func LoadWith(path string, catalog appprofile.BundleCatalog) (appprofile.ResolvedProfile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return appprofile.ResolvedProfile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return appprofile.ResolvedProfile{}, err
	}

	resolved, err := appprofile.ResolveSource(raw, catalog)
	resolved.BaseDir = filepath.Dir(path)
	return resolved, err
}
