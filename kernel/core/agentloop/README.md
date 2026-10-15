# agentloop

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns the default Driver, AttachedRun, request state and Step/Turn settlement. Import `github.com/RussellLuo/qi/kernel/core/agentloop`. Production dependencies include Gordis transitively through capability packages; this package does not directly import Gordis, plugins, bridge or boot. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

Framework activation belongs to the [plugin](plugin/README.md) subpackage.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/agentloop
```
