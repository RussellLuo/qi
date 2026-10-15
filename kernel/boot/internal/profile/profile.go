package profile

import (
	"bytes"
	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"

	"encoding/json"
	"errors"
	"fmt"

	"github.com/RussellLuo/qi/kernel/core/modelcatalog"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"sort"
	"sync"
)

func rawObject(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	return raw, nil
}

type CatalogSnapshot struct {
	Plugins []PluginDescriptor `json:"plugins"`
}

type Catalog struct {
	profilePolicy        func(Profile) error
	completeDependencies bool
	externalCapabilities map[string]bool
	mu                   sync.RWMutex
	plugins              map[string]PluginDescriptor
	pluginSchemas        map[string]*jsonschema.Schema
	checks               Checks
}

func NewCatalog(plugins []PluginDescriptor) (*Catalog, error) {
	catalog := &Catalog{
		plugins:       make(map[string]PluginDescriptor),
		pluginSchemas: make(map[string]*jsonschema.Schema),
	}

	for _, plugin := range plugins {
		if !configcodec.ValidPluginID(plugin.ID) {
			return nil, fmt.Errorf("runtime configuration catalog: invalid plugin ID %q", plugin.ID)
		}
		if _, exists := catalog.plugins[plugin.ID]; exists {
			return nil, fmt.Errorf("runtime configuration catalog: duplicate plugin %q", plugin.ID)
		}
		if plugin.Title == "" {
			plugin.Title = plugin.ID
		}

		if plugin.UpdatePolicy == "" {
			if plugin.Mutable {
				plugin.UpdatePolicy = UpdateReplaceGeneration
			} else {
				plugin.UpdatePolicy = UpdateImmutable
			}
		}

		switch plugin.UpdatePolicy {
		case UpdateReplaceGeneration, UpdateRestartHost, UpdateImmutable:
		default:
			return nil, fmt.Errorf("runtime configuration catalog: plugin %q has invalid update policy %q", plugin.ID, plugin.UpdatePolicy)
		}

		plugin.Mutable = plugin.UpdatePolicy == UpdateReplaceGeneration
		if len(plugin.ConfigSchema) == 0 {
			plugin.ConfigSchema = json.RawMessage(`{"type":"object"}`)
		} else {
			plugin.ConfigSchema = append(json.RawMessage(nil), plugin.ConfigSchema...)
		}

		compiled, err := compilePluginSchema(plugin.ID, plugin.ConfigSchema)
		if err != nil {
			return nil, fmt.Errorf("runtime configuration catalog: plugin %q config schema: %w", plugin.ID, err)
		}
		if plugin.Compatibility.HostAPI == "" {
			plugin.Compatibility.HostAPI = HostAPIVersion
		}

		plugin.Provides = cloneCapabilities(plugin.Provides)
		plugin.Requires = cloneCapabilities(plugin.Requires)
		plugin.Compatibility.Contracts = append([]string(nil), plugin.Compatibility.Contracts...)
		catalog.plugins[plugin.ID] = plugin
		catalog.pluginSchemas[plugin.ID] = compiled
	}

	return catalog, nil
}

