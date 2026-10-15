# Headless 任务消费者

[English](README.md) | 中文

`qi/cli/headless` 每次接收一个任务，输出结果后退出。内建应用已包含该类型，由 Profile 显式启用。首次配置模型和运行见[入门](../../docs/getting-started.zh.md)。

## 启用与选择模型

将下面的实例加入已有基础服务和模型提供者的 Profile：

```yaml
instances:
  - id: headless
    plugin: qi/cli/headless
    config:
      model: coding-model
```

`model` 是提供者 catalog 中的产品模型 ID，不是供应商模型名。省略时须已有显式默认模型贡献。`base` 不添加消费者。Profile 字段与覆盖规则见[配置参考](../../../kernel/docs/profiles.zh.md)，模型字段见 [OpenAI-compatible](../../llm/openai/README.zh.md)。

## 命令与输入

```text
qi --profile <path> -- [选项] [任务词...]
```

第一个 `--` 将 launcher 参数与任务消费者参数分开。

| 选项 | 用途 |
| --- | --- |
| `--session-id <id>` | 恢复指定会话；需要持久化存储。省略时创建新会话。 |
| `--json` | 将执行事件以逐行 JSON 写入 stdout。 |
| `-h`、`--help` | 查看应用帮助，不读取任务输入、不启动 Host。 |

以下命令中的 `qi` 表示已构建的可执行文件，`profile.yaml` 须包含模型和 headless：

```sh
qi --profile profile.yaml -- 'Hello' 'world'
printf '%s\n' 'Hello world' | qi --profile profile.yaml -- -
qi --profile profile.yaml -- --session-id 'session-...' '继续上次的任务'
```

多个任务词用空格拼成一个任务。独立的 `-`，或没有任务参数时的非终端 stdin，会读取完整 UTF-8 输入；空输入报错。`-` 必须是唯一任务参数。任务以 `-` 开头时，用第二个 `--` 结束消费者选项，例如 `qi --profile profile.yaml -- -- '-task'`。`qi --help` 查看 launcher 帮助；`qi --profile profile.yaml -- --help` 查看当前应用参数。

## 输出与结果判断

普通模式将最终回答写入 stdout，以换行结束。推理片段实时写入 stderr，连续片段共用 `qi: reasoning:` 标题；诊断也写入 stderr。

`--json` 按行输出 JSON：执行内容从已提交事实投影，另有会话元信息、最终文本和调用错误。`thinking`、`text` 不代表实时 token 增量。以下是一次正常完成调用的输出节选，省略了步骤和文本事件：

```json
{"type":"session","sessionId":"session-...","cwd":"/path/to/workspace"}
{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"completed"}}
{"type":"final","text":"Hello world"}
```

| 事件 `type` | 主要内容 |
| --- | --- |
| `session` | `sessionId` 和 `cwd`，用于后续恢复。 |
| `status` | `phase` 为 `turn_start`、`step_start`、`step_end` 或 `turn_end`；结束轮次包含 `reason`。 |
| `thinking`、`text` | 已提交消息中的推理或正文，内容在 `text`。 |
| `tool_call`、`tool_result` | 工具调用及模型可见的结果，使用 `callId` 关联。 |
| `final` | 本次调用的最终文本。失败轮次也可能包含文本。 |
| `error` | 调用错误，说明在 `message`。 |

判断执行结果时，检查 `type == "status"`、`phase == "turn_end"` 的事件，其 `reason.kind == "completed"` 表示本轮正常完成。还须检查进程退出码，以确认输出和清理成功。`final` 或实时推理不能单独作为完成依据；文件内容、测试是否通过等任务要求仍需实际检查。

## 恢复与执行限制

- `--session-id` 需要 [JSONL 等持久化提供者](../../session/jsonl/README.zh.md)，并保持原工作目录和原持久化目录。内存会话、历史不存在或已被占用、cwd 不匹配，以及 Preset 或 subagent/fork 来源的历史均不能恢复。
- 每次命令处理一个任务；多次调用可以继续同一会话。
- 默认每轮最多执行 8 个 Step；每个 Step 包含一次模型请求及其工具处理，模型重试仍属于同一 Step。需要继续执行第 9 个 Step 时以错误结束。当前 headless 没有调整该上限的命令行或 Profile 字段。

## Go API 与生命周期

公开消费者入口为 `Plugin`、`Parser` 和 `Extension`。自定义入口组合一次 builtin 即可；再次选择 headless Extension 会重复注册。模型选择在 Ready 阶段解析，准备并释放预检租约，不调用 Stream；失败时回滚已激活服务，不提交任务。

任务通过 Factory.Create 或 Factory.Resume 获取 Handle。结果从本次调用区间的已提交事实读取，结束后排空并释放 Handle，再关闭 Host。追加、输出、取消和清理错误沿调用路径传播。具体执行与资源所有权见 [kernel 架构](../../../kernel/docs/architecture.zh.md)。

输入准备可取消且不关闭借用的 stdin；不可中断的底层 Read 可能存活到进程退出。JSONL 恢复通过 StoreOpener.Open 获取历史，无 cwd 的旧 header 在修复写入前被拒绝。
