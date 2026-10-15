# 构建 Qi 应用

[English](building.md) | 中文

对象关系见[核心概念](concepts.zh.md)；运行时分层、执行流程与资源所有权见[架构](architecture.zh.md)。

选择应用所需能力，构建二进制，再通过 Profile 运行。`qi-build` 根据所选 Go 扩展生成入口并编译，无需手写 `main` 或构建清单。

首次运行和真实模型配置由 [builtin 入门](../../builtin/docs/getting-started.zh.md)介绍。本页聚焦 qi-build：如何选择扩展包、映射 module 源码、解析版本和交付应用。下面用最小 `greet` 工具插件演示扩展构建；构建和查看帮助无需模型凭据，实际执行任务需要配置模型服务。

先理解三个概念：

- **module** 是版本与源码映射单位，拥有独立 `go.mod`。
- 所选包的 **Extension** 描述可用 Plugin 类型与 Bundle。Plugin 类型实现能力，Bundle 组合实例配置；描述这些能力不启动实例。
- **Profile** 从已编译类型与 Bundle 定义中选择、配置运行实例。新增 Go 扩展需要重新构建，调整已编译能力的实例配置只需更换 Profile。

先完成[快速开始](#quick-start)，再按需阅读[扩展与替换](#extensions)、[版本与 fork](#versions)、[标准发行](#distributions)、[交付](#delivery)、[错误处理](#errors)或[参考](#reference)。

<a id="quick-start"></a>

## 1. 构建包含扩展的应用

从完整仓库根目录构建工具插件应用，构建工具放在 `dist/`，应用与配置副本放在 `dist/tool-plugin/`：

```sh
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build
./dist/qi-build \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --with github.com/RussellLuo/qi/builtin/examples/tool-plugin \
  --output ./dist/tool-plugin/qi
cp builtin/examples/tool-plugin/profile.yaml dist/tool-plugin/profile.yaml
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- --help
```

命令显示 headless 的任务参数帮助，不激活插件或调用模型。按[工具插件示例](../../builtin/examples/tool-plugin/README.zh.md)配置 `dist/tool-plugin/profile.yaml` 后，即可使用已构建的应用执行任务。独立 kernel 可以自行构建 qi-build；生成此应用还需 builtin 源码或可获取的 builtin 版本，见[独立 kernel](#core-only)。

<a id="extensions"></a>

## 2. 选择扩展与替换依赖

`--with package=./directory` 选择可导入的根包或子包 Extension；directory 是包含 `go.mod` 的所属 module 根目录。`--replace module=./directory` 只替换依赖源码，不调用其工厂。普通 Go 依赖不会自动启用对应 Extension。

core 始终包含在构建中，`builtin.Extension()` 只调用一次。**`github.com/RussellLuo/qi/kernel` 不是必填参数**；快速开始显式指定它，是为了使用此仓库中未发布的本地 core。只有覆盖 core 版本/源码时，才需 core `--with` 或 `--replace`。

快速开始的 `--with` 发布 `example/tool-plugin` 类型，让 Profile 可以声明 `greet` 实例。它使用的 Tool Runtime、模型提供者和 headless 已由 builtin 包含，无需再次选择这些能力的工厂。

`--replace` 映射 kernel/builtin 的本地源码，并不再次发布它们的类型。依赖一个 Go 包只让代码可用；要发布某个扩展的插件类型，仍须选择该包的 `Extension()`。

独立进程工具还需交付子程序；这属于运行时部署，具体步骤见[进程工具示例](../../builtin/examples/tool-process/README.zh.md)。

本地路径相对调用者 cwd 解析，不相对临时生成工程解析；带空格路径须加引号。每个替换目录需有身份匹配的 `go.mod`；构建器不自动发现邻接 modules，也不继承 `go.work` 映射。源码 module 自带替换时，见[源码约束](#source-rules)。

一个 module 中的多个工厂包可分别选择，例如 `--with example.com/extensions/greet --with example.com/extensions/format --replace example.com/extensions=./extensions`。也可对每个子包使用 `--with package=./extensions`，directory 仍是同一个 module 根目录。嵌套 module 须有自己的显式映射，不能借父 module 的源码目录跨越边界。

<a id="versions"></a>

## 3. 指定版本与远端 fork

版本查询支持 `latest`、tag、伪版本、分支和 commit，由 Go 解析。未指定 core 参数时，查询取 `QI_VERSION`，否则 `latest`；未选扩展时仍包含默认内建集合。扩展省略查询也默认 `latest`。

core 查询优先级为 **core `--with` 显式版本 > 位置参数版本 > `QI_VERSION` > `latest`**。位置参数与 core `--with` 显式版本冲突时报错；只替换 core 源码时，保留位置参数/环境查询。

本地源码没有显式查询时，不执行默认远端查询，而使用开发占位（`v0.0.0`，`/v2` 则为 `v2.0.0`）。显式固定版本配本地源码也无需下载该 module；显式浮动查询仍会查询远端。module path 须符合其主版本。

以下远端命令是**语法占位**，公网 Qi/plugin 安装与发行仍未验证。所有 `<...>` 须替换为真实可获取的值，并使用新的输出路径：

```text
qi-build <qi-tag-or-commit> --with <extension-module>@<tag-or-commit> --output ./dist/qi
qi-build --with <extension-module> --output ./dist/qi
qi-build --with <extension-module>=<fork-module>@<branch-or-commit> --output ./dist/qi
qi-build --replace <dependency-module>@<fixed-version>=<fork-module>@<tag> --output ./dist/qi
```

fork 保留原导入路径，记录标明替换 module/版本及 sums。`--replace` 左侧可选版本必须固定，不能是分支；远端目标省略查询时默认 `latest`。

Go 最小版本选择（MVS）可能调整请求版本；记录区分查询、要求版本与最终图。再次执行分支或 `latest` 可能得到不同结果，保存记录支持审计，不保证浮动命令可重复。legacy 清单保留严格版本相等校验。

<a id="distributions"></a>

## 4. Workspace 发行构建

见 [跨模块指南](https://github.com/RussellLuo/qi/tree/main/docs/workspace-building.zh.md)。

<a id="delivery"></a>

## 5. 使用与检查产物

成功交付二进制与 `<output>.build/`。任一路径已存在均拒绝覆盖，失败不交付新二进制或部分记录。默认输出是 `./qi`（Windows 为 `./qi.exe`）；建议在仓库根显式指定 `dist/` 等输出目录，将生成文件集中在已由 `.gitignore` 排除的目录中。

运行需要二进制、调用者 Profile 及其配置引用的外部资源，不需要 Go、源码或构建记录；builtin 的 base YAML 已嵌入。

| `.build/` 内路径 | 用途 |
| --- | --- |
| `request.json`、`resolved-build.json` | 新 CLI 请求、cwd、查询、解析版本与替换 |
| `record.json` | 实际 module 图、本地输入摘要、环境、二进制 SHA-256 与日志索引 |
| `project/` | 生成入口/工厂聚合及最终 `go.mod` / `go.sum` |
| `dependencies.json`、`logs/` | 生产 package 闭包与命令结果 |
| `manifest-input.yaml`、`manifest.yaml` | 原始/规范化 manifest，仅 legacy 模式 |

新记录使用 `qi.build-record/v1alpha2`，legacy 使用 v1alpha1。所有本地映射参与摘要，包括普通依赖替换；摘要覆盖 module 文件、编译使用的源码/嵌入资源，并在编译后再次核对。`localDevelopment=true` 标记本地输入。记录保存路径和摘要，不是完整源码备份。见[固定工程重编译](#recompile)。

<a id="rebuild"></a>

### 重复构建

`qi-build` 不覆盖已有二进制或对应的 `.build/` 记录。重复构建本页的工具插件示例时，先删除这两项，再执行原构建命令：

```sh
rm -f dist/tool-plugin/qi
rm -rf dist/tool-plugin/qi.build
```

也可以通过 `--output` 指定另一个输出目录。其他示例同样处理各自的二进制和 `.build/`；Profile、工作文件和会话历史可以继续保留。普通 `go build` 可以覆盖已有二进制。

<a id="errors"></a>

## 6. 排查常见错误

| 现象 | 处理 |
| --- | --- |
| 输出或 `.build` 已存在 | 见[重复构建](#rebuild)，清理旧二进制及记录，或更换输出目录 |
| module 身份不匹配/跨越嵌套 module | 为工厂包提供所属 module 的源码；`--replace` 左侧必须是 module 路径 |
| 工厂缺失/签名错误、所选包为 `main`、重复 ID 或无效 schema | 检查[包工厂约定](#extension-contract)和注册所有权 |
| 源码有 `go.mod` replacements | 将映射移到 CLI `--replace`，见[源码约束](#source-rules) |
| 意外下载/依赖不可获取 | 检查 `QI_VERSION` 与显式查询，再补充本地映射或可用依赖/缓存 |
| Profile 报未知 Plugin/Bundle | 构建时选入其 Extension，Go 依赖本身不注册类型 |
| Profile 缺失/无效或缺少凭据 | 提供 `--profile`，准备供应商配置/环境 |
| 输入模式混用/无 manifest 使用 `--source` | 使用新 CLI 或 [legacy 模式](#legacy)之一 |
| 构建期间本地输入变化 | 使用稳定源码快照重试 |

<a id="reference"></a>

## 7. 参考

### 完整命令语法

```text
qi-build [<qi_version>]
  [--with <package[@version][=replacement]> ...]
  [--replace <module[@version]=replacement> ...]
  [--output <file>]

qi-build --manifest <build.yaml> --output <file>
  [--source <selected-module>=<directory> ...]
```

唯一可选 core 版本可与参数混排。`-h` / `--help` 在访问源码/网络前返回。`qi-build` 不提供 `build` 子命令、`--core` 参数或 `--with ./directory` 简写；应用直接使用 `qi --profile <path>`，没有 `run` 子命令。

| 输入 | 效果 |
| --- | --- |
| `--with package` / `package@query` | 按 latest/指定查询选择根包或子包 Extension |
| `--with package=./directory` | 选择所属 module 的本地源码，使用隐式开发版本 |
| `--with package@query=./directory` | 保留 module 版本请求并覆盖其源码 |
| `--with module=fork/module@query` | 保留原导入路径，使用远端 fork |
| `--replace module[@fixed-version]=replacement` | 替换依赖，不选入其 Extension |
| `--output file` | 指定二进制路径，记录路径为 `<file>.build` |

<a id="source-rules"></a>

### 源码与替换约束

本地路径在生成工程前转为绝对路径并解析符号链接，须有正常 `go.mod` 声明所映射 module。源码 `go.mod` 中的 replace 被拒绝，须在 CLI 声明 `--replace`（legacy 为已选 module 的 `--source`）。缺少映射的依赖仍须可获取，构建器不会悄悄改版本重试。

重复显式 `--with` 包、重复 `--replace` 键、冲突/重叠替换报错。同 module 的包共享一次 require/replace：一个显式 query 供该 module 的隐式选择共同使用，不同显式 query 或源码目录冲突则失败。子包内联 source 与 module 映射相同可合并。各工厂的注册仍分别 Compose，重复 ID 继续拒绝。未生效替换记录 `used=false`，不启用代码；实际图中的 Replace 路径与 sums 标识源码，要求版本本身不能证明源码身份。

<a id="extension-contract"></a>

### 扩展作者约定

每个可选扩展是某个 Go module 内可导入的根包或子包，导出：

```go
func Extension() application.Extension
```

工厂纯描述 registrations/Bundles，不创建 Host、不启动实例/listener/进程，也不读取运行凭据。生成代码用 `application.Compose` 显式组合所选工厂与 builtin，再调用 `boot.Run`。包/工厂缺失、所选包为 `main`、签名错误、重复 Plugin/Bundle ID、无效元数据/schema 都在交付前失败。见 [SDK 说明](../core/README.md)。

一个 module 可发布多个 Plugin 类型，总 Extension 引用的实现进入编译闭包，即使 Profile 未启用它们。需要独立代码选择时可发布不同工厂包；同 module 的包共享版本与源码。聚合工厂已拥有的依赖工厂被再次选择时，可能触发重复 ID；构建器不去重，也不全局发现工厂。

<a id="legacy"></a>

### Legacy manifest 兼容

`qi.build/v1alpha1` 保留固定版本、显式 `execution: local`、严格未知字段/单文档检查及可选 `package`（默认 module 根包）。两种 CLI 均支持子包工厂；Process execution 和 legacy 最终版本升级仍被拒绝。忽略 `QI_VERSION`，不可混用 with/replace/core 位置参数，须指定 `--output`。`--source` 仅映射已选 core/plugin modules，其他依赖须由 Go 获取。

<details>
<summary>本地 legacy 示例（先完成快速开始，在仓库根目录执行）</summary>

`v0.0.0` 是本地占位，不代表已发布版本。

```sh
cat > dist/build.yaml <<'YAML'
apiVersion: qi.build/v1alpha1
core:
  version: v0.0.0
plugins:
  - module: github.com/RussellLuo/qi/builtin
    package: github.com/RussellLuo/qi/builtin/examples/tool-plugin
    version: v0.0.0
    execution: local
YAML

./dist/qi-build \
  --manifest dist/build.yaml \
  --source github.com/RussellLuo/qi/kernel=./kernel \
  --source github.com/RussellLuo/qi/builtin=./builtin \
  --output ./dist/legacy/qi
./dist/legacy/qi --profile dist/tool-plugin/profile.yaml -- --help
```

</details>

<a id="recompile"></a>

### 重编译保存的工程

保留相同源码快照、本地替换路径与工具链，确保依赖可获取。从单仓库根目录使用快速开始的产物：

```sh
mkdir -p dist/recompiled
GOWORK=off go -C dist/tool-plugin/qi.build/project mod download all
GOWORK=off go -C dist/tool-plugin/qi.build/project mod verify
GOWORK=off CGO_ENABLED=0 go -C dist/tool-plugin/qi.build/project build \
  -mod=readonly -trimpath -o "$(pwd)/dist/recompiled/qi" .
./dist/recompiled/qi --profile dist/tool-plugin/profile.yaml -- --help
```

重编译后的应用应显示相同的参数帮助。此流程不是 replay 子命令，也不保证跨工具链字节一致。

<a id="core-only"></a>

### 独立 kernel 与构建环境

从仓库根目录执行以下命令，kernel module 仅使用自有源码即可运行 Go 测试并构建 qi-build，无需 builtin 或可选插件源码：

```sh
GOWORK=off go -C kernel test ./...
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build
```

生成应用始终包含默认内建集合。使用本地未发布源码时，分别映射两个实际 module 根目录：

```sh
./dist/qi-build \
  --replace github.com/RussellLuo/qi/kernel=/path/to/kernel \
  --replace github.com/RussellLuo/qi/builtin=/path/to/builtin \
  --output ./dist/qi
```

默认 query 由 kernel 与 builtin 分别解析，两者的伪版本可以不同；隐式本地源码使用合法开发占位。生成 go.mod 明确 require 两个实际 module，记录分别保留其版本和源码。不增加 builtin 专用参数，也不改变清单或记录协议。legacy core.version 是默认两个 module 各自使用的固定 query，--source 可以分别映射 kernel 与 builtin。

需要 Go 1.27、Python 3，以及已缓存或可下载的固定依赖。生成程序运行时无需 Go 或源码；应用显式选择 Profile，凭据由环境准备。子目录发布使用 kernel/vX.Y.Z 与 builtin/vX.Y.Z tag。冷缓存下载、公网发布、真实模型和其他平台仍须单独验证。
