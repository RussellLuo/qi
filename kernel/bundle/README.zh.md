# 基础类型与 base Bundle

[English](README.md) · [能力地图](../core/README.zh.md)

导入 `github.com/RussellLuo/qi/kernel/bundle`，调用 `bundle.Extension()` 可发布六种基础类型：`gordis/events`、`qi/session`、`qi/llm`、`qi/prompt`、`qi/tools` 和 `qi/agent-loop`。本包拥有 events 注册及唯一的 [base.yaml](base.yaml)，其余类型的描述符归各能力的 `plugin` 工厂。

工厂只描述可用类型和 Bundle，不创建 Host、激活实例或读取凭据。Profile 通过 `bundles: [base]` 引入六个基础服务实例。Session Runtime 未发现持久化提供者时使用内存；base 不包含模型提供者、持久化提供者、文件工具或消费者。

[builtin/bundle](../../builtin/bundle/extension.go) 在此基础上增加 OpenAI-compatible、JSONL、文件工具和 headless，共发布十种类型，并复用同一个 base 定义。完整内建应用只需组合一次 builtin Extension；再组合本包 Extension 会导致重复类型和 Bundle ID。

自定义入口如果仅需基础服务，可以显式组合本包 Extension 与自己的扩展，再调用 `boot.Run`。qi-build 生成入口则默认组合 builtin。扩展描述、Profile 选择和挂载的区别见[核心概念](../docs/concepts.zh.md)。

从仓库根目录执行：

```sh
go -C kernel test ./bundle
go -C builtin test ./bundle
```

第一条命令编译本包；builtin 的聚合测试检查十种类型（含六种基础类型）、base 实例及重复注册拒绝。
