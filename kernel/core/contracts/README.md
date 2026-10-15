# contracts

[中文](README.zh.md) · [Core map](../README.md)

This public foundation owns contribution ownership, shared Agent activity/Inbox/Driver/observation ports and data, SessionReservation and the ChunkStream alias. Capability types belong to tools, llm, prompt and agentmiddleware; contracts does not import those packages. Import `github.com/RussellLuo/qi/kernel/core/contracts`. Production dependencies contain no Gordis, plugins or boot layer. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

`ContributionOwner` exposes identity, lease acquisition and cleanup registration. Runtime low-level registration accepts this contract; Scope Bindings adapt it internally. Activity and observation ports remain shared across Agent, Session, Driver and event adapters. Protocol owns messages, persistence and stream wire definitions.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/contracts
```
