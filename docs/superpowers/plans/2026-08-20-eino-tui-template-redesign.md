# Eino TUI Template Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current full-stack application with a runnable TUI template built around a transport-neutral Gateway, Eino's native classic ReAct Agent, one OpenAI-compatible model, one Calculator Tool, and optional OpenTelemetry stdout tracing.

**Architecture:** A deep `internal/gateway` module owns Eino message conversion, ReAct streaming, Tool lifecycle projection, cancellation, and error normalization. Bubble Tea is an outer adapter, while configuration, model construction, Tool construction, and observability are assembled once by `internal/app`; there is no HTTP server, persistence, authorization, Memory, MCP implementation, Skill runtime, custom loop, or plugin kernel.

**Tech Stack:** Go 1.25, Eino v0.9.13, Eino OpenAI adapter v0.1.13, Bubble Tea v1.3.10, Bubbles v0.21.1, Lip Gloss v1.1.0, Zap v1.28.0, OpenTelemetry Go v1.45.0, YAML v3.

## Global Constraints

- Work in `/Users/user/Desktop/Code/agent_go/easygo_agent`; this is the real Git and Go module root.
- Preserve the module path `easygo-agent` and `go 1.25.0` directive.
- Leave all changes unstaged and uncommitted. Do not create/amend commits, push, merge, rebase, or publish a pull request.
- Do not reset or overwrite unrelated or ambiguous working-tree changes.
- Delete the complete `easygo_agent_frontend` directory, including nested Git metadata, only after verifying the exact target path.
- Inventory but do not delete ignored `skills/builtin` and `skills/workspaces` runtime data.
- Use Eino `flow/agent/react.NewAgent`, `model.ToolCallingChatModel`, `compose.ToolsNodeConfig`, and native Tool interfaces.
- Do not create custom Agent, Model, Tool, Memory, MCP, plugin-registry, job-loop, or authorization frameworks.
- Use one OpenAI-compatible model configured at process startup.
- Read the API Key only from `EASYGO_AGENT_API_KEY`; never log, trace, serialize, or commit it.
- Use `zap.Logger` directly. Every function returning an error logs the error with safe typed fields before returning it.
- Add an immediately preceding purpose comment to every function, method, test function, and anonymous function. Named function comments begin with the exact function name.
- Do not add SQL, GORM, Redis, raw SQL, database schemas, HTTP listeners, or network calls outside the model adapter.
- Use TDD: establish a focused failing test, verify the failure, implement the smallest behavior, and rerun the focused test before broad verification.

---

## File Map

### New or fully replaced production files

- `cmd/agent/main.go`: flag parsing, signal context, process exit status.
- `configs/config.example.yaml`: committed non-secret configuration.
- `.env.example`: documents only `EASYGO_AGENT_API_KEY=`.
- `internal/app/app.go`: dependency construction, Bubble Tea execution, ordered shutdown.
- `internal/chatmodel/openai.go`: constructs the Eino OpenAI-compatible `ToolCallingChatModel`.
- `internal/config/config.go`: strict YAML loading, defaults, environment injection, operational validation.
- `internal/gateway/types.go`: messages, requests, event kinds, events, and Runner/EventStream interfaces.
- `internal/gateway/gateway.go`: ReAct construction, run-scoped callbacks, Eino conversion, and stream producer.
- `internal/gateway/stream.go`: idempotent cancellation, receive, EOF, and cleanup.
- `internal/observability/logger.go`: production Zap construction.
- `internal/observability/tracing.go`: OTel provider lifecycle and Eino callback span bridge.
- `internal/tools/calculator.go`: Eino `InferTool` Calculator implementation.
- `internal/tui/model.go`: Bubble Tea model and state transitions.
- `internal/tui/render.go`: transcript/status rendering and styles.
- `main.go`: removed after `cmd/agent/main.go` exists.

### New or fully replaced tests

- `internal/config/config_test.go`
- `internal/gateway/gateway_test.go`
- `internal/gateway/stream_test.go`
- `internal/observability/tracing_test.go`
- `internal/tools/calculator_test.go`
- `internal/tui/model_test.go`

### Documentation

