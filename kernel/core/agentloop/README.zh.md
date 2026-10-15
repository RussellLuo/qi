# agentloop

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有默认 Driver、AttachedRun、请求状态与 Step/Turn 结算。导入 `github.com/RussellLuo/qi/kernel/core/agentloop`。生产依赖通过能力包间接包含 Gordis；本包不直接导入 Gordis、插件、bridge 或 boot；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

框架激活归 [plugin](plugin/README.zh.md) 子包。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/agentloop
```
