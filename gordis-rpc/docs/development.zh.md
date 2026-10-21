# 开发与验证

[English](development.md) | 中文 · [返回 README](../README.zh.md)

以下命令从 `gordis-rpc/` 执行，需使用与 [go.mod](../go.mod) 匹配的 Go 工具链。`GOWORK=off` 使用模块自身依赖；可设置 `GOCACHE=/tmp/qi-review-cache` 指定可写缓存。

模块已固定支持 jsonschemax 的 `github.com/RussellLuo/jsonschema-go v0.0.0-20261020143036-d0e0ff947dfa`。Go 按普通模块依赖下载，无需相邻 jsonschema-go 目录或本地 replace；仓库工作区和集成探针也使用同一固定版本。

## 常用检查

```sh
# 库、生成器及真实子进程回归。
GOWORK=off go test -race -count=1 -timeout=180s ./...

# 只运行公开示例的真实 Host/child 与 Local/process 对照。
GOWORK=off go test -race -count=1 ./examples/...

# 修改生成器或契约后，重新生成库内 fixture。
GOWORK=off go generate ./internal/genfixture/contract ./internal/streamfixture/contract
```

生成后的只读一致性检查：

```sh
cd internal/genfixture/contract
GOWORK=off go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go -check
```

basic 的生成与运行命令见 [Binding](bindings.zh.md) 和 [basic 示例](../examples/basic/README.zh.md)。生成文件提交源码，不应依赖使用者构建时生成。

## 测试分别证明什么

库内测试覆盖配置声明、Schema 适配、默认值、严格解码、映射与隔离，不初始化业务的 DescribeProgram 回收，Initialize 前的 Host 校验与 child 独立校验，以及 Unit/边界拒绝、握手校验、启动期限、导出校验取消、真实子进程调用与回调、流取消、激活失败回滚及回收。测试针对适配层职责，不重复 jsonschema-go 的类型/约束矩阵，也不证明完整 meta-schema 合法性。fixture 位于模块内，不依赖 Qi 或 PoC。示例测试另检查 Host 不包含 child 业务实现、普通契约与业务包不依赖 RPC。

验证 Qi 工具适配与 Profile 集成时，从 Qi 仓库根目录执行，并启用仓库工作区：

```sh
go test -race -count=1 -timeout=180s ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/...
go test -race ./kernel/boot/...
```

这些检查覆盖工具语义、进程清理、默认注册与 Profile 路径准备，不等同于外部真实模型验证。当前可运行示例见[示例导航](../examples/README.zh.md)。

程序配置分别记录[声明验收](../../docs/gordis-rpc-program-config-c01-fork/README.zh.md)、[运行/协议验收](../../docs/gordis-rpc-program-config-c02/README.zh.md)、[Qi/PoC/compat 与 U03 集成验收](../../docs/gordis-rpc-program-config-c03/README.zh.md)及[文档收尾验收](../../docs/gordis-rpc-program-config-c04/README.zh.md)。完整集成命令见[配置方案](../../docs/gordis-rpc-program-config-plan.zh.md#6-验收)。真实进程检查需要 Unix socket 与本地监听权限；保留失败记录并新增证据，不改写历史基线。

## 发行边界

当前模块在 Qi 仓库孵化，尚未发布；计划在 tool、LLM 和 prompt 适配验证共享底座后迁入独立仓库，并保留 module path。go.mod 已无本地 replace。Qi 集成构建显式映射尚未发布的 Qi/Gordis/RPC 源码，jsonschema-go 使用固定远程版本；qi-build 直接消费实际 gordis-rpc 目录，见[远程依赖更新验收](../../docs/gordis-rpc-program-config-remote-deps/README.zh.md)。此前 C03/C04 记录保留验收当时的本地 fork 条件。

已有验收记录的平台是 darwin/arm64。正式版本构建、独立发行依赖、其他目标平台和外部真实模型链路仍需各自验证；本库不替代 Qi 的 LLM/prompt 领域适配。更改未发布 API 或协议时，Host、child 与生成代码须同步更新。

协议仍为 `gordis-rpc/1`，transport 握手版本为 1；这不表示可混用配置载荷变更前后的二进制。ConfigTarget、ConfigMode、ProcessConfigMode、ForwardConfig、NoConfig 已删除；有配置程序通过 Unit.Config/NewConfig/Configure 声明，无配置程序使用 ConfigSpec 零值。历史验收页保留当时 API；[stream 示例](../examples/stream/README.zh.md)已交付，Unit 配方仍属后续阶段。
