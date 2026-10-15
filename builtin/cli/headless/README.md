# Headless task consumer

English | [中文](README.zh.md)

`qi/cli/headless` accepts one task per invocation, prints its result and exits. The builtin application includes this type; a Profile explicitly enables it. For model setup and a first run, follow [getting started](../../docs/getting-started.md).

## Enable and select a model

Add this instance to a Profile that already provides foundational services and a model provider:

```yaml
instances:
  - id: headless
    plugin: qi/cli/headless
    config:
      model: coding-model
```

`model` is a product model ID from the provider's catalog, not its vendor model name. Omission requires an explicit default model contribution. `base` adds no consumer. See the [Profile reference](../../../kernel/docs/profiles.md) for fields and overrides, and [OpenAI-compatible](../../llm/openai/README.md) for model settings.

## Commands and input

```text
qi --profile <path> -- [options] [task...]
```

The first `--` separates launcher arguments from task consumer arguments.

| Option | Purpose |
| --- | --- |
| `--session-id <id>` | Resume this exact Session; requires persistent storage. Omission creates a new Session. |
| `--json` | Write execution events to stdout as newline-delimited JSON. |
| `-h`, `--help` | Show application help without reading task input or starting a Host. |

Here `qi` stands for the built executable, and `profile.yaml` must include a model and headless:

```sh
qi --profile profile.yaml -- 'Hello' 'world'
printf '%s\n' 'Hello world' | qi --profile profile.yaml -- -
qi --profile profile.yaml -- --session-id 'session-...' 'Continue the previous task'
```

Multiple task words form one task joined by spaces. A standalone `-`, or no task arguments with non-terminal stdin, reads complete UTF-8 input; empty input fails. `-` must be the only task argument. For a task starting with `-`, use a second `--` to end consumer options: `qi --profile profile.yaml -- -- '-task'`. Use `qi --help` for launcher help and `qi --profile profile.yaml -- --help` for the selected application's options.

## Output and result interpretation

Plain mode writes the final answer to stdout with a trailing newline. Reasoning deltas stream to stderr under a shared `qi: reasoning:` heading; diagnostics also go to stderr.

`--json` writes one JSON event per line: execution content is projected from committed facts, alongside Session metadata, final text and invocation errors. `thinking` and `text` are not live token deltas. This excerpt shows a normally completed invocation, omitting step and text events:

```json
{"type":"session","sessionId":"session-...","cwd":"/path/to/workspace"}
{"type":"status","phase":"turn_end","turn":1,"reason":{"kind":"completed"}}
{"type":"final","text":"Hello world"}
```

| Event `type` | Main content |
| --- | --- |
| `session` | `sessionId` and `cwd` for later resume. |
| `status` | `phase` is `turn_start`, `step_start`, `step_end` or `turn_end`; a turn ending includes `reason`. |
| `thinking`, `text` | Reasoning or body text from committed messages, in `text`. |
| `tool_call`, `tool_result` | Tool calls and model-facing results, correlated by `callId`. |
| `final` | Final text for this invocation. Failed turns can also contain text. |
| `error` | Invocation error described by `message`. |

Check the event with `type == "status"` and `phase == "turn_end"`: `reason.kind == "completed"` means the turn completed normally. Also check the process exit code for output and cleanup success. A `final` event or live reasoning alone does not establish completion. Task requirements such as correct file contents or passing tests still need direct verification.

## Resume and execution limits

- `--session-id` requires a [persistence provider such as JSONL](../../session/jsonl/README.md), the original working directory and the same persistence directory. Memory Sessions, missing or occupied histories, mismatched cwd, Presets and subagent/fork histories cannot be resumed.
- Each command handles one task; separate invocations can continue the same Session.
- Each turn defaults to at most 8 Steps. A Step contains a model request and its tool handling; model retries stay within the same Step. Attempting a ninth Step ends with an error. Headless currently has no CLI or Profile field to change this limit.

## Go API and lifecycle

`Plugin`, `Parser` and `Extension` form the public consumer API. A handwritten entry composes builtin once; selecting headless separately duplicates its registration. Model selection resolves at Ready and prepares/releases a validation lease without calling Stream. Failure rolls back activated services before task submission.

Tasks acquire a Handle through Factory.Create or Factory.Resume. Results come from committed facts in the current invocation interval. Shutdown drains and disposes the Handle before closing the Host; append, output, cancellation and cleanup errors propagate through the invocation. See [kernel architecture](../../../kernel/docs/architecture.md) for execution and ownership.

Input preparation can be canceled without closing borrowed stdin; an uninterruptible underlying Read can survive until process exit. JSONL resume opens history through StoreOpener.Open; old headers without cwd are rejected before repair writes.
