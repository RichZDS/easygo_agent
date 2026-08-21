# Eino TUI Template Redesign

**Date:** 2026-08-20  
**Status:** Approved design; implementation not started

## 1. Purpose

Refactor `easygo-agent` from a full-stack application into a small, runnable Go template for deriving future Agent projects.

The template demonstrates only a few foundational integrations:

- a terminal UI;
- a transport-neutral Agent gateway;
- Eino's native ReAct Agent;
- one OpenAI-compatible chat model;
- one deterministic Eino Tool example;
- optional OpenTelemetry tracing.

The project optimizes for copying or forking and then modifying. It does not promise compatibility as an importable Go library, and it does not attempt to provide production-ready HTTP APIs, persistence, authentication, authorization, orchestration, or plugin infrastructure.

## 2. Confirmed Decisions

1. This is a destructive redesign. Existing package paths, configuration formats, command arguments, and old tests do not need compatibility layers.
2. Business orchestration uses Eino native interfaces. Mature libraries may be used for the TUI, configuration, logging, and telemetry.
3. The default Agent is Eino's classic ReAct Agent with one minimal Tool.
4. Tools use Eino's native `tool.BaseTool` and `tool.InvokableTool` interfaces. The project will not define an equivalent Tool interface.
5. Memory implementation and abstractions are removed. The TUI keeps messages only for the life of the current process.
6. MCP implementation and abstractions are not invented. Memory and MCP are documented as deferred extension points.
7. No independent loop system remains. ReAct iteration and `MaxStep` are owned by Eino.
8. The earlier “everything is a component/plugin” idea is deferred. There is no universal registry, plugin kernel, dynamic discovery system, or lifecycle framework.
9. The Gateway remains available as an outward seam, but this template exposes no HTTP, gRPC, SSE, or message-queue endpoint.
10. The default template binds one model. Derived projects may add routing, fallback, or multiple models.
11. All user identity, model authorization, provider ownership, entitlement, quota, model allowlist, and enabled-state logic is removed.
12. Model connectivity fields are still validated so startup fails clearly when the template cannot run.
13. The default tracing adapter uses OpenTelemetry with an opt-in stdout exporter.
14. The whole React/Vite frontend, including its nested Git metadata, may be deleted.
15. Tracked Skill implementation and examples may be deleted. Ignored runtime Skill directories must first be inventoried and are not automatically deleted.
16. Existing unrelated or ambiguous working-tree changes must be preserved. No reset, staging, commit, rebase, push, or pull request is authorized.

## 3. Architecture Choice

Use a deep Gateway with outer adapters.

```text
TUI adapter ----> Gateway ----> Eino ReAct
                    |             |-- OpenAI-compatible model
Future adapter -----+             `-- Calculator Tool
                    |
                    `----------> OpenTelemetry callbacks
```

The rejected alternatives are:

- **TUI calls Eino directly:** less code initially, but duplicates event conversion and cancellation when a future HTTP adapter is added.
- **Dynamic plugin kernel:** creates hypothetical seams and registration machinery before the template has multiple real adapters.

The Gateway earns its seam by hiding Eino message conversion, streaming mechanics, Tool lifecycle projection, cancellation, cleanup, and error normalization from every transport adapter.

## 4. Proposed Repository Shape

```text
easygo_agent/
|-- cmd/agent/
|   `-- main.go
|-- configs/
|   `-- config.example.yaml
|-- internal/
|   |-- app/
|   |-- chatmodel/
|   |-- config/
|   |-- gateway/
|   |-- observability/
|   |-- tools/
|   `-- tui/
|-- docs/
|   |-- architecture.md
|   `-- extensions.md
|-- .env.example
|-- README.md
|-- go.mod
`-- go.sum
```

### 4.1 Module responsibilities

- `cmd/agent`: a minimal process entry point.
- `internal/app`: dependency construction, TUI lifecycle, and ordered resource shutdown. It contains no Agent rules.
- `internal/config`: YAML and environment loading plus runtime field validation.
- `internal/chatmodel`: construction of the Eino OpenAI-compatible chat-model adapter.
- `internal/gateway`: the stable run interface and its Eino ReAct implementation.
- `internal/tools`: the Calculator Tool and the fixed Tool assembly point.
- `internal/observability`: Zap setup, OTel setup, and the Eino callback-to-span bridge.
- `internal/tui`: Bubble Tea state, commands, rendering, and current-process conversation history.

There is no `pkg` directory. Production modules use concrete dependencies or Eino interfaces. A small consumer-owned Runner interface is allowed inside `internal/tui` so TUI state tests can use a fake adapter.

## 5. Dependency Direction

```text
cmd -> app -> config
          |-> tui -> gateway
          |-> gateway -> Eino ReAct
          |-> chatmodel -> Eino OpenAI-compatible adapter
          |-> tools -> Eino Tool
          `-> observability -> Zap + Eino callbacks + OTel
```

Dependencies are passed into constructors. Production packages do not create hidden global dependencies. Eino callbacks are attached to each run; they are not registered globally.

