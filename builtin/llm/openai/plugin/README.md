# qi/llm/openai-compatible plugin

[Implementation and configuration](../README.md) · [中文](README.zh.md)

This adapter owns `Plugin`, Profile configuration and the pure `Extension()` factory. The type remains `qi/llm/openai-compatible` with `replace-generation` updates. It imports public core/boot contracts and the parent implementation. Host activation, rollback and cleanup tests belong to boot; implementation tests remain in the parent package.

Activation gets `llm.RuntimeKey` and calls `runtime.Bind(scope).RegisterProvider` with the Adapter and static catalog. Both contributions belong to the exact Scope generation; catalog failure rolls back the new Adapter. The implementation and transport lifecycle remain in their existing owners.