func (c *Catalog) Snapshot() CatalogSnapshot {
	if c == nil {
		return CatalogSnapshot{}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := CatalogSnapshot{Plugins: []PluginDescriptor{}}

	for _, plugin := range c.plugins {
		plugin.ConfigSchema = append(json.RawMessage(nil), plugin.ConfigSchema...)
		plugin.Provides = cloneCapabilities(plugin.Provides)
		plugin.Requires = cloneCapabilities(plugin.Requires)
		plugin.Compatibility.Contracts = append([]string(nil), plugin.Compatibility.Contracts...)
		snapshot.Plugins = append(snapshot.Plugins, plugin)
	}

	sort.Slice(snapshot.Plugins, func(i, j int) bool { return snapshot.Plugins[i].ID < snapshot.Plugins[j].ID })

	return snapshot
}

// SetProfilePolicy installs bootstrap admission policy shared by every mutation
// source. The policy must be immutable, read-only and must not call this Catalog.
func (c *Catalog) SetProfilePolicy(policy func(Profile) error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.profilePolicy = policy
}

// Validate checks a Profile against the trusted Catalog without applying it.
func (c *Catalog) Validate(profile Profile) error {
	_, err := c.validate(profile)
	return err
}

func (c *Catalog) validate(profile Profile) (normalizedProfile, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.profilePolicy != nil {
		if err := c.profilePolicy(profile); err != nil {
			return normalizedProfile{}, err
		}
	}

	if profile.APIVersion == "" {
		profile.APIVersion = ProfileAPIVersion
	}

	if profile.APIVersion != ProfileAPIVersion {
		return normalizedProfile{}, fmt.Errorf("runtime profile: unsupported apiVersion %q; expected %s", profile.APIVersion, ProfileAPIVersion)
	}

	normalized := normalizedProfile{
		profile: profile.Clone(), instances: make(map[string]normalizedInstance),
	}

	for index := range normalized.profile.Instances {
		instance := normalized.profile.Instances[index]
		if !configcodec.ValidIdentifier(instance.ID) || !configcodec.ValidPluginID(instance.Plugin) {
			return normalizedProfile{}, fmt.Errorf("runtime profile: invalid instance %q or plugin %q", instance.ID, instance.Plugin)
		}

		descriptor, known := c.plugins[instance.Plugin]
		if !known {
			return normalizedProfile{}, fmt.Errorf("runtime profile: plugin %q is not in the trusted catalog", instance.Plugin)
		}
		if _, duplicate := normalized.instances[instance.ID]; duplicate {
			return normalizedProfile{}, fmt.Errorf("runtime profile: duplicate instance ID %q", instance.ID)
		}

		raw, err := rawObject(instance.Config)
		if err != nil {
			return normalizedProfile{}, fmt.Errorf("runtime profile: encode instance %q config: %w", instance.ID, err)
		}
		if err := validatePluginJSON(c.pluginSchemas[instance.Plugin], raw); err != nil {
			return normalizedProfile{}, fmt.Errorf("runtime profile: instance %q config does not match schema: %w", instance.ID, err)
		}
		if descriptor.ValidateConfig != nil {
			if err := descriptor.ValidateConfig(append(json.RawMessage(nil), raw...)); err != nil {
				return normalizedProfile{}, fmt.Errorf("runtime profile: validate instance %q config: %w", instance.ID, err)
			}
		}

		effectiveRaw := raw
		if c.checks.ResolveConfig != nil {
			effectiveRaw, err = c.checks.ResolveConfig(instance, raw)
			if err != nil {
				return normalizedProfile{}, err
			}
		}

		normalized.instances[instance.ID] = normalizedInstance{
			spec:       instance,
			descriptor: descriptor,
			raw:        effectiveRaw,
			digest:     configcodec.Digest(effectiveRaw),
		}
	}

	models := make(map[string]modelcatalog.Spec)
	modelRoutes := make(map[string]string)
	restrictions := make(map[string][]string)
	defaultModel := ""
	hasDefaults := false

	for _, instanceID := range sortedKeys(normalized.instances) {
		instance := normalized.instances[instanceID]
		description := modelcatalog.Description{}
		var err error
		if instance.descriptor.DescribeModels != nil {
			description, err = instance.descriptor.DescribeModels(instance.raw)
		}

		if err != nil {
			return normalizedProfile{}, fmt.Errorf("runtime profile: describe instance %q: %w", instanceID, err)
		}

		for route, provider := range description.Routes {
			if route == "" || provider == "" {
				return normalizedProfile{}, fmt.Errorf("runtime profile: instance %q contributed an invalid model route", instanceID)
			}
			if _, duplicate := modelRoutes[route]; duplicate {
				return normalizedProfile{}, fmt.Errorf("runtime profile: duplicate LLM route %q", route)
			}

			modelRoutes[route] = provider
			restrictions[route] = description.Restrictions
		}

		for _, model := range description.Models {
			if _, duplicate := models[model.ID]; duplicate {
				return normalizedProfile{}, fmt.Errorf("runtime profile: duplicate model ID %q", model.ID)
			}

			models[model.ID] = model
		}

		if description.Defaults != nil {
			if hasDefaults || defaultModel != "" {
				return normalizedProfile{}, errors.New("runtime profile: multiple default model declarations")
			}

			hasDefaults = true
			defaultModel = description.Defaults.Model

		}
	}
	// Mirror the Runtime's exact-model-over-wildcard binding rule. A product
	// model must resolve to its own provider instance, rather than accidentally
	// selecting another adapter with the same provider name.
	for _, model := range models {
		var exact, wildcard []string

		for route, provider := range modelRoutes {
			if provider != model.Provider {
				continue
			}

			allowed := restrictions[route]
			if len(allowed) == 0 {
				wildcard = append(wildcard, route)
				continue
			}

			for _, name := range allowed {
				if name == model.Model {
					exact = append(exact, route)
					break
				}
			}
		}

		candidates := exact
		if len(candidates) == 0 {
			candidates = wildcard
		}

		if len(candidates) != 1 || candidates[0] != model.Route {
			return normalizedProfile{}, fmt.Errorf("runtime profile: model %q has ambiguous or mismatched provider/route binding", model.ID)
		}
	}
	// A model catalog can be selected explicitly by its consumer. A default
	// is optional, but any contributed default must resolve to that catalog.
	if hasDefaults {
		if _, ok := models[defaultModel]; !ok {
			return normalizedProfile{}, fmt.Errorf("runtime profile: default model %q is not declared", defaultModel)
		}
	}

	for _, instance := range normalized.instances {
		if instance.descriptor.RequiresModels && len(models) == 0 {
			return normalizedProfile{}, fmt.Errorf("runtime profile: instance %q requires at least one declared model", instance.spec.ID)
		}
	}

	if c.checks.CheckProfile != nil {
		if err := c.checks.CheckProfile(normalized.profile); err != nil {
			return normalizedProfile{}, err
		}
	}
	// Validate protected entry points and their complete desired dependency graph
	// before restart-host planning can retain old actual providers.
	if err := validateDependencies(normalized.instances, c.completeDependencies, c.externalCapabilities); err != nil {
		return normalizedProfile{}, err
	}

	sort.Slice(normalized.profile.Instances, func(i, j int) bool { return normalized.profile.Instances[i].ID < normalized.profile.Instances[j].ID })
	profileDigest, err := normalized.profile.Digest()
	if err != nil {
		return normalizedProfile{}, fmt.Errorf("runtime profile: compute digest: %w", err)
	}

	normalized.digest = profileDigest

	return normalized, nil
}

// Public Profile instances share the root Env. Provides/Requires come from the
// registered Gordis specs; extension contributions are not Service providers.
func validateDependencies(instances map[string]normalizedInstance, complete bool, external map[string]bool) error {
	providers := map[string][]string{}

	for _, id := range sortedKeys(instances) {
		for _, capability := range instances[id].descriptor.Provides {
			providers[capability.ID] = append(providers[capability.ID], id)
		}
	}

	for service := range external {
		if len(providers[service]) != 0 {
			return fmt.Errorf("runtime profile: service %q has multiple providers (including trusted bootstrap)", service)
		}
	}

	if complete {
		for _, service := range sortedKeys(providers) {
			if len(providers[service]) != 1 {
				return fmt.Errorf("runtime profile: service %q has multiple providers", service)
			}
		}
	}

	checked := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if checked[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("runtime profile: cyclic service dependencies at instance %q", id)
		}

		visiting[id] = true

		for _, capability := range instances[id].descriptor.Requires {
			ids := providers[capability.ID]
			if external[capability.ID] && len(ids) == 0 {
				continue
			}

			if len(ids) != 1 || ids[0] == id {
				return fmt.Errorf(
					"runtime profile: instance %q requires one external provider of service %q, got %d",
					id,
					capability.ID,
					len(ids),
				)
			}
			if err := visit(ids[0]); err != nil {
				return err
			}
		}

		delete(visiting, id)
		checked[id] = true

		return nil
	}

	for _, id := range sortedKeys(instances) {
		if complete || instances[id].descriptor.PreflightDependencies {
			if err := visit(id); err != nil {
				return err
			}
		}
	}

	return nil
}

