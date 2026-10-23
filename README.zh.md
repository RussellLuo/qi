# Qi

[English](README.md) | 中文

Qi（气）是一个最小化、插件化、可扩展的 Agent harness，将模型调用、工具执行、会话历史和插件生命周期组织在一起，用于组装智能体应用。

你可以直接使用内置组件运行命令行智能体，也可以编写自己的模型、工具或观察插件。构建时选择应用包含哪些能力，运行时通过 **Profile** 选择并配置组件。

## 项目结构

| 目录 | 职责 |
| --- | --- |
| [kernel](kernel/README.zh.md) | 核心运行时、公开 API、Profile 启动与 `qi-build` 构建工具。 |
| [builtin](builtin/README.zh.md) | 内建命令行应用、模型接入、文件工具、会话持久化与完整示例。 |

builtin 基于 kernel 的公开 API 实现；kernel 不依赖 builtin。跨进程插件使用独立的 [Gordis RPC 模块](https://github.com/RussellLuo/gordis/tree/main/rpc)。

## 从这里开始

- [内建应用入门](builtin/docs/getting-started.zh.md)：构建内建应用、配置模型并完成第一次任务。
- [内建应用概念](builtin/docs/concepts.zh.md)：认识命令行智能体的组件及其关系。
- [核心概念](kernel/docs/concepts.zh.md)：认识 Agent、Session、插件及其关系。
- [Profile 参考](kernel/docs/profiles.zh.md)：选择插件并配置实例。
- [应用构建](kernel/docs/building.zh.md)：选择扩展及其版本，交付应用。

## 许可证

[MIT](LICENSE)
