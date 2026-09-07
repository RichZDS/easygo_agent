# EasyGo Eino Agent Template

一个可复制的 Go Agent 模板，提供完整 CLI、HTTP 会话接口和 PostgreSQL 历史持久化，无前端。

## Agent loop

```mermaid
flowchart TD
  CLI[CLI: 用户名 / 会话] --> Runtime[共用 Runtime]
  HTTP[HTTP: 用户 / 会话接口] --> Runtime
  Runtime --> Load[获取会话锁 / 加载 PostgreSQL 上下文 / 加入输入]
  Load --> Check[Eino BeforeModelRewriteState: 检查输入预算]
  Check -->|超过预算| Summary[独立 context-compressor Agent]
  Summary --> Finalize[Eino 原生摘要整理 / 再检查预算]
  Finalize --> Model[主 Agent 调用模型]
  Check -->|预算内| Model
  Model -->|有工具调用| Tools[Eino 执行工具 / 获取原生结果]
  Tools --> Check
  Model -->|最终回复| Save[短事务保存原始轮次与最终上下文]
  Save --> Done[释放会话锁 / completed]
```

循环由 Eino `Deep Agent` 执行，模型返回工具调用就继续，返回最终回答就结束；`max_steps` 是推理迭代上限。模板关闭 Deep Agent 的内置 todo 和通用 task 工具，默认业务工具是 Calculator。摘要 Agent 独立调用 `subagent` 模型，不使用工具，也不递归压缩。

[实施计划](doc/agent-loop-plan.md)记录了本次拆分和验证范围。

## 原生消息与存储

消息统一使用 `*schema.AgenticMessage`，通过标准 JSON 序列化直接写入 JSONB。保留 `role`、`content_blocks`、`response_meta`、`extra` 及其中的工具调用 ID、参数、结果、推理块、usage、供应商扩展。流式片段通过 Eino `schema.ConcatAgenticMessages` 合并，不把展示文本重新包装成历史。

| 数据 | 保存方式 |
| --- | --- |
| 用户名、会话 ID、创建/更新时间 | `agent_sessions` |
| 下一轮模型上下文（可能包含摘要） | `agent_sessions.context_messages`，原生消息数组 |
| 原始用户输入与主 Agent 输出 | `agent_turns.messages`，原生消息数组 |
| 轮次状态 | `completed` / `failed` / `canceled` |

成功时，Eino `AfterAgent` 提供最终 state，原始轮次和该 state 在一个短事务里提交。压缩只改变模型上下文，历史接口仍能读取原始工具记录。

失败或取消时，保存已接收的完整/部分原生消息供历史展示；下一轮上下文保留之前的上下文与本轮用户输入，不包含失败轮次的不完整工具链。数据库写入失败会返回 `failed`，不会报告完成。进程被强制终止时，未提交轮次可能丢失；当前不提供中途 checkpoint 恢复或工具副作用的 exactly-once 保证。

PostgreSQL 每轮加载一次上下文，轮次期间由 Eino 内存 state 管理循环，结束时提交一次。跨进程互斥使用 PostgreSQL session advisory lock，占用一条专用连接，但不会在推理期间保持长事务。锁随连接关闭释放；`max_conns` 同时约束可持有连接的运行数量。连接池饱和时新请求等待连接。

`database.driver: memory` 使用相同 Store 接口，适合开发和测试；该模式退出即丢失历史。当前没有额外跨轮缓存。

## 上下文预算

`agent.context_tokens` 是主模型的**输入预算**，必须为模型输出和供应商协议开销留出余量。例如模型总窗口为 32k 时，可以从 24k 输入预算开始。

每次模型调用前，包括每次工具返回后，Eino summarization 中间件都会检查消息和工具定义。通过原生 `TokenCounter` 扩展，以 UTF-8 JSON 字节数作保守文本/工具估算，同时参考模型返回的 `TokenUsage`；这不是精确 tokenizer，通常会提前压缩。增加图片、音频或特殊供应商工具后，应替换为该供应商的计数实现。

