# Gordis 接桥

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开包拥有低层 `ScopeOwner`/`NewScopeOwner` API、Agent Factory 生命周期实现、观察 Topic 与 `EventDispatcher`。能力包拥有各自类型、服务 Key 与 Runtime Binding；核心 Runtime 不导入 bridge。

Agent Provider 调用 `NewAgentFactoryService(providerScope, agent.FactoryOptions)`，通过 `agent.FactoryKey` 发布返回的 `agent.FactoryProvider`。消费者声明依赖，通过 `callerScope.Get(agent.FactoryKey)` 取得 Provider，再调用 `provider.Bind(callerScope)`。绑定按调用方的精确 generation 管理 Handle，保持固定 Ready 租约直到发布；借用 Registry/Store/Driver，保留 Provider/调用方停止与排空顺序。nil/无效调用方返回 `agent.ErrClosed`。

Tool、LLM、Prompt 和 Agent middleware 贡献者使用所属能力 Runtime Key 和 `runtime.Bind(scope)`，无需导入 bridge。显式 owner 集成仍可向原 Runtime 注册 API 传入 `NewScopeOwner(scope)`。disposer 撤销贡献，快照租约排空后才关闭插件自有资源；事件分发被动观察，不拥有执行。

测试与本地 helper 随所属包维护。在仓库根目录执行 `go -C kernel test -race ./core/gordisbridge ./core/internal/gordisowner`，验证 Factory 发布/准入、owner generation/排空和原始本地观察；注册回滚、已租用快照和过期 disposer 由能力 Binding 测试覆盖。
