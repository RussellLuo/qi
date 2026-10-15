# qi/tools 插件

[English](README.md) · [能力地图](../../README.zh.md)

本包通过 Gordis API 以 `tools.RuntimeKey` 发布 Tool Runtime。`Extension()` 只声明可用类型、空配置 schema 与 immutable 更新策略；builtin 显式聚合该工厂，Profile 选择实例后才激活。父能力包拥有类型、Key 与 Binding，不依赖本插件。

Host 组合与资源生命周期回归由 boot 的本地集成套件执行；本包的元数据与重复组合通过 builtin 回归验证。
