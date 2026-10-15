// Package modelcatalog defines provider-owned model declarations and their
// derived product view. It contains no runtime or Profile state.
package modelcatalog

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

type Entry struct {
	ID         string         `json:"id"`
	Label      string         `json:"label,omitempty"`
	Model      string         `json:"model"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Spec is a read-only DTO; Route and Provider come from the owning adapter.
type Spec struct {
	ID         string         `json:"id" yaml:"id"`
	Label      string         `json:"label" yaml:"label"`
	Route      string         `json:"route" yaml:"route"`
	Provider   string         `json:"provider" yaml:"provider"`
	Model      string         `json:"model" yaml:"model"`
	Parameters map[string]any `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

type Defaults struct {
	Model string `json:"model"`
}

// Description is the pure result of strictly decoding a plugin configuration.
// Both preflight and activation use the plugin's DescribeModels method.
type Description struct {
	Routes map[string]string
	// Restrictions are adapter model names, distinct from product model IDs.
	Restrictions []string
	Models       []Spec
	Defaults     *Defaults
}

type Describer interface{ DescribeModels() (Description, error) }

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func Provider(route, provider string, allowed []string, entries []Entry) (Description, error) {
	result := Description{
		Routes:       map[string]string{route: provider},
		Restrictions: append([]string(nil), allowed...),
	}
	seen := map[string]bool{}

	for _, entry := range entries {
		if !identifier.MatchString(entry.ID) || entry.Model == "" {
			return Description{}, fmt.Errorf("invalid model %q", entry.ID)
		}
		if seen[entry.ID] {
			return Description{}, fmt.Errorf("duplicate model ID %q", entry.ID)
		}

		seen[entry.ID] = true
		if len(allowed) > 0 {
			found := false

			for _, name := range allowed {
				if name == entry.Model {
					found = true
				}
			}

			if !found {
				return Description{}, fmt.Errorf("catalog model %q is not provided by the adapter models restriction", entry.ID)
			}
		}

		label := entry.Label
		if label == "" {
			label = provider + " / " + entry.Model
		}

		model := Spec{
			ID:         entry.ID,
			Label:      label,
			Route:      route,
			Provider:   provider,
			Model:      entry.Model,
			Parameters: entry.Parameters,
		}
		if _, err := model.LLMConfig(); err != nil {
			return Description{}, fmt.Errorf("model %q parameters: %w", entry.ID, err)
		}

		result.Models = append(result.Models, model.Clone())
	}

	return result, nil
}

func (m Spec) Clone() Spec {
	if m.Parameters != nil {
		raw, err := json.Marshal(m.Parameters)
		if err == nil {
			m.Parameters = nil
			_ = json.Unmarshal(raw, &m.Parameters)
		}
	}

	return m
}

func (m Spec) LLMConfig() (protocol.CallConfig, error) {
	fields := map[string]json.RawMessage{}

	for key, value := range m.Parameters {
		if key != "reasoningEffort" && key != "temperature" && key != "maxTokens" && key != "stop" {
			return protocol.CallConfig{}, fmt.Errorf("unsupported model parameter %q; configure private options on the adapter", key)
		}

		raw, err := json.Marshal(value)
		if err != nil {
			return protocol.CallConfig{}, err
		}

		fields[key] = raw
	}

	fields["provider"], _ = json.Marshal(m.Provider)
	fields["model"], _ = json.Marshal(m.Model)
	raw, err := json.Marshal(fields)
	if err != nil {
		return protocol.CallConfig{}, err
	}

	return protocol.DecodeCallConfig(raw)
}

const EntrySchema = `{"type":"array","items":{"type":"object","properties":{"id":{"type":"string","minLength":1},"label":{"type":"string"},"model":{"type":"string","minLength":1},"parameters":{"type":"object"}},"required":["id","model"],"additionalProperties":false}}`
const DefaultsSchema = `{"type":"object","properties":{"model":{"type":"string","minLength":1}},"required":["model"],"additionalProperties":false}`
