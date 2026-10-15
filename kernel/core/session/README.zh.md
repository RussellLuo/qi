# session

[English](README.md) · [Core 能力地图](../README.zh.md)

本公开领域包拥有 Session 历史、投影、快照、inbox、恢复、预留、Runtime 与持久化契约。导入 `github.com/RussellLuo/qi/kernel/core/session`。公开服务 Key 依赖 Gordis，包不导入插件或 boot；资源获取和释放保持显式。应用路径使用 Extension、Profile 和 boot.Run。

`StoreKey`（`qi/session`）与 `PersistenceKey`（`qi/session-persistence`）是本包的 typed 服务 Key。Session 消费者声明 `StoreKey.Spec()` 并调用 `scope.Get(session.StoreKey)`。持久化保持可选：插件在业务获取时通过 `scope.Lookup(session.PersistenceKey)` 发现 provider，再调用 `provider.Acquire(ctx)`，不声明必需的持久化依赖。

`NewRuntime(RuntimeOptions{})` 选择纯内存模式，没有文件 I/O。Runtime 实现 `ResourceStore` 与 `StoreOpener`；纯内存模式的 `Open` 返回 `ErrResumeUnsupported`。`LiveSession` 是两种模式共用的状态机实现，Runtime 维护唯一实例表。

`RuntimeOptions.ResolvePersistence` 在每次 Create/Open 事务中取得一次后端租约；nil 或正常缺失选择内存，错误直接传播。`Get` 只查询已打开实例。`Open` 取得句柄、读取已提交历史，依次执行 `HeaderCheck` 与领域校验，再调用 `Prepare` 修复物理尾部；逻辑修复仍由 `Session.Attach` 完成。提交成功后才推进事实、投影和观察。`qi/session` 发布 Session 服务，业务获取时通过 Lookup 发现可选 provider，再调用 Acquire。每个实例保留原后端、句柄与租约，Append 不再发现后端。

`Release` 关闭精确取得的句柄并撤销运行实例，保留历史；`Delete` 使用已打开实例的原绑定；内存实例不会误删当前后端中的同 ID 历史，未打开 ID 则取得一次临时租约执行删除；`RollbackCreate` 只回滚精确的新建实例，拒绝通过 `Open` 取得的实例。旧实例和外来实例不能释放或回滚替代实例。清理失败保留所有权并阻止再次获取，应重试原操作；`ErrCleanupPending` 拒绝把失败清理切换为另一种操作。未发布实例及临时租约的失败清理由 `Close` 重试。句柄清理与删除完成后才释放租约；释放重试不重复成功的删除或回滚。`RuntimeCleanupError` 保留 Runtime 及显式 Close 重试入口。

先排空 Factory/Handle，再由 Runtime 释放历史句柄与资源租约；资源 owner 在最后一个引用释放后关闭物理后端。`Runtime.Close(ctx)` 停止新准入并释放已脱离的实例；仍有预约或运行的实例返回错误并保留资源，排空后可重试。Runtime 不直接调用 Backend.Close。resolver、持久化方法与 `HeaderCheck` 在实例表锁内调用，不能重入 Runtime。

框架激活归 [plugin](plugin/README.zh.md) 子包。

`PersistenceProvider`、`PersistenceLease` 与 `PersistenceResolver` 定义逐事务可选获取后端的契约。租约独立于插件就绪状态保护后端资源；释放前必须关闭或回滚取得的历史句柄，释放失败继续保留清理责任。resolver 区分正常缺失与获取错误。插件在 Activate 安装 resolver；整个组合就绪后，业务获取通过 Lookup 发现 provider，再调用 Acquire。

测试、helper 和案例数据优先在本包 `_test.go` 中维护；只有验证独立源码目录或实际文件结构时才使用 testdata。

从仓库根目录运行：

```sh
go -C kernel test ./core/session
```
