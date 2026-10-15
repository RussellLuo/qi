# 文件工具

[English](README.md) · [能力地图](https://github.com/RussellLuo/qi/tree/main/kernel/core/README.zh.md)

`qi/tools/fs` 是内置文件插件，向共享 Tool Runtime 贡献两个工具：

- `read_file`：读取 UTF-8 文本，返回行号，支持分页。
- `write_file`：创建文件或完整覆盖已有文件。

相对路径优先按当前会话的工作目录（cwd）解析，没有会话 cwd 时使用插件激活时的进程工作目录。`cmd/qi` 与 qi-build 生成的应用通过 builtin 提供该插件，由 Profile 显式启用。

## 启用

将 `fs` 实例加入 Profile 即可，无需配置 `root`：

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
```

这个片段启用文件工具。命令行应用还需按照[模型指南](../../llm/openai/README.zh.md)加入模型提供者与 `qi/cli/headless`。在仓库根目录构建，再进入工作目录运行补齐后的 Profile：

```sh
mkdir -p dist/fs/workspace
go -C builtin build -o ../dist/fs/qi ./cmd/qi
```

将补齐后的 Profile 保存为 `dist/fs/profile.yaml`，再运行：

```sh
(cd dist/fs/workspace && ../qi --profile ../profile.yaml -- \
  '用 write_file 创建 hello.txt，分两行写入 hello 和 world，再用 read_file 读取。')
```

工作目录不以 Profile 文件所在目录为基准；headless 将启动时的 cwd 写入 Session Header，AgentLoop 在每次工具调用时传递它。`base` 保留六个基础服务实例，不自动启用文件工具。一个 Tool Runtime 只允许一个 `qi/tools/fs` 实例，第二个实例会因工具名冲突失败。

## 配置与参数

插件只有三个可选读取配置，均有默认值；配置写在 Profile 的 `config` 中，工具参数在每次调用时以 JSON 对象传入。读取始终流式扫描，不增加 DSH 的整读 / 流读切换配置。

| 用途 | 字段 | 类型 | 必填 / 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| 插件配置 | `read_limit` | 正整数 | 2000 | 默认及最大返回行数。 |
| 插件配置 | `read_max_line_length` | 正整数 | 2000 | 单行原文保留的 UTF-16 单元上限，不计截断提示，与 DSH 的字符计数一致。 |
| 插件配置 | `read_max_bytes` | 正整数 | 51200 | 返回行文本及行间换行的 UTF-8 字节上限，不计信封与 footer。 |
| 两工具的参数 | `file_path` | 字符串 | 必填 | 相对会话 cwd 的路径或绝对路径，不能为空白。 |
| `read_file` 参数 | `offset` | 正整数 | 1 | 首个返回行的行号，从 1 开始。 |
| `read_file` 参数 | `limit` | 正整数 | `read_limit` | 本次最多返回的行数，不能超过 `read_limit`。 |
| `write_file` 参数 | `content` | 字符串 | 必填 | 完整 UTF-8 文本，允许空字符串。 |

默认值仅在省略字段时使用。正整数字段拒绝零值、负值和小数；配置和工具参数都拒绝额外字段。例如：

`read_file`：

```json
{"file_path":"hello.txt","offset":1,"limit":2}
```

`write_file`：

```json
{"file_path":"hello.txt","content":"hello\nworld\n"}
```

## 读写结果

成功结果包含结构化 `Value` 和模型可见 `Content`。以下示例展示两者分别返回什么。

### 读取

Value 包含 `path`、`offset`、带 `number` / `text` 的 `lines` 和准确总行数 `totalLines`：

```json
{"path":"/path/to/workspace/hello.txt","offset":1,"lines":[{"number":1,"text":"hello"},{"number":2,"text":"world"}],"totalLines":2}
```

唯一的 `protocol.TextBlock` 使用 DSH 信封：

```text
<path>/path/to/workspace/hello.txt</path>
<type>file</type>
<content>
1: hello
2: world

(End of file - total 2 lines)
</content>
```

普通分页 footer 为 `(Showing lines S-E of T. Use offset=N to continue.)`，字节截断为 `(Output capped. Showing lines S-E. Use offset=N to continue.)`，按提示 offset 续读。若首行展示文本已超出字节上限，可能返回空行窗口并提示相同 offset，需要增大 `read_max_bytes`。

超长行添加 `... (line truncated to N chars)`；按 UTF-16 单元截断，切在代理对中间时显示 U+FFFD，保证有效 UTF-8。读取流式扫描至 EOF，准确计数并验证 UTF-8，只保存有界窗口。空文件允许 offset=1，其他越过 EOF 的 offset 报错；CRLF 按 LF 展示，尾换行不额外增加一行。

### 写入

写入 Value 包含 `path`、`operation`（`create` 或 `update`）、`before`（文本或 null）及 `after`（文本）。before / after 将 CRLF 规范化为 LF，落盘保留输入换行形式。创建、旧内容非 UTF-8，或前后任一侧达到本地后端的 10 MiB diff 采集阈值时，before 为 null，不能据此判断文件原先不存在。

```json
{"path":"/path/to/workspace/hello.txt","operation":"create","before":null,"after":"hello\nworld\n"}
```

模型只看到以下确认；覆盖时正文为 `Updated file`：

```text
<path>/path/to/workspace/hello.txt</path>
<type>file</type>
<content>
Created file
</content>
```

## 路径与写入规则

相对路径按会话 cwd 解析，绝对路径直接使用；输出 `path` 和文本信封使用绝对展示路径。cwd 是路径解析基准，当前本地后端按宿主权限访问文件，不提供工作区围栏。只操作普通文件；读取和写入均跟随符号链接，写入保留链接本身。POSIX 上先解析链接再处理 `..`，因此 `link/../file.txt` 会访问链接目标的父目录。

没有独立的写入大小上限；旧文本 diff 采集有界，默认阈值为 10 MiB，属于后端行为而非插件配置。父目录必须已存在。写入先使用同目录临时文件，校验后替换目标，失败保留原文件并清理临时文件；同一访问对象对同一物理路径的写入串行化。不提供外部修改版本防护或先读后写要求。图片读取、局部编辑、搜索、Bash、沙箱升权和 UI diff 元数据不在本次实现范围内。

## Go API 与生命周期

注册与 Profile 预检不进行文件访问。自定义应用只组合一次 `builtin.Extension()`，额外组合 `fsplugin.Extension()` 会重复注册；后者位于 `github.com/RussellLuo/qi/builtin/tools/fs/plugin`。

父 Go 包名为 `fstool`，导入路径为 `github.com/RussellLuo/qi/builtin/tools/fs`。`New(DefaultOptions(""))` 创建 `FS`，固定无会话时的默认 cwd，`NewReadDefinition(files)` / `NewWriteDefinition(files)` 生成独立定义。定义及调用仍可能使用对象时应保持其存活，之后调用 `FS.Close()`；直接传入的 `Options` 必须已补齐限制，非正值拒绝。`plugin` 子包负责 Gordis 配置和注册：Scope owner 在停止时撤销贡献，已有 Snapshot / 调用租约释放后关闭访问对象。自行准备的 Runtime Snapshot 必须关闭。直接调用 Snapshot 时，可用 `PrepareWithContext(call, tools.ExecutionContext{CWD: cwd})` 指定绝对 cwd；同一 Snapshot 的各调用独立保存该值，middleware 必须保留它。直接使用父包时，可通过 `Options.CWD` 和 `Options.DiffBasisMaxBytes` 调整后端默认值。

两工具使用 `ToolFailureResult`，参数、路径、文件及限制错误成为模型可见失败结果。调用按 context 取消；发出取消请求不等于已准入工作已经退出。Runtime 等待实际 worker 结束，Snapshot 关闭与 Host 停止也会等待租约排空。

## 验证

在仓库根目录执行：

```sh
go -C builtin test -race ./tools/fs/... ./bundle
```

父包固定案例注明 DSH 来源，运行不依赖该 checkout。读写单元测试验证 Value schema、完整 Content 文本、限制、绝对路径与符号链接；[执行测试](execution_test.go)直接验证并发调用的 cwd 隔离和取消，无需启动 Profile。[插件测试](plugin/plugin_test.go)负责配置、注册、Snapshot 租约与关闭；保留一项 [Session cwd 测试](session_cwd_test.go)，验证真实 boot → headless → AgentLoop → 文件工具链路。本地测试不需要模型服务或网络，也不证明真实模型已经完成 tool call。

旧 Profile 请删除 `root`、`max_write_bytes` 和 `diff_basis_max_bytes`；这些插件字段已移除，预检会拒绝它们。工作目录由会话 cwd 提供。
