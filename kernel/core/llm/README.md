# llm

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns Adapter contributions, product-model resolution, prepared calls and stream accumulation. Import `github.com/RussellLuo/qi/kernel/core/llm`. The Runtime includes Gordis Scope binding and retains its explicit owner API; it does not depend on plugins or boot. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

Framework activation belongs to the [plugin](plugin/README.md) subpackage.

The package owns `Adapter`, `Request`, `AdapterRegistration`, `ModelInfo`, `ResolvedRetryPolicy`, `RuntimeKey` and `Binding`. A plugin declares `RuntimeKey.Spec()` in Requires, gets the service with `scope.Get(llm.RuntimeKey)`, then registers through `runtime.Bind(scope).RegisterProvider(registration, models)`. Binding captures the exact Scope generation without acquiring a Ready lease; activation can register contributions before Ready. Prepared snapshots retain their existing leases and drain before plugin resource cleanup. Registration methods accepting `contracts.ContributionOwner` remain available for ordinary Go programs and tests.

Binding also offers `RegisterAdapter`, `RegisterModels` and `RegisterDefaults`. `RegisterProvider` rolls back only its new adapter when catalog registration fails. It does not publish both registries atomically or close plugin transports. Empty model lists retain the low-level API's no-contribution behavior.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/llm
```
