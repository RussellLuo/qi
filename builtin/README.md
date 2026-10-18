# Qi builtin

English | [中文](README.zh.md)

Qi's builtin command-line application and capability collection provide model access, file operations, Session persistence and command-line task execution. A Profile selects and configures plugin instances.

## Capability guide

| Plugin ID | Purpose |
| --- | --- |
| [qi/llm/openai-compatible](llm/openai/README.md) | Access OpenAI-compatible models. |
| [qi/session/persistence-jsonl](session/jsonl/README.md) | Save and resume JSONL Session histories. |
| [qi/tools/fs](tools/fs/README.md) | Read and write files. |
| qi/tools/process | Load tool programs configured in a Profile; included in the default Bundle. |
| [qi/cli/headless](cli/headless/README.md) | Run command-line tasks and output text or JSON results. |

## Start here

- [Getting started](docs/getting-started.md): build the application, configure a model and run a first task.
- [Application concepts](docs/concepts.md): understand the components in a Profile.
- [Profile reference](../kernel/docs/profiles.md): add instances, override configuration and control activation.

## Examples

| Example | Goal |
| --- | --- |
| [Coding agent](examples/coding-agent/README.md) | Generate code, continue a Session and verify the result. |
| [Tool plugin](examples/tool-plugin/README.md) | Implement and register a minimal `greet` tool. |
| [Process tool](examples/tool-process/README.md) | Run the same tool in a separate process. |
