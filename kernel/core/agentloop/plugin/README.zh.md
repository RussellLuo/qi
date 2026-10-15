# qi/agent-loop 插件

[English](README.md) · [能力地图](../../README.zh.md)

本包通过公开 `agent` Key 发布 Factory/Registry/Initializer/Observer，通过 `agentmiddleware.RuntimeKey` 发布 middleware。Factory 服务实现 `agent.FactoryProvider`；`gordisbridge.NewAgentFactoryService` 保留其生命周期实现，事件分发也仍由 bridge 适配。`Extension()` 只声明可用类型、空配置 schema 与 immutable 更新策略；builtin 显式聚合该工厂，Profile 选择实例后才激活。父领域包不依赖本插件，其能力依赖包含 Gordis。

Host 组合与资源生命周期回归由 boot 的本地集成套件执行；本包的元数据与重复组合通过 builtin 回归验证。
