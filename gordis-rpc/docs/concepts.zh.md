# 核心概念

[English](concepts.md) | 中文 · [返回 README](../README.zh.md)

以 Greeter 为例：业务 Plugin 实现 `Greeter`，消费者通过 `scope.Get(contract.Key)` 调用它。把 provider 放进子进程后，消费者取得的是实现同一接口的代理；业务契约和 Plugin 仍使用普通 Gordis API。

## 从接口到进程

| 概念 | 解决什么问题 | Greeter 中的对应物 |
| --- | --- | --- |
| 契约（contract） | 调用什么服务 | Go 接口 `Greeter`、类型化 `Key` 和契约版本 |
| Binding | 如何跨进程调用该契约 | 将 typed client、方法 handler 和版本绑定到 Key |
| Boundary | 程序边界允许哪些服务进出 | child 需要的 Imports、提供的 Exports |
| Unit | 子进程里运行哪些实例，如何装配 | Plugin、实例、配置目标、导入和带来源的导出 |
| Session | 某次启动后，调用属于哪个运行实例 | 会话 ID、Host 实例及 generation、诊断 |

契约和业务 Plugin 无需导入 RPC。生成或手写的 Binding 放在适配包，Host 与 child 的装配代码显式引用它。参见 [basic 文件布局](../examples/basic/README.zh.md#普通代码与程序装配)。

## Imports / Exports 始终以 child 为参照

- **Imports**：child 依赖、由 Host 提供的服务，例如日志或回调。Host 通过 Scope.Get 固定依赖，再提供回调通道。
- **Exports**：child 提供、供 Host 使用的服务，例如 Greeter。Host 取得相应的 typed client。

因此 `NewProcess` 将 Imports 声明为 Host Plugin 的 Requires，将 Exports 声明为 Provides。两端校验完整的契约名、版本和方向，不能各自只声明需要调用的部分。

没有 Imports 时无需回调 server。零导出、仅导入或无服务的任务 Unit 也合法。

## Unit 是闭合的静态装配

`Unit.Plugins` 注册普通 Plugin，`Instances` 指定运行实例；`ConfigTarget` 指定接收 Host 业务配置的实例。每个 Export 明确写出来源 `Instance` 与 Binding。

所有 Requires 必须由 Unit 内实例或 Imports 满足，每个服务名只能有一个 provider；当前不支持 Unit 内多 provider 的 View 路由。依赖排序、循环检测、激活和回滚由 Gordis 负责。

一个 Unit 可以包含多个 Plugin、多个实例和多个导出；它不等于一个业务 Plugin。

## 程序身份与服务契约各自命名

`Identity.ID` 是程序的稳定身份，`Identity.Version` 是程序版本，`Identity.Protocol` 是底座协议 `rpc.Protocol`。服务 Key 的名称与 Binding.Version 则标识契约及其兼容版本。

一个程序可以提供多个契约，因此程序 ID 无需等于契约 ID。`ProcessOptions.PluginID` 又是 Host 注册加载器的逻辑 Plugin ID。basic 在 [Host](../examples/basic/host/cmd/main.go) 和 [插件程序](../examples/basic/plugin/cmd/main.go) 入口直接声明程序身份与各自的边界，普通接口、Key 和契约版本放在 [contract](../examples/basic/contract/service.go)。

Host 可用非零 Identity 固定预期身份；零值允许启动后通过 Describe 发现身份，Boundary 仍须精确匹配。

## 配置与运行实例

`NewProcess` 创建可注册的 Plugin 原型，不启动程序。每个 `Spec.New` 产生独立状态，激活时才建立自己的子进程与 Session。

实例 Config 保持业务 JSON 对象，例如 `{"prefix":"Welcome"}`。默认 `ForwardConfig` 将它复制并交给 child 的 ConfigTarget，由普通 Plugin 解码、验证并应用默认值。无 ConfigTarget 的 Unit 显式选择 `NoConfig`，只接受缺省配置或空对象，向 child 发送 nil；业务字段在启动前报错。

程序路径、参数、环境、身份、Boundary 和 Limits 来自 ProcessOptions，与业务配置分开。

接下来阅读[架构与生命周期](architecture.zh.md)，了解何时服务可用，以及失败时如何回收。
