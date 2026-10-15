package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
	yaml "gopkg.in/yaml.v3"
)

// ProfileSource is the user-owned declaration, distinct from the execution Profile.
type ProfileSource struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Bundles    []string       `json:"bundles,omitempty" yaml:"bundles,omitempty"`
	Instances  []InstanceSpec `json:"instances,omitempty" yaml:"instances,omitempty"`
	Overrides  []Override     `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

func ParseSource(raw []byte) (ProfileSource, error) {
	root, err := configcodec.ParseDeclaration(raw, "user")
	if err != nil {
		return ProfileSource{}, err
	}
	if err := configcodec.CheckLayerNode(root, "user", false); err != nil {
		return ProfileSource{}, err
	}

	var source ProfileSource
	if err := root.Decode(&source); err != nil {
		return ProfileSource{}, errors.New("user: cannot decode source declaration")
	}

	return normalizeSource(source)
}

// JSON transport decoding must retain the same duplicate-key/null/tag boundary.
func (s *ProfileSource) UnmarshalJSON(raw []byte) error {
	if !json.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("user: source must be a JSON object")
	}

	value, err := ParseSource(raw)
	if err == nil {
		*s = value
	}

	return err
}

func MarshalSource(source ProfileSource) ([]byte, error) {
	value, err := normalizeSource(source)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(value)
}

// Digest hashes normalized declarations, retaining order and override presence.
// Bundle content digests belong in ResolvedProfile.BundleInputs, not this digest.
func (s ProfileSource) Digest() (string, error) {
	value, err := normalizeSource(s)
	if err != nil {
		return "", err
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return "", errors.New("user: cannot encode source declaration")
	}

	return configcodec.Digest(raw), nil
}

func (s ProfileSource) Clone() ProfileSource {
	s.Bundles = append([]string(nil), s.Bundles...)
	s.Instances = (Profile{Instances: s.Instances}).Clone().Instances
	s.Overrides = (BundleDefinition{Overrides: s.Overrides}).Clone().Overrides
	return s
}

func normalizeSource(source ProfileSource) (ProfileSource, error) {
	if err := configcodec.CheckVersion(source.APIVersion, ProfileAPIVersion, "user"); err != nil {
		return ProfileSource{}, err
	}

	seen := map[string]bool{}

	for i, id := range source.Bundles {
		if !configcodec.ValidIdentifier(id) || seen[id] {
			return ProfileSource{}, fmt.Errorf("user.bundles[%d]: invalid or duplicate bundle %q", i, id)
		}

		seen[id] = true
	}

	instances, overrides, err := normalizeLayer(source.Instances, source.Overrides, "user")
	if err != nil {
		return ProfileSource{}, err
	}

	return ProfileSource{
		APIVersion: ProfileAPIVersion,
		Bundles:    append([]string(nil), source.Bundles...),
		Instances:  instances,
		Overrides:  overrides,
	}, nil
}
