# Development and verification

English | [中文](development.zh.md) · [README](../README.md)

Run these commands from `gordis-rpc/` with a Go toolchain matching [go.mod](../go.mod). `GOWORK=off` uses the module's own dependencies. Set `GOCACHE=/tmp/qi-review-cache` if you need a writable cache.

The module pins `github.com/RussellLuo/jsonschema-go v0.0.0-20261020143036-d0e0ff947dfa` with jsonschemax support. Go downloads it through normal module resolution; no adjacent jsonschema-go checkout or local replace is needed. The root workspace and integration probes use the same pinned dependency.

## Common checks

```sh
# Runtime, generator and real child-process regressions.
GOWORK=off go test -race -count=1 -timeout=180s ./...

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

Module tests cover configuration declaration, Schema adaptation, defaults, strict decoding, mapping and isolation; DescribeProgram reaping without initialization; Host validation before Initialize and independent child validation; Unit/boundary rejection, handshake checks, startup deadlines, export validation cancellation, real subprocess calls and callbacks, stream cancellation, activation rollback and reaping. They test the adapter's responsibilities, not a second copy of jsonschema-go's inference/constraint matrix or full meta-schema validity. Fixtures live in the module and need neither Qi nor the PoC. Example tests also check that the Host excludes child business implementations and ordinary contracts/business packages do not depend on RPC.

To check the Qi tool adapter and Profile integration, run from the Qi repository root with its workspace enabled:

```sh
go test -race -count=1 -timeout=180s ./builtin/tools/process/... ./builtin/bundle ./builtin/examples/tool-process/...
go test -race ./kernel/boot/...
```

These checks cover tool semantics, process cleanup, default registration and Profile path preparation. They do not establish external real-model behavior. Available runnable examples are listed in the [example index](../examples/README.md).

Program configuration acceptance is recorded separately for [declaration](../../docs/gordis-rpc-program-config-c01-fork/README.zh.md), [runtime/protocol](../../docs/gordis-rpc-program-config-c02/README.zh.md), [Qi/PoC/compat and U03 integration](../../docs/gordis-rpc-program-config-c03/README.zh.md) and [documentation closure](../../docs/gordis-rpc-program-config-c04/README.zh.md). The [configuration plan](../../docs/gordis-rpc-program-config-plan.zh.md#6-验收) lists full integration commands. Real process checks require Unix sockets and local listeners; retain failures and add new evidence without rewriting historical baselines.

## Release boundaries

The module is incubated in Qi and unpublished. It is planned to move to a standalone repository, retaining its module path, after tool, LLM and prompt adapters validate the shared substrate. Its go.mod has no local replaces. Qi integration builds explicitly map the unpublished Qi/Gordis/RPC sources; jsonschema-go uses its pinned remote version, and qi-build consumes the actual gordis-rpc directory directly. See the [remote dependency update](../../docs/gordis-rpc-program-config-remote-deps/README.zh.md). Earlier C03/C04 records retain the local-fork setup used at acceptance time.

Existing acceptance records cover darwin/arm64. Normal version builds, standalone release dependencies, other platforms and external real-model flows need separate verification. This library does not replace Qi's LLM/prompt adapters. Update Host, child and generated code together when changing unpublished APIs or protocols.

Protocol remains `gordis-rpc/1` with transport handshake version 1; this does not allow mixing binaries built before and after the configuration payload change. ConfigTarget, ConfigMode, ProcessConfigMode, ForwardConfig and NoConfig have been removed. Declare configured programs with Unit.Config/NewConfig/Configure and unconfigured programs with ConfigSpec's zero value. Historical acceptance pages retain the API used at the time. Future stream and Unit recipe work remain separate example stages.
