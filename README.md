# Qi

English | [中文](README.zh.md)

Qi (/tʃiː/) is a minimal, plugin-driven, extensible agent harness. It brings model calls, tool execution, session history and plugin lifecycles together to build agent applications.

Run a command-line agent with builtin components, or write your own model, tool or observer plugins. Builds select the capabilities available to an application; a **Profile** selects and configures the components used at runtime.

## Project structure

| Directory | Responsibility |
| --- | --- |
| [kernel](kernel/README.md) | Core runtimes, public APIs, Profile startup and the `qi-build` tool. |
| [builtin](builtin/README.md) | Builtin command-line application, model access, file tools, session persistence and complete examples. |

Builtin uses public kernel APIs. Kernel does not depend on builtin. Cross-process plugins use the independent [Gordis RPC module](https://github.com/RussellLuo/gordis/tree/main/rpc).

## Start here

- [Getting started](builtin/docs/getting-started.md): build the builtin application, configure a model and run a first task.
- [Builtin application concepts](builtin/docs/concepts.md): understand the components of a command-line agent.
- [Core concepts](kernel/docs/concepts.md): understand Agents, Sessions, plugins and their relationships.
- [Profile reference](kernel/docs/profiles.md): select plugins and configure instances.
- [Application building](kernel/docs/building.md): select extensions and versions to deliver an application.

## License

[MIT](LICENSE)
