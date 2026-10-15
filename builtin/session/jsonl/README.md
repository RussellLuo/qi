# JSONL persistence

English | [中文](README.zh.md)

Save append-only JSONL Session histories for later processes to resume. The builtin application includes `qi/session/persistence-jsonl`; a Profile must explicitly enable it.

## Enable

Append this instance to the existing `instances` list of a Profile with `base`, a model and headless:

```yaml
instances:
  - id: session-persistence
    plugin: qi/session/persistence-jsonl
    config:
      directory: .qi/sessions
```

`directory` is required and must be nonempty. Relative paths resolve from process cwd. See the [model plugin guide](../../llm/openai/README.md) for model setup and the [Profile reference](../../../kernel/docs/profiles.md) for instance and override rules.

## Continue an existing Session

Use `--json` on the first invocation to get its `sessionId`. On the next invocation, pass `--session-id <id>` from the same working directory with the same storage directory. See [Headless](../../cli/headless/README.md) for commands and the [coding agent](../../examples/coding-agent/README.md) for a code generation and resume exercise.

## Storage behavior and limits

- Each Session has an append-only JSONL log. Normal shutdown closes files and releases the directory lock while retaining history. Explicit Store deletion removes the log.
- Activation creates the directory and acquires its exclusive writer lock. Only one active writer may use a directory. Directory or lock errors prevent application execution; registration and Profile preflight do not open storage.
- Recovery truncates interrupted trailing appends and repairs interrupted execution. Malformed committed records are reported as corruption when that history is opened; unrelated histories remain usable.
- Locking supports Windows and the Unix platforms listed in adjacent build-tagged files. Other platforms reject activation.
- Without an enabled provider, new Sessions use memory and cannot resume across processes. Conflicting providers fail preflight.

## Go API and lifecycle

A handwritten entry composes `builtin.Extension()` once, importing `builtin` from `github.com/RussellLuo/qi/builtin/bundle`. Also adding `jsonlplugin.Extension()` duplicates registration. Custom plugins depend on `core/session.Store`, without depending on this implementation.

The public `NewPersistence(directory)` constructor returns a `Backend` implementing `core/session.Persistence`. It creates the directory and acquires its writer lock, without scanning or opening Session histories. Use `NewOwner(directory)` for Runtime acquisition: its provider supplies resource leases through `RuntimeOptions.ResolvePersistence`. Runtime owns all domain Session instances. The backend only tracks acquired file handles.

Runtime `Get` queries opened instances. `Open` reads the requested committed prefix, validates admission and domain history, then prepares the physical tail; `Session.Attach` commits logical repair and the existing end-seed marker policy. Corrupt files are reported when that history is opened. A different history can still be used. Header/codec/sequence failures return `ErrCorruptLog`; domain transition failures retain the core Session error. Rejected history is not truncated or repaired.

Release closes a file acquisition while retaining history; Delete removes history; RollbackCreate removes only the exact new history. Failed appends roll back and sync the preceding prefix before further writes or release; failed cleanup retains the handle and directory lock for retry. Drain Factory/Handles before Runtime releases histories and leases. The owner closes Backend after its last reference releases; Backend.Close refuses outstanding transferred handles.

`NewOwner(directory)` provides a separate resource owner implementing
`session.PersistenceProvider`. `Acquire(ctx)` returns a lease with a fixed Backend;
`Stop()` rejects new acquisition. `Owner.Close()` releases the plugin reference
without waiting for active leases. Existing handles remain usable and the directory
stays locked until the last lease releases. Close or roll back every history handle
before `lease.Release()`. Final close failures return `*OwnerCloseError` with the
retained `Owner`; retry `error.Owner.Close()` or the original release after repairing
the cause. The JSONL plugin publishes this owner as `session.PersistenceProvider`; its OnStop
rejects acquisition and its Defer releases the plugin reference. Runtime discovers the provider with Lookup only at business acquisition, then calls Acquire.
