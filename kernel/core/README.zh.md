# 公开领域库

[English](README.md) · [架构](../docs/architecture.zh.md)

| 包 | 职责 |
| --- | --- |
| [protocol](protocol) | 消息、事实、stream/frame 定义与 codec；直接验证 wire 格式往返与非法输入 |
| [contracts](contracts) | 公共贡献 owner、活动/Inbox/Driver/观察端口与数据，以及 ChunkStream |
| [session](session) | 历史、投影、快照、Inbox、恢复、预约和 Runtime |
| [agent](agent) | Factory、Registry、Handle、初始化、观察、取消与活动调度 |
| [agentloop](agentloop) | 默认 Driver、AttachedRun、请求状态和 Step/Turn 结算 |
| [llm](llm) | Adapter 注册、产品模型解析、准备调用和 stream 累积 |
| [modelcatalog](modelcatalog) | 产品模型元数据 |
| [prompt](prompt) | Prompt 贡献与准备快照 |
| [tools](tools) | Tool 贡献、准备调用、middleware、执行身份与可命名的 abort 错误 |
| [agentmiddleware](agentmiddleware) | Agent middleware 快照与 PreparedStep |
| [runprep](runprep) | 显式执行依赖与准备后的 Runtime generation |

能力类型有唯一公开归属：`tools.Definition/Schema/Execution`、`llm.Adapter/Request`、`prompt.Context/Section/Snapshot` 和 `agentmiddleware.Middleware/StepInput`。LLM 与 Prompt 使用 Tool 类型；Agent middleware 使用 Prompt 与 LLM 类型。`modelcatalog.Spec.LLMConfig()` 直接返回 `protocol.CallConfig`。

Tool、LLM、Prompt 和 Agent middleware 拥有 `RuntimeKey` 与实例 `Runtime.Bind(scope)`；Agent、Session 拥有各自服务 Key。这六个能力包直接依赖 Gordis。AgentLoop、runprep 和具体业务实现可通过能力类型间接依赖 Gordis，但不直接导入它。核心 Runtime 不导入 boot、具体插件或 gordisbridge。contracts、protocol、modelcatalog 与私有 registry/observation helper 的生产闭包仍不依赖 Gordis 或能力包。`internal/gordisowner` 只负责 Scope 到公共 owner 契约的适配；适配层使用公开 API，不访问私有注册表。

贡献插件在 `Requires` 中声明能力 Key，取得 Runtime 后绑定自己的精确 generation：

```go
runtime, err := scope.Get(tools.RuntimeKey)
if err != nil {
    return err
}
_, err = runtime.Bind(scope).Register(definition)
return err
```

`Bind` 捕获 owner，不取得租约，也不重新查找服务；操作委托原有显式 owner Runtime API。Snapshot/Prepared 调用取得并释放租约。Scope 停止撤销贡献，插件自有 transport 和文件资源在排空后关闭。无效 Scope、nil/关闭 Runtime 的错误由操作返回。见 [Tool Binding](tools/README.zh.md)、[LLM Binding](llm/README.zh.md)、[Prompt Binding](prompt/README.zh.md)和 [middleware Binding](agentmiddleware/README.zh.md)。

普通调用方用显式 FactoryOptions 构造 `agent.NewFactory`。框架绑定可用 `agent.NewBoundFactory(options, acquire)`，将固定 Ready 租约保持到 Handle 发布。acquire 在持锁路径中不得 I/O、调用业务回调或重入 Factory。Handle.Dispose 保留持久历史，只释放自己拥有的资源。

每个包拥有本地 `_test.go` helper 和功能回归。协议与请求序列化直接使用 Go 表驱动测试；Session 投影、流累积、attempt 输出及 JSONL 恢复的输入与预期结果也直接在 `_test.go` 中定义。测试不依赖历史源码快照、摘要清单或邻包 testdata。

```sh
# 从仓库根目录执行
go -C kernel test ./core/...
```

[Gordis 接桥](gordisbridge/README.zh.md) 是独立的公开框架适配，测试随包维护；上述领域包不导入它。