func compilePluginSchema(id string, raw json.RawMessage) (*jsonschema.Schema, error) {
	if !json.Valid(raw) {
		return nil, errors.New("schema is not valid JSON")
	}

	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}

	compiler := jsonschema.NewCompiler()
	location := "urn:qi:plugin-config:" + id
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}

	return compiler.Compile(location)
}

func validatePluginJSON(schema *jsonschema.Schema, raw json.RawMessage) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}

	return schema.Validate(value)
}

func cloneCapabilities(values []CapabilityRef) []CapabilityRef {
	if values == nil {
		return []CapabilityRef{}
	}

	return append([]CapabilityRef(nil), values...)
}

type normalizedProfile struct {
	profile   Profile
	digest    string
	instances map[string]normalizedInstance
}

type normalizedInstance struct {
	spec       InstanceSpec
	descriptor PluginDescriptor
	raw        json.RawMessage
	digest     string
}

// Normalize exposes the same pure candidate preflight to launchers.
func (c *Catalog) Normalize(profile Profile) (Profile, error) {
	normalized, err := c.validate(profile)
	return normalized.profile, err
}

// NormalizeComplete also checks every configured Service dependency, including
// contributions outside the protected runtime entry graph. It does not activate.
func (c *Catalog) NormalizeComplete(profile Profile) (Profile, error) {
	normalized, err := c.validate(profile)
	if err != nil {
		return Profile{}, err
	}
	if err := validateDependencies(normalized.instances, true, c.externalCapabilities); err != nil {
		return Profile{}, err
	}

	return normalized.profile, nil
}

// Prepared exposes validated execution configuration to management adapters.
// It is derived from the Profile and never owns runtime state.
type Prepared struct {
	Profile   Profile
	Digest    string
	Instances map[string]PreparedInstance
}

type PreparedInstance struct {
	Spec       InstanceSpec
	Descriptor PluginDescriptor
	Raw        json.RawMessage
	Digest     string
}

func (c *Catalog) Prepare(p Profile) (Prepared, error) {
	n, err := c.validate(p)
	if err != nil {
		return Prepared{}, err
	}

	result := Prepared{
		Profile:   n.profile,
		Digest:    n.digest,
		Instances: map[string]PreparedInstance{},
	}

	for id, in := range n.instances {
		result.Instances[id] = PreparedInstance{in.spec, in.descriptor, in.raw, in.digest}
	}

	return result, nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))

	for k := range values {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// Checks supplies pure capability admission at the existing preflight points.
// ResolveConfig freezes execution config after schema validation; CheckProfile
// runs after model/default checks and before dependency admission. Callbacks
// must not mutate declarations, activate plugins or call this Catalog.
type Checks struct {
	ResolveConfig func(InstanceSpec, json.RawMessage) (json.RawMessage, error)
	CheckProfile  func(Profile) error
}

// SetChecks installs the composition's checks before the Catalog is shared.
func (c *Catalog) SetChecks(checks Checks) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.checks = checks
}
