package profile

import (
	"fmt"

	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
)

type BundleInput struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// ComposedInstance retains disabled declarations for public type admission.
// Layer is the last declaration/override layer, not a complete origin history.
type ComposedInstance struct {
	Spec        InstanceSpec
	Disabled    bool
	Layer       string
	ConfigLayer string // Last whole-config write, including a plugin-change reset.
}

type ResolvedProfile struct {
	Source       ProfileSource
	Profile      Profile
	Declarations []ComposedInstance
	SourceDigest string
	BundleInputs []BundleInput
	// Content is the exact file snapshot, nil for programmatic sources.
	Content []byte
}

// Compose performs only structural composition. Call ValidateDeclarations to
// admit every declared type, then use the existing Catalog/Plan on Profile for
// active config, model, package, dependency and entry-point preflight.
func Compose(source ProfileSource, catalog BundleCatalog) (ResolvedProfile, error) {
	source, err := normalizeSource(source)
	if err != nil {
		return ResolvedProfile{}, err
	}

	result := ResolvedProfile{
		Source:       source,
		Profile:      Profile{APIVersion: ProfileAPIVersion, Instances: []InstanceSpec{}},
		Declarations: []ComposedInstance{},
		BundleInputs: []BundleInput{},
	}
	byID := map[string]int{}
	apply := func(instances []InstanceSpec, overrides []Override, layer string) error {
		for i, instance := range instances {
			if _, exists := byID[instance.ID]; exists {
				return fmt.Errorf("%s.instances[%d] instance %q: already declared; use overrides", layer, i, instance.ID)
			}

			byID[instance.ID] = len(result.Declarations)
			result.Declarations = append(result.Declarations, ComposedInstance{
				Spec:        instance,
				Layer:       layer,
				ConfigLayer: layer,
			})
		}

		for i, override := range overrides {
			index, exists := byID[override.ID]
			if !exists {
				return fmt.Errorf("%s.overrides[%d] instance %q: unknown target", layer, i, override.ID)
			}

			entry := &result.Declarations[index]
			if override.Plugin != nil && *override.Plugin != entry.Spec.Plugin {
				entry.Spec.Plugin = *override.Plugin
				entry.Spec.Config = map[string]any{}
				entry.Spec.RestartToken = ""
				entry.ConfigLayer = layer
			}

			if override.Config != nil {
				entry.Spec.Config = configcodec.CloneMap(*override.Config)
				entry.ConfigLayer = layer
			}

			if override.RestartToken != nil {
				entry.Spec.RestartToken = *override.RestartToken
			}

			if override.Disabled != nil {
				entry.Disabled = *override.Disabled
			}

			entry.Layer = layer
		}

		return nil
	}

	for i, id := range source.Bundles {
		if catalog == nil {
			return ResolvedProfile{}, fmt.Errorf("user.bundles[%d]: unknown bundle %q", i, id)
		}

		definition, contentDigest, exists := catalog.Lookup(id)
		if !exists {
			return ResolvedProfile{}, fmt.Errorf("user.bundles[%d]: unknown bundle %q", i, id)
		}
		if definition.ID != id || contentDigest == "" {
			return ResolvedProfile{}, fmt.Errorf("bundle %q: invalid catalog entry", id)
		}

		definition, err = normalizeBundle(definition)
		if err != nil {
			return ResolvedProfile{}, err
		}
		if err := apply(definition.Instances, definition.Overrides, fmt.Sprintf("bundle %q", id)); err != nil {
			return ResolvedProfile{}, err
		}

		result.BundleInputs = append(result.BundleInputs, BundleInput{ID: id, Digest: contentDigest})
	}

	if err := apply(source.Instances, source.Overrides, "user"); err != nil {
		return ResolvedProfile{}, err
	}

	for _, entry := range result.Declarations {
		if !entry.Disabled {
			result.Profile.Instances = append(result.Profile.Instances, entry.Spec)
		}
	}
	// Detach the source, execution graph and logical table from one another.
	result.Source = source.Clone()
	result.Profile = result.Profile.Clone()
	result.SourceDigest, err = source.Digest()
	if err != nil {
		return ResolvedProfile{}, err
	}

	return result, nil
}

// ValidateDeclarations checks only structural/public type admission, including
// disabled entries. Their configs and dependencies are deliberately not checked.
func (c *Catalog) ValidateDeclarations(declarations []ComposedInstance) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, entry := range declarations {
		if err := configcodec.CheckPlugin(entry.Spec.Plugin, entry.Layer); err != nil {
			return err
		}
		if _, exists := c.plugins[entry.Spec.Plugin]; !exists {
			return fmt.Errorf(
				"%s instance %q.plugin: plugin %q is not in the trusted catalog of public plugins",
				entry.Layer,
				entry.Spec.ID,
				entry.Spec.Plugin,
			)
		}
	}

	return nil
}

// ResolveSource freezes the parsed declaration and the exact bytes read together.
func ResolveSource(raw []byte, catalog BundleCatalog) (ResolvedProfile, error) {
	source, err := ParseSource(raw)
	if err != nil {
		return ResolvedProfile{}, err
	}

	resolved, err := Compose(source, catalog)
	if err != nil {
		return ResolvedProfile{}, err
	}

	resolved.Content = append([]byte{}, raw...)

	return resolved, nil
}

func SourceFromProfile(profile Profile) ProfileSource {
	return ProfileSource{APIVersion: profile.APIVersion, Instances: profile.Clone().Instances}
}

func (source ProfileSource) Composed() bool {
	return len(source.Bundles) > 0 || len(source.Overrides) > 0
}

func (resolved ResolvedProfile) Clone() ResolvedProfile {
	result := resolved
	result.Source = resolved.Source.Clone()
	result.Profile = resolved.Profile.Clone()
	result.BundleInputs = append([]BundleInput{}, resolved.BundleInputs...)
	result.Declarations = append([]ComposedInstance{}, resolved.Declarations...)

	for i := range result.Declarations {
		result.Declarations[i].Spec.Config = configcodec.CloneMap(result.Declarations[i].Spec.Config)
	}

	if resolved.Content != nil {
		result.Content = append([]byte{}, resolved.Content...)
	}

	return result
}
