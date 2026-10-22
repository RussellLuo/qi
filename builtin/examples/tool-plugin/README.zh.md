# 第一个自定义工具插件

[English](README.md) | 中文

`greet` 只接收必填的 `name` 参数，固定返回 `"Hello, {name}!"`。用这个最小例子学习工具定义、执行和插件注册。

模型使用内置 OpenAI-compatible 插件。需要 Go 1.27、支持工具调用的模型服务及其凭据。

## 构建并运行

以下命令在仓库根目录执行。qi-build 将本示例的 Extension 编入应用，自动生成入口：

```sh
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build

./dist/qi-build \
  --output ./dist/tool-plugin/qi \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --replace github.com/RussellLuo/gordis-rpc=./gordis-rpc \
  --with github.com/RussellLuo/qi/builtin/examples/tool-plugin

cp builtin/examples/tool-plugin/profile.yaml dist/tool-plugin/profile.yaml
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- --help
```

以上帮助命令不激活插件或调用模型，无需模型凭据。

重复构建同一路径时，见[构建指南](../../../kernel/docs/building.zh.md#rebuild)。

按[模型配置说明](../../llm/openai/README.zh.md#model-config)编辑 `dist/tool-plugin/profile.yaml`，设置供应商模型名、endpoint 和所需凭据；产品模型 ID `greet-model` 可以保留。

明确要求模型调用工具：

```sh
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- \
  '请调用 greet 工具，向 Qi 打招呼。'
```

模型传入 `{"name":"Qi"}`，工具返回 JSON 字符串 `"Hello, Qi!"`，随后由模型回答。只输入 `hello` 时，模型可能直接回复而不调用工具。

## 如何实现

业务实现位于 [plugin/plugin.go](plugin/plugin.go)，由本示例和进程示例共同导入：

- `Spec` 声明 `tools.RuntimeKey.Spec()` 依赖。`Activate` 通过 `scope.Get(tools.RuntimeKey)` 取得 Tool Runtime，再向 `runtime.Bind(scope).Register` 传入 `tools.Definition`，声明 `greet`、必填的字符串参数 `name` 和字符串结果。Tool Runtime 在执行前验证参数。
- 执行函数解析 `name`，返回 `json.Marshal("Hello, " + args.Name + "!")`。`tools.JSONTool` 将结果同时用于结构化 `Value` 和模型可见文本。
- Binding 将工具贡献归于当前 Scope generation；插件停止时自动撤销。
- 外层 [extension.go](extension.go) 的 `Extension` 发布共享 Plugin 类型，[Profile](profile.yaml) 通过 `example/tool-plugin` 选择实例。

示例属于 builtin Go module，通过公开 API 依赖 Qi。

应用构建细节见[构建指南](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.zh.md)，工具契约见 [Tool Runtime](https://github.com/RussellLuo/qi/tree/main/kernel/core/tools/README.zh.md)。
