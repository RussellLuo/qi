# tools

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开能力包拥有 Tool 定义、准备调用、middleware、执行身份、typed abort 错误、`RuntimeKey` 与 Scope Binding。导入 `github.com/RussellLuo/qi/kernel/core/tools`。服务访问与 owner 适配依赖 Gordis；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

框架激活归 [plugin](plugin/README.zh.md) 子包。

在插件的 `Requires` 声明 `tools.RuntimeKey.Spec()`，然后在 Activate 中登记：

```go
runtime, err := scope.Get(tools.RuntimeKey)
if err != nil {
    return err
}
_, err = runtime.Bind(scope).Register(definition)
return err
```

Binding 还提供 `RegisterBatch` 和 `RegisterMiddleware`。它捕获精确的 Scope generation，不获取租约。Scope 停止时撤销贡献；已有 Snapshot 关闭并释放租约后，才关闭插件自有资源。原有 `Runtime.Register(owner, definition)` 等方法继续用于显式 owner 接入和无需 Host 的业务测试。

`Snapshot.PrepareWithContext` 按调用捕获 `tools.ExecutionContext`，包含执行身份和可选的绝对 Session cwd。同一 Snapshot 的调用独立保存 cwd，middleware 必须在实际派发时保留身份和 cwd。不指定 cwd 时可使用 `Prepare` / `PrepareWithIdentity`；已准入调用结束后关闭 Snapshot。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/tools
```

进程桥接框架可用 `NewExport()` 创建隔离注册容器，通过 `Runtime()` 满足相同 RuntimeKey，在业务激活后 `Seal()` 并读取 `Describe()`。描述采用实际编译后的有效默认值。`Execute()` 只调用原始业务函数，不重复执行 Host 策略；每次调用持有原 owner/registration 的租约直到 worker 真正返回，空闲时不占租约。框架必须监控 `Done()/Err()`，在不支持的操作或 owner 停止后撤回远端贡献，并在 owner OnStop 之外调用 `Close()` 排空。该容器拒绝 middleware、本地准备/查询和封存后变更。普通 `NewRuntime()` 不受此限制。具体 wire、大小限制和 Child 装配见 [process](../../../builtin/tools/process/README.zh.md)。
