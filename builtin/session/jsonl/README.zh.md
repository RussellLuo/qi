# JSONL 持久化

[English](README.md) | 中文

为会话保存追加式 JSONL 历史，供后续进程恢复。内建应用已包含 `qi/session/persistence-jsonl`，需要在 Profile 中显式启用。

## 启用

在已选择 `base`、模型和 headless 的 Profile 中，将以下实例追加到现有 `instances` 列表：

```yaml
instances:
  - id: session-persistence
    plugin: qi/session/persistence-jsonl
    config:
      directory: .qi/sessions
```

`directory` 必填且不能为空；相对路径以进程工作目录为基准。完整模型配置见[模型插件指南](../../llm/openai/README.zh.md)，实例与覆盖规则见 [Profile 参考](../../../kernel/docs/profiles.zh.md)。

## 继续已有会话

第一次调用使用 `--json` 取得 `sessionId`，下一次在同一工作目录、使用同一存储目录传入 `--session-id <id>`。完整命令见 [Headless](../../cli/headless/README.zh.md)，代码生成与会话继续练习见[编码智能体](../../examples/coding-agent/README.zh.md)。

## 存储行为与限制

- 每个 Session 使用一个追加式 JSONL 事件日志。正常关闭释放文件和目录写锁，保留历史；显式 Store 删除操作才删除日志。
- 激活时创建目录并取得独占写锁，同一目录只允许一个活跃写入者。目录或锁错误会阻止应用运行；注册和 Profile 预检不打开存储。
- 恢复时截断未完成的尾部追加，并修复中断执行；已提交记录损坏时报告错误。损坏只在打开该历史时报告，不阻止使用其他历史。
- 写锁支持 Windows 及相邻 build tag 文件列出的 Unix 平台；其他平台拒绝激活。
- 未启用提供者时，新 Session 使用内存，不能跨进程恢复；同时声明冲突的提供者会在预检时失败。

## Go API 与生命周期

自定义入口只需组合一次 `builtin.Extension()`，其中 `builtin` 导入自 `github.com/RussellLuo/qi/builtin/bundle`；同时加入 `jsonlplugin.Extension()` 会重复注册。自定义插件依赖 `core/session.Store`，无需依赖此实现。

公开构造函数 `NewPersistence(directory)` 返回实现 `core/session.Persistence` 的 `Backend`。构造时创建目录并取得写锁，不扫描或打开 Session 历史。Runtime 的获取通过 `NewOwner(directory)` 提供资源租约，注入 `RuntimeOptions.ResolvePersistence`；Runtime 拥有全部领域 Session 实例，后端只记录取得的文件句柄。

Runtime 的 `Get` 只查询已打开实例。`Open` 读取指定历史的已提交前缀，完成入口与领域校验，再准备物理尾部；`Session.Attach` 提交逻辑修复并执行既有 end-seed 标记规则。损坏文件在打开该历史时报告，不阻止使用其他历史。header/codec/sequence 错误返回 `ErrCorruptLog`，领域转换错误保留 core Session 的错误。校验拒绝不会截断或修复历史。

Release 关闭本次文件句柄并保留历史；Delete 删除历史；RollbackCreate 只删除精确的新建历史。失败追加在继续写入或释放前，先回滚并同步此前前缀；清理失败保留句柄和目录锁以便重试。先排空 Factory/Handle，再由 Runtime 释放句柄与租约，owner 在最后一个引用释放后关闭 Backend；Backend.Close 拒绝仍有已转移句柄的情况。

`NewOwner(directory)` 提供独立资源 owner，实现 `session.PersistenceProvider`。`Acquire(ctx)` 返回绑定固定 Backend 的租约；`Stop()` 停止新获取。`Owner.Close()` 释放插件引用，不等待活跃租约；既有句柄仍可使用，最后一次租约释放前目录锁保持持有。调用 `lease.Release()` 前须关闭或回滚全部历史句柄。最终关闭失败返回携带保留 owner 的 `*OwnerCloseError`；修复原因后可重试 `error.Owner.Close()` 或原释放操作。JSONL plugin 发布此 owner 作为 `session.PersistenceProvider`；OnStop 停止获取，Defer 释放插件引用。Runtime 只在业务获取时通过 Lookup 发现 provider，再调用 Acquire。
