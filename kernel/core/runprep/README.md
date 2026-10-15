# runprep

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns explicit execution dependencies and prepared runtime generations. Import `github.com/RussellLuo/qi/kernel/core/runprep`. Production dependencies include Gordis transitively through capability packages; this package does not directly import Gordis, plugins, bridge or boot. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

`PreparedRun.PrepareToolWithContext` passes the call's identity and Session cwd to the frozen Tool Snapshot. AgentLoop obtains cwd from the current Session Header; each call retains its own context without mutating the prepared run.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/runprep
```
