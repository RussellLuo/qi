# Qi kernel

[English](README.md) | 中文

Qi 的核心库与应用装配工具，提供 Agent 执行、会话管理、模型调用和工具执行等基础能力。

组装应用时，先组合扩展，声明可用的插件类型，再用 Profile 选择并配置实例。启动时，kernel 校验配置与服务依赖，激活插件并管理其生命周期。

## 基础插件

[kernel Bundle 扩展](bundle/README.zh.md) 汇总以下插件类型：

| 插件 ID | 用途 |
| --- | --- |
| [gordis/events](bundle/README.zh.md) | 提供事件总线，来自 [Gordis](https://github.com/RussellLuo/gordis)。 |
| [qi/session](core/session/plugin/README.zh.md) | 管理会话和历史记录。 |
| [qi/llm](core/llm/plugin/README.zh.md) | 注册模型适配器，选择模型并准备调用。 |
| [qi/prompt](core/prompt/plugin/README.zh.md) | 注册和组装 Prompt 内容。 |
| [qi/tools](core/tools/plugin/README.zh.md) | 注册、校验和执行工具。 |
| [qi/agent-loop](core/agentloop/plugin/README.zh.md) | 创建和管理 Agent，组织模型与工具执行。 |

`bundle.Extension()` 声明这些插件类型，并提供 `base` Bundle；Profile 通过 `bundles: [base]` 启用这组基础服务实例。

## 目录导览

| 入口 | 用途 |
| --- | --- |
| [core](core/README.zh.md) | Agent 执行、会话历史，以及模型、Prompt 和工具等共享服务。 |
| [boot](boot/README.zh.md) | 注册扩展、组合应用，按 Profile 启动插件实例。 |
| [bundle](bundle/README.zh.md) | 基础服务注册，以及可复用的 `base` 实例配置。 |
| [qi-build](cmd/qi-build) | 选择扩展及其版本，生成应用二进制。 |

## 进一步了解

- [核心概念](docs/concepts.zh.md)：认识 Agent、Session、插件与 Profile。
- [架构](docs/architecture.zh.md)：了解执行流程与资源生命周期。
- [Profile 参考](docs/profiles.zh.md)：选择插件并配置实例。
- [应用构建](docs/building.zh.md)：组合扩展并交付应用。
