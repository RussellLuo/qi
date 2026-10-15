# qi/session 插件

[English](README.md) · [能力地图](../../README.zh.md)

本包创建唯一 Session Runtime 并发布 `session.StoreKey`。`Extension()` 声明空配置对象与 immutable 更新策略；builtin 注册类型，`base` 选择 Runtime。在 Profile 中添加持久化提供者即可保存历史；旧 `mode` 配置直接拒绝。

Activate 只安装领域 resolver，不查询存储。整个组合 Ready 后，Create/Open 或未打开 ID 的 Delete 才在 Runtime Scope 的 View 中调用 Lookup，再向发现的 provider 调用 Acquire。只有正常缺失才选择内存或返回 ErrResumeUnsupported；发现、获取和存储错误直接传播。Lookup 不建立依赖边或租约；每个事务保留独立的后端资源租约和精确历史句柄，Append 始终使用原句柄。

`WithHeaderCheck` 绑定入口校验，先于物理尾部准备与逻辑恢复。即使后端 Scope 先清理，资源 owner 仍保留目录锁，直到全部句柄和租约释放。Factory/Handle 先排空，Runtime 再清理；失败保留明确重试 owner。父领域包拥有 Gordis 服务 Key，不依赖本适配包。配置见 [JSONL 说明](https://github.com/RussellLuo/qi/tree/main/builtin/session/jsonl/README.zh.md)。

Host 组合与资源生命周期测试归 boot；builtin 检查元数据及重复组合。
