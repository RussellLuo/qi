# prompt

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有 Prompt 贡献与准备快照。导入 `github.com/RussellLuo/qi/kernel/core/prompt`。Runtime 接入 Gordis Scope 绑定，同时保留显式 owner API；不依赖插件或 boot。资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

框架激活归 [plugin](plugin/README.zh.md) 子包。

本包拥有 `Context`、`Section`、`Diagnostic` 与 `Snapshot`、`RuntimeKey` 与 `Binding`。插件在 Requires 中声明 `RuntimeKey.Spec()`，通过 `scope.Get(prompt.RuntimeKey)` 获取服务，再调用 `runtime.Bind(scope).Register(section)`。Binding 记录精确 Scope generation，不获取 Ready 租约，允许在 Activate 登记贡献。Prepared 快照沿用既有租约，在插件资源关闭前排空。接受 `contracts.ContributionOwner` 的注册方法继续供普通 Go 程序和本地测试使用。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/prompt
```
