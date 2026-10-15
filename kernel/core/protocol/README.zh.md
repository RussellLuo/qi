# protocol

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有消息、事件、流与 frame 的协议定义、codec 及冻结参考用例。导入 `github.com/RussellLuo/qi/kernel/core/protocol`。生产依赖不包含 Gordis、插件或 boot；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/protocol
```