- `README.md`: rewritten to match implemented template behavior.
- `docs/architecture.md`: dependency direction and Gateway protocol.
- `docs/extensions.md`: deferred Memory, MCP, HTTP/RPC, multi-model, and OTLP work with trigger conditions.
- `CONTEXT.md`: removed because its durable HTTP/MySQL domain model is obsolete.
- `docs/adr/0001-use-eino-runner-for-synchronous-turn-execution.md`: removed because the accepted decision is superseded.
- `docs/superpowers/specs/2026-08-20-eino-tui-template-redesign.md`: retained as the approved design.
- `docs/superpowers/plans/2026-08-20-eino-tui-template-redesign.md`: retained as this implementation plan.

---

### Task 1: Strict Non-Secret Configuration

**Files:**
- Replace: `internal/config/config.go`
- Replace: `internal/config/config_test.go`
- Replace: `configs/config.yaml` with `configs/config.example.yaml`
- Create: `.env.example`

**Interfaces:**
- Produces: `config.Load(path string, lookupEnv func(string) (string, bool), logger *zap.Logger) (config.Config, error)`.
- Produces: `config.Config` with `Agent`, `Model`, `Tracing`, and runtime-only `APIKey` fields.
- Consumes: YAML v3 and an injected environment lookup function.

- [ ] **Step 1: Replace the old configuration tests with operational configuration tests**

Cover exact defaults and validation:

```go
// TestLoadValidConfig verifies strict YAML loading and API Key injection.
func TestLoadValidConfig(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `agent:
  system_prompt: test
  max_steps: 8
model:
  name: test-model
  base_url: https://example.com/v1
  timeout: 2m
tracing:
  enabled: false
  exporter: stdout
`)

	// lookupEnv returns the test API Key without reading the process environment.
	lookupEnv := func(key string) (string, bool) {
		return "test-key", key == "EASYGO_AGENT_API_KEY"
	}

	got, err := Load(path, lookupEnv, zap.NewNop())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Model.APIKey != "test-key" || got.Agent.MaxSteps != 8 {
		t.Fatalf("Load() = %#v", got)
	}
}
```

Add table cases for unknown YAML fields, missing model name, missing API Key, malformed Base URL, non-positive timeout, non-positive max steps, and a file body that contains `api_key`.

- [ ] **Step 2: Run the focused configuration test and observe the old contract fail**

Run: `go test ./internal/config -run TestLoadValidConfig -count=1`

Expected: FAIL because the old MySQL/Skill `Config` and loader do not expose the new contract.

- [ ] **Step 3: Implement strict loading, defaults, and validation**

Use these exact public types:

```go
type Config struct {
	Agent   AgentConfig   `yaml:"agent"`
	Model   ModelConfig   `yaml:"model"`
	Tracing TracingConfig `yaml:"tracing"`
}

type AgentConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
	MaxSteps    int    `yaml:"max_steps"`
}

type ModelConfig struct {
	Name    string   `yaml:"name"`
	BaseURL string   `yaml:"base_url"`
	Timeout Duration `yaml:"timeout"`
	APIKey  string   `yaml:"-"`
}

type TracingConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Exporter string `yaml:"exporter"`
}

type Duration struct {
	time.Duration
}
```

`Duration.UnmarshalYAML` parses the scalar with `time.ParseDuration` and does not log the scalar value. `Load` must use `yaml.Decoder.KnownFields(true)`, reject more than one YAML document, inject `EASYGO_AGENT_API_KEY`, and call an unexported `validate`. Log only the configuration path, field name, and error; never log the API Key or full decoded structure.

- [ ] **Step 4: Add committed example files without secrets**

`configs/config.example.yaml` must contain the approved system prompt, `max_steps: 8`, model name, empty Base URL, `timeout: 120s`, and disabled stdout tracing. `.env.example` contains exactly:

```dotenv
EASYGO_AGENT_API_KEY=
```

- [ ] **Step 5: Run all configuration tests**

Run: `go test ./internal/config -count=1`

Expected: PASS with no network access.

---

### Task 2: Native Eino Calculator Tool

**Files:**
- Create: `internal/tools/calculator.go`
- Create: `internal/tools/calculator_test.go`

**Interfaces:**
- Produces: `tools.NewCalculator(logger *zap.Logger) (tool.InvokableTool, error)`.
- Consumes: Eino `tool/utils.InferTool`.

