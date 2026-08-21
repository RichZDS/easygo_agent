# Architecture

## Modules

```text
cmd/agent -> internal/app -> internal/config
                          |-> internal/tui -> internal/gateway
                          |-> internal/chatmodel -> Eino OpenAI adapter
                          |-> internal/tools -> Eino Tool
                          `-> internal/observability -> Zap + OTel

internal/gateway -> Eino ReAct
```

- `app` assembles and shuts down process resources.
- `config` strictly loads non-secret YAML and injects the API Key from the environment.
- `tui` owns terminal state and current-process conversation history.
- `gateway` owns Eino conversion, ReAct execution, stream cleanup, Tool event projection, cancellation, and error normalization.
- `chatmodel` constructs one OpenAI-compatible `ToolCallingChatModel`.
- `tools` provides the fixed Calculator example through Eino's native Tool interface.
- `observability` constructs Zap, OTel, and the payload-blind Eino callback bridge.

There is no `pkg` directory because this repository is designed to be copied or forked, not imported as a stable Go library.

## Run Flow

```text
user input
  -> TUI appends the user message to process-local history
  -> Gateway validates stable messages
  -> Gateway starts an asynchronous Eino ReAct stream
  -> model may request Calculator
  -> Gateway projects ToolStart / ToolEnd
  -> Gateway projects TextDelta chunks
  -> Gateway emits exactly one Completed / Canceled / Failed event
  -> TUI appends only Completed assistant text to model history
```

Eino startup occurs inside the Gateway producer. This matters because ReAct may synchronously inspect the first model chunk to determine whether it contains Tool calls. Returning the outward Stream first allows the caller to cancel even before that first chunk arrives.

## Gateway Contract

The outward request contains only text messages with `system`, `user`, or `assistant` roles. It contains no Eino types, user identity, provider identity, permissions, quotas, or transport metadata.

Events:

- `TextDelta`: incremental assistant text;
- `ToolStart`: Tool name only;
- `ToolEnd`: Tool name and optional error;
- `Completed`: authoritative final assistant text;
- `Canceled`: caller cancellation;
- `Failed`: non-cancellation runtime failure.

After an outward Stream is returned, exactly one terminal event is produced, followed by `io.EOF`. `Close` is idempotent, cancels active work, and waits for the producer to release its Eino stream.

Gateway instances hold only immutable Agent dependencies. Per-run history, callbacks, contexts, streams, spans, and event queues are independent, so separate runs may execute concurrently.

## TUI State

The Bubble Tea adapter has three states:

- `idle`: input is editable and `Enter` submits;
- `running`: one Gateway Stream is active and duplicate submission is ignored;
- `quitting`: Bubble Tea exits.

All state mutation happens in Bubble Tea `Update`. Blocking `Recv` and `Close` operations run as commands that return messages to `Update`.

Current-process history begins with the configured system prompt. User messages and completed assistant messages are reused. Tool events and partial failed/canceled output are display-only.

## Cancellation and Shutdown

- Running `Ctrl+C` cancels the run context.
- Idle `Ctrl+C`, `SIGINT`, or `SIGTERM` exits the program.
- Gateway cancellation closes the Eino stream and emits `Canceled`.
- Application shutdown flushes OTel and then Zap.

## Observability

Tracing is no-op by default. When enabled, `agent.run` is the root span and the Eino callback bridge creates model and Tool child spans. The bridge does not inspect callback payloads, preventing prompt, response, Tool input, and Tool output from entering trace attributes.

Callbacks are injected for each run rather than registered globally. Concurrent runs therefore do not share callback state.
