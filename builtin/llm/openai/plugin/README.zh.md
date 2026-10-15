# qi/llm/openai-compatible 插件

[实现与配置](../README.zh.md) · [English](README.md)

本适配包拥有 `Plugin`、Profile 配置及纯 `Extension()` 工厂，类型仍为 `qi/llm/openai-compatible`，更新策略为 `replace-generation`。它通过公开 core/boot 契约使用父包实现。Host 激活、回滚和清理测试归 boot；具体实现测试归父包。

Activate 通过 `llm.RuntimeKey` 获取 Runtime，调用 `runtime.Bind(scope).RegisterProvider` 登记 Adapter 和静态目录。两项贡献归属同一精确 Scope generation；目录失败回滚本次 Adapter。业务实现与 transport 生命周期仍由原有组件负责。