- [ ] **Step 1: Write table-driven behavior tests through Eino's native Tool interface**

```go
// TestCalculator verifies all supported operations through InvokableRun.
func TestCalculator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		arguments string
		want      string
		wantErr   bool
	}{
		{name: "add", arguments: `{"operation":"add","a":2,"b":3}`, want: `{"result":5}`},
		{name: "subtract", arguments: `{"operation":"subtract","a":7,"b":2}`, want: `{"result":5}`},
		{name: "multiply", arguments: `{"operation":"multiply","a":4,"b":3}`, want: `{"result":12}`},
		{name: "divide", arguments: `{"operation":"divide","a":8,"b":2}`, want: `{"result":4}`},
		{name: "divide by zero", arguments: `{"operation":"divide","a":8,"b":0}`, wantErr: true},
		{name: "unknown operation", arguments: `{"operation":"pow","a":2,"b":3}`, wantErr: true},
	}
	// Test cases call tool.InvokableRun and compare JSON semantically.
}
```

- [ ] **Step 2: Verify the Calculator tests fail before production code exists**

Run: `go test ./internal/tools -count=1`

Expected: FAIL because `NewCalculator` does not exist.

- [ ] **Step 3: Implement the Calculator with `InferTool`**

Use these schema types:

```go
type CalculatorInput struct {
	Operation string  `json:"operation" jsonschema:"description=Operation to perform,enum=add,enum=subtract,enum=multiply,enum=divide"`
	A         float64 `json:"a" jsonschema:"description=Left operand"`
	B         float64 `json:"b" jsonschema:"description=Right operand"`
}

type CalculatorOutput struct {
	Result float64 `json:"result"`
}
```

`NewCalculator` calls `utils.InferTool("calculator", ...)`. Its immediately-commented function literal switches over the four operations. Division by zero and unknown operations are logged with `zap.String("operation", input.Operation)` and returned as wrapped errors. Do not log operand values or generated JSON.

- [ ] **Step 4: Run Calculator tests**

Run: `go test ./internal/tools -count=1`

Expected: PASS.

---

### Task 3: Optional OpenTelemetry and Eino Callback Bridge

