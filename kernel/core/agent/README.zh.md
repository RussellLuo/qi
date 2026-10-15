# agent

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有Factory、Registry、Handle、初始化、观察、取消与活动调度。导入 `github.com/RussellLuo/qi/kernel/core/agent`。公开服务 Key 依赖 Gordis，包不导入插件或 boot；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

`RegistryKey`、`InitializerKey`、`ObserverKey` 与 `FactoryKey` 归本包。`FactoryProvider` 定义 Scope 绑定契约，Provider 实现仍在 `gordisbridge`。消费者在 Requires 中声明 `FactoryKey.Spec()`，通过 `scope.Get(agent.FactoryKey)` 取得服务，再调用 `provider.Bind(scope)`。同一 generation 重复绑定返回同一 Factory；Activate 中可绑定，Create/Attach/Resume 要求调用方和提供方 Ready。无效或停止的 Scope 返回错误，调用方/提供方停止时保持现有 Handle 排空顺序。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/agent
```
