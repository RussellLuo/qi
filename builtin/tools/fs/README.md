# File tools

[中文](README.zh.md) · [Packages](https://github.com/RussellLuo/qi/tree/main/kernel/core/README.md)

`qi/tools/fs` is a builtin file plugin that contributes two tools to the shared Tool Runtime:

- `read_file`: read UTF-8 text with line numbers and pagination.
- `write_file`: create a file or fully replace an existing file.

Relative paths prefer the calling Session working directory (cwd), falling back to the process working directory captured at plugin activation when no Session cwd is supplied. `cmd/qi` and qi-build applications include the plugin through builtin; a Profile explicitly enables it.

## Enable the tools

Add the `fs` instance to your Profile; no `root` configuration is required:

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
```

This fragment enables the tools. For a command-line application, also include a model provider and `qi/cli/headless`, following the [model guide](../../llm/openai/README.md). Build from the repository root, then enter your workspace and run the completed Profile:

```sh
mkdir -p dist/fs/workspace
go -C builtin build -o ../dist/fs/qi ./cmd/qi
```

Save the completed Profile as `dist/fs/profile.yaml`, then run:

```sh
(cd dist/fs/workspace && ../qi --profile ../profile.yaml -- \
  'Use write_file to create hello.txt containing hello and world on separate lines, then read it with read_file.')
```

The working directory is independent of the Profile file's directory. Headless records its startup cwd in the Session Header; AgentLoop carries it with every tool call. `base` keeps its six service instances; it does not enable file tools. Only one `qi/tools/fs` instance can use a Tool Runtime: another instance conflicts on the same tool names.

## Configuration and arguments

The plugin has three optional read settings, all with defaults. Configuration belongs in the Profile's `config`; tool arguments are JSON objects supplied with each call. Reads always stream, so DSH's whole-file/stream threshold is not exposed.

| Used by | Field | Type | Required / default | Meaning |
| --- | --- | --- | --- | --- |
| Plugin configuration | `read_limit` | Positive integer | 2000 | Default and maximum number of returned lines. |
| Plugin configuration | `read_max_line_length` | Positive integer | 2000 | Maximum retained UTF-16 units per line, excluding the truncation marker, matching DSH's character counting. |
| Plugin configuration | `read_max_bytes` | Positive integer | 51200 | UTF-8 bytes of returned line text and intervening newlines; excludes the envelope/footer. |
| Both tools' arguments | `file_path` | String | Required | A path relative to Session cwd or an absolute path; must not be blank. |
| `read_file` arguments | `offset` | Positive integer | 1 | First returned line number, starting at 1. |
| `read_file` arguments | `limit` | Positive integer | `read_limit` | Maximum lines for this call; cannot exceed `read_limit`. |
| `write_file` arguments | `content` | String | Required | Complete UTF-8 text; may be empty. |

Defaults apply only when fields are omitted. Positive-integer fields reject zero, negative and fractional values. Both configuration and tool arguments reject extra fields. For example:

`read_file`:

```json
{"file_path":"hello.txt","offset":1,"limit":2}
```

`write_file`:

```json
{"file_path":"hello.txt","content":"hello\nworld\n"}
```

## Read and write results

Successful results include structured `Value` and model-visible `Content`. These examples show what each contains.

### Reading

Value contains `path`, `offset`, `lines` with `number` and `text`, and `totalLines`:

```json
{"path":"/path/to/workspace/hello.txt","offset":1,"lines":[{"number":1,"text":"hello"},{"number":2,"text":"world"}],"totalLines":2}
```

Its sole `protocol.TextBlock` uses the DSH envelope:

```text
<path>/path/to/workspace/hello.txt</path>
<type>file</type>
<content>
1: hello
2: world

(End of file - total 2 lines)
</content>
```

Ordinary pagination ends with `(Showing lines S-E of T. Use offset=N to continue.)`; a byte cap uses `(Output capped. Showing lines S-E. Use offset=N to continue.)`. Continue at the suggested offset. An oversized first line may return no lines and suggest the same offset; increase `read_max_bytes` to fit its displayed text.

Long lines append `... (line truncated to N chars)`. Character truncation counts UTF-16 units; a split surrogate pair is rendered as U+FFFD to preserve valid UTF-8. The reader scans to EOF to count all lines and validate UTF-8 while retaining only a bounded window. An empty file allows offset 1; other offsets beyond EOF fail. CRLF is displayed as LF, and a trailing newline adds no extra line.

### Writing

The write Value contains `path`, `operation` (`create` or `update`), `before` (text or null) and `after` (text). `before` and `after` normalize CRLF to LF; disk bytes retain the input's newline form. `before` is null for creation, invalid UTF-8 old content, or when either side reaches the local backend's 10 MiB diff-capture threshold. Null does not prove the file was absent.

```json
{"path":"/path/to/workspace/hello.txt","operation":"create","before":null,"after":"hello\nworld\n"}
```

The model sees only this confirmation, with `Updated file` for replacement:

```text
<path>/path/to/workspace/hello.txt</path>
<type>file</type>
<content>
Created file
</content>
```

## Paths and write behavior

Relative paths use Session cwd; absolute paths are used directly. The output `path` and envelope contain the absolute display path. Cwd is a resolution base; this local backend accesses files under host permissions without workspace confinement. Only regular files are supported. Reads and writes follow symlinks, and writes preserve the link itself. POSIX resolves links before `..`, so `link/../file.txt` reaches the link target's parent.

There is no independent write-size limit. Old-text diff capture is bounded at 10 MiB by default, a backend behavior rather than plugin configuration. Parent directories must already exist. Writes use a temporary file in the same directory and replace the target after validation; failures clean up the temporary file and preserve the original. Writes to the same physical path within one access object are serialized. External modification detection and a prior-read requirement are not provided. Image reads, partial edits, search, Bash, sandbox escalation and UI diff metadata are outside this implementation.

## Go API and lifecycle

Registration and Profile preflight perform no file access. For custom assemblies, compose `builtin.Extension()` once; combining it with `fsplugin.Extension()` duplicates the registration. The latter belongs to `github.com/RussellLuo/qi/builtin/tools/fs/plugin`.

The parent Go package is `fstool`, imported as `github.com/RussellLuo/qi/builtin/tools/fs`. `New(DefaultOptions(""))` creates an `FS` and captures the fallback cwd; `NewReadDefinition(files)` and `NewWriteDefinition(files)` produce independent definitions. Keep the object alive while definitions or calls may use it, then call `FS.Close()`. `Options` expects all limits already resolved and rejects non-positive values. The `plugin` child owns Gordis configuration and registration. Its Scope owner revokes contributions on stop and closes the access object after existing Snapshot/call leases drain. Close every Runtime Snapshot you prepare. Direct Snapshot callers can supply an absolute cwd with `PrepareWithContext(call, tools.ExecutionContext{CWD: cwd})`. Calls on one Snapshot retain independent cwd values; middleware must preserve them. Direct parent-package users can set `Options.CWD` and `Options.DiffBasisMaxBytes` to adjust backend defaults.

Both definitions use `ToolFailureResult`: argument, path, file and limit errors become model-visible failure results. Calls use their context for cancellation; cancellation requests do not imply that admitted work has already exited. Runtime waits for the worker, and Snapshot close and Host shutdown retain leases until it drains.

## Verification

From the repository root:

```sh
go -C builtin test -race ./tools/fs/... ./bundle
```

The parent fixtures cite DSH source and run without that checkout. Read/write unit tests check Value schemas, complete Content text, limits, absolute paths and symlinks. [Execution tests](execution_test.go) directly verify concurrent per-call cwd isolation and cancellation without starting a Profile. [Plugin tests](plugin/plugin_test.go) own configuration, registration, Snapshot leases and shutdown. One [Session cwd test](session_cwd_test.go) retains the real boot → headless → AgentLoop → file-tool path. These local tests require no model service or network and do not prove a real model's tool calls.

Remove `root`, `max_write_bytes` and `diff_basis_max_bytes` from older Profiles. These plugin fields have been removed and preflight rejects them; Session cwd now supplies the working directory.
