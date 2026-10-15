# 用内置组件运行编码智能体

[English](README.md) | 中文

从空工作目录生成一个计算器，再继续同一会话修改并添加测试。此例使用内建模型、文件工具、JSONL 和 headless，无需编写插件；组件关系见[内建应用概念](../../docs/concepts.zh.md)。

先按[入门指南](../../docs/getting-started.zh.md)完成构建和模型配置，准备好 `dist/coding-agent/qi`、`profile.yaml` 和 `workspace/`。以下从仓库根目录继续；最后手动运行生成的测试需要 Python 3。

## 首次生成代码

进入工作目录后提交任务。`--json` 输出会话 ID 和执行事件，便于下一步继续同一会话：

```sh
cd dist/coding-agent/workspace
../qi --profile ../profile.yaml -- --json \
  '用 write_file 创建 calculator.py，实现 add、subtract、multiply、divide 四个函数，再用 read_file 回读检查。'
```

检查退出码并打开 `calculator.py` 核对四个函数。复制 `type` 为 `session` 的事件中的 `sessionId`，替换下一步的 `session-...`。每次不带 `--session-id` 都创建新会话。

## 继续同一会话修改

```sh
../qi --profile ../profile.yaml -- --session-id 'session-...' \
  '读取刚才的计算器，修改 divide，使除数为零时抛出 ValueError；用 Python unittest 创建 test_calculator.py，覆盖四则运算和除零，再回读两个文件并总结改动。'
```

历史保存在工作目录下的 `.qi/coding-agent/sessions`；恢复会话时保持工作目录和存储目录一致。

## 检查修改并运行测试

检查两个文件的内容。`write_file` 完整覆盖已有文件，因此修改前应先读取。当前示例只提供文件读写，测试由你在同一工作目录执行：

```sh
python3 -m unittest -v test_calculator
```

测试通过才说明这些案例验证了预期行为；模型的总结或本轮正常完成不能替代测试。保留 `dist/coding-agent/` 可继续练习。命令参数、JSON 与恢复限制见 [Headless](../../cli/headless/README.zh.md)。

配置细节见[文件工具](../../tools/fs/README.zh.md)、[OpenAI-compatible](../../llm/openai/README.zh.md)和 [JSONL 会话](../../session/jsonl/README.zh.md)；使用 qi-build 构建见[构建指南](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.zh.md)。进一步学习[自定义工具插件](../tool-plugin/README.zh.md)与[进程工具](../tool-process/README.zh.md)。
