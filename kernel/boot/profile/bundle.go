package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
	yaml "gopkg.in/yaml.v3"
)

// Override uses pointers to preserve omission, including false, "" and {}.
// Explicit null is rejected at the source decoding boundary.
type Override struct {
	ID           string          `json:"id" yaml:"id"`
	Plugin       *string         `json:"plugin,omitempty" yaml:"plugin,omitempty"`
	Config       *map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
	RestartToken *string         `json:"restartToken,omitempty" yaml:"restartToken,omitempty"`
	Disabled     *bool           `json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

// BundleDefinition is one trusted layer. It cannot reference another Bundle.
type BundleDefinition struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	ID         string         `json:"id" yaml:"id"`
	Title      string         `json:"title,omitempty" yaml:"title,omitempty"`
	Instances  []InstanceSpec `json:"instances,omitempty" yaml:"instances,omitempty"`
	Overrides  []Override     `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

func ParseBundleDefinition(raw []byte) (BundleDefinition, error) {
	root, err := configcodec.ParseDeclaration(raw, "bundle definition")
	if err != nil {
		return BundleDefinition{}, err
	}

	layer := "bundle definition"

	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "id" && root.Content[i+1].Tag == "!!str" {
			layer = fmt.Sprintf("bundle %q", root.Content[i+1].Value)
		}
	}

	if err := configcodec.CheckLayerNode(root, layer, true); err != nil {
		return BundleDefinition{}, err
	}

	var definition BundleDefinition
	if err := root.Decode(&definition); err != nil {
		return BundleDefinition{}, fmt.Errorf("%s: cannot decode definition", layer)
	}

	return normalizeDefinition(definition)
}

func (d *BundleDefinition) UnmarshalJSON(raw []byte) error {
	if !json.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("bundle definition: must be a JSON object")
	}

	value, err := ParseBundleDefinition(raw)
	if err == nil {
		*d = value
	}

	return err
}

func MarshalBundleDefinition(definition BundleDefinition) ([]byte, error) {
	value, err := normalizeDefinition(definition)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(value)
}

func (d BundleDefinition) Clone() BundleDefinition {
	d.Instances = (Profile{Instances: d.Instances}).Clone().Instances
	d.Overrides = cloneOverrides(d.Overrides)
	return d
}

func cloneOverrides(items []Override) []Override {
	result := make([]Override, len(items))

	for i, item := range items {
		result[i] = item
		if item.Plugin != nil {
			value := *item.Plugin
			result[i].Plugin = &value
		}

		if item.Config != nil {
			value := configcodec.CloneMap(*item.Config)
			result[i].Config = &value
		}

		if item.RestartToken != nil {
			value := *item.RestartToken
			result[i].RestartToken = &value
		}

		if item.Disabled != nil {
			value := *item.Disabled
			result[i].Disabled = &value
		}
	}

	return result
}

func normalizeDefinition(definition BundleDefinition) (BundleDefinition, error) {
	layer := fmt.Sprintf("bundle %q", definition.ID)
	if !configcodec.ValidIdentifier(definition.ID) {
		return BundleDefinition{}, fmt.Errorf("%s.id: invalid identifier", layer)
	}
	if err := configcodec.CheckVersion(definition.APIVersion, ProfileAPIVersion, layer); err != nil {
		return BundleDefinition{}, err
	}

	instances, overrides, err := normalizeLayer(definition.Instances, definition.Overrides, layer)
	if err != nil {
		return BundleDefinition{}, err
	}

	return BundleDefinition{
		APIVersion: ProfileAPIVersion,
		ID:         definition.ID,
		Title:      definition.Title,
		Instances:  instances,
		Overrides:  overrides,
	}, nil
}
