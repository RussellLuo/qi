# qi/session/persistence-jsonl 插件

[实现与配置](../README.zh.md) · [English](README.md)

本适配包创建 JSONL 资源 owner，通过 `session.PersistenceKey` 发布 `session.PersistenceProvider`。它拥有 `Plugin`、必填非空 `directory` 配置、immutable 更新策略与 `Extension()`，不创建领域 Session，激活时不扫描历史。`qi/session` 在整个组合 Ready 后通过 Lookup 发现 provider，业务获取时调用 Acquire 并保留精确租约与句柄。OnStop 停止新获取，Defer 释放插件引用，不等待活跃租约；最后一个引用才关闭物理后端，失败携带 owner 供显式 Close 重试。

Host 激活、绑定、回滚与清理测试归 boot；文件与恢复测试归父包。
