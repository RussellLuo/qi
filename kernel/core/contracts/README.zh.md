# contracts

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开基础包拥有贡献 owner、公共 Agent 活动/Inbox/Driver/观察端口与数据、SessionReservation 和 ChunkStream alias。能力专属类型归 tools、llm、prompt 和 agentmiddleware；contracts 不导入这些能力包。导入 `github.com/RussellLuo/qi/kernel/core/contracts`。生产依赖不包含 Gordis、插件或 boot；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

`ContributionOwner` 提供身份、租约获取与清理登记。Runtime 低层注册接受此契约，Scope Binding 在内部适配它。活动和观察端口继续供 Agent、Session、Driver 与事件适配共同使用；消息、持久化和 stream wire 定义归 protocol。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/contracts
```
