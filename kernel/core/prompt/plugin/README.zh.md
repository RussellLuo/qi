# qi/prompt 插件

[English](README.md) · [能力地图](../../README.zh.md)

本包通过公开 `prompt.RuntimeKey` 和 Gordis API 提供 Prompt Runtime。`Extension()` 只声明可用类型、空配置 schema 与 immutable 更新策略；builtin 显式聚合该工厂，Profile 选择实例后才激活。父 Runtime 拥有能力类型、服务 Key 与 Scope Binding，不依赖本插件。

Host 组合与资源生命周期回归由 boot 的本地集成套件执行；本包的元数据与重复组合通过 builtin 回归验证。

本包测试通过 Gordis 验证 Prompt Runtime 贡献租约、owner 撤销与重新挂载。
