> 此文记录 `eba47cf` 的 Go 本地应用阶段。当前三服务 RPC 部署以 [services/README.md](../services/README.md) 和 [RPC 契约](../contracts/rpc-v1.md) 为准；旧服务命令和 Bearer 接线不适用于新端口。

# AI gateway

`pkg/gateway` implements `pkg/ai.Client` using HTTP and the Go standard library. It is independent of the agent loop, storage, tools, Eino and CLI workshop. A model alias selects a configured protocol, full endpoint URL and upstream model ID.

```go
g, err := gateway.New(gateway.Config{
    Models: map[string]gateway.Model{
        "primary": {
            Protocol: "responses",
            Endpoint: "https://api.openai.com/v1/responses",
            Model: "YOUR_UPSTREAM_MODEL_ID",
            APIKey: os.Getenv("OPENAI_API_KEY"),
        },
    },
    Observer: func(o gateway.Observation) { /* metrics, no prompts */ },
})
if err != nil { return err }
response, err := g.Complete(ctx, ai.Request{
    Model: "primary",
    Messages: []ai.Message{{Role: "user", Content: []ai.Block{
        {Type: "text", Text: "Hello"},
    }}},
}, nil)
```

`HTTPClient` in `gateway.Config` optionally supplies a transport and timeout. The gateway copies configuration maps and the HTTP client; redirects are disabled to keep credentials at the configured endpoint. The default client timeout is two minutes. `Models()` returns sorted aliases, without endpoint URLs or credentials.

## Protocol support

| Protocol | Requests and responses | Streaming terminal |
| --- | --- | --- |
| `chat_completions` | Messages, images, function tools/results, text, `reasoning_content`, usage/cache reads | A valid finish reason followed by `[DONE]` |
| `responses` | Input messages/images, function calls/outputs, text, opaque reasoning items including encrypted state, usage/cache reads | `response.completed` with a completed response |
| `anthropic` | System blocks, Messages/images, `tool_use`/`tool_result`, signed and redacted thinking, usage/cache reads/writes | Closed content blocks, stop reason and `message_stop` |
| `custom` | Configured object paths containing canonical messages/tools/content and usage | Unsupported; a streaming request fails before HTTP dispatch |

Function call IDs survive tool-result round trips. `IsError` maps to Anthropic's native flag; Chat/Responses encode an error result as a JSON string with `is_error` and `content`. Tool choice accepts `auto`, `none` and `required` (`any` for Anthropic). Responses function schemas set `strict: false` to retain the caller's schema semantics. Anthropic needs a positive token budget supplied with `MaxOutputTokens` or model defaults. Token budget names are translated to `max_completion_tokens`, `max_output_tokens` or `max_tokens` as appropriate.

Reasoning continuation uses `ai.Block.ProviderState`: a JSON envelope with `protocol` and `value`. Preserve the entire returned assistant message when appending tool results. Chat stores/replays `reasoning_content`; Responses stores/replays the complete reasoning item, including an empty summary and opaque encrypted content; Anthropic stores/replays the complete thinking or redacted-thinking block, including signatures assembled from streaming deltas. State from another protocol is rejected. Plain assistant reasoning without opaque state is accepted: Chat uses its reasoning field, while Responses/Anthropic retain that text as ordinary assistant text instead of inventing provider signatures or item IDs. Do not print opaque state in telemetry.

Unsupported roles, content blocks, built-in provider tool outputs and finish reasons fail explicitly. This package does not implement audio/video generation, server-side provider tools, multiple chat choices, persistent provider conversations, async background responses or retries. It does not execute tools. Model-specific parameters can still cause an upstream rejection; the gateway does not guess model capability or repair an invalid request.

## Streaming and errors

Pass a non-nil `func(ai.Event) error` to `Complete` to request native SSE. Deltas are delivered as each complete SSE event arrives, before the provider finishes. Callback errors stop reading and close the upstream response; the original callback error is available through `errors.Is`/`errors.As`. Context cancellation preserves the context error in the error chain.

Deltas are provisional. Only the response returned with a nil error is authoritative. Responses uses the complete response supplied in `response.completed`; Chat and Anthropic reconstruct their final response from deltas. Do not execute partially streamed tool arguments. No retry occurs after partial output (or before output).

Missing terminal events, invalid JSON/tool arguments, provider error events, `failed`/`incomplete` Responses statuses and truncated token budgets return errors. Unknown informational SSE events may be ignored; unsupported final output blocks cannot silently disappear. Limits bound total response bytes, SSE line/event bytes and total streaming bytes. Defaults: 16 MiB JSON bodies, 64 MiB streams, 1 MiB SSE lines/events. Limits are configurable on gateway and remote client. `Complete` discards a partial final response on failure; already emitted deltas cannot be retracted.

Errors have a stable `gateway.Error.Code`, a sanitized static message and optional HTTP status. Upstream response bodies, raw network error URLs, prompts and credential values are excluded from the public message. The error chain may contain an underlying network/callback error for local diagnosis; do not serialize it into logs.

## Parameters, prices and observations

`Model.Parameters` holds JSON defaults. `Request.Parameters` overrides those defaults, then typed request knobs override their corresponding defaults/extra parameters. `ParameterMap` maps canonical or vendor parameter names to top-level vendor fields; conflicting names in the same layer fail. Mapping destinations cannot include object paths. Structural fields (model/messages/input/system/tools/stream), authentication fields and provider conversation controls are reserved, even through a parameter map. Custom mapped request roots are protected too. Headers are model configuration only; generated content type, accept, auth, host and transfer headers cannot be replaced.

