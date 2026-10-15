# 内建应用入门

[English](getting-started.md) | 中文

使用内置组件构建命令行智能体，配置模型并完成一次文件读写任务，无需编写插件。本页使用 [coding-agent Profile](../examples/coding-agent/profile.yaml)。

需要 Go 1.27、已缓存或可下载的依赖，以及支持工具调用的 OpenAI-compatible 模型服务。构建和查看帮助无需模型服务或凭据；执行任务时按服务要求准备凭据。以下命令从仓库根目录开始执行。

## 1. 构建应用并查看帮助

构建已有的 `cmd/qi`，复制编码智能体配置并准备工作目录：

```sh
mkdir -p dist/coding-agent/workspace
go -C builtin build -o ../dist/coding-agent/qi ./cmd/qi
cp builtin/examples/coding-agent/profile.yaml dist/coding-agent/profile.yaml
./dist/coding-agent/qi --profile dist/coding-agent/profile.yaml -- --help
```

此 Profile 已选择模型、文件工具、JSONL 持久化和 headless 消费者。帮助只解析配置与参数元数据，不读取任务输入、加载凭据或调用模型。

## 2. 配置模型服务

编辑 `dist/coding-agent/profile.yaml` 中的 `model.config`：

- 将 `catalog[0].model` 的 `your-model` 替换为供应商模型名。
- 使用兼容服务时，将 `endpoint` 改为完整的 Chat Completions URL。
- 按服务要求在当前 shell 设置 `OPENAI_API_KEY`；不需要凭据的本地服务可以删除 `api_key_env`。

消费者使用的产品模型 ID `coding-model` 可以保持不变；它与供应商模型名用途不同。Qi 不加载 `.env`。配置细节见 [OpenAI-compatible](../llm/openai/README.zh.md)。

## 3. 完成第一次任务

在准备好的工作目录运行应用，明确要求模型使用文件工具。`--json` 便于查看工具事件和会话 ID：

```sh
(cd dist/coding-agent/workspace && \
  ../qi --profile ../profile.yaml -- --json \
  '用 write_file 创建 hello.txt，写入 Hello Qi，再用 read_file 回读检查。')
```

检查进程退出码，并打开 `dist/coding-agent/workspace/hello.txt` 核对内容。`phase: turn_end` 事件中的 `reason.kind: completed` 表示本轮正常完成；文件是否符合要求仍以实际检查为准。JSON 字段和输出规则见 [Headless](../cli/headless/README.zh.md#输出与结果判断)。

以上命令在子 shell 中切换目录，运行后仍回到仓库根目录。相对文件和存储路径按工作目录解析。接下来可用同一套应用与配置完成[生成代码、继续会话和测试练习](../examples/coding-agent/README.zh.md)。

## 4. 继续学习

| 目标 | 阅读入口 |
| --- | --- |
| 理解 Profile、模型、消费者和共享服务 | [内建应用概念](concepts.zh.md) |
| 新增实例和覆盖配置 | [Profile 参考](../../kernel/docs/profiles.zh.md) |
| 实现最小工具并构建扩展应用 | [工具插件](../examples/tool-plugin/README.zh.md) |
| 将同一个工具放入独立进程 | [进程工具](../examples/tool-process/README.zh.md) |
| 选择扩展、版本和交付产物 | [kernel 构建指南](../../kernel/docs/building.zh.md) |

三个公开示例共享 builtin module。能力配置分别见[文件工具](../tools/fs/README.zh.md)、[JSONL](../session/jsonl/README.zh.md)和 [headless](../cli/headless/README.zh.md)。
