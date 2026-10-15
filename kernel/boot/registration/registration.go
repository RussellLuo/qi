// Package registration defines trusted plugin metadata without activation.
package registration

import (
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"github.com/RussellLuo/qi/kernel/boot/profile"
)

// PluginRegistration is the single application-owned source used to construct
// both the Gordis prototype set and the runtime configuration Catalog.
// ProfileVisible stays explicit because controller-only plugins must not become
// a path for profile.Profile callers to bypass trusted package resolution.
type PluginRegistration struct {
	Descriptor     profile.PluginDescriptor
	Plugin         gordis.Plugin
	ProfileVisible bool
	Invocation     launch.Parser
	Completes      bool

	// ValidateCandidate is pure capability admission, applied only when this
	// type is declared. It must not activate or modify the candidate.
	ValidateCandidate func(profile.Profile) error
}
