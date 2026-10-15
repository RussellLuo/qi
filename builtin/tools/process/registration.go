package process

import (
	"errors"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

// Registration derives identity and service declarations from the ordinary
// Plugin. The supplied descriptor retains its schema and pure config validator;
// business Validate runs in the child, before any host contribution is visible.
func Registration(prototype gordis.Plugin, descriptor profile.PluginDescriptor, options Options) (registration.PluginRegistration, error) {
	adapter, err := NewAdapter(prototype, options)
	if err != nil {
		return registration.PluginRegistration{}, err
	}
	spec := adapter.Spec()
	if descriptor.ID != "" && descriptor.ID != spec.ID {
		return registration.PluginRegistration{}, errors.New("toolprocess: descriptor ID differs from Plugin Spec")
	}
	descriptor.ID = spec.ID
	descriptor.Requires = make([]profile.CapabilityRef, 0, len(spec.Requires))
	for _, service := range spec.Requires {
		descriptor.Requires = append(descriptor.Requires, profile.CapabilityRef{ID: service.Name()})
	}
	descriptor.Provides = make([]profile.CapabilityRef, 0, len(spec.Provides))
	for _, service := range spec.Provides {
		descriptor.Provides = append(descriptor.Provides, profile.CapabilityRef{ID: service.Name()})
	}
	descriptor.ConfigSchema = append([]byte(nil), descriptor.ConfigSchema...)
	descriptor.Compatibility.Contracts = append([]string(nil), descriptor.Compatibility.Contracts...)
	return registration.PluginRegistration{Plugin: adapter, Descriptor: descriptor, ProfileVisible: true}, nil
}
