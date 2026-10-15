# Builtin OpenAI-compatible model plugin

[中文](README.zh.md)

`cmd/qi` and qi-build generated applications already include `qi/llm/openai-compatible`.
Select a model instance and `qi/cli/headless` in an explicit Profile; no additional
`--with` is needed. The `base` Bundle supplies shared services and does not mount a
model. No credentials or connections are read or opened by `Extension()`.

From the repository root:

```sh
mkdir -p dist/openai
go -C builtin build -o ../dist/openai/qi ./cmd/qi
cat > dist/openai/profile.yaml <<'YAML'
apiVersion: qi.profile/v1alpha1
bundles: [base]
instances:
  - id: model
    plugin: qi/llm/openai-compatible
    config:
      endpoint: https://api.openai.com/v1/chat/completions
      api_key_env: OPENAI_API_KEY
      timeout_seconds: 120
      catalog: [{id: chat, model: your-model}]
  - id: headless
    plugin: qi/cli/headless
    config:
      model: chat
YAML
# Edit model.config.catalog[0].model in dist/openai/profile.yaml for your service.
# Prepare OPENAI_API_KEY in this shell before running Qi.
./dist/openai/qi --profile dist/openai/profile.yaml -- 'Hello' 'world'
```

<a id="model-config"></a>

## Configure a model service

In the model instance's `config`:

- Replace `catalog[].model` with the vendor model name; keep the product `id` used by headless `config.model`.
- Set `endpoint` to the full Chat Completions URL, including its path. Change it for your compatible service.
- Set the variable named by `api_key_env` in the current shell; omit this field for a service without credentials. Qi does not load `.env`.

The examples use `OPENAI_API_KEY`. Model and consumer instances are added to the same Profile; see the [Profile reference](../../../kernel/docs/profiles.md) for declaration and override rules.

## Configuration reference

| Profile setting | Behavior |
| --- | --- |
| `endpoint` | Required absolute HTTP(S) Chat Completions URL. |
| `provider`, `route` | Defaults to `openai`, `chat`. Headless selects a product model ID from `catalog`. |
| `api_key_env` | Reads this named variable only during activation; an empty named variable is an error. |
| `headers` | Additional HTTP headers; use the credential setting for Authorization. |
| `defaults` | Canonical call defaults such as `{maxTokens: 1024}` and `{temperature: 0.2}`. |
| `max_tokens_parameter` | `max_completion_tokens` by default; choose `max_tokens` for older compatible services. No model-name guessing. |
| `reasoning_history` | `omit` by default. Choose `reasoning_content` only when the backend requires this separate history field. Reasoning is never merged into visible text. |
| `timeout_seconds` | Bounds a complete streamed request; zero or omitted uses 120 seconds. |
| `parameters` | Provider-specific JSON fields; canonical fields and both token-limit names cannot be overridden. |
| `catalog`, `context_window` | Optional static model descriptions and context metadata. No remote model discovery. |

The wire format covers one Chat Completions choice, text, function Tool Calls,
Tool results, `reasoning_content` deltas and usage. Refusals become classified
`REFUSAL` errors; `content_filter` becomes `CONTENT_FILTER`. Neither can become an
empty successful answer. Cancellation and timeout stop the HTTP request, while
AgentLoop retains its own durable settlement responsibilities. Responses API,
images/audio and multiple choices are outside this adapter's scope.

## Go API and lifecycle

Adapter registration belongs to the provider Scope. An injected `HTTPClient` is
caller-owned and is not mutated or closed by Qi. Retry decisions belong to Agent
middleware; this plugin does not silently retry requests. The API key can also be
injected programmatically through `Plugin.APIKey` rather than Profile data.

[OpenAI Docs](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
describes the current token-limit fields;
[streaming events](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events)
describes refusal and finish fields. Other compatible services can differ.

[Adapter unit tests](adapter_test.go) cover encoding, SSE, refusal, deadlines (including incomplete response bodies) and reasoning history through an injected transport.
One [Profile test](profile_test.go) retains the provider → headless → AgentLoop → tool continuation path and checks committed Session facts and cleanup. It uses an in-memory HTTP transport without a listener or duplicate TCP variant. `TestRealAPISmoke` is optional:
set `QI_OPENAI_ENDPOINT`, `QI_OPENAI_MODEL` and, if needed, `QI_OPENAI_API_KEY` to
verify a real service's text response. Local tests do not establish compatibility
with every provider or live Tool calling.
