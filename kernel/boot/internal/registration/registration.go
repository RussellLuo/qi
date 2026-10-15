package registration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
	"github.com/RussellLuo/qi/kernel/boot/internal/profile"
	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
)

// ResolveRegistrations validates that product metadata describes the same
// plugin type Gordis will register. Service capabilities and semantic config
// validation are derived from the prototype rather than copied by hand.
func ResolveRegistrations(registrations []PluginRegistration) ([]gordis.Plugin, []profile.PluginDescriptor, error) {
	plugins := make([]gordis.Plugin, 0, len(registrations))
	descriptors := make([]profile.PluginDescriptor, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))

	for _, registration := range registrations {
		if nilPlugin(registration.Plugin) {
			return nil, nil, errors.New("runtime plugin registration contains a nil prototype")
		}

		spec := registration.Plugin.Spec()
		if !configcodec.ValidPluginID(spec.ID) || spec.New == nil {
			return nil, nil, fmt.Errorf("runtime plugin registration has invalid Gordis spec %q", spec.ID)
		}
		if _, duplicate := seen[spec.ID]; duplicate {
			return nil, nil, fmt.Errorf("runtime plugin registration duplicates %q", spec.ID)
		}

		seen[spec.ID] = struct{}{}
		descriptor := registration.Descriptor
		if descriptor.ID == "" {
			descriptor.ID = spec.ID
		}

		if descriptor.ID != spec.ID {
			return nil, nil, fmt.Errorf("runtime plugin registration descriptor %q does not match Gordis plugin %q", descriptor.ID, spec.ID)
		}

		descriptor.Requires = capabilityRefs(spec.Requires)
		descriptor.Provides = capabilityRefs(spec.Provides)
		prototypeValidator := pluginConfigValidator(spec)
		customValidator := descriptor.ValidateConfig
		descriptor.ValidateConfig = func(raw json.RawMessage) error {
			if err := prototypeValidator(raw); err != nil {
				return err
			}
			if customValidator != nil {
				return customValidator(raw)
			}

			return nil
		}
		if _, ok := registration.Plugin.(modelcatalog.Describer); ok {
			descriptor.DescribeModels = func(raw json.RawMessage) (modelcatalog.Description, error) {
				candidate, err := DecodePluginConfig(spec, raw)
				if err != nil {
					return modelcatalog.Description{}, err
				}

				return candidate.(modelcatalog.Describer).DescribeModels()
			}
		}

		plugins = append(plugins, registration.Plugin)
		if registration.ProfileVisible {
			descriptors = append(descriptors, descriptor)
		}
	}

	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].ID < descriptors[j].ID })

	return plugins, descriptors, nil
}

func capabilityRefs(specs []gordis.ServiceSpec) []profile.CapabilityRef {
	refs := make([]profile.CapabilityRef, 0, len(specs))

	for _, spec := range specs {
		refs = append(refs, profile.CapabilityRef{ID: spec.Name()})
	}

	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })

	return refs
}

func pluginConfigValidator(spec gordis.PluginSpec) profile.ConfigValidator {
	return func(raw json.RawMessage) error {
		_, err := DecodePluginConfig(spec, raw)
		return err
	}
}

// DecodePluginConfig constructs and validates a detached configuration candidate.
// Capability adapters use it to derive descriptors without activating plugins.
func DecodePluginConfig(spec gordis.PluginSpec, raw json.RawMessage) (gordis.Plugin, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, errors.New("configuration must be a JSON object")
	}

	candidate := spec.New()
	if nilPlugin(candidate) {
		return nil, errors.New("plugin constructor returned nil")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(candidate); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("configuration must contain exactly one JSON object")
	}
	if validator, ok := candidate.(gordis.Validator); ok {
		if err := validator.Validate(); err != nil {
			return nil, err
		}
	}

	return candidate, nil
}

func nilPlugin(plugin gordis.Plugin) bool {
	if plugin == nil {
		return true
	}

	value := reflect.ValueOf(plugin)

	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
