// Package profile owns public declarations, runtime codecs and detached metadata.
// Candidate preflight and user-source composition remain private.
package profile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	yaml "gopkg.in/yaml.v3"
)

const ProfileAPIVersion = "qi.profile/v1alpha1"

type Profile struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Instances  []InstanceSpec `json:"instances" yaml:"instances"`
}

// UnmarshalJSON keeps HTTP apply requests on the same strict decoding boundary
// as file/watch/CLI input, including nested Profile values.
func (p *Profile) UnmarshalJSON(raw []byte) error {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("runtime profile must be an object")
	}

	profile, err := Parse(raw)
	if err != nil {
		return err
	}

	*p = profile

	return nil
}

type InstanceSpec struct {
	ID           string         `json:"id" yaml:"id"`
	Plugin       string         `json:"plugin" yaml:"plugin"`
	Config       map[string]any `json:"config,omitempty" yaml:"config,omitempty"`
	RestartToken string         `json:"restartToken,omitempty" yaml:"restartToken,omitempty"`
}

func Parse(raw []byte) (Profile, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode runtime profile: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Profile{}, errors.New("decode runtime profile: multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return Profile{}, fmt.Errorf("decode runtime profile trailing document: %w", err)
	}

	if profile.APIVersion != "" && profile.APIVersion != ProfileAPIVersion {
		return Profile{}, fmt.Errorf("runtime profile: unsupported apiVersion %q; expected %s", profile.APIVersion, ProfileAPIVersion)
	}
	for _, instance := range profile.Instances {
		if err := configcodec.CheckPlugin(instance.Plugin, "instance "+instance.ID); err != nil {
			return Profile{}, err
		}
	}
	return profile, nil
}

func Marshal(profile Profile) ([]byte, error) {
	profile = profile.Clone()
	return yaml.Marshal(profile)
}

func (p Profile) Clone() Profile {
	cloned := Profile{APIVersion: p.APIVersion}
	cloned.Instances = make([]InstanceSpec, len(p.Instances))

	for index, instance := range p.Instances {
		cloned.Instances[index] = instance
		cloned.Instances[index].Config = configcodec.CloneMap(instance.Config)
	}

	return cloned
}

func (p Profile) Digest() (string, error) {
	normalized := p.Clone()
	normalized.sort()
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256(raw)

	return "sha256-" + hex.EncodeToString(digest[:]), nil
}

func (p *Profile) sort() {
	sort.Slice(p.Instances, func(i, j int) bool { return p.Instances[i].ID < p.Instances[j].ID })

}

type UpdatePolicy string

const (
	UpdateReplaceGeneration UpdatePolicy = "replace-generation"
	UpdateRestartHost       UpdatePolicy = "restart-host"
	UpdateImmutable         UpdatePolicy = "immutable"
)

const HostAPIVersion = "qi.host/v1alpha1"

type CapabilityRef struct {
	ID string `json:"id"`
}

type Compatibility struct {
	HostAPI   string   `json:"hostApi"`
	ClientAPI string   `json:"clientApi,omitempty"`
	Contracts []string `json:"contracts,omitempty"`
}

type ConfigValidator func(json.RawMessage) error

type PluginDescriptor struct {
	ID            string          `json:"id"`
	Title         string          `json:"title"`
	ConfigSchema  json.RawMessage `json:"configSchema"`
	UpdatePolicy  UpdatePolicy    `json:"updatePolicy"`
	Provides      []CapabilityRef `json:"provides"`
	Requires      []CapabilityRef `json:"requires"`
	Compatibility Compatibility   `json:"compatibility"`

	// Deprecated compatibility view of UpdatePolicy.
	Mutable bool `json:"mutable"`

	// Require a complete Service graph for protected entry points.
	PreflightDependencies bool                                                    `json:"-"`
	RequiresModels        bool                                                    `json:"-"`
	ValidateConfig        ConfigValidator                                         `json:"-"`
	DescribeModels        func(json.RawMessage) (modelcatalog.Description, error) `json:"-"`
}
