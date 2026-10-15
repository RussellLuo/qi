# qi/llm plugin

[中文](README.zh.md) · [Package map](../../README.md)

This package provides LLM Runtime through public `llm.RuntimeKey` and Gordis APIs. `Extension()` declares the available type, empty configuration schema and immutable update policy. Builtin explicitly aggregates it; activation occurs only for Profile-selected instances. The parent Runtime owns its types, service Key and Scope Binding, and does not depend on this plugin.

Boot owns Host integration and resource-lifetime tests; builtin checks this factory's metadata and duplicate composition.
