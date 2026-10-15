# Gordis integration

[中文](README.zh.md) · [Core map](../README.md)

This public package owns the low-level `ScopeOwner`/`NewScopeOwner` API, Agent Factory lifecycle implementation, observation Topics and `EventDispatcher`. Capability packages own their types, service Keys and Runtime Bindings. Core runtimes do not import this bridge.

An Agent provider calls `NewAgentFactoryService(providerScope, agent.FactoryOptions)` and publishes the `agent.FactoryProvider` result with `agent.FactoryKey`. Consumers declare that dependency, get the provider with `callerScope.Get(agent.FactoryKey)` and call `provider.Bind(callerScope)`. The bound Factory owns Handles under the exact caller generation and holds fixed Ready leases through publication; it borrows Registry/Store/Driver and preserves provider stop, caller stop and drain ordering. Nil/invalid callers return `agent.ErrClosed`.

Tool, LLM, Prompt and Agent middleware contributors use the capability's Runtime Key and `runtime.Bind(scope)` without importing bridge. Explicit-owner integrations can still pass `NewScopeOwner(scope)` to the original Runtime registration APIs. Disposers withdraw contributions; snapshot leases drain before plugin-owned resources close. Event dispatch is passive and does not own execution.

Tests and local helpers live in their owning packages. From the repository root, `go -C kernel test -race ./core/gordisbridge ./core/internal/gordisowner` checks Factory publication/admission, owner generation/drain and raw local observations. Capability Binding tests cover registration rollback, leased snapshots and stale disposers.
