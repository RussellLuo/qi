# Getting started with builtin applications

English | [中文](getting-started.zh.md)

Build a command-line agent with builtin components, configure a model and complete a file read/write task without writing plugins. This guide uses the [coding-agent Profile](../examples/coding-agent/profile.yaml).

Use Go 1.27, cached or downloadable dependencies, and a tool-capable OpenAI-compatible model service. Builds and application help need no model service or credentials; prepare any credentials required by your service before executing tasks. Start the commands at the repository root.

## 1. Build the application and inspect help

Build the existing `cmd/qi`, copy the coding agent configuration and prepare its workspace:

```sh
mkdir -p dist/coding-agent/workspace
go -C builtin build -o ../dist/coding-agent/qi ./cmd/qi
cp builtin/examples/coding-agent/profile.yaml dist/coding-agent/profile.yaml
./dist/coding-agent/qi --profile dist/coding-agent/profile.yaml -- --help
```

This Profile already selects a model, file tools, JSONL persistence and the headless consumer. Help only parses configuration and argument metadata; it does not read task input, load credentials or call the model.

## 2. Configure a model service

Edit `model.config` in `dist/coding-agent/profile.yaml`:

- Replace `your-model` in `catalog[0].model` with the vendor model name.
- For a compatible service, set `endpoint` to its complete Chat Completions URL.
- Set `OPENAI_API_KEY` in the current shell if required by the service; local services without credentials can omit `api_key_env`.

Keep `coding-model` as the consumer's product model ID; it serves a different purpose from the vendor model name. Qi does not load `.env` files. See [OpenAI-compatible](../llm/openai/README.md) for configuration details.

## 3. Complete a first task

Run from the prepared workspace and explicitly ask the model to use file tools. `--json` lets you inspect tool events and the Session ID:

```sh
(cd dist/coding-agent/workspace && \
  ../qi --profile ../profile.yaml -- --json \
  'Use write_file to create hello.txt containing Hello Qi, then check it with read_file.')
```

Check the process exit code and open `dist/coding-agent/workspace/hello.txt` to verify its contents. A `phase: turn_end` event with `reason.kind: completed` means the turn completed normally; verify the actual file against your requirements. See [Headless](../cli/headless/README.md#output-and-result-interpretation) for JSON fields and output rules.

The commands change directories in a subshell, leaving your shell at the repository root. Relative file and storage paths resolve from the working directory. Continue with the same application and configuration in the [code generation, Session resume and testing exercise](../examples/coding-agent/README.md).

## 4. Continue learning

| Goal | Reading |
| --- | --- |
| Understand Profiles, models, consumers and shared services | [Builtin application concepts](concepts.md) |
| Add instances and override configuration | [Profile reference](../../kernel/docs/profiles.md) |
| Implement a minimal tool and build an extended application | [Tool plugin](../examples/tool-plugin/README.md) |
| Run the same tool in a separate process | [Process tool](../examples/tool-process/README.md) |
| Select extensions, versions and delivery output | [Kernel building guide](../../kernel/docs/building.md) |

The three public examples share builtin. See [file tools](../tools/fs/README.md), [JSONL](../session/jsonl/README.md) and [headless](../cli/headless/README.md) for capability configuration.