**Files:**
- Create: `internal/observability/logger.go`
- Create: `internal/observability/tracing.go`
- Create: `internal/observability/tracing_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `observability.NewLogger() (*zap.Logger, error)`.
- Produces: `observability.NewTracing(ctx context.Context, cfg config.TracingConfig, logger *zap.Logger) (*observability.Tracing, error)`.
- Produces: `Tracing.Tracer trace.Tracer`, `Tracing.Handler callbacks.Handler`, and `Tracing.Shutdown(context.Context) error`.
- Consumes: Task 1 `config.TracingConfig`.

- [ ] **Step 1: Add exact OTel dependencies**

Run:

```bash
go get go.opentelemetry.io/otel@v1.45.0
go get go.opentelemetry.io/otel/sdk@v1.45.0
go get go.opentelemetry.io/otel/exporters/stdout/stdouttrace@v1.45.0
```

Expected: the three modules appear in `go.mod`/`go.sum`; do not run `go mod tidy` until legacy packages are removed.

- [ ] **Step 2: Write span bridge tests with an in-memory recorder**

Tests must construct an SDK `tracetest.SpanRecorder`, invoke the returned callback with synthetic ChatModel and Tool `RunInfo`, and assert:

```go
if got := endedSpanNames(recorder); !slices.Equal(got, []string{"model.generate", "tool.execute"}) {
	t.Fatalf("span names = %v", got)
}
for _, span := range recorder.Ended() {
	assertNoSensitiveAttributes(t, span.Attributes(), []string{"secret-key", "prompt text", `{"a":1}`})
}
```

Also test disabled tracing, unsupported exporter values, error span status, and idempotent shutdown.

- [ ] **Step 3: Run the focused tracing test and observe failure**

Run: `go test ./internal/observability -run TestEinoCallbackCreatesSafeSpans -count=1`

Expected: FAIL because the observability package does not exist.

- [ ] **Step 4: Implement Zap construction and tracing lifecycle**

`NewLogger` uses `zap.NewProduction`. If construction fails, log the construction error through a `zap.NewNop()` fallback before returning it.

`NewTracing` behavior:

```go
type Tracing struct {
	Tracer   trace.Tracer
	Handler  callbacks.Handler
	shutdown func(context.Context) error
	once     sync.Once
	err      error
}
```

- Disabled: return a no-op tracer, a no-op callback handler, and an idempotent no-op shutdown.
- Enabled with `stdout`: build `stdouttrace.New(stdouttrace.WithPrettyPrint())`, an SDK `TracerProvider` with `sdktrace.WithBatcher(exporter)`, and a callback handler.
- Any other exporter: log and return `unsupported tracing exporter`.
- Callback `OnStart` filters `RunInfo.Component` to ChatModel and Tool, starts `model.generate` or `tool.execute`, and attaches only safe attributes.
- Callback `OnEnd` ends the span.
- Callback `OnError` records the error, sets error status, and ends the span.
- The bridge never converts or inspects callback input/output, so prompt, response, Tool input, and Tool output cannot enter attributes.

- [ ] **Step 5: Run observability tests**

Run: `go test ./internal/observability -count=1`

Expected: PASS.

---

### Task 4: Transport-Neutral Gateway and Real ReAct Execution

**Files:**
- Create: `internal/gateway/types.go`
- Create: `internal/gateway/stream.go`
- Create: `internal/gateway/gateway.go`
- Create: `internal/gateway/stream_test.go`
- Create: `internal/gateway/gateway_test.go`

**Interfaces:**
- Produces: `gateway.Runner`, `gateway.EventStream`, `gateway.Request`, `gateway.Message`, `gateway.Event`, and `gateway.Gateway`.
- Produces: `gateway.New(ctx context.Context, cfg gateway.Config, chatModel model.ToolCallingChatModel, tools []tool.BaseTool, tracer trace.Tracer, traceHandler callbacks.Handler, logger *zap.Logger) (*gateway.Gateway, error)`.
- Consumes: Eino ReAct, Task 2 native Tools, and Task 3 tracer/callback handler.

- [ ] **Step 1: Define the outward types in a failing contract test**

The production declarations must match:

```go
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type Request struct {
	Messages []Message
}

type EventKind string

const (
	EventTextDelta EventKind = "text_delta"
	EventToolStart EventKind = "tool_start"
	EventToolEnd   EventKind = "tool_end"
	EventCompleted EventKind = "completed"
	EventCanceled  EventKind = "canceled"
	EventFailed    EventKind = "failed"
)

type Event struct {
	Kind     EventKind
	Text     string
	ToolName string
	Err      error
}

type EventStream interface {
	Recv() (Event, error)
	Close() error
}

type Runner interface {
	Run(context.Context, Request) (EventStream, error)
}
```

Tests assert invalid roles and empty user content fail before model execution, and no exported Gateway type references Eino `schema.Message` or `schema.StreamReader`.

- [ ] **Step 2: Write Stream lifecycle tests**

Cover ordered receive, exactly one terminal event, EOF after termination, idempotent `Close`, cancellation unblocking a producer, and no send after close. Use a one-second test timeout to expose leaks.

- [ ] **Step 3: Run Gateway tests and observe failure**

Run: `go test ./internal/gateway -count=1`

Expected: FAIL because Gateway production files do not exist.

- [ ] **Step 4: Implement the Stream primitive**

Use a bounded event channel, derived context, idempotent cancel, and a producer completion channel. Every send selects between the event channel and derived context. `Close` cancels the context and waits for producer completion. `Recv` returns `io.EOF` only after the event channel closes. Expected `io.EOF` and caller cancellation are not emitted as `Failed`. Because the project rule requires every returned error to be logged, `Recv` logs terminal `io.EOF` with only `zap.String("stage", "stream_exhausted")` before returning it.

- [ ] **Step 5: Add a fake `ToolCallingChatModel` that drives real Eino ReAct**

The fake used only in `gateway_test.go` implements:

```go
type fakeToolCallingModel struct {
	tools []*schema.ToolInfo
}

// WithTools returns an immutable fake bound to the requested Tool definitions.
func (f *fakeToolCallingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error)

// Generate delegates to the same deterministic response selection as Stream.
func (f *fakeToolCallingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)

