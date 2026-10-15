# qi/tools plugin

[中文](README.zh.md) · [Package map](../../README.md)

This package publishes Tool Runtime with `tools.RuntimeKey` through Gordis APIs. `Extension()` declares the available type, empty configuration schema and immutable update policy. Builtin explicitly aggregates it; activation occurs only for Profile-selected instances. The parent capability package owns the types, Key and Binding; it does not depend on this plugin.

Boot owns Host integration and resource-lifetime tests; builtin checks this factory's metadata and duplicate composition.
