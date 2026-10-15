# 独立进程中的工具插件

[English](README.md) | 中文

将 [tool-plugin](../tool-plugin/README.zh.md) 中的 `greet(name)` 工具放入独立子进程。它接收必填的 `name` 参数，返回 JSON 字符串 `"Hello, {name}!"`。

模型使用内置 OpenAI-compatible 插件。需要 Go 1.27、支持工具调用的模型服务及其凭据。

## 构建并运行

以下命令在仓库根目录执行。qi-build 自动生成宿主入口，`greet` 子程序单独构建：

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

以上帮助命令不激活插件或调用模型，无需模型凭据。

重复构建同一路径时，见[构建指南](../../../kernel/docs/building.zh.md#rebuild)。

将 `qi`、`greet` 和 `profile.yaml` 放在同一交付目录，运行时不需要 Go。

按[模型配置说明](../../llm/openai/README.zh.md#model-config)编辑 `dist/tool-process/profile.yaml`，设置供应商模型名、endpoint 和所需凭据；产品模型 ID `greet-model` 可以保留。

明确要求模型调用工具：

```sh
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- \
  '请调用 greet 工具，向 Qi 打招呼。'
```

模型传入 `{"name":"Qi"}`，子程序返回 `"Hello, Qi!"`，再由模型回答。只输入问候语时，模型可能直接回复而不调用工具。

## 如何实现

实现分为两部分：

- [plugin.go](plugin.go) 定义普通 Tool Plugin。`Spec` 声明 Tool Runtime 依赖，`Activate` 通过 `runtime.Bind(scope).Register` 注册 `greet`，与进程内示例相同。
- `Extension` 用 `toolprocess.Registration` 将该 Plugin 包装为进程适配器，[Profile](profile.yaml) 通过 `example/tool-process` 选择实例。
- `toolprocess.Options{Command: "greet"}` 在 `qi` 可执行文件旁查找子程序，不依赖工作目录或 PATH；交付目录可以整体移动。
- [cmd/greet/main.go](cmd/greet/main.go) 将 Plugin 和标准 IO 交给 `toolprocess.ServePlugin`，在子进程中运行插件。

子程序默认获得空环境，模型凭据留在宿主。本适配器支持只依赖 Tool Runtime、返回 JSON 值的工具；Session cwd 和 Agent/Turn 身份不会传给子程序。

示例属于 builtin Go module，通过公开 API 依赖 Qi。

应用构建细节见[构建指南](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.zh.md)，工具契约见 [Tool Runtime](https://github.com/RussellLuo/qi/tree/main/kernel/core/tools/README.zh.md)。
