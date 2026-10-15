# Public domain libraries

[中文](README.zh.md) · [Architecture](../docs/architecture.md)

| Package | Owns |
| --- | --- |
| [protocol](protocol) | Message, event, stream and frame wire definitions/codecs; direct wire format round-trip and validation tests |
| [contracts](contracts) | Shared contribution owner, activity/Inbox/Driver/observation ports and data, plus ChunkStream |
| [session](session) | Session history, projections, snapshots, inbox, recovery, reservations and Runtime |
| [agent](agent) | Factory, Registry, Handle, initialization, observation, cancellation and activity scheduling |
| [agentloop](agentloop) | Default Driver, AttachedRun, request state and Step/Turn settlement |
| [llm](llm) | Adapter registration, product-model resolution, prepared calls and stream accumulation |
| [modelcatalog](modelcatalog) | Product model metadata |
| [prompt](prompt) | Prompt contributions and prepared snapshots |
| [tools](tools) | Tool contributions, prepared calls, middleware, execution identity and typed abort errors |
| [agentmiddleware](agentmiddleware) | Agent middleware snapshots and prepared steps |
| [runprep](runprep) | Explicit execution dependencies and prepared runtime generations |

Capability types have one public owner: `tools.Definition/Schema/Execution`, `llm.Adapter/Request`, `prompt.Context/Section/Snapshot` and `agentmiddleware.Middleware/StepInput`. LLM and Prompt depend on Tool types; Agent middleware depends on Prompt and LLM. `modelcatalog.Spec.LLMConfig()` returns `protocol.CallConfig`.

Tools, LLM, Prompt and Agent middleware own `RuntimeKey` and instance `Runtime.Bind(scope)`; Agent and Session own their service Keys. These packages directly depend on Gordis. AgentLoop, runprep and concrete business implementations may depend on it transitively through capability types, but do not directly import it. Core runtimes never import boot, concrete plugins or gordisbridge. The production closures of contracts, protocol, modelcatalog and the private registry/observation helpers remain free of Gordis and capabilities. `internal/gordisowner` only adapts Scopes to the shared owner contract; adapters use public APIs rather than private registries.

Contribution plugins declare the capability Key in `Requires`, get the Runtime once, then bind their exact generation:

```go
runtime, err := scope.Get(tools.RuntimeKey)
if err != nil {
    return err
}
_, err = runtime.Bind(scope).Register(definition)
return err
```

`Bind` captures an owner without acquiring a lease or looking up another service. Operations delegate to the original owner-based Runtime APIs; Snapshots/Prepared calls acquire and release leases. Scope stop withdraws contributions, while plugin-owned transports and files close after drain. Invalid Scopes and nil/closed Runtimes return operation errors. See [Tool Binding](tools/README.md), [LLM Binding](llm/README.md), [Prompt Binding](prompt/README.md) and [middleware Binding](agentmiddleware/README.md).

Ordinary callers construct `agent.NewFactory` from explicit `FactoryOptions`. Framework bindings may use `agent.NewBoundFactory(options, acquire)` to hold fixed Ready leases through Handle publication. Its acquire callback must not perform I/O, invoke business callbacks or reenter the Factory while its lock is held. Handle disposal preserves persistent history and releases only resources it owns.

Each package owns its `_test.go` helpers and behavior tests. Protocol and request serialization use inline Go test tables. Session projection, stream assembly, attempt emission and JSONL recovery define their case inputs and expected results directly in `_test.go`. Tests do not depend on historical source snapshots, hash manifests or neighboring testdata.

```sh
# From the repository root
go -C kernel test ./core/...
```

[Gordis integration](gordisbridge/README.md) is a separate public framework adapter, with its own tests; domain packages never import it.
