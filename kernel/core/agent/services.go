package agent

import "github.com/RussellLuo/gordis"

// FactoryProvider supplies factories bound to an exact caller generation.
// Callers declare FactoryKey as a dependency and bind the service to their Scope.
// Create, Attach and Resume require both caller and provider to be Ready.
type FactoryProvider interface {
	Bind(*gordis.Scope) (Factory, error)
}

var (
	RegistryKey    = gordis.NewKey[Registry]("qi/agent-registry")
	InitializerKey = gordis.NewKey[InitializerRegistry]("qi/agent-initializer")
	ObserverKey    = gordis.NewKey[AgentEvents]("qi/agent-observer")
	FactoryKey     = gordis.NewKey[FactoryProvider]("qi/agent-factory")
)