`Pricing` is optional and has `Currency`, `InputPerMillion`, `OutputPerMillion`, `CacheReadPerMillion` and `CacheWritePerMillion`. Supply your own agreed rates; the example asserts no current prices. Rates must be finite and nonnegative. A configured zero is an explicit zero rate. Canonical `Usage.InputTokens` includes cache reads and writes; Anthropic's separate counters are normalized into that total. Cost charges uncached input, cached reads, cached writes and output once each. Missing usage or pricing means `Cost.Known == false`, not a free request. Cumulative streaming usage replaces previous counters, rather than adding them.

`Observer` runs synchronously exactly once per `Complete`, including unknown aliases, validation failures, cancellation and provider failures. The callback must support concurrent calls and must not panic. Fields: request ID, model alias, protocol, HTTP status (zero before a response), latency, first-delta duration plus `HasFirstDelta`, usage, cost and error code. No messages, tool arguments, endpoint URLs, provider state or credentials are included. Durations serialize as nanoseconds. A missing request ID is generated by the gateway. Usage/cost remain unknown on a failed request because no authoritative response was completed; a failed provider call can still incur upstream charges.

## HTTP server and remote client

```go
handler := gateway.NewHandler(g, gateway.HandlerConfig{
    BearerToken: os.Getenv("AI_GATEWAY_BEARER_TOKEN"),
    MaxRequestBytes: 16 << 20,
})

remote, err := gateway.NewHTTPClient(gateway.HTTPClientConfig{
    Endpoint: "http://127.0.0.1:8090/v1/generate", // full URL
    BearerToken: os.Getenv("AI_GATEWAY_BEARER_TOKEN"),
})
// remote implements ai.Client; Complete has the same JSON/streaming contract.
```

- `GET /healthz`: public readiness of the process, without probing providers.
- `GET /v1/models`: `{"models":["alias", ...]}`; bearer protected when configured.
- `POST /v1/generate`: flattened `ai.Request` plus optional `"stream":true`.

JSON returns `ai.Response`; errors return `{"error":{"code":"...","message":"...","status":...}}`. Generation validates methods and request size, rejects unknown/trailing JSON, and enforces bearer authentication when configured. Streaming uses `event: delta` containing `ai.Event`, then exactly one `event: response` containing the authoritative `ai.Response`, or `event: error` if failure follows a delta. An error before the first event uses a JSON error and an HTTP error status. The remote client recognizes both forms and requires the final response event. Bearer checking covers generation/model listing; health remains public.

## Standalone process

```sh
go run ./cmd/ai-gateway -config ./configs/ai-gateway.example.json
# default address: 127.0.0.1:8090
```

Copy the example, remove unused model entries and replace placeholder model IDs/endpoints. Provide the named environment variables through your operator environment. The JSON file accepts `api_key_env`, `bearer_token_env` and `header_env` references; literal `api_key` and `bearer_token` fields are rejected. Literal credential-looking headers (key/token/auth/secret/cookie) are rejected; use environment references. A named but missing or empty variable fails startup. Do not put credentials in endpoint URLs; URL user information is rejected. `api_key_env` populates the standard protocol auth header. Additional vendor credentials can use `header_env`; it does not override the protocol's generated Authorization/X-Api-Key header.

The command writes one concurrency-safe JSON observation per generation call to stderr. It rejects non-loopback listening addresses unless a bearer token has been resolved. For example, `-listen 0.0.0.0:8090` requires `bearer_token_env`. Binding publicly and deploying are operator actions. The process handles SIGINT/SIGTERM with a ten-second graceful shutdown window; active requests may finish during that window, then connections close if the deadline expires.

## Custom JSON mapping

Paths are dot-separated object keys only. There is no expression language, array indexing or arbitrary code execution. Request keys: `model`, `messages` (required), plus optional `tools`, `max_output_tokens`, `temperature`, `tool_choice`. If a request uses a field without a mapping, it fails explicitly. Mapped messages/tools retain the canonical JSON structures from `pkg/ai`.

Response keys: optional `id`, exactly one of `text`/`content`, required `finish_reason`, optional `usage.input_tokens`, `usage.output_tokens`, `usage.cache_read_tokens`, `usage.cache_write_tokens`. `content` must be a canonical block array. Missing configured paths, overlapping paths or incompatible value types fail. Canonical input usage already includes cache subsets. Finish reasons accept `stop`/`completed`/`end_turn` and `tool_calls`/`tool_use`; truncation/refusal reasons are errors. This adapter supports object-field mapping, not a universal transformation of unrelated provider message schemas.

## Verification and source references

```sh
go test ./pkg/gateway/... ./cmd/ai-gateway/...
go test -race ./pkg/gateway/... ./cmd/ai-gateway/...
go vet ./pkg/gateway/... ./cmd/ai-gateway/...
```

Tests use real local `httptest` HTTP servers and a real command subprocess. They cover each protocol's tool calls/results, two-turn reasoning/state replay, split streaming arguments, cumulative cache usage, immediate callback delivery, provider errors/truncation, callback errors/cancellation, bounded reads, protected parameters, remote handler/client and command shutdown. They require no live provider credentials and do not claim live LLM verification.

Protocol references consulted during implementation: [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling), [OpenAI streaming responses](https://developers.openai.com/api/docs/guides/streaming-responses), [OpenAI reasoning continuation](https://developers.openai.com/api/docs/guides/reasoning), [Anthropic SSE messages](https://platform.claude.com/docs/en/build-with-claude/streaming), [Anthropic thinking preservation](https://platform.claude.com/docs/en/build-with-claude/extended-thinking).