## 6. Gateway Interface

The exact Go declarations may be refined for idiomatic naming during implementation, but the behavioral interface is fixed.

```go
type Request struct {
	Messages []Message
}

type Gateway struct {
	// Owns the configured ReAct Agent and event projection.
}

func (g *Gateway) Run(ctx context.Context, req Request) (*Stream, error)
func (s *Stream) Recv() (Event, error)
func (s *Stream) Close() error
```

`Message` exposes only stable role and content fields. Eino `schema.Message`, ReAct steps, callback payloads, and stream readers stay inside `internal/gateway`.

### 6.1 Events

- `TextDelta`: incremental assistant text.
- `ToolStart`: Tool name and invocation start.
- `ToolEnd`: Tool name and completion or Tool error.
- `Completed`: the authoritative final assistant message.
- `Canceled`: caller-requested cancellation.
- `Failed`: a non-cancellation run error.

### 6.2 Stream contract

1. `Run` returns only validation or setup errors synchronously.
2. After `Run` returns a Stream, exactly one terminal event is produced: `Completed`, `Canceled`, or `Failed`.
3. `Recv` returns `io.EOF` after the terminal event.
4. `Close` is idempotent. Closing an active Stream cancels it and closes the underlying Eino stream reader.
5. `context.Context` is the only cancellation mechanism.
6. The Gateway stores no conversation state and contains no mutable per-run global state.
7. Separate runs can execute concurrently.
8. A Tool error first appears in `ToolEnd`; Eino ReAct decides whether that error ends or continues the run.

The request contains no user ID, provider ID, authorization data, quota data, or transport metadata.

## 7. Eino Runtime

Use:

- `github.com/cloudwego/eino/flow/agent/react.NewAgent`;
- `model.ToolCallingChatModel` in `react.AgentConfig`;
- `compose.ToolsNodeConfig` for fixed Tools;
- `react.Agent.Stream` for streaming output;
- per-run compose callback options for event and tracing callbacks.

The current `agenticopenai` adapter is incompatible with classic ReAct's `ToolCallingChatModel` contract and must be removed. Replace it with the classic Eino OpenAI-compatible model adapter.

`MaxStep` is configured through Eino and defaults to `8`. No custom retry loop, execution loop, job loop, or Agent runtime framework is added.

## 8. Calculator Tool

Create the Tool with Eino's `tool/utils.InferTool`.

Input:

```text
operation: add | subtract | multiply | divide
a: number
b: number
```

The Tool has no network, filesystem, database, clock, or random dependency. Division by zero returns a Tool error. It exists to demonstrate argument schema inference, ReAct Tool selection, callbacks, and deterministic tests.

## 9. TUI Behavior

Use Bubble Tea and Lip Gloss with a simple transcript, input area, and status line. Do not build a general-purpose terminal frontend.

States:

- `idle`: accepts and submits input;
- `running`: consumes one Gateway Stream and rejects duplicate submission;
- `quitting`: cancels work and closes resources.

Keys:

- `Enter`: submit input;
- `Ctrl+C` while running: cancel the active run;
- `Ctrl+C` while idle: exit.

The TUI owns current-process history. It sends that complete history in the next Gateway request. A completed assistant message is appended to history. Partial text from a canceled or failed run remains visible but is not appended to future model context. Tool lifecycle events are visible status lines and are not stored as cross-turn history.

Bubble Tea's `Update` function is the only place that mutates UI state. Stream reads return through Bubble Tea commands and messages; background goroutines never mutate the model directly.

The template has no session list, persistent history, model selector, configuration editor, mouse workflow, or rich Markdown renderer.

## 10. Configuration

Committed YAML contains only non-secret values:

```yaml
agent:
  system_prompt: "You are a helpful assistant."
  max_steps: 8

model:
  name: "gpt-4.1-mini"
  base_url: ""
  timeout: 120s

tracing:
  enabled: false
  exporter: stdout
```

The API Key is read only from `EASYGO_AGENT_API_KEY`. `.env.example` may document the variable with an empty value, but the application does not automatically load a real `.env` file.

Validation checks only operational correctness:

- model name is present;
- API Key is present for a real run;
- Base URL, when present, is syntactically valid;
- timeout is positive;
- `max_steps` is positive.

There is no account, ownership, authorization, quota, allowlist, enabled-state, provider-database, model-discovery, or credential-encryption logic.

## 11. Logging and Tracing

Use the project's established `zap.Logger`, not a new logging abstraction. Delete the old request-ID and goroutine-ID mechanisms.

Every function that returns an error logs it before returning, with typed Zap fields relevant to that layer. Repeated propagation is logged at each returning layer because this is a project-level Go rule. Logs never contain credentials, prompts, responses, Tool arguments, or Tool results.

All named functions, methods, test functions, and anonymous functions have an immediately preceding purpose comment. Named function comments begin with the exact function name.

Tracing behavior:

