# Tools in a separate process

English | [中文](README.zh.md)

Run the `greet` tool from [tool-plugin](../tool-plugin/README.md) in a separate process. The host loads the tool program through a Profile; adding another tool program with the same contract requires no host rebuild.

The model uses the builtin OpenAI-compatible plugin. You need Go 1.27 and a model service supporting tool calls, with its credentials.

## Build and run

Run these commands at the repository root to build the host and tool program separately. The default host includes the process loader:

```sh
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build

./dist/qi-build \
  --output ./dist/tool-process/qi \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --replace github.com/RussellLuo/gordis-rpc=./gordis-rpc

mkdir -p dist/tool-process/packages/greet
go -C builtin build -o ../dist/tool-process/packages/greet/greet ./examples/tool-process/cmd/greet
cp builtin/examples/tool-process/profile.yaml dist/tool-process/profile.yaml
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- --help
```

The help command does not activate plugins or call a model and needs no model credentials.

See [rebuilding](../../../kernel/docs/building.md#rebuild) when building the same output again.

Follow [model setup](../../llm/openai/README.md#model-config) to edit `dist/tool-process/profile.yaml`, setting the vendor model name, endpoint and any required credentials. Keep the product model ID `greet-model`.

Explicitly ask the model to call the tool:

```sh
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- \
  'Please call the greet tool to say hello to Qi.'
```

The model passes `{"name":"Qi"}` and the child program returns the JSON string `"Hello, Qi!"`. The model then answers. With only `hello` as input, the model may reply directly without calling a tool.

You can move the entire `dist/tool-process/` directory; Go is not needed at runtime.

## How it works

- The [shared Plugin](../tool-plugin/plugin/plugin.go) is the single business implementation used by both examples, registering `greet` through `runtime.Bind(scope).Register`.
- [cmd/greet/main.go](cmd/greet/main.go) imports that same business package and calls `process.Main(new(greet.Plugin))`, without importing the local Extension or Host startup assembly.
- The [Profile](profile.yaml) selects the tool by program path, with business configuration directly under `config`:

```yaml
- id: greet
  plugin: ./packages/greet/greet
```

The two examples retain separate build and delivery instructions and share one business implementation. Their Profiles keep the same instance IDs, business configuration and consumers; only `greet.plugin` changes from `example/tool-plugin` to the program path. This example has no business configuration. Local deployment requires the Extension compiled into the Host; process deployment requires the tool executable.

Relative paths resolve from the selected Profile file's directory, so they keep working from another working directory or after moving the delivery directory.

This example belongs to the builtin Go module using Qi's public APIs. See the [process package guide (Chinese)](../../tools/process/README.zh.md) for process contracts and assembly details.