摘要提示词、摘要整理及状态替换均复用 Eino 原生 summarization。压缩后仍超预算、摘要为空或摘要模型报错，会明确失败，不截断原始历史。摘要模型需要足够的上下文窗口来容纳待压缩内容及摘要指令；单条巨大工具结果可能仍需业务工具自身分页或限制返回量。

## 启动

需要 Go 1.25+、支持 Tool Calling 的 OpenAI-compatible 模型。默认使用 PostgreSQL；所有命令在 `backend` 目录运行。

```powershell
Copy-Item .env.example .env
# 编辑 .env，填写 MODEL_API_KEY、SUBMODEL_API_KEY。
# DATABASE_URL 示例与下面的本地开发数据库一致。
docker compose up -d postgres
go run . -user alice
```

`.env` 仅补充当前环境中未设置的变量。已有 `.env` 时请直接补齐字段。

```dotenv
MODEL_API_KEY=your-main-model-key
SUBMODEL_API_KEY=your-summary-model-key
DATABASE_URL=postgres://easygo:easygo@127.0.0.1:5432/easygo?sslmode=disable
```

数据库启动时自动执行 `internal/conversation/schema.sql` 的幂等建表，多个进程的初始化通过事务锁串行化。示例 Compose 的凭证仅用于 loopback 本地开发。

配置位于 `configs/config.yaml`，也可用 `-config` 指定其他 YAML：

```yaml
agent:
  max_steps: 8
  context_tokens: 24000
model:
  name: deepseek-v4-pro
  base_url: https://api.deepseek.com
  timeout: 120s
  apikey: "{MODEL_API_KEY}"
subagent:
  name: deepseek-v4-flash
  base_url: https://api.deepseek.com
  timeout: 120s
  apikey: "{SUBMODEL_API_KEY}"
database:
  driver: postgres
  dsn: "{DATABASE_URL}"
  max_conns: 16
http:
  address: 127.0.0.1:8080
```

省略整个 `subagent` 块时，摘要 Agent 使用主模型配置；仍是独立 Agent。配置严格校验未知字段；模型 key 和数据库 DSN 必须使用 `{ENV}` 引用。

## CLI

```powershell
# 默认恢复该用户名最近使用的会话；省略 -user 时使用操作系统用户名。
go run . -user alice
# 创建新会话。
go run . -user alice -new
# 列出会话 / 按 ID 恢复。
go run . -user alice -list
go run . -user alice -session <session-id>
# 单次执行，适合脚本；正文输出到 stdout，会话标识输出到 stderr。
go run . -user alice -prompt "用计算器计算 12 * 34"
```

CLI 启动时展示该会话的原始历史。`Enter` 提交，生成或压缩中 `Ctrl+C` 取消当前轮次，空闲时 `Ctrl+C` 退出。历史读取和模型运行发生在后台 `tea.Cmd`，不会阻塞输入事件循环。取消后可以继续输入；外部退出时会取消并释放当前运行。

## HTTP API

```powershell
go run . -mode gateway
```

HTTP 默认只监听 `127.0.0.1:8080`。URL 中的用户名是会话命名空间，不是已验证身份；模板不包含登录认证。公开部署时，由可信网关认证并绑定用户身份。

| 方法与路径 | 行为 |
| --- | --- |
| `GET /healthz` | 进程健康检查 |
| `POST /v1/users/{user}/sessions` | 创建会话，返回会话对象 |
| `GET /v1/users/{user}/sessions?limit=50&offset=0` | 最近更新优先的会话列表 |
| `GET /v1/users/{user}/sessions/{id}/messages?after=0&limit=50` | 原始历史轮次及 `next_after` 游标 |
| `POST /v1/users/{user}/sessions/{id}/runs` | 执行一轮；请求体 `{"input":"..."}` |