// Stream first requests calculator, then returns two final text chunks after the Tool result exists.
func (f *fakeToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
```

Before a Tool result, return an assistant Tool call with ID `call-1`, function name `calculator`, and arguments `{"operation":"add","a":2,"b":3}`. After a Tool result appears in input, return assistant chunks `"result "` and `"is 5"` using `schema.StreamReaderFromArray`.

- [ ] **Step 6: Implement Gateway construction and run projection**

`gateway.Config` contains `MaxSteps int`. `New` calls `react.NewAgent` with `ToolCallingModel`, `compose.ToolsNodeConfig{Tools: tools}`, `MaxStep`, and stable node names.

`Run` must:

1. validate and convert stable messages to Eino messages;
2. derive a run context and start `agent.run` with the injected tracer;
3. create a run-scoped Tool callback using `react.BuildAgentCallback` that emits `ToolStart`/`ToolEnd` without arguments or outputs;
4. combine that callback and the Task 3 handler through `compose.WithCallbacks`;
5. call `react.Agent.Stream`;
6. read and close the Eino stream in one producer goroutine;
7. emit non-empty assistant content as `TextDelta` and accumulate final content;
8. map context cancellation to `Canceled`, other receive errors to `Failed`, and clean EOF to `Completed`;
9. close the OTel root span and event stream exactly once.

Every error path logs with safe fields such as stage, event kind, and Tool name; it does not log messages, model output, Tool input/output, or credentials.

- [ ] **Step 7: Verify ReAct, Tool events, cancellation, and concurrency**

Run:

```bash
go test ./internal/gateway -count=1
go test -race ./internal/gateway -count=1
```

Expected: PASS. The deterministic test observes ToolStart, ToolEnd, two TextDelta events, one Completed event with `result is 5`, and then `io.EOF`.

---

### Task 5: Bubble Tea TUI Adapter

**Files:**
- Create: `internal/tui/model.go`
- Create: `internal/tui/render.go`
- Create: `internal/tui/model_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `tui.New(runner gateway.Runner, systemPrompt string, logger *zap.Logger) *tui.Model`.
- Consumes: Task 4 stable Gateway interfaces only; it imports no Eino package.

- [ ] **Step 1: Add exact TUI dependencies**

Run:

```bash
go get github.com/charmbracelet/bubbletea@v1.3.10
go get github.com/charmbracelet/bubbles@v0.21.1
go get github.com/charmbracelet/lipgloss@v1.1.0
```

Expected: all three modules appear in `go.mod`/`go.sum`.

- [ ] **Step 2: Write state-machine tests before the TUI implementation**

Create a fake Runner and fake EventStream in `model_test.go`:

```go
type fakeRunner struct {
	stream gateway.EventStream
	calls  int
	ctx    context.Context
}

// Run records the invocation and returns the configured stream.
func (f *fakeRunner) Run(ctx context.Context, _ gateway.Request) (gateway.EventStream, error) {
	f.calls++
	f.ctx = ctx
	return f.stream, nil
}

type fakeStream struct {
	events []gateway.Event
	index  int
	closed bool
}

// Recv returns deterministic events followed by EOF.
func (f *fakeStream) Recv() (gateway.Event, error) {
	if f.index == len(f.events) {
		return gateway.Event{}, io.EOF
	}
	event := f.events[f.index]
	f.index++
	return event, nil
}

// Close records that the TUI released the stream.
func (f *fakeStream) Close() error {
	f.closed = true
	return nil
}

// TestCompletedRunAppendsHistory verifies only authoritative completed text is reused.
func TestCompletedRunAppendsHistory(t *testing.T) {
	stream := &fakeStream{events: []gateway.Event{
		{Kind: gateway.EventTextDelta, Text: "partial"},
		{Kind: gateway.EventCompleted, Text: "final"},
	}}
	runner := &fakeRunner{stream: stream}
	model := New(runner, "system", zap.NewNop())
	model.input.SetValue("hello")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if command == nil || runner.calls != 1 {
		t.Fatalf("submit command = %v, calls = %d", command, runner.calls)
	}
	model = applyEvent(t, model, stream.events[0])
	model = applyEvent(t, model, stream.events[1])
	if got := model.history[len(model.history)-1]; got.Content != "final" {
		t.Fatalf("last history message = %#v", got)
	}
}

// TestFailedRunDoesNotAppendPartialHistory verifies failed partial output stays visible only.
func TestFailedRunDoesNotAppendPartialHistory(t *testing.T) {
	stream := &fakeStream{events: []gateway.Event{
		{Kind: gateway.EventTextDelta, Text: "partial"},
		{Kind: gateway.EventFailed, Err: errors.New("model failed")},
	}}
	model := submittedModel(t, stream)
	wantHistory := len(model.history)
	model = applyEvent(t, model, stream.events[0])
	model = applyEvent(t, model, stream.events[1])
	if len(model.history) != wantHistory || !strings.Contains(model.transcript, "partial") {
		t.Fatalf("history length = %d, transcript = %q", len(model.history), model.transcript)
	}
}

// TestControlCCancelsWhileRunningAndQuitsWhileIdle verifies the two-stage key behavior.
func TestControlCCancelsWhileRunningAndQuitsWhileIdle(t *testing.T) {
	stream := &fakeStream{}
	model := submittedModel(t, stream)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	model = updated.(*Model)
	if !stream.closed || model.state != idle {
		t.Fatalf("closed = %v, state = %v", stream.closed, model.state)
	}
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if command == nil {
		t.Fatal("idle Ctrl+C did not return tea.Quit command")
	}
}

// TestDuplicateSubmitIsIgnored verifies only one active stream exists.
func TestDuplicateSubmitIsIgnored(t *testing.T) {
	runner := &fakeRunner{stream: &fakeStream{}}
	model := New(runner, "system", zap.NewNop())
	model.input.SetValue("first")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	model.input.SetValue("second")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if runner.calls != 1 {
		t.Fatalf("Runner calls = %d, want 1", runner.calls)
	}
}
```

`submittedModel` and `applyEvent` are test helpers with required purpose comments. `applyEvent` sends the package-private Gateway event message through `Update`; it does not mutate `Model` fields directly.

- [ ] **Step 3: Run the TUI tests and observe failure**

Run: `go test ./internal/tui -count=1`

Expected: FAIL because `tui.New` and the state machine do not exist.

- [ ] **Step 4: Implement the minimal Bubble Tea state machine**

Use three internal states: `idle`, `running`, and `quitting`. The model owns:

- a Bubbles `viewport.Model` transcript;
- a Bubbles `textarea.Model` input;
- current-process `[]gateway.Message` history beginning with the system message;
- current partial text;
- the active `gateway.EventStream` and cancel function;
- a `gateway.Runner` and `*zap.Logger`.

`Enter` in idle trims and submits non-empty input. While running it does not submit. A stream command reads one event and returns it as a Bubble Tea message; `Update` applies the event and schedules the next read. `Completed` appends the authoritative assistant message. `Canceled` and `Failed` keep partial display but do not append it. Tool events add short `started`/`completed` transcript lines.

`Ctrl+C` while running cancels and closes the stream; while idle it returns `tea.Quit`. No background goroutine mutates the Bubble Tea model.

- [ ] **Step 5: Implement restrained rendering**

`render.go` uses Lip Gloss for a title, transcript border, input border, and status line. It must render correctly without color and must not include model management, session lists, configuration editing, mouse handling, or Markdown parsing.

- [ ] **Step 6: Run TUI tests**

Run:

```bash
go test ./internal/tui -count=1
go test -race ./internal/tui -count=1
```

Expected: PASS.

---

### Task 6: Model Adapter, Application Assembly, and Executable

**Files:**
- Create: `internal/chatmodel/openai.go`
- Replace: `internal/app/app.go`
- Create: `cmd/agent/main.go`
- Remove: `main.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Produces: `chatmodel.New(ctx context.Context, cfg config.ModelConfig, logger *zap.Logger) (model.ToolCallingChatModel, error)`.
- Produces: `app.Run(ctx context.Context, configPath string) error`.
- Consumes: Tasks 1–5.

- [ ] **Step 1: Add the classic Eino OpenAI adapter**

Run:

```bash
go get github.com/cloudwego/eino-ext/components/model/openai@v0.1.13
```

Expected: the classic adapter is direct in `go.mod`; do not remove `agenticopenai` until legacy code is deleted.

- [ ] **Step 2: Write chat-model construction tests for safe field mapping**

Factor an unexported `toOpenAIConfig(config.ModelConfig) *openai.ChatModelConfig` and test that name, Base URL, timeout, and API Key map exactly. The test must never print the resulting struct because it contains the test credential.

- [ ] **Step 3: Implement the chat-model constructor**

`chatmodel.New` calls `openai.NewChatModel` and returns it as `model.ToolCallingChatModel`. On error, log model name and Base URL host only; never log API Key, URL query, headers, or full config.

- [ ] **Step 4: Write an application assembly smoke test using an injected program runner**

Keep the production `app.Run` interface small, but place actual assembly in an unexported `build` function whose external constructors can be replaced in the package test. Verify shutdown order: active TUI completes, tracing flushes, and Zap sync runs. Ignore only the documented terminal-specific `zap.Sync` invalid-argument error.

- [ ] **Step 5: Implement application assembly**

`app.Run` performs this exact order:

1. create Zap;
2. load strict configuration using `os.LookupEnv`;
3. create optional tracing;
4. create Calculator Tool;
5. create OpenAI-compatible chat model;
6. create Gateway with `MaxSteps`, tracer, and callback handler;
7. create and run `tea.NewProgram(tui.New(agentGateway, cfg.Agent.SystemPrompt, logger), tea.WithAltScreen())`;
8. close active resources, flush tracing, and sync Zap.

Every returned error is logged at the returning layer with a safe `stage` field and wrapped with `%w`.

- [ ] **Step 6: Implement the command entry point**

`cmd/agent/main.go` uses a signal-aware context for `os.Interrupt` and `syscall.SIGTERM`, accepts `-config` defaulting to `configs/config.example.yaml`, calls `app.Run`, prints one concise startup/runtime error to stderr, and exits non-zero. The root `main.go` is removed so there is one executable entry point.

- [ ] **Step 7: Verify new production packages compile together**

Run:

```bash
go test ./internal/app ./internal/chatmodel ./cmd/agent -count=1
go vet ./internal/app ./internal/chatmodel ./cmd/agent
```

Expected: PASS without contacting a model.

---

### Task 7: Remove the Legacy Application and Rewrite Documentation

**Files:**
- Remove: `easygo_agent_frontend/` including nested `.git`
- Remove: `compose.yaml`
- Remove: `sql/`
- Remove obsolete packages under `internal/agent`, `internal/controller`, `internal/credential`, `internal/cronjob`, `internal/framejob`, `internal/model`, `internal/platform`, `internal/server`, `internal/service`, `internal/skill`, and `internal/wire`
- Preserve new `internal/app`, `internal/config`, and `internal/observability`
- Remove: `skills/builtin-src/`
- Preserve: ignored `skills/builtin/` and `skills/workspaces/`
- Remove: `CONTEXT.md`
- Remove: `docs/adr/0001-use-eino-runner-for-synchronous-turn-execution.md`
- Remove: obsolete 2026-08-04 backend-boundary design and plan
- Replace: `README.md`
- Create: `docs/architecture.md`
- Create: `docs/extensions.md`
- Modify: `.gitignore` if obsolete paths remain
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: the complete runnable template from Tasks 1–6.
- Produces: a repository containing only the new template and intentionally preserved user data/history documents.

- [ ] **Step 1: Reconfirm destructive targets and inventory ignored runtime data**

Run:

```bash
git status --short
git ls-files -s easygo_agent_frontend
find skills/builtin skills/workspaces -maxdepth 4 -print 2>/dev/null | sort
```

Expected: the frontend target resolves exactly to `/Users/user/Desktop/Code/agent_go/easygo_agent/easygo_agent_frontend`; ignored runtime paths are listed and remain untouched.

- [ ] **Step 2: Remove the authorized frontend target and tracked legacy files**

Use `apply_patch` for tracked text-file deletions. For the nested frontend repository, after the Step 1 path check, remove only this exact authorized path:

```bash
rm -rf -- /Users/user/Desktop/Code/agent_go/easygo_agent/easygo_agent_frontend
```

Do not use globs, environment variables, `git clean`, or `git reset`. Do not delete `skills/builtin` or `skills/workspaces`.

- [ ] **Step 3: Rewrite README with only real behavior**

README sections must be:

1. purpose and explicit non-goals;
2. architecture summary;
3. prerequisites and configuration;
4. `EASYGO_AGENT_API_KEY` setup;
5. `go run ./cmd/agent -config configs/config.example.yaml`;
6. TUI keys;
7. optional stdout tracing;
8. test commands;
9. links to architecture and extensions documents;
10. warning that the previously committed MySQL credential must be rotated because deletion does not remove Git history.

- [ ] **Step 4: Write architecture and extension documents**

`docs/architecture.md` documents the dependency direction, Gateway request/event/stream contract, current-process history ownership, Eino ReAct/Tool usage, tracing callback scope, and shutdown flow.

`docs/extensions.md` names trigger conditions rather than adding empty interfaces:

- Memory: define only after choosing checkpoint/history/retrieval semantics.
- MCP: define only after choosing client/server role and transport.
- HTTP/RPC: add an adapter over `gateway.Runner`; the derived project owns auth, quotas, serialization, streaming, and deployment.
- Multiple models: add routing only after concrete selection/fallback rules exist.
- OTLP: replace the exporter in `internal/app`; do not change Gateway/TUI.

- [ ] **Step 5: Tidy dependencies after all legacy imports are gone**

Run: `go mod tidy`

Expected direct dependencies are limited to Eino, the classic Eino OpenAI adapter, Bubble Tea/Bubbles/Lip Gloss, Zap, OTel SDK/stdout exporter, and YAML v3. `agenticopenai`, Gin, GORM, MySQL, JWT, cron, Wire, doublestar, godotenv, lumberjack, and unrelated provider libraries are absent.

- [ ] **Step 6: Search for forbidden legacy capability residue**

Run:

```bash
rg -n "gin-gonic|gorm|mysql|jwt|agenticopenai|cronjob|framejob|compact|Skill|skills/|SSE|CORS|Authorization|MCP|Memory" --glob '!docs/superpowers/**' --glob '!docs/extensions.md' .
```

Expected: no implementation or active README/config claims. Any intentional “not implemented” occurrence in `docs/extensions.md` is reviewed manually.

---

### Task 8: Full Verification and Manual Handoff

**Files:**
- Modify only files required to fix failures found by the commands below.

**Interfaces:**
- Consumes: all previous tasks.
- Produces: verified unstaged working-tree changes ready for user review.

- [ ] **Step 1: Format all Go files**

Run: `gofmt -w cmd internal`

Expected: command exits zero.

- [ ] **Step 2: Verify all function declarations have purpose comments**

Inspect `go doc`/lint output and use `rg -n '^func |^\s*func\(' cmd internal --glob '*.go'` to review every named and anonymous function against the project rule. Add missing comments with `apply_patch`.

- [ ] **Step 3: Run the complete test suite**

Run: `go test ./... -count=1`

Expected: PASS.

- [ ] **Step 4: Run the race detector**

Run: `go test -race ./... -count=1`

Expected: PASS with no race or goroutine-leak symptom.

- [ ] **Step 5: Run static and diff verification**

Run:

```bash
go vet ./...
git diff --check
```

Expected: both commands exit zero.

- [ ] **Step 6: Audit dependency and capability scope**

Run:

```bash
go list -deps ./... >/dev/null
go mod graph
git status --short
find skills/builtin skills/workspaces -maxdepth 4 -print 2>/dev/null | sort
```

Expected: the program resolves; old direct dependencies and packages are absent; ignored runtime Skill data still exists; all changes remain unstaged.

- [ ] **Step 7: Perform an opt-in manual smoke run only when the user provides a disposable API Key**

Run:

```bash
EASYGO_AGENT_API_KEY='<user-provided-disposable-key>' go run ./cmd/agent -config configs/config.example.yaml
```

Expected: TUI opens, streams a basic response, can trigger Calculator, cancels on `Ctrl+C` while running, and exits on `Ctrl+C` while idle. If no disposable credential is supplied, report this manual check as not run; do not obtain or infer credentials from old configuration.

- [ ] **Step 8: Report the handoff without publishing changes**

Summarize created, replaced, deleted, and intentionally preserved paths; list exact verification results; call out the unrun live-model smoke test if applicable; repeat the credential-rotation warning. Do not stage or commit.
