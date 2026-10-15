# A Tool Plugin in a separate process

English | [中文](README.zh.md)

Run the same `greet(name)` tool as [tool-plugin](../tool-plugin/README.md) in a child process. It takes a required `name` argument and returns the JSON string `"Hello, {name}!"`.

The model uses the builtin OpenAI-compatible plugin. You need Go 1.27 and a model service supporting tool calls, with its credentials.

## Build and run

Run these commands at the repository root. qi-build generates the host entrypoint; build the `greet` child separately:

```sh
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build

./dist/qi-build \
  --output ./dist/tool-process/qi \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --with github.com/RussellLuo/qi/builtin/examples/tool-process

go -C builtin build -o ../dist/tool-process/greet ./examples/tool-process/cmd/greet
cp builtin/examples/tool-process/profile.yaml dist/tool-process/profile.yaml
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- --help
```

The help command does not activate plugins or call a model and needs no model credentials.

See [rebuilding](../../../kernel/docs/building.md#rebuild) when building the same output again.

Keep `qi`, `greet` and `profile.yaml` in one deployment directory. Go is not needed at runtime.

Follow [model setup](../../llm/openai/README.md#model-config) to edit `dist/tool-process/profile.yaml`, setting the vendor model name, endpoint and any required credentials. Keep the product model ID `greet-model`.

Explicitly ask the model to call the tool:

```sh
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- \
  'Please call the greet tool to say hello to Qi.'
```

The model supplies `{"name":"Qi"}`, the child returns `"Hello, Qi!"`, and the model replies. A plain greeting may produce a response without a tool call.

## How it works

The implementation has two parts:

- [plugin.go](plugin.go) defines an ordinary Tool Plugin. `Spec` declares the Tool Runtime dependency; `Activate` registers `greet` through `runtime.Bind(scope).Register`, just as in the in-process example.
- `Extension` uses `toolprocess.Registration` to wrap that Plugin in a process adapter. The [Profile](profile.yaml) selects it using `example/tool-process`.
- `toolprocess.Options{Command: "greet"}` locates the child beside the `qi` executable, independent of the working directory or PATH. You can move the deployment directory as a whole.
- [cmd/greet/main.go](cmd/greet/main.go) passes the Plugin and standard IO to `toolprocess.ServePlugin`, which runs it in the child process.

The child receives an empty environment by default; model credentials stay in the host. This adapter supports tools that require only Tool Runtime and return JSON values. Session cwd and Agent/Turn identity are not passed to the child.

This example belongs to the builtin Go module using Qi's public APIs.

See the [building guide](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.md) for application builds and [Tool Runtime](https://github.com/RussellLuo/qi/tree/main/kernel/core/tools/README.md) for the tool contract.
