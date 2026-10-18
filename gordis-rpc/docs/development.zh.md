# 开发与验证

[English](development.md) | 中文 · [返回 README](../README.zh.md)

以下命令从 `gordis-rpc/` 执行，需使用与 [go.mod](../go.mod) 匹配的 Go 工具链。`GOWORK=off` 使用模块自身依赖；可设置 `GOCACHE=/tmp/qi-review-cache` 指定可写缓存。

## 常用检查

```sh
# 库、生成器及真实子进程回归。
GOWORK=off go test -race ./...

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

库内测试覆盖 Unit/边界拒绝、握手校验、启动期限、导出校验取消、真实子进程调用与回调、流取消、激活失败回滚及回收。fixture 位于模块内，不依赖 Qi 或 PoC。示例测试另检查 Host 不包含 child 业务实现、普通契约与业务包不依赖 RPC。

验证 Qi 工具适配与 Profile 集成时，从 Qi 仓库根目录执行，并启用仓库工作区：

```sh
go test -race ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/...
go test -race ./kernel/boot/...
```

这些检查覆盖工具语义、进程清理、默认注册与 Profile 路径准备，不等同于外部真实模型验证。当前可运行示例见[示例导航](../examples/README.zh.md)。

## 发行边界

当前模块在 Qi 仓库孵化，尚未发布；计划在 tool、LLM 和 prompt 适配验证共享底座后迁入独立仓库，并保留 module path。go.mod 不含本地 replace；开发工作区与集成构建显式映射尚未发布的源码。

已有验收记录的平台是 darwin/arm64。正式版本构建、独立发行依赖、其他目标平台和外部真实模型链路仍需各自验证；本库不替代 Qi 的 LLM/prompt 领域适配。更改未发布 API 或协议时，Host、child 与生成代码须同步更新。
