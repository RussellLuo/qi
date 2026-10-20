# Examples

English | [中文](README.zh.md)

Start with [basic](basic/README.md): an ordinary Greeter Plugin, a generated typed
Binding, the library process loader, and a standalone plugin program Unit.
Program configuration is declared with NewConfig, defaults come from New, and
Configure maps it to ordinary instance configuration.

Continue with [duplex](duplex/README.md): the plugin program imports the Host's
Directory and calls it from Greeter. Both sides use ordinary business Plugins,
with one Import and one Export. Its zero ConfigSpec accepts only omitted input
or an empty object, without a Host configuration mode.

From `gordis-rpc/`, run `GOWORK=off go test -race -count=1 ./examples/...` for real
Host and plugin programs, configuration, Local/process comparison and dependency checks.

The stream example, Unit assembly recipe and optional events integration belong
to later stages and are not implemented yet.
