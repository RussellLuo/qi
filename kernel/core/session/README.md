# session

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns Session history, projection, snapshot, inbox, recovery, reservations, Runtime and persistence contracts. Import `github.com/RussellLuo/qi/kernel/core/session`. Public service Keys depend on Gordis; the package does not import plugins or boot. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

`StoreKey` (`qi/session`) and `PersistenceKey` (`qi/session-persistence`) are the typed service Keys. Session consumers declare `StoreKey.Spec()` and use `scope.Get(session.StoreKey)`. Persistence stays optional: the plugin uses `scope.Lookup(session.PersistenceKey)` at business acquisition, then `provider.Acquire(ctx)`; it does not declare a required persistence dependency.

`NewRuntime(RuntimeOptions{})` selects pure memory mode without file I/O. It implements `ResourceStore` and `StoreOpener`; `Open` returns `ErrResumeUnsupported` in memory mode. `LiveSession` implements the shared state machine in both modes; Runtime maintains the sole instance table.

`RuntimeOptions.ResolvePersistence` acquires one backend lease per Create/Open transaction; nil or normal absence selects memory, while errors propagate. `Get` queries opened instances only. `Open` acquires a handle, reads committed history, applies `HeaderCheck` and domain validation, then calls `Prepare` for physical tail repair. Logical repair remains in `Session.Attach`. Commits must succeed before facts, projection and observations advance. `qi/session` publishes the Session service and resolves an optional provider with Lookup at business acquisition, then calls Acquire. Each entry retains its original backend, handle and lease; Append never discovers a backend again.

`Release` closes the exact acquisition and withdraws the running instance while preserving history. `Delete` uses the original binding for opened instances; memory instances cannot delete same-ID backend history. Unopened IDs acquire one temporary lease for deletion. `RollbackCreate` removes only the exact new acquisition and refuses an instance obtained by `Open`. Stale or foreign instances cannot release or roll back a replacement. Failed cleanup retains ownership and blocks reacquisition; retry the same operation. `ErrCleanupPending` rejects switching a failed cleanup to another operation. Unpublished acquisition and temporary-lease cleanup are retried by `Close`. Handle cleanup and deletion complete before lease release; a release retry never repeats successful deletion or rollback. `RuntimeCleanupError` retains the Runtime and its explicit Close retry path.

Drain Factory/Handles before Runtime releases histories and resource leases; the resource owner closes the physical backend after its last reference releases. `Runtime.Close(ctx)` stops new admission and releases detached instances; active attachments/runs return errors and retain their resources for a later retry. Runtime never directly calls Backend.Close. The resolver, persistence methods and `HeaderCheck` must not reenter Runtime while its instance-table lock is held.

Framework activation belongs to the [plugin](plugin/README.md) subpackage.

`PersistenceProvider`, `PersistenceLease` and `PersistenceResolver` define optional
per-transaction backend acquisition. A lease protects backend resources independently
of plugin readiness. Close or roll back its history handles before releasing it;
failed release retains cleanup responsibility. The resolver distinguishes normal
absence from acquisition errors. Activate installs a resolver; after the whole composition is Ready, business
acquisition uses Lookup followed by Acquire. Discovery and acquisition errors propagate.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/session
```
