# qi/session/persistence-jsonl plugin

[Implementation and configuration](../README.md) · [中文](README.zh.md)

This adapter creates a JSONL resource owner and publishes `session.PersistenceProvider` under `session.PersistenceKey`. It owns `Plugin`, required nonempty `directory` configuration, immutable updates and `Extension()`. It creates no domain Session and never scans histories at activation. `qi/session` discovers the provider with Lookup after the whole composition is Ready; business acquisition calls Acquire and retains the exact lease and handle. OnStop rejects new acquisition; Defer releases the plugin reference without waiting for active leases. The last reference closes the physical backend; failures expose the owner for explicit Close retry.

Host activation, binding, rollback and cleanup tests belong to boot; file and recovery tests remain in the parent package.
