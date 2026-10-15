package profile

import "github.com/RussellLuo/gordis"

// RequireCompleteDependencies makes every launch/online candidate check its
// entire graph, including unused duplicate providers, before any mutation.
func (c *Catalog) RequireCompleteDependencies() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.completeDependencies = true
}

// MountPlan orders actual service dependencies and preserves frozen effective
// configuration, without activation. Managed plans still use the Reconciler
// to publish model/Preset revisions rather than mounting this slice directly.
func (c *Catalog) MountPlan(profile Profile) (Profile, []gordis.InstanceSpec, error) {
	normalized, err := c.validate(profile)
	if err != nil {
		return Profile{}, nil, err
	}
	if err := validateDependencies(normalized.instances, true, c.externalCapabilities); err != nil {
		return Profile{}, nil, err
	}

	providers := map[string]string{}

	for id, instance := range normalized.instances {
		for _, service := range instance.descriptor.Provides {
			providers[service.ID] = id
		}
	}

	seen := map[string]bool{}
	var specs []gordis.InstanceSpec
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}

		seen[id] = true
		instance := normalized.instances[id]

		for _, service := range instance.descriptor.Requires {
			if provider := providers[service.ID]; provider != "" {
				visit(provider)
			}
		}

		specs = append(specs, gordis.InstanceSpec{
			ID:     id,
			Plugin: instance.spec.Plugin,
			Config: append([]byte(nil), instance.raw...),
		})
	}

	for _, id := range sortedKeys(normalized.instances) {
		visit(id)
	}

	return normalized.profile, specs, nil
}

// ProvideExternalCapabilities records services supplied by trusted bootstrap
// carriers, outside the public Profile. Public instances cannot shadow them.
// Call before sharing the Catalog with runtime callers.
func (c *Catalog) ProvideExternalCapabilities(services ...CapabilityRef) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.externalCapabilities == nil {
		c.externalCapabilities = map[string]bool{}
	}

	for _, service := range services {
		c.externalCapabilities[service.ID] = true
	}
}