- disabled tracing uses a no-op tracer provider;
- enabled tracing uses the OTel SDK and stdout trace exporter;
- each run owns an `agent.run` span;
- the Eino callback bridge creates model and Tool child spans;
- attributes may include model name, Tool name, duration, and outcome;
- attributes never include prompt, response, Tool input/output, API Key, or HTTP headers;
- tracer shutdown flushes pending spans during application shutdown;
- future projects can replace stdout with OTLP without changing Gateway or TUI behavior.

The Eino dependency set does not currently include a general-purpose official OTel callback adapter. The template therefore contains a thin Eino callback-to-OTel span bridge rather than adding a vendor-specific APM adapter.

## 12. Error Handling

- Configuration, model, logging, or tracer initialization failures occur before TUI startup, are logged safely, are printed concisely, and produce a non-zero exit.
- Runtime errors produce `Failed`; the TUI displays the error and returns to `idle`.
- Cancellation produces `Canceled` and is not logged as an application error.
- Tool errors produce `ToolEnd` with an error outcome before Eino decides whether to continue.
- Errors wrap their cause with `%w` where they are returned.
- The project has no custom numeric error-code platform.
- User-visible errors never include secrets, full provider response bodies, or HTTP headers.

## 13. Deletion Scope

Remove the following old capabilities and their tests, configuration, documentation, and dependencies:

- the `easygo_agent_frontend` gitlink, working tree, and nested `.git` metadata;
- Gin, CORS, HTTP routes, HTTP handlers, controllers, SSE, and response envelopes;
- JWT, accounts, users, middleware authorization, and all model permission checks;
- MySQL, GORM, SQL initialization, provider/model persistence, sessions, chat messages, runs, and cron audit data;
- AES credential encryption and model discovery;
- Memory, compaction, summarization claims, and persistence behavior;
- cron jobs, frame jobs, retries outside Eino, and custom execution loops;
- Skill manifests, stores, synchronization, workspaces, filesystem tools, and hot reload;
- the existing Deep Agent/ADK runtime, `agenticopenai`, old callback wiring, and old Wire graph;
- `compose.yaml` and dependencies that no remaining package imports.

The current tracked configuration contains a remote MySQL address and plaintext password. Removing the file does not remove the secret from Git history. The credential owner must rotate it outside this code change. Git history is not rewritten by this task.

Before implementation, ignored `skills/builtin` and `skills/workspaces` are inventoried. They are not deleted automatically. Existing unrelated or ambiguous working-tree changes are not reset or overwritten.

## 14. Deferred Extensions

These are deliberate future-project TODOs, not partially implemented template features:

- **Memory:** introduce a seam only when a derived project chooses concrete history, checkpoint, or retrieval semantics. Do not add an unused interface now.
- **MCP:** introduce an adapter only when a derived project selects an MCP client/server role and transport. Do not add an unused interface now.
- **HTTP or RPC:** implement a transport adapter over Gateway. Authentication, request limits, serialization, streaming protocol, and deployment are owned by that derived project.
- **Multiple models:** add routing and fallback around model construction only when a derived project has concrete selection rules.
- **OTLP:** replace the stdout exporter during application assembly; Gateway and TUI interfaces remain unchanged.

## 15. Testing Strategy

### 15.1 Gateway

Use a fake `ToolCallingChatModel` with the real Eino ReAct Agent to verify:

- text streaming and final-message projection;
- Calculator Tool invocation and Tool lifecycle events;
- exactly one terminal event;
- `io.EOF` after termination;
- context cancellation;
- idempotent Stream close;
- concurrent independent runs;
- Eino types do not leak through the outward interface.

### 15.2 TUI

Use a consumer-owned minimal Runner interface and fake adapter to verify:

- `idle -> running -> idle` transitions;
- incremental rendering;
- Tool status rendering;
- cancellation behavior;
- completed output enters current-process history;
- canceled and failed partial output does not enter history;
- duplicate submission is rejected while running.

### 15.3 Configuration, Tool, and observability

- Table-test YAML and environment loading, invalid values, and secret absence.
- Table-test all Calculator operations and division by zero.
- Use an OTel in-memory exporter to verify parent/child spans and outcomes.
- Assert that spans and logs contain no API Key, prompt, response, Tool arguments, or Tool output.

Automated tests do not call a real model or external network. README documents an opt-in manual smoke run.

## 16. Verification and Acceptance

Run:

```bash
gofmt
go mod tidy
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Acceptance requires all of the following:

1. The template starts a working TUI and can stream a model response.
2. The ReAct Agent can call the Calculator Tool.
3. The Gateway is transport-neutral and exposes none of Eino's concrete stream types.
4. Cancellation closes the underlying stream without goroutine leaks.
5. Optional stdout tracing produces the expected span hierarchy without sensitive content.
6. The default application has one model and no external API listener.
7. No frontend, authentication, database, Memory implementation, MCP implementation, custom loop, job scheduler, Skill runtime, or HTTP server remains.
8. Documentation describes only implemented behavior and explicitly marks deferred extensions.
9. No ignored runtime data is deleted without separate confirmation.
10. All changes remain unstaged and uncommitted for user review.
