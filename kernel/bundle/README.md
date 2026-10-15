# Foundational types and the base Bundle

[中文](README.zh.md) · [Package map](../core/README.md)

Import `github.com/RussellLuo/qi/kernel/bundle` and call `bundle.Extension()` to publish six foundational types: `gordis/events`, `qi/session`, `qi/llm`, `qi/prompt`, `qi/tools` and `qi/agent-loop`. This package owns the events registration and sole [base.yaml](base.yaml); other type descriptors belong to each capability's `plugin` factory.

The factory only describes types and Bundles. It does not create a Host, activate instances or read credentials. A Profile selects `bundles: [base]` to introduce six foundational service instances. Session Runtime uses memory when no persistence provider is found. Base includes no model provider, persistence provider, file tools or consumer.

[builtin/bundle](../../builtin/bundle/extension.go) adds OpenAI-compatible access, JSONL, file tools and headless, yielding ten types and reusing the same base definition. A complete builtin application composes builtin once; also composing this Extension would duplicate type and Bundle IDs.

A handwritten entry needing only foundational services can explicitly compose this Extension with its own extensions, then call `boot.Run`. qi-build generated entries include builtin by default. Read [Core concepts](../docs/concepts.md) for descriptions, Profile selection and mounting.

From the repository root:

```sh
go -C kernel test ./bundle
go -C builtin test ./bundle
```

The first command compiles this package. Builtin aggregate tests cover ten types (including the six foundational types), base instances and duplicate rejection.
