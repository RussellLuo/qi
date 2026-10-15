# agent

[中文](README.zh.md) · [Core map](../README.md)

This public domain package owns Factory, Registry, Handle, initialization, observation, cancellation and activity scheduling. Import `github.com/RussellLuo/qi/kernel/core/agent`. Public service Keys depend on Gordis; the package does not import plugins or boot. Resource acquisition and release remain explicit; the application path uses Extension, Profile and boot.Run.

`RegistryKey`, `InitializerKey`, `ObserverKey` and `FactoryKey` belong to this package. `FactoryProvider` supplies the Scope binding contract; the provider implementation remains in `gordisbridge`. Declare `FactoryKey.Spec()` in Requires, get the provider with `scope.Get(agent.FactoryKey)`, then call `provider.Bind(scope)`. Repeated binding within one generation returns the same Factory. Bind can run during Activate; Create/Attach/Resume require caller and provider Ready. Invalid or stopped Scopes return an error, and caller/provider stop retains the existing Handle drain ordering.

Tests, helpers and case data live in local `_test.go` files. Use `testdata` only when a test needs a separate source tree or on-disk fixture.

From the repository root:

```sh
go -C kernel test ./core/agent
```
