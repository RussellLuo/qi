# 独立进程中的工具插件

[English](README.md) | 中文

将 [tool-plugin](../tool-plugin/README.zh.md) 中的 `greet` 工具放到独立进程运行。宿主通过 Profile 加载工具程序；新增同契约的工具程序无需重新构建宿主。

模型使用内置 OpenAI-compatible 插件。需要 Go 1.27、支持工具调用的模型服务及其凭据。

## 构建并运行

以下命令在仓库根目录执行，分别构建宿主和工具程序。默认宿主已包含进程加载器：

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

以上帮助命令不激活插件或调用模型，无需模型凭据。

重复构建同一路径时，见[构建指南](../../../kernel/docs/building.zh.md#rebuild)。

按[模型配置说明](../../llm/openai/README.zh.md#model-config)编辑 `dist/tool-process/profile.yaml`，设置供应商模型名、endpoint 和所需凭据；产品模型 ID `greet-model` 可以保留。

明确要求模型调用工具：

```sh
./dist/tool-process/qi --profile dist/tool-process/profile.yaml -- \
  '请调用 greet 工具，向 Qi 打招呼。'
```

模型传入 `{"name":"Qi"}`，子程序返回 JSON 字符串 `"Hello, Qi!"`，随后由模型回答。只输入 `hello` 时，模型可能直接回复而不调用工具。

可整体移动 `dist/tool-process/`；运行机器不需要 Go。

## 如何实现

- [共享 Plugin](../tool-plugin/plugin/plugin.go) 是进程内示例和本示例唯一的业务实现，通过 `runtime.Bind(scope).Register` 注册 `greet`。
- [cmd/greet/main.go](cmd/greet/main.go) 直接导入同一个业务包，调用 `process.Main(new(greet.Plugin))`；不导入本地 Extension 或 Host 启动装配。
- [Profile](profile.yaml) 通过程序路径选择工具，`config` 直接填写业务配置：

```yaml
- id: greet
  plugin: ./packages/greet/greet
```

两个示例保留各自的构建和交付方式，业务源码只有一份。两个 Profile 的实例 ID、业务配置和消费者相同，仅 `greet.plugin` 从 `example/tool-plugin` 改为程序路径。本例无业务配置；本地部署须先将 Extension 编入 Host，进程部署须提供工具程序。

相对路径以所选 Profile 文件目录为基准，因此从其他工作目录运行或整体搬迁后仍可使用。

示例属于 builtin Go module，通过公开 API 依赖 Qi。进程契约与装配细节见 [process 包说明](../../tools/process/README.zh.md)。
