package registration

import (
	"fmt"

	"github.com/RussellLuo/gordis"
	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	launchcontract "github.com/RussellLuo/qi/kernel/boot/launch"
)

type Candidate struct {
	Profile           appprofile.Profile
	Source            *appprofile.ResolvedProfile
	Bundles           appprofile.BundleCatalog
	Registrations     []PluginRegistration
	Plugins           []gordis.Plugin
	Catalog           *appprofile.Catalog
	Mounts            []gordis.InstanceSpec
	ConsumerID        string
	Parser            launchcontract.Parser
	CompletionOwnerID string
}

func Plan(registrations []PluginRegistration, p appprofile.Profile) (*Candidate, error) {
	_, descriptors, err := ResolveRegistrations(registrations)
	if err != nil {
		return nil, err
	}

	catalog, err := appprofile.NewCatalog(descriptors)
	if err != nil {
		return nil, err
	}

	return PlanWithCatalog(registrations, p, catalog)
}

// PlanWithCatalog accepts an explicit catalog decorated by the composition owner.
func PlanWithCatalog(registrations []PluginRegistration, p appprofile.Profile, catalog *appprofile.Catalog) (*Candidate, error) {
	for _, in := range p.Instances {
		if in.ID == launchcontract.CarrierID {
			return nil, fmt.Errorf("instance id %q is reserved for launch context", in.ID)
		}
	}

	plugins, _, err := ResolveRegistrations(registrations)
	if err != nil {
		return nil, err
	}

	catalog.ProvideExternalCapabilities(appprofile.CapabilityRef{ID: launchcontract.LaunchContextKey.Spec().Name()})
	catalog.RequireCompleteDependencies()
	normalized, mounts, err := catalog.MountPlan(p)
	if err != nil {
		return nil, err
	}

	consumer, parser, owner, err := InvocationCapabilities(registrations, normalized)
	if err != nil {
		return nil, err
	}

	return &Candidate{
		Profile:           normalized,
		Registrations:     registrations,
		Plugins:           plugins,
		Catalog:           catalog,
		Mounts:            mounts,
		ConsumerID:        consumer,
		Parser:            parser,
		CompletionOwnerID: owner,
	}, nil
}

func InvocationCapabilities(registrations []PluginRegistration, profile appprofile.Profile) (string, launchcontract.Parser, string, error) {
	byID := map[string]PluginRegistration{}

	for _, registration := range registrations {
		byID[registration.Plugin.Spec().ID] = registration
	}

	consumer, owner := "", ""
	var parser launchcontract.Parser

	for _, instance := range profile.Instances {
		registration := byID[instance.Plugin]
		if registration.Invocation != nil {
			if consumer != "" {
				return "", nil, "", fmt.Errorf("multiple CLI argument consumers: %s and %s", consumer, instance.ID)
			}

			consumer, parser = instance.ID, registration.Invocation
		}

		if registration.Completes {
			if owner != "" {
				return "", nil, "", fmt.Errorf("multiple completion owners: %s and %s", owner, instance.ID)
			}

			owner = instance.ID
		}

		if registration.ValidateCandidate != nil {
			if err := registration.ValidateCandidate(profile.Clone()); err != nil {
				return "", nil, "", fmt.Errorf("capability %s: %w", instance.ID, err)
			}
		}
	}

	return consumer, parser, owner, nil
}

// InvocationParser only selects trusted pure metadata for file-based help.
// It does not mount, inspect artifacts or execute capability admission.
func InvocationParser(registrations []PluginRegistration, profile appprofile.Profile) (launchcontract.Parser, error) {
	byID := map[string]PluginRegistration{}

	for _, registration := range registrations {
		byID[registration.Plugin.Spec().ID] = registration
	}

	var parser launchcontract.Parser
	var consumers []string

	for _, instance := range profile.Instances {
		registration, ok := byID[instance.Plugin]
		if !ok || !registration.ProfileVisible {
			return nil, fmt.Errorf("plugin %q is not in the trusted catalog", instance.Plugin)
		}
		if registration.Invocation != nil {
			consumers = append(consumers, instance.ID)
			parser = registration.Invocation
		}
	}

	if len(consumers) > 1 {
		return nil, fmt.Errorf("multiple CLI argument consumers: %v", consumers)
	}

	return parser, nil
}
