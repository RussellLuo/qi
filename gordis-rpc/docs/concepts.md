# Core concepts

English | [中文](concepts.zh.md) · [README](../README.md)

Consider Greeter: a business Plugin implements `Greeter`, and a consumer calls it through `scope.Get(contract.Key)`. Moving the provider into a child process gives the consumer a proxy implementing the same interface. Contracts and business Plugins keep using ordinary Gordis APIs.

## From interface to process

| Concept | Question it answers | Greeter equivalent |
| --- | --- | --- |
| Contract | What service can I call? | The `Greeter` interface, typed Key and contract version |
| Binding | How does that contract cross a process boundary? | Typed client, method handlers and version bound to the Key |
| Boundary | Which services cross the program boundary? | Child Imports and Exports |
| Unit | What runs and how is it assembled inside the child? | Plugins, instances, configuration target, imports and sourced exports |
| Session | Which running instance does a call belong to? | Session ID, Host instance/generation and diagnostics |

Contracts and business Plugins need no RPC imports. Generated or handwritten Bindings live in adapter packages, referenced explicitly by Host and child assembly code. See the [basic layout](../examples/basic/README.md).

## Imports and Exports are relative to the child

- **Imports** are services the child needs from the Host, such as logging or callbacks. The Host resolves them through Scope.Get and exposes a callback channel.
- **Exports** are services the child provides to the Host, such as Greeter. The Host obtains typed clients for them.

Consequently, `NewProcess` maps Imports to the Host Plugin's Requires and Exports to Provides. Both sides check the complete set of contract names, versions and directions, even if some services are never called.

Without Imports, no callback server is needed. Units with no exports, only imports, or no services are valid too.

## A Unit is a closed static assembly

`Unit.Plugins` registers ordinary Plugins; `Instances` selects running instances. `ConfigTarget` selects the instance receiving Host business configuration. Each Export names its source `Instance` and Binding.

Every Requires must be satisfied by a Unit instance or an Import. Each service name has a single provider; multiple-provider View routing within a Unit is not supported. Gordis owns dependency ordering, cycle detection, activation and rollback.

A Unit may contain several Plugins, instances and exports; it need not correspond to one business Plugin.

## Program identity and service contracts have separate names

`Identity.ID` identifies the program, `Identity.Version` its version, and `Identity.Protocol` the substrate protocol `rpc.Protocol`. A service Key's name and Binding.Version identify a contract and its compatibility version.

One program can provide several contracts, so its ID need not match a contract ID. `ProcessOptions.PluginID` separately names the loader registered with the Host. Basic declares program identity and each side's boundary directly in the [Host](../examples/basic/host/cmd/main.go) and [plugin program](../examples/basic/plugin/cmd/main.go) entries. Ordinary interfaces, Keys and contract versions live in [contract](../examples/basic/contract/service.go).

A nonzero Host Identity pins the expected identity. A zero value permits discovery through Describe after startup; the Boundary must still match exactly.

## Configuration and running instances

`NewProcess` constructs a registrable Plugin prototype without starting a program. Each `Spec.New` creates independent state; activation starts its own child process and Session.

Instance Config remains a business JSON object, such as `{"prefix":"Welcome"}`. The default `ForwardConfig` copies it to the child's ConfigTarget, where the ordinary Plugin decodes, validates and applies defaults. A Unit without ConfigTarget explicitly uses `NoConfig`: omitted configuration or an empty object sends nil, and business fields fail before startup.

Program path, arguments, environment, identity, Boundary and Limits come from ProcessOptions, separately from business configuration.

Continue with [architecture and lifecycle](architecture.md) for readiness and failure handling.
