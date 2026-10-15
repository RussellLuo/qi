# Your first custom tool plugin

English | [中文](README.zh.md)

`greet` takes one required argument, `name`, and always returns `"Hello, {name}!"`. Use this minimal example to learn tool definitions, execution and plugin registration.

The model uses the builtin OpenAI-compatible plugin. You need Go 1.27 and a model service supporting tool calls, with its credentials.

## Build and run

Run these commands at the repository root. qi-build compiles this example's Extension into the application and generates the entrypoint:

```sh
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build

./dist/qi-build \
  --output ./dist/tool-plugin/qi \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --with github.com/RussellLuo/qi/builtin/examples/tool-plugin

cp builtin/examples/tool-plugin/profile.yaml dist/tool-plugin/profile.yaml
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- --help
```

The help command does not activate plugins or call a model and needs no model credentials.

See [rebuilding](../../../kernel/docs/building.md#rebuild) when building the same output again.

Follow [model setup](../../llm/openai/README.md#model-config) to edit `dist/tool-plugin/profile.yaml`, setting the vendor model name, endpoint and any required credentials. Keep the product model ID `greet-model`.

Explicitly ask the model to call the tool:

```sh
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- \
  'Please call the greet tool to say hello to Qi.'
```

The model passes `{"name":"Qi"}` and the tool returns the JSON string `"Hello, Qi!"`. The model then answers. With only `hello` as input, the model may reply directly without calling a tool.

## How it works

All implementation lives in [plugin.go](plugin.go):

- `Spec` declares `tools.RuntimeKey.Spec()` as a dependency. `Activate` obtains the Tool Runtime with `scope.Get(tools.RuntimeKey)` and calls `runtime.Bind(scope).Register` with a `tools.Definition`, declaring `greet`, its required string argument `name` and its string result. The Tool Runtime validates arguments before execution.
- The execution function decodes `name` and returns `json.Marshal("Hello, " + args.Name + "!")`. `tools.JSONTool` uses the result for both structured `Value` and model-facing text.
- Binding assigns the contribution to this Scope generation. Stopping the plugin withdraws it automatically.
- `Extension` publishes the plugin type. The [Profile](profile.yaml) selects an instance using `example/tool-plugin`.

This example belongs to the builtin Go module using Qi's public APIs.

See the [building guide](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.md) for application builds and [Tool Runtime](https://github.com/RussellLuo/qi/tree/main/kernel/core/tools/README.md) for the tool contract.
