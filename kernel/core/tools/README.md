# tools

[中文](README.zh.md) · [Core map](../README.md)

This public capability package owns Tool definitions, prepared calls, middleware, execution identity, typed abort errors, `RuntimeKey` and Scope Binding. Import `github.com/RussellLuo/qi/kernel/core/tools`. It depends on Gordis for service access and ownership; resource acquisition and release remain explicit. The application path uses Extension, Profile and boot.Run.

Framework activation belongs to the [plugin](plugin/README.md) subpackage.

Declare `tools.RuntimeKey.Spec()` in the plugin's `Requires`, then register during Activate:

```go
runtime, err := scope.Get(tools.RuntimeKey)
if err != nil {
    return err
}
_, err = runtime.Bind(scope).Register(definition)
return err
```

Binding also offers `RegisterBatch` and `RegisterMiddleware`. It captures one Scope generation without acquiring a lease. Stopping the Scope withdraws its contributions; existing Snapshots keep their leases until closed, before plugin resources close. The original `Runtime.Register(owner, definition)` methods remain available for explicit ownership and business tests without a Host.

`Snapshot.PrepareWithContext` captures `tools.ExecutionContext` per call: execution identity and an optional absolute Session cwd. Calls on a shared Snapshot keep independent cwd values; middleware must preserve both identity and cwd through dispatch. `Prepare` and `PrepareWithIdentity` remain suitable when no cwd is supplied. Close the Snapshot after admitted calls drain.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/tools
```
