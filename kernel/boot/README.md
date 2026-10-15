# Public integration and application composition

[中文](README.zh.md) · [Packages](../core/README.md)

| Package | Caller and responsibility |
| --- | --- |
| [profile](profile) | Plugin metadata, execution Profile codec, Bundle definitions/codec and detached Clone; read-only BundleCatalog contract |
| [registration](registration) | PluginRegistration binds a Gordis prototype to its Profile and invocation metadata |
| [launch](launch) | Finite consumers use Parser/Invocation/Prepared, Context, ProcessControl, Task and HeaderCheckBinder |
| [boot.Run](run.go) | Application process entry, explicit Profile startup, signals and cleanup |
| [application](application) | Assemblers and extension factories describe Extension/Application; Compose validates static metadata and freezes explicit Bundle definitions |

Provider and contribution lifecycles use [core/gordisbridge](../core/gordisbridge). Domain definitions live in [core](../core/README.md). Metadata declarations and Compose do not activate plugins or create a Host. Compose has no implicit builtin selection.

`boot.Run` is the process entry. Its private CLI, strict source codec/preflight, Bundle catalog, Coordinator, Host runtime and process supervisor live under `internal`. Public Compose uses only pure checks and catalog construction, while the private executor uses public leaf contracts without importing application or boot. Use boot.Run and the canonical public owners; private constructors remain inside boot/internal.

This is a complete builtin application entry. `builtin` aliases the `builtin/bundle` import; pass custom Extensions as additional Compose arguments. See the [Profile reference](../docs/profiles.md) for user configuration.

```go
package main

import (
	"fmt"
	"os"

	builtin "github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
)

func main() {
	app, err := application.Compose(builtin.Extension())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(boot.Run(os.Args[1:], launch.Input{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}, app))
}
```

Extension fields use `registration.PluginRegistration` and `profile.BundleDefinition`. Application exposes `profile.BundleCatalog.Lookup`; concrete catalog constructors and startup controls are private implementation concerns. There is one canonical public definition for each type.

Pure Compose tests and strict Bundle tests live here. Source/preflight and Coordinator tests live with their private owners; the builder owns its external fixture and executes it through `internal/build/build_test.go`.
