# Configure a Profile

English | [中文](profiles.zh.md)

A Profile is a YAML file read when an application starts. It selects plugin instances and supplies their configuration. It can use only plugin types and Bundles compiled into the application; adding a Go extension requires rebuilding. For a first run, follow [builtin getting started](../../builtin/docs/getting-started.md).

## Declare instances

This complete Profile enables foundational services and file tools. It has no task consumer, so the application stays running until a stop signal. To execute model tasks, add a model and headless as shown in the [coding-agent Profile](../../builtin/examples/coding-agent/profile.yaml).

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
    config:
      read_limit: 100
      read_max_line_length: 1000
      read_max_bytes: 10240
```

| Field | Purpose |
| --- | --- |
| `apiVersion` | Use `qi.profile/v1alpha1`. |
| `bundles` | Expand compiled Bundles in order, such as `base`. Optional. |
| `instances` | Add plugin instances, naming each with `id` and selecting its type with `plugin`. Optional. |
| `instances[].config` | Configuration object defined by the plugin; an empty object when omitted. |
| `overrides` | Change instances previously declared by Bundles or `instances`. Optional. |

`id` names an instance in this runtime graph; `plugin` is a plugin type ID. For example, `fs` and `qi/tools/fs` identify the instance and implementation respectively. Neither is a Go import path.

## Change existing instances

Bundles expand in list order. Each Bundle adds its instances and then applies its overrides; the user's `instances` and `overrides` follow. Instance names cannot be declared twice. Use `overrides` to change an existing instance.

This Profile adjusts the `fs` read limit:

```yaml
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: fs
    plugin: qi/tools/fs
    config:
      read_limit: 100
      read_max_line_length: 1000
      read_max_bytes: 10240
overrides:
  - id: fs
    config:
      read_limit: 50
```

**`config` replaces the whole object; fields are not merged.** The resulting `fs.config` contains only `read_limit: 50`; the other two settings return to defaults of 2000 and 51200. Repeat any custom values you want to keep in the new `config`.

| Override field | Behavior |
| --- | --- |
| `id` | Required; must name an existing instance. |
| `config` | Replaces configuration; omission preserves it, while `{}` clears it. |
| `plugin` | Changes the implementation type. A type change clears the old configuration and `restartToken` before applying this override. |
| `disabled` | `true` excludes the instance from mounting; `false` enables it again. |
| `restartToken` | Instance restart marker. The unified CLI does not watch files; restart the application after editing its Profile. |

For example, the override `{id: fs, disabled: true}` disables file tools. Disabling an instance does not automatically disable its consumers; the remaining graph must still satisfy dependencies. Disabled declarations must reference compiled public types, but their configuration and runtime dependencies are not checked.

## Common errors and paths

- Unknown Plugin or Bundle: check its spelling and whether its Extension is compiled into the application.
- Duplicate instance or missing override target: use `instances` to add and `overrides` to change.
- Missing required settings after an override: check whether you expected a field merge instead of whole-object replacement.
- Profile declarations reject unknown fields, duplicate keys and multiple YAML documents. Declaration fields such as `instances` and `config` cannot be explicit `null`; values inside configuration objects are checked by the plugin schema.

A relative `--profile` path resolves from process cwd. For tool and storage paths, see [file tools](../../builtin/tools/fs/README.md) and [JSONL persistence](../../builtin/session/jsonl/README.md); neither uses the Profile directory as its base.

## Go API

User YAML contains Bundle and override declarations; expansion produces an execution Profile containing only active instances. Public `boot/profile.Profile` and `profile.Parse` operate on execution Profiles, not user YAML with Bundles. Applications load user files through `boot.Run`; see [boot](../boot/README.md) for a handwritten entry.