`limit` 为 1–100。历史的 `after` 是上次返回的轮次 ID；`messages` 字段直接返回 Eino 原生消息。POST runs 默认返回最终 JSON；添加 `Accept: text/event-stream` 获取 SSE。请求体最大 64 KiB，每次 HTTP 请求最长 10 分钟；客户端断连会取消运行。

PowerShell 示例：

```powershell
$session = Invoke-RestMethod -Method Post http://127.0.0.1:8080/v1/users/alice/sessions
$runUrl = "http://127.0.0.1:8080/v1/users/alice/sessions/$($session.id)/runs"
Invoke-RestMethod -Method Post $runUrl -ContentType application/json -Body '{"input":"计算 12 * 34"}'
Invoke-RestMethod "http://127.0.0.1:8080/v1/users/alice/sessions/$($session.id)/messages"
```

SSE 的每个事件包含 `event: <kind>` 和 JSON `data`，kind 包括：

- `text_delta`：`text` 文本增量。
- `tool_started` / `tool_finished`：`tool` 名称。
- `compressing` / `compressed`：摘要状态。
- `completed`：本轮完整展示文本，且已持久化。
- `failed` / `canceled`：运行终态。

无效请求返回 400，会话不存在或用户名不匹配返回 404，同会话正在运行返回 409。SSE 已开始后的错误通过终态事件传递。Gateway 和 CLI 共享同一数据库时，可访问同一用户名下的同一会话。

## 模块位置

- `internal/agent/deepagent.go`：构造 Eino 主 Agent、摘要 Agent、上下文预算中间件。
- `internal/agent/runtime`：加载/提交原生上下文、流合并、运行生命周期和展示事件。
- `internal/conversation`：PostgreSQL 与内存存储实现。
- `internal/tui`：终端输入、历史展示、流式输出和取消。
- `internal/gateway`：HTTP JSON / SSE 接口。
- `internal/app`：配置、依赖组装和模式切换。

## 运行结束与资源释放

`Start` 只预留当前 runtime，首次 `Next()` 才加载历史并调用 Agent。`Cancel()` 非阻塞地请求取消；runtime 会自动提交已接收的原生审计、关闭消息流并释放会话锁，即使调用方已经停止读取事件。父 context 取消也会触发同样的收尾。

退出时调用 `Run.Close()`：它请求取消并等待收尾，返回最终 `Event`，可与 `Next()` 并发或重复调用。`Close()` 不消费展示事件，也不会把已经完成的结果改为取消；提交失败返回 `failed`。HTTP、单次 CLI 与 TUI 共用这一规则，TUI 的关闭错误会返回应用入口。

收尾提交使用独立的 10 秒超时 context；运行中的模型、工具和 Store 应遵守传入 context 的取消信号。成功终态只在提交和释放完成后可见。首次 `Next()` 前取消会释放预留，不调用模型或创建审计轮次。

## 验证

```powershell
go test ./...
go vet ./...
# 需要启用 CGO 且安装 C 编译器。
go test -race ./...
# 使用专用测试数据库。测试只清理自己的随机用户名记录。
$env:EASYGO_TEST_DATABASE_URL = 'postgres://easygo:easygo@127.0.0.1:5432/easygo?sslmode=disable'
go test ./internal/conversation -run TestPostgresContract -v -count=1
```

模型测试使用 fake model 或本地 OpenAI-compatible 测试服务器，不调用真实模型。涵盖工具循环、原生流合并、跨轮恢复、摘要成功/失败、摘要超预算、最大迭代、取消、提交失败、会话隔离、分页、HTTP 断连和单次 CLI 启动。PostgreSQL 集成测试未配置 `EASYGO_TEST_DATABASE_URL` 时明确跳过。

Eino 参考：[Summarization middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_summarization/)。实际实现以 `go.mod` 固定的 Eino v0.9.13 源码为准。
