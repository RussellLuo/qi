# 通用进程工具

本包提供 `qi/tools/process` 通用加载器与 Tool 领域适配。作者使用 `Main(plugin)` 或 `ServePlugin(ctx, plugin, options...)`；业务 Plugin 仍然声明 `Requires tools.RuntimeKey`，在 Activate 中通过 `Get → Bind.Register/RegisterBatch` 注册工具。框架使用 `Extension()`；额外 Host 服务通过 `HostExtension(id, HostOptions{Imports: ...})` 显式声明。

构建与 Profile 示例见 [tool-process](../../examples/tool-process/README.zh.md)。local Host 与 Child 可以导入同一个业务 Plugin 包；process-only Host 只需通用 loader。实际激活时读取各自 RPC Session 中 Child 的封存描述，无需在 Host 编译业务 metadata 或程序目录。

## 执行与生命周期

Child 框架创建 `tools.Export`，将其真实 `*tools.Runtime` 提供给业务 Plugin。Unit 完成配置、Validate 和业务激活后，`ProviderBinding.Validate` 封存实际注册的**编译后** `DefinitionInfo`。未指定的 Schema 和 FailureMode 使用 Runtime 已有的有效默认值，不另保留一份原始业务声明。Host 检查描述并原子注册全部代理；失败时由已有 gordis-rpc 生命周期回收进程。

执行策略只在 Host Runtime 运行：参数/输出 Schema 校验、Pre/Execute/Post middleware、定义 Timeout 和 FailureMode。Child `Export.Execute` 只调用原始业务 `Definition.Execute`，每次调用租借原始 registration 与 owner generation；空闲导出不持有永久快照。取消不会提前释放 Child 的 worker 容量或 owner 租约，须等实际业务返回或 panic。框架 Close 停止准入、排空实际调用并关闭注册容器，不能在原 owner 的 OnStop 中同步等待 Close。

进程部署仅支持封存时固定的工具贡献。Child 注册 middleware、调用 Runtime.Prepare/PrepareSelected/Schemas/Close、封存后注册或主动撤回，均会使整个导出失效。即使业务忽略返回的错误，`Export.Done/Err` 仍会通过 Child 框架的 AfterReady 任务通知现有 RPC 生命周期，Host 撤回该程序的贡献并回收进程。原 owner 停止也使导出退休。普通本地 `tools.NewRuntime()` 保持原有动态注册与 middleware 行为。

业务代码应只使用注册能力；业务资源继续由原 Scope 管理。进程、Session、Ready、取消、退出与 CleanupReport 由 gordis-rpc 负责。默认不继承 Host 环境；CleanupComplete 与 PID 回收分别观察。

## 契约与完整字段

RPC 契约 ID 保持 `qi/tools/provider`，Go 接口/绑定为 `Provider`、`ProviderKey`、`ProviderBinding`。业务 wire 版本为 **2**，完整 Binding 版本为 **`rpcgen.json/1:2`**。Host 与 Child 必须同步升级、重新构建；已有 RPC 的精确边界校验在 Initialize 前拒绝版本不匹配的制品。没有双协议协商、旧 wire 兼容入口或失败回退。底座协议仍为 `gordis-rpc/1`。

- `Tool` 完整传递编译后 DefinitionInfo，包括 DeferLoading、FailureMode 和有符号纳秒 `TimeoutNanos`；负值、非整毫秒和超过旧 30 秒上限的值均不截断。
- `CallRequest` 传递完整 Execution：Agent/Session/Turn/Step、绝对或空 CWD、ToolCall ID/Name、原始 `CallArguments`，以及 Host middleware 改写后的独立 `Arguments`。不新增 RequestID；进程 generation 由 Session 固定。
- `Client.Call` 返回完整 `tools.ExecutionResult` 和独立调用错误。Value、Meta、原始参数与 Schema 以 byte slices 传输，避免 JSON 浮点转换和字节正规化；Content 使用 `protocol.EncodeContent/DecodeContent`，AdditionalContexts 使用 canonical Message Codec。支持多块/非文本内容、结构化 Failure/Info/Reason、附加上下文及 ConcludesTurn；nil 与空 Content/contexts 保持区别。
- 业务返回的结构化失败结果与原生 Go error 分开传递。原生 error 保留消息和分类；取消/超时在 Host 恢复 `errors.Is` 因果，任意 Go error 类型身份不跨进程。panic、协议/容量/退出错误另行分类。Host Runtime 应用最终 FailureMode。

