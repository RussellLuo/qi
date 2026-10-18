# Development and verification

English | [中文](development.zh.md) · [README](../README.md)

Run these commands from `gordis-rpc/` with a Go toolchain matching [go.mod](../go.mod). `GOWORK=off` uses the module's own dependencies. Set `GOCACHE=/tmp/qi-review-cache` if you need a writable cache.

## Common checks

```sh
# Runtime, generator and real child-process regressions.
GOWORK=off go test -race ./...

# Only public examples: real Host/child and Local/process comparisons.
GOWORK=off go test -race -count=1 ./examples/...

# Regenerate library fixtures after generator or contract changes.
GOWORK=off go generate ./internal/genfixture/contract ./internal/streamfixture/contract
```

After generation, check consistency without writing:

```sh
cd internal/genfixture/contract
GOWORK=off go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go -check
```

See [Bindings](bindings.md) and [basic](../examples/basic/README.md) for basic generation and run commands. Commit generated files; consumers should not need generation during builds.

## What each check establishes

Module tests cover Unit/boundary rejection, handshake checks, startup deadlines, export validation cancellation, real subprocess calls and callbacks, stream cancellation, activation rollback and reaping. Fixtures live in the module and need neither Qi nor the PoC. Example tests also check that the Host excludes child business implementations and ordinary contracts/business packages do not depend on RPC.

To check the Qi tool adapter and Profile integration, run from the Qi repository root with its workspace enabled:

```sh
go test -race ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/...
go test -race ./kernel/boot/...
```

These checks cover tool semantics, process cleanup, default registration and Profile path preparation. They do not establish external real-model behavior. Available runnable examples are listed in the [example index](../examples/README.md).

## Release boundaries

The module is incubated in Qi and unpublished. It is planned to move to a standalone repository, retaining its module path, after tool, LLM and prompt adapters validate the shared substrate. Its go.mod has no local replaces; the development workspace and integration builds explicitly map unpublished source.

Existing acceptance records cover darwin/arm64. Normal version builds, standalone release dependencies, other platforms and external real-model flows need separate verification. This library does not replace Qi's LLM/prompt adapters. Update Host, child and generated code together when changing unpublished APIs or protocols.
