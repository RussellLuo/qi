# 通用进程工具

本包是 `qi/tools/process` 通用加载器与工具领域适配。普通作者使用 `Main(plugin)` 或 `ServePlugin(ctx,plugin,options...)`；框架装配使用 `Extension()`，需要额外 Host 服务时使用 `HostExtension(id,HostOptions{Imports:...})`。

程序完成配置、Validate、激活、定义验证和 Host 原子批量注册后，才完成 Host 激活。任何阶段失败都由底座在返回错误前同步回收进程，Scope 清理再调用时幂等。

进程、会话、Ready、取消与清理由独立 gordis-rpc 负责。本包维护 Provider 契约、工具语义校验、真实 Runtime 装配与代理贡献；JSON 编解码由 rpcgen 生成。默认不继承 Host 环境；可选导入必须显式声明并进入 Requires。业务错误使用固定安全消息，取消/超时保留因果，进程退出与调用取消分开分类。CleanupComplete 与 PID 回收分开观察。

宿主通过 Extension/HostExtension 和程序路径加载。工具只支持 JSON 结果，不支持丰富内容、附加上下文或结束 Turn 的结果。

完整构建与 Profile 示例见 [tool-process](../../examples/tool-process/README.zh.md)。从仓库根目录执行 `go test -race ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/... ./kernel/boot/...` 验证工具适配与 Profile 集成；真实外部模型、正式模块版本发行与跨平台另列验收。

## 命名约定

Go 包 `builtin/tools/process` 与 Host 插件 ID `qi/tools/process` 表示进程加载机制。RPC 契约 `qi/tools/provider` 表示远端工具能力，Go 接口为 `Provider`，通过 `ProviderKey` / `ProviderBinding` 绑定；业务契约版本为 `1`，生成编码格式共同组成 Binding 版本 `rpcgen.json/1:1`。

`identity.id` 标识具体交付程序，默认直接使用业务 `Spec.ID`，可通过 `ServerOptions.ID` 覆盖；版本默认 `dev`。身份允许 slash 和 Unicode，不接受空白、控制字符或超过 512 字节的值；与工具名称的校验规则独立。`gordis-rpc/1` 标识底座协议。

后续领域沿用同一约定：`builtin/llm/process`、`qi/llm/process`、`qi/llm/provider`；Prompt 对应 `builtin/prompt/process`、`qi/prompt/process`、`qi/prompt/provider`。这些是命名参考，尚未提供对应进程适配。

## 调用与校验边界

`CallRequest` 只含 `tool`、`arguments` 和 `toolCallId`。ToolCallID 来自 Runtime；进程 generation 由底座 Session 固定，不另造 RequestID/InvocationID。Provider 契约声明工具能力，程序身份直接使用 gordis-rpc Identity，不再公开 Metadata/Capabilities。

Host 在注册代理前检查接收到的定义；子进程在 Ready 前检查实际导出，两者各守自己的输入边界。Codec.Validate 接收初始化 context。两端分别保留并发上限：调用取消返回后，子进程中实际 worker 可能仍在运行，Host 的容量释放不能替代子进程限流。

Provider 接口、Key 与 DTO 位于 `internal/contract`，公开类型通过别名保留；生成编解码位于 `internal/bindings`。`CallError` 的本地 cause 不进入 wire。装配层使用生成的 Codec 工厂设置 Validate，保留 Ready 前的定义检查。RawMessage 的内部结构、工具数量、大小限制、Schema 和结果语义仍由本包负责。

子进程 Unit 只装配 Runtime 提供方与业务 Plugin 两个实例。Runtime 提供方同时发布 Runtime 和普通 Provider Service；业务 Plugin 向同一个 Runtime 注册工具，所有实例 Ready 后才检查导出定义。`server.go` 集中维护子进程装配，`extension.go` 维护 Host 加载与 Profile 适配。

从 `builtin/` 重新生成并检查：

```sh
go generate ./tools/process/internal/contract
go -C tools/process/internal/contract run github.com/RussellLuo/gordis-rpc/cmd/rpcgen -type Provider -key Key -version Version -package bindings -out ../bindings/provider.go -check
```

生成代码提交源码，普通构建不运行生成器。当前 Binding 已从手写版本 `1` 切换到 `rpcgen.json/1:1`，无旧 wire 回退；宿主和子程序须同步重建。核心库测试见 `gordis-rpc`，本包测试负责工具语义与贡献生命周期，示例负责交付链路。

## Profile 与程序路径

默认 builtin 装配已包含加载器。推荐直接引用程序路径，`config` 保持为业务配置：

```yaml
- id: greet
  plugin: ./packages/greet/greet
```

以 `./`、`../` 开头或当前平台绝对路径表示程序引用。相对路径以所选 Profile 文件目录为基准，Bundle 和 override 中的引用也使用这一基准。不会执行 shell 展开或 PATH 查找；程序化来源的相对引用必须显式提供绝对基准目录。

显式加载器写法仍受支持：

```yaml
- id: greet
  plugin: qi/tools/process
  config:
    command: packages/greet/greet
    config: {}
```

此写法的相对 `command` 以宿主可执行文件目录为基准；绝对 `command` 直接使用。外层部署字段不会进入内层业务配置。路径引用由启动层转换为绝对 command 后交给同一加载器，因此两种写法的相对路径基准不同。

选择性宿主须显式包含 process 扩展。宿主在业务激活前通过 Describe/Initialize 握手检查协议、身份和精确边界。默认自动发现身份；显式加载器写法可在外层 config 中使用 `identity: {id: example/tool-process, version: dev}` 固定预期身份。路径引用的业务 config 中同名字段仍是业务字段。

原地替换程序后需要重启宿主或修改实例的 `restartToken`；宿主无需重建。仅交付程序和 Profile，无需额外描述文件。