公开 DTO 是 `internal/contract` 的别名（含 `Result`、`Failure`、`FailureInfo`），生成 Codec 位于 `internal/bindings`。框架不要求作者实现这些类型。CallError 的本地 cause 不进入 wire。

大小/容量限制是明确的传输边界：最多 32 个工具，名称 128 字节，描述 2048 字节，每个 Schema 64 KiB，原始/有效参数各 128 KiB，整个 JSON 编码后的 CallResponse（含 base64 和错误 envelope）128 KiB，原生错误消息 1024 字节；RPC 默认 MaxMessage 为 256 KiB，两端默认 MaxPending 为 16。超限、未知 canonical 内容或不可编码结果明确报错，不截断或静默降级。实际 Child worker 返回后才释放 Child 许可，Host 调用取消返回不等于远端执行结束。传输取消可能无法带回远端随后产生的结果，仍须通过退出/排空证据判断资源结束。

从仓库根目录验证：

```sh
go test -race ./kernel/core/tools/... ./builtin/tools/process/...
go generate ./builtin/tools/process/internal/contract
go -C builtin/tools/process/internal/contract run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Provider -key Key -version Version -package bindings -out ../bindings/provider.go -check
```

## Profile 与程序路径

默认 builtin 已包含加载器。推荐引用程序路径，`config` 保持为业务配置：

```yaml
- id: greet
  plugin: ./packages/greet/greet
```

`./`、`../` 开头或当前平台绝对路径表示程序引用。相对路径以最终 Profile 文件目录为基准；Bundle/override 也使用这一基准。不会执行 shell 展开或 PATH 查找。程序化来源的相对引用须显式提供绝对基准目录。

也可显式使用加载器：

```yaml
- id: greet
  plugin: qi/tools/process
  config:
    command: packages/greet/greet
    config: {}
```

此形式的相对 command 以 Host 可执行文件目录为基准，绝对 command 直接使用。路径引用由既有启动层转换为绝对 command 后交给同一加载器，两种相对基准不同。外层部署字段不会进入业务 config。

程序 `identity.id` 默认使用业务 Spec.ID，可通过 ServerOptions.ID 覆盖；版本默认 dev。身份允许 slash/Unicode，不接受空白、控制字符或超过 512 字节的值。显式加载器可在外层 config 用 `identity: {id: example/tool-plugin, version: dev}` 固定身份；路径引用的业务 config 中同名字段仍是业务字段。

替换同版本程序后需重启 Host 或修改实例 restartToken；不涉及 wire 升级时 Host 无需因业务更新而重建。仅需交付程序和 Profile，无额外描述文件。

## 生命周期与交付验证

Host Scope 停止时先撤回工具贡献，再等待已取得的 Snapshot 租约释放，随后关闭 Child Session。Child 原业务 owner 的租约一直持有到真实 Execute 返回；即使调用取消或定义超时已经返回 Host，也不能提前执行业务清理。退出证据包括 worker 屏障、CleanupComplete、Reaped 和 PID 不存在，单独的取消返回或进程消失不足以证明正常清理。

本地 Runtime 等待真实 worker 后返回；进程调用可能因 RPC 取消先返回，远端随后产生的结果不保证送达。两种部署的取消/超时分类与 owner 排空已分别验证，不承诺取消后的返回时序或迟到结果完全相同。重启必须排空旧 generation；新 generation Ready 后才能取得新贡献，关闭的旧 Snapshot 和旧 Session 不会指向新 generation。

测试覆盖同一 Plugin 的本地/真实 Child、失败策略、慢 worker、回滚恢复、旧 generation 和重启。实际交付验证使用 qi-build 生成的 Host 与本地脚本化 SSE 模型，覆盖 Headless → Agent → Session → Tool 调用、JSONL 恢复、帮助零启动与整体搬迁。真实供应商模型、性能、其他平台和独立模块版本发布仍需分别验证。
