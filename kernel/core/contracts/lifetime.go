// Package contracts defines the native seams shared by core runtimes and their
// adapters. Ownership and observation depend on these ports rather than a Host.
package contracts

import (
	"errors"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

var (
	ErrClosed           = errors.New("runtime is closed")
	ErrAlreadyUsed      = errors.New("prepared value was already used")
	ErrOwnerUnavailable = errors.New("contribution owner is unavailable")
)

// OwnerIdentity names one exact lifecycle generation. It is diagnostic data;
// correctness comes from Acquire and the exact-registration disposer.
type OwnerIdentity struct {
	ID         string
	Generation uint64
}

func (i OwnerIdentity) Valid() bool { return i.ID != "" && i.Generation != 0 }

// ContributionOwner is the only lifecycle dependency used by the native
// qi runtimes. Native registries and services use this port independently of
// Gordis; the Host bridge maps it to a plugin Scope.
type ContributionOwner interface {
	Identity() OwnerIdentity
	// Acquire retains this generation until the returned release is called.
	Acquire() (release func(), err error)
	// DeferCleanup binds withdrawal to the owner's lifecycle.
	DeferCleanup(name string, cleanup func()) error
}

// ObservationDispatcher is the non-blocking, fact-only side of qi
// extension dispatch. Behavior-changing middleware remains in owner-bound
// registries so a Prepared Run can freeze one exact generation set.
type ObservationDispatcher interface {
	// Session ownership is carried separately from the canonical event JSON.
	SessionAppended(protocol.SessionID, protocol.SessionEvent)
	AssistantStream(AssistantStreamObservation)
	// DriverEvent carries raw local execution facts.
	DriverEvent(DriverEvent)
}
