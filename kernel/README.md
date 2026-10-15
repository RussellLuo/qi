# Qi kernel

English | [中文](README.zh.md)

Qi's core libraries and application assembly tools provide Agent execution, session management, model calls and tool execution.

To assemble an application, compose extensions that describe the available plugin types, then use a Profile to select and configure instances. At startup, kernel validates configuration and service dependencies, activates the plugins and manages their lifecycles.

## Foundational plugins

The [kernel Bundle extension](bundle/README.md) brings together these plugin types:

| Plugin ID | Purpose |
| --- | --- |
| [gordis/events](bundle/README.md) | Event bus provided by [Gordis](https://github.com/RussellLuo/gordis). |
| [qi/session](core/session/plugin/README.md) | Manage Sessions and their history. |
| [qi/llm](core/llm/plugin/README.md) | Register model adapters, select models and prepare calls. |
| [qi/prompt](core/prompt/plugin/README.md) | Register and assemble prompt content. |
| [qi/tools](core/tools/plugin/README.md) | Register, validate and execute tools. |
| [qi/agent-loop](core/agentloop/plugin/README.md) | Create and manage Agents, coordinating model and tool execution. |

`bundle.Extension()` declares these plugin types and provides the `base` Bundle. A Profile selects `bundles: [base]` to activate this set of foundational service instances.

## Directory guide

| Entry | Purpose |
| --- | --- |
| [core](core/README.md) | Agent execution, session history, and shared model, prompt and tool services. |
| [boot](boot/README.md) | Register extensions, compose an application and start plugin instances from a Profile. |
| [bundle](bundle/README.md) | Foundational service registrations and the reusable `base` instance configuration. |
| [qi-build](cmd/qi-build) | Select extensions and their versions to generate an application binary. |

## Read more

- [Core concepts](docs/concepts.md): understand Agents, Sessions, plugins and Profiles.
- [Architecture](docs/architecture.md): follow execution and resource lifecycles.
- [Profile reference](docs/profiles.md): select plugins and configure instances.
- [Application building](docs/building.md): combine extensions and deliver an application.
