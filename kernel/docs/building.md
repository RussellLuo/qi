# Building Qi applications

English | [中文](building.zh.md)

Build a binary with the capabilities your application needs, then run it with a Profile. `qi-build` generates the entrypoint and compiles your chosen Go extensions; you do not need to write a `main` or build manifest.

For object relationships, read [Core concepts](concepts.md); for runtime layers, execution and resource ownership, read [Architecture](architecture.md).

For a first run and real model configuration, follow [builtin getting started](../../builtin/docs/getting-started.md). This guide focuses on qi-build: selecting extension packages, mapping module sources, resolving versions and delivering applications. The example builds a minimal `greet` tool plugin. Building and inspecting help need no model credentials; executing a task requires a configured model service.

Three concepts connect these steps:

- A **module** owns versions and source mappings, with an independent `go.mod`.
- A selected package's **Extension** describes available Plugin types and Bundles. A Plugin type implements a capability; a Bundle groups instance configuration. Describing them does not start instances.
- A **Profile** selects and configures instances from those compiled types and Bundle definitions. Adding a Go extension requires rebuilding; changing configuration among compiled types only requires another Profile.

Start with [quick start](#quick-start), then read [extensions and replacements](#extensions), [versions and forks](#versions), [standard distributions](#distributions), [delivery](#delivery), [troubleshooting](#errors) or the [reference](#reference) as needed.

<a id="quick-start"></a>

## 1. Build an application with an extension

With Go 1.27 installed, run from the complete repository root. Build qi-build in `dist/` and keep the tool plugin application and configuration copy in `dist/tool-plugin/`:

```sh
mkdir -p dist
go -C kernel build -o ../dist/qi-build ./cmd/qi-build
./dist/qi-build \
  --replace github.com/RussellLuo/qi/kernel=./kernel \
  --replace github.com/RussellLuo/qi/builtin=./builtin \
  --with github.com/RussellLuo/qi/builtin/examples/tool-plugin \
  --output ./dist/tool-plugin/qi
cp builtin/examples/tool-plugin/profile.yaml dist/tool-plugin/profile.yaml
./dist/tool-plugin/qi --profile dist/tool-plugin/profile.yaml -- --help
```

The command prints headless task options without activating plugins or calling a model. Follow [the tool plugin example](../../builtin/examples/tool-plugin/README.md) to configure `dist/tool-plugin/profile.yaml`, then use the built application to execute tasks. Kernel can build qi-build independently; generating this application also needs builtin source or an obtainable builtin version. See [independent kernel](#core-only).

<a id="extensions"></a>

## 2. Select extensions and replace dependencies

`--with package=./directory` selects an importable package Extension; the directory is its owning module root, containing `go.mod`. `--replace module=./directory` changes a dependency source without calling its factory. Ordinary Go dependencies do not automatically enable their Extensions.

Core is always included and `builtin.Extension()` is called once. **`github.com/RussellLuo/qi/kernel` is not a required argument**; quick start specifies it because this checkout supplies unpublished local core. Use core `--with` or `--replace` only to override its version/source.

Quick start's `--with` publishes the `example/tool-plugin` type so the Profile can declare a `greet` instance. Builtin already includes its Tool Runtime, model provider and headless; do not select those factories again.

`--replace` maps local kernel/builtin source without publishing their types again. Depending on a Go package only supplies code; publishing an extension's plugin types still requires selecting its `Extension()` package.

A process tool also needs its child executable at deployment time. See [the process tool example](../../builtin/examples/tool-process/README.md) for that separate deployment step.

Local paths resolve from the caller's cwd, not from the temporary generated project. Quote paths containing spaces. Each replacement directory needs a matching `go.mod`; the builder does not discover neighboring modules or inherit `go.work` mappings. See [source constraints](#source-rules) if a module has its own replacements.

Select multiple factory packages from one module with, for example, `--with example.com/extensions/greet --with example.com/extensions/format --replace example.com/extensions=./extensions`. Each package may instead use an inline `--with package=./extensions` mapping to the same module root. Nested modules need their own explicit mappings; a parent source mapping cannot cross that boundary.

<a id="versions"></a>

## 3. Choose versions and remote forks

Queries accept `latest`, tags, pseudo-versions, branches and commits through Go. Without a core argument, the query is `QI_VERSION` or otherwise `latest`; with no extensions, the result still includes the default builtin collection. An extension without a query also defaults to `latest`.

Core query priority is **explicit core `--with` version > positional version > `QI_VERSION` > `latest`**. Conflicting explicit positional/core `--with` versions fail. A source-only core replacement preserves the positional/environment query.

For local source without an explicit query, no default remote query runs: the builder uses a development placeholder (`v0.0.0`, or `v2.0.0` for `/v2`). Explicit fixed versions with local source also need no download of that module. Explicit floating queries still query remotely. Use the correct major-version module path.

These remote commands are **syntax placeholders**; public Qi/plugin installation and releases remain unverified. Replace every `<...>` with a real obtainable value and choose a new output path:

```text
qi-build <qi-tag-or-commit> --with <extension-module>@<tag-or-commit> --output ./dist/qi
qi-build --with <extension-module> --output ./dist/qi
qi-build --with <extension-module>=<fork-module>@<branch-or-commit> --output ./dist/qi
qi-build --replace <dependency-module>@<fixed-version>=<fork-module>@<tag> --output ./dist/qi
```

A fork keeps the original import path; the record identifies the replacement module/version and sums. The optional left-hand `--replace` version must be fixed, not a branch; a remote target with no query defaults to `latest`.

Go's minimal version selection (MVS) can adjust requested versions. Records distinguish the query, required version and final graph. Re-running a branch or `latest` may change the result; saved records support audit, not repeatability of the floating command. Legacy manifests retain strict version equality.

<a id="distributions"></a>

## 4. Workspace distribution builds

See the [workspace guide](https://github.com/RussellLuo/qi/tree/main/docs/workspace-building.md).

<a id="delivery"></a>

## 5. Use and inspect the output

Success delivers a binary and `<output>.build/`. Either existing path causes refusal to overwrite; failures deliver no new binary or partial record. The default output is `./qi` (`./qi.exe` on Windows). Use an explicit output directory such as `dist/` to keep generated files under the root `.gitignore` exclusion.

Runtime needs the binary, the caller's Profile and any configured external resources. Go, source and build records are not needed; builtin's base YAML is embedded.

| Path inside `.build/` | Purpose |
| --- | --- |
| `request.json`, `resolved-build.json` | New CLI request, cwd, queries, resolved versions and replacements |
| `record.json` | Actual module graph, local input hashes, environment, binary SHA-256 and log index |
| `project/` | Generated entrypoint/factory aggregation and final `go.mod` / `go.sum` |
| `dependencies.json`, `logs/` | Production package closure and command results |
| `manifest-input.yaml`, `manifest.yaml` | Original/normalized manifest, legacy mode only |

New records use `qi.build-record/v1alpha2`; legacy uses v1alpha1. Every local mapping participates in hashes, including ordinary dependency replacements. Digests cover module files and compilation-used sources/embedded resources, checked again after compilation. `localDevelopment=true` marks local input. Records store paths and hashes, not a full source backup. See [fixed-project recompilation](#recompile).

<a id="rebuild"></a>

### Rebuilding

`qi-build` refuses existing binaries or their `.build/` records. To rebuild this guide's tool plugin example, remove both, then repeat the original build command:

```sh
rm -f dist/tool-plugin/qi
rm -rf dist/tool-plugin/qi.build
```

Alternatively, choose another directory with `--output`. Apply the same rule to each example's own binary and `.build/` records; keep Profiles, working files and Session history. Ordinary `go build` can replace an existing binary.

<a id="errors"></a>

## 6. Resolve common errors

| Symptom | Action |
| --- | --- |
| Output or `.build` exists | See [rebuilding](#rebuild): remove the old binary and record, or choose another directory |
| Module identity mismatch / nested module boundary | Use the factory package with its owning module source; `--replace` keys must be module paths |
| Missing/wrong factory, package is `main`, duplicate IDs or invalid schema | Check the [package factory contract](#extension-contract) and registration ownership |
| Source has `go.mod` replacements | Move mappings to CLI `--replace`; see [source constraints](#source-rules) |
| Unexpected download / dependency unavailable | Check `QI_VERSION` and explicit queries, then provide missing local mappings or accessible dependencies/cache |
| Unknown Plugin/Bundle in a Profile | Select its Extension when building; Go dependency alone does not register it |
| Missing/invalid Profile or credentials | Supply `--profile` and prepare provider configuration/environment |
| Mixed input modes / `--source` without manifest | Use either the new CLI or [legacy mode](#legacy) |
| Local input changed during build | Retry with a stable source snapshot |

<a id="reference"></a>

## 7. Reference

### Complete command syntax

```text
qi-build [<qi_version>]
  [--with <package[@version][=replacement]> ...]
  [--replace <module[@version]=replacement> ...]
  [--output <file>]

qi-build --manifest <build.yaml> --output <file>
  [--source <selected-module>=<directory> ...]
```

One optional core version can appear before or after flags. `-h` / `--help` returns before source/network access. `qi-build` has no `build` subcommand, `--core` flag or `--with ./directory` shorthand; application execution is directly `qi --profile <path>`, without a `run` subcommand.

| Input | Effect |
| --- | --- |
| `--with package` / `package@query` | Select a root/subpackage Extension using latest/the query |
| `--with package=./directory` | Select local module source with an implicit development version |
| `--with package@query=./directory` | Keep the module version request and override its source |
| `--with module=fork/module@query` | Select the original import path with a remote fork |
| `--replace module[@fixed-version]=replacement` | Replace a dependency without selecting its Extension |
| `--output file` | Choose binary path; record path is `<file>.build` |

<a id="source-rules"></a>

### Source and replacement constraints

Local paths become absolute with symlinks resolved before project generation, and must contain a regular `go.mod` declaring the mapped module. Source `go.mod` replaces are rejected: declare them with CLI `--replace` (legacy: `--source` for selected modules). Missing mappings still require obtainable dependencies; the builder does not silently change versions and retry.

Duplicate explicit `--with` packages, repeated `--replace` keys and conflicting/overlapping replacements fail. Packages owned by one module share one require/replace: one explicit query applies to its implicit selections; different explicit queries or source directories fail. Identical inline package mappings and module mappings merge. Registrations from separate factories are still composed independently and duplicate IDs fail. An unused replacement records `used=false` without enabling code; actual graph Replace paths and sums identify source, not the requested version alone.

<a id="extension-contract"></a>

### Extension author contract

Each selectable extension is an importable root package or subpackage of a Go module, exporting:

```go
func Extension() application.Extension
```

The factory purely describes registrations/Bundles; it creates no Host, starts no instances/listeners/processes and reads no runtime credentials. Generated code explicitly composes selected factories and builtin with `application.Compose`, then calls `boot.Run`. Missing packages/factories, a `main` package, wrong signatures, duplicate Plugin/Bundle IDs and invalid metadata/schemas fail before delivery. See the [SDK overview](../core/README.md).

A module can publish multiple Plugin types. Implementations referenced by its total Extension enter the compilation closure, even when the Profile leaves them inactive. Publish separate factory packages for independent code selection; packages in one module share its version/source. Selecting a dependency factory already owned by an aggregate can cause duplicate IDs; the builder does not deduplicate or discover factories globally.

<a id="legacy"></a>

### Legacy manifest compatibility

`qi.build/v1alpha1` keeps fixed versions, explicit `execution: local`, strict unknown-field/single-document checks and optional `package` (default: module root). Both CLI modes support subpackage factories; Process execution and legacy final version upgrades fail. It ignores `QI_VERSION`, cannot mix with with/replace/core positional arguments, and requires `--output`. `--source` maps only selected core/plugin modules; other dependencies must be available to Go.

<details>
<summary>Local legacy example (complete quick start first, then run from the repository root)</summary>

`v0.0.0` is a local placeholder, not a published release.

```sh
cat > dist/build.yaml <<'YAML'
apiVersion: qi.build/v1alpha1
core:
  version: v0.0.0
plugins:
  - module: github.com/RussellLuo/qi/builtin
    package: github.com/RussellLuo/qi/builtin/examples/tool-plugin
    version: v0.0.0
    execution: local
YAML

./dist/qi-build \
  --manifest dist/build.yaml \
  --source github.com/RussellLuo/qi/kernel=./kernel \
  --source github.com/RussellLuo/qi/builtin=./builtin \
  --output ./dist/legacy/qi
./dist/legacy/qi --profile dist/tool-plugin/profile.yaml -- --help
```

</details>

<a id="recompile"></a>

### Recompile a saved project

Keep the same source snapshots, local replacement paths and toolchain, with dependencies available. From the monorepo root, using quick start's output:

```sh
mkdir -p dist/recompiled
GOWORK=off go -C dist/tool-plugin/qi.build/project mod download all
GOWORK=off go -C dist/tool-plugin/qi.build/project mod verify
GOWORK=off CGO_ENABLED=0 go -C dist/tool-plugin/qi.build/project build \
  -mod=readonly -trimpath -o "$(pwd)/dist/recompiled/qi" .
./dist/recompiled/qi --profile dist/tool-plugin/profile.yaml -- --help
```

The rebuilt application should print the same argument help. This is not a replay subcommand or a guarantee of identical bytes across toolchains.

<a id="core-only"></a>

### Independent kernel and build environment

Kernel tests and qi-build use the public dependencies pinned in kernel/go.mod. Tests use owned builtin fixtures and need no repository builtin, optional plugin source or adjacent Gordis checkout. Run from the repository root:

```sh
GOWORK=off go -C kernel test ./...
mkdir -p dist
GOWORK=off go -C kernel build -o ../dist/qi-build ./cmd/qi-build
```

A generated application always includes the default builtin collection. For local unpublished sources, map both actual module roots:

```sh
./dist/qi-build \
  --replace github.com/RussellLuo/qi/kernel=/path/to/kernel \
  --replace github.com/RussellLuo/qi/builtin=/path/to/builtin \
  --output ./dist/qi
```

The default query applies independently to kernel and builtin; their resolved pseudo-versions may differ. An implicit local source uses a valid development placeholder. The generated go.mod requires both actual modules and the record retains each version/source. No builtin parameter or new manifest/record protocol is introduced. Legacy core.version is the fixed query used for the default module identities; --source may map kernel and builtin separately.

Builds require Go 1.27 and cached or downloadable pinned dependencies; kernel's process tests also need Python 3. The generated program runs without Go or source. Applications select an explicit Profile; provider credentials belong to the environment. Public subdirectory releases use kernel/vX.Y.Z and builtin/vX.Y.Z tags. Use local source mappings for Qi modules that have not been published.

The tool uses Gordis `Run → Prepare → Build`; legacy arguments enter the same Build through a local compatibility parser. Qi owns version policy, Extension/Compose/boot.Run generation and its two record schemas. Compilation uses the current host OS/ARCH, `CGO_ENABLED=0`, `GOWORK=off`, empty `GOFLAGS` and `GOTOOLCHAIN=local`. Cross compilation and custom CGO settings are outside this build policy.
