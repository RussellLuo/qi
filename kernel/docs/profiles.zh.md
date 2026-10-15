# 配置 Profile

[English](profiles.md) | 中文

Profile 是应用每次启动时读取的 YAML 文件，用来选择插件实例并提供配置。它只能使用已编入应用的插件类型与 Bundle；新增 Go 扩展需要重新构建。首次运行见 [builtin 入门](../../builtin/docs/getting-started.zh.md)。

## 声明实例

下面是一份完整的 Profile，启用基础服务和文件工具。它没有任务消费者，启动后会保持运行，直到收到停止信号。需要执行模型任务时，参考 [coding-agent Profile](../../builtin/examples/coding-agent/profile.yaml)加入模型和 headless。

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
    config:
      read_limit: 100
      read_max_line_length: 1000
      read_max_bytes: 10240
```

| 字段 | 用途 |
| --- | --- |
| `apiVersion` | 使用 `qi.profile/v1alpha1`。 |
| `bundles` | 按顺序展开已编入应用的 Bundle，例如 `base`。可省略。 |
| `instances` | 新增插件实例，每项通过 `id` 命名、通过 `plugin` 选择类型。可省略。 |
| `instances[].config` | 该实例的配置对象，字段由插件定义；省略时为空对象。 |
| `overrides` | 修改此前由 Bundle 或 `instances` 声明的实例。可省略。 |

`id` 是本次运行图中的实例名，`plugin` 是插件类型 ID。例如 `fs` 和 `qi/tools/fs` 分别标识实例与实现；二者都不是 Go 导入路径。

## 修改已有实例

Bundle 按列表顺序展开，每个 Bundle 先新增实例，再应用自身覆盖；随后处理用户的 `instances` 和 `overrides`。实例名不能重复声明，修改已有实例须用 `overrides`。

下面在同一份 Profile 中调整 `fs` 的读取行数：

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
    config:
      read_limit: 100
      read_max_line_length: 1000
      read_max_bytes: 10240
overrides:
  - id: fs
    config:
      read_limit: 50
```

**`config` 替换整个配置对象，不做逐字段合并。** 覆盖后的 `fs.config` 只有 `read_limit: 50`；另外两个字段恢复为默认的 2000 和 51200。若需要保留自定义值，应在新的 `config` 中一并写出。

| 覆盖字段 | 行为 |
| --- | --- |
| `id` | 必填，必须指向已声明的实例。 |
| `config` | 整体替换配置；省略则保留原配置，`{}` 清空配置。 |
| `plugin` | 更换实现类型；类型改变时先清空原配置和 `restartToken`，再应用本次覆盖。 |
| `disabled` | `true` 不挂载该实例；`false` 恢复启用。 |
| `restartToken` | 实例重启标记；统一 CLI 不监视文件，修改 Profile 后需要重新启动应用。 |

例如，覆盖项 `{id: fs, disabled: true}` 可以关闭文件工具。关闭实例不会自动关闭依赖它的其他实例；剩余运行图仍须满足依赖。disabled 声明也必须引用已编入应用的公开类型，但不校验它的配置和运行依赖。

## 常见错误与路径

- 未知 Plugin 或 Bundle：先检查拼写，再确认其 Extension 已被编入应用。
- 实例重复或覆盖目标不存在：`instances` 用于新增，`overrides` 用于修改。
- 覆盖后缺少必填配置：检查是否把 `config` 误当成了字段合并。
- Profile 声明不接受未知字段、重复键或多个 YAML 文档；`instances`、`config` 等声明字段不能是显式 `null`。配置对象内部的值由插件 schema 校验。

`--profile` 的相对路径按启动进程的工作目录解析。文件工具和 JSONL 存储的路径规则分别见[文件工具](../../builtin/tools/fs/README.zh.md)与[持久化](../../builtin/session/jsonl/README.zh.md)，不以 Profile 所在目录为基准。

## Go API

用户 YAML 包含 Bundle 和覆盖声明；展开后得到仅含活动实例的执行 Profile。公开 `boot/profile.Profile` 和 `profile.Parse` 面向执行 Profile，不是用户 YAML 的 Bundle 解析入口。应用通过 `boot.Run` 加载用户文件，手写入口见 [boot](../boot/README.zh.md)。
