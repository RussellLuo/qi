# qi/agent-loop plugin

[中文](README.zh.md) · [Package map](../../README.md)

This package publishes Factory/Registry/Initializer/Observer with the public `agent` Keys, and middleware with `agentmiddleware.RuntimeKey`. The Factory service implements `agent.FactoryProvider`; `gordisbridge.NewAgentFactoryService` retains its lifecycle implementation and event dispatch. `Extension()` declares the available type, empty configuration schema and immutable update policy. Builtin explicitly aggregates it; activation occurs only for Profile-selected instances. The parent domain does not import this plugin; its capability dependencies include Gordis.

Boot owns Host integration and resource-lifetime tests; builtin checks this factory's metadata and duplicate composition.
