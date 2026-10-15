# llm

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有 Adapter 贡献、产品模型选择、准备调用与流累积。导入 `github.com/RussellLuo/qi/kernel/core/llm`。Runtime 接入 Gordis Scope 绑定，同时保留显式 owner API；不依赖插件或 boot。资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

框架激活归 [plugin](plugin/README.zh.md) 子包。

本包拥有 `Adapter`、`Request`、`AdapterRegistration`、`ModelInfo`、`ResolvedRetryPolicy`、`RuntimeKey` 与 `Binding`。插件在 Requires 中声明 `RuntimeKey.Spec()`，通过 `scope.Get(llm.RuntimeKey)` 获取服务，再调用 `runtime.Bind(scope).RegisterProvider(registration, models)`。Binding 记录精确 Scope generation，不获取 Ready 租约，允许在 Activate 登记贡献。Prepared 快照沿用既有租约，在插件资源关闭前排空。接受 `contracts.ContributionOwner` 的注册方法继续供普通 Go 程序和本地测试使用。

Binding 还提供 `RegisterAdapter`、`RegisterModels` 与 `RegisterDefaults`。`RegisterProvider` 在目录注册失败时只撤销本次新登记的 Adapter；它不保证两个注册表原子发布，也不关闭插件 transport。空模型列表保留低层 API 的无贡献行为。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/llm
```
