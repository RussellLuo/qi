# Qi builtin

[English](README.md) | 中文

Qi 的内建命令行应用与能力集合，提供模型接入、文件读写、会话持久化和命令行任务执行，通过 Profile 选择并配置插件实例。

## 能力导览

| 插件 ID | 用途 |
| --- | --- |
| [qi/llm/openai-compatible](llm/openai/README.zh.md) | 接入 OpenAI-compatible 模型。 |
| [qi/session/persistence-jsonl](session/jsonl/README.zh.md) | 保存和恢复 JSONL 会话历史。 |
| [qi/tools/fs](tools/fs/README.zh.md) | 读取和写入文件。 |
| [qi/tools/process](tools/process/README.zh.md) | 加载 Profile 指定的工具程序，默认 Bundle 已包含。 |
| [qi/cli/headless](cli/headless/README.zh.md) | 执行命令行任务，输出文本或 JSON 结果。 |

## 开始使用

- [入门](docs/getting-started.zh.md)：构建应用、配置模型并完成第一次任务。
- [内建应用概念](docs/concepts.zh.md)：理解 Profile 中各组件的关系。
- [Profile 配置参考](../kernel/docs/profiles.zh.md)：新增实例、覆盖配置与启用规则。

## 示例

| 示例 | 学习目标 |
| --- | --- |
| [Coding agent](examples/coding-agent/README.zh.md) | 生成代码、继续会话并检查结果。 |
| [Tool plugin](examples/tool-plugin/README.zh.md) | 实现和注册最小 `greet` 工具。 |
| [Process tool](examples/tool-process/README.zh.md) | 将同一个工具放入独立进程。 |
