# protocol

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns message, event, stream and frame wire definitions, codecs and frozen reference cases. Import `github.com/RussellLuo/qi/kernel/core/protocol`. Production dependencies contain no Gordis, plugins or boot layer. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/protocol
```
