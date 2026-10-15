# qi/session plugin

[中文](README.zh.md) · [Package map](../../README.md)

This package creates the sole Session Runtime and publishes `session.StoreKey`. `Extension()` declares empty configuration and an immutable update policy; builtin registers the type and `base` selects the Runtime. Adding a persistence provider to the Profile enables persistence. Legacy `mode` configuration is rejected.

Activate installs a domain resolver without querying storage. After the whole composition is Ready, Create/Open or unopened Delete uses Lookup in the Runtime Scope's View, then Acquire on the discovered provider. Only normal absence selects memory or returns ErrResumeUnsupported; discovery, acquisition and storage errors propagate. Lookup establishes no dependency or lease; each transaction retains its independent backend lease and exact history handle. Append uses that original handle.

`WithHeaderCheck` binds entry admission before physical tail preparation and logical recovery. The resource owner retains the directory lock until all handles and leases release, including when the backend Scope cleans up first. Factory/Handles drain before Runtime; failed cleanup retains an explicit retry owner. The domain package owns the Gordis service Keys and does not import this adapter. See [JSONL configuration](https://github.com/RussellLuo/qi/tree/main/builtin/session/jsonl/README.md).

Boot owns Host integration and resource lifetime tests; builtin checks metadata and duplicate composition.
