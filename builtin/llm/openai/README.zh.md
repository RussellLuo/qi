# 内置 OpenAI-compatible 模型插件

[English](README.md)

`cmd/qi` 和 qi-build 生成的应用已经包含 `qi/llm/openai-compatible`。通过显式
Profile 选择模型实例和 `qi/cli/headless` 即可运行，无需额外 `--with`。`base`
Bundle 提供共享服务，不挂载模型实例。`Extension()` 不读取凭据或建立连接。

在仓库根目录执行：

```sh
mkdir -p dist/openai
go -C builtin build -o ../dist/openai/qi ./cmd/qi
cat > dist/openai/profile.yaml <<'YAML'
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: model
    plugin: qi/llm/openai-compatible
    config:
      endpoint: https://api.openai.com/v1/chat/completions
      api_key_env: OPENAI_API_KEY
      timeout_seconds: 120
      catalog: [{id: chat, model: your-model}]
  - id: headless
    plugin: qi/cli/headless
    config:
      model: chat
YAML
# 将 dist/openai/profile.yaml 的 model.config.catalog[0].model 改为服务提供的模型。
# 运行前在当前 shell 中准备 OPENAI_API_KEY。
./dist/openai/qi --profile dist/openai/profile.yaml -- 'Hello' 'world'
```

<a id="model-config"></a>

## 配置模型服务

在模型实例的 `config` 中：

- 将 `catalog[].model` 改为供应商模型名；产品 `id` 与 headless `config.model` 对应，可以保留。
- 将 `endpoint` 设置为带路径的完整 Chat Completions URL；使用兼容服务时修改为该服务地址。
- 在当前 shell 设置 `api_key_env` 指定的变量；无需凭据的服务可省略该字段。Qi 不加载 `.env`。

示例使用 `OPENAI_API_KEY`。模型与消费者实例放在同一份 Profile 中，声明与覆盖规则见 [Profile 参考](../../../kernel/docs/profiles.zh.md)。

## 配置参考

| Profile 配置 | 行为 |
| --- | --- |
| `endpoint` | 必填，完整的 HTTP(S) Chat Completions URL。 |
| `provider`、`route` | 默认 `openai`、`chat`；消费者选择 catalog 中的产品模型 ID。 |
| `api_key_env` | 仅在激活时读取指定变量；指定变量为空时报错。 |
| `headers` | 附加 HTTP header；Authorization 使用凭据配置。 |
| `defaults` | 标准调用默认值，例如 `{maxTokens: 1024}`、`{temperature: 0.2}`。 |
| `max_tokens_parameter` | 默认 `max_completion_tokens`；旧兼容服务可选择 `max_tokens`，不根据模型名称猜测。 |
| `reasoning_history` | 默认 `omit`；后端要求独立推理历史字段时选择 `reasoning_content`。推理不会拼入可见正文。 |
| `timeout_seconds` | 限制单次流式请求的完整时长；零或省略为 120 秒。 |
| `parameters` | 供应商专属 JSON 参数；禁止覆盖标准字段及两个 token 上限字段。 |
| `catalog`、`context_window` | 可选静态模型目录和上下文元数据；不远端发现模型。 |

协议范围为单个 Chat Completions choice、文本、function Tool Calls、Tool 结果、
`reasoning_content` 增量和 usage。拒绝回答产生 `REFUSAL` 分类错误；
`content_filter` 产生 `CONTENT_FILTER`，两者都不会变成空的成功答案。取消和超时
停止 HTTP 请求，已准入工作的持久化结算仍由 AgentLoop 负责。Responses API、
图像/音频和多个 choices 不属于此适配器的范围。

## Go API 与生命周期

Adapter 注册属于模型提供者 Scope。程序注入的 `HTTPClient` 由调用者拥有，Qi
不会修改或关闭它。重试由 Agent middleware 决定，插件不会隐式重试。程序也可以
通过 `Plugin.APIKey` 注入凭据，不必把密钥写入 Profile。

[OpenAI Docs](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
说明当前 token 上限字段；[流协议](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events)
说明 refusal 与结束字段。其他兼容服务可能存在差异。

[Adapter 单元测试](adapter_test.go)通过注入 transport 验证编码、SSE、拒绝、期限（含未结束的响应体）与推理历史。
保留一项 [Profile 测试](profile_test.go)，验证 provider → headless → AgentLoop → 工具继续的完整链路、已提交 Session 事实与清理；它使用内存 HTTP transport，无需监听，也不重复执行 TCP 变体。可选 `TestRealAPISmoke` 使用 `QI_OPENAI_ENDPOINT`、`QI_OPENAI_MODEL`
及按需提供的 `QI_OPENAI_API_KEY` 验证真实服务的文本回答。本地测试不证明所有
供应商兼容性或真实服务的工具调用。
