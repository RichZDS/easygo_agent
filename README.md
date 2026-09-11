# EasyGo Eino Agent Template

一个可复制的 Go Agent 模板，提供完整 CLI、HTTP 会话接口、PostgreSQL 历史持久化，以及独立数据库支持的用户级长期记忆，无前端。

## Agent loop

```mermaid
flowchart TD
  CLI[CLI / TUI] --> Queue[共享 QueueManager]
  HTTP[HTTP] --> Queue
  Queue --> Runtime[一次 run 的 Runtime]
  Runtime --> Load[获取已 claim 的 lease / 加载上下文 / 加入输入]
  Load --> Recall[独立记忆库: 按时间与调用次数 Top 5]
  Recall --> Check
  Check[Eino BeforeModelRewriteState: 检查输入预算]
  Check -->|超过预算| Summary[独立 context-compressor Agent]
  Summary --> Finalize[Eino 原生摘要整理 / 再检查预算]
  Finalize --> Model[主 Agent 调用模型]
  Check -->|预算内| Model
  Model -->|有工具调用| Tools[Eino 执行工具 / 获取原生结果]
  Tools --> Check
  Model -->|最终回复| Save[短事务保存原始轮次与最终上下文]
  Save --> Done[原子提交 run、turn、context / completed]
```

循环由 Eino `Deep Agent` 执行，模型返回工具调用就继续，返回最终回答就结束；`max_steps` 是推理迭代上限。模板关闭 Deep Agent 的内置 todo 和通用 task 工具，默认业务工具是 Calculator。摘要 Agent 独立调用 `subagent` 模型，不使用工具，也不递归压缩。

[实施计划](doc/agent-loop-plan.md)记录了本次拆分和验证范围。每次提交先写入 `agent_runs`，再由共享队列按会话 FIFO 调度；队列元数据不会进入模型上下文。

## 原生消息与存储

消息统一使用 `*schema.AgenticMessage`，通过标准 JSON 序列化直接写入 JSONB。保留 `role`、`content_blocks`、`response_meta`、`extra` 及其中的工具调用 ID、参数、结果、推理块、usage、供应商扩展。流式片段通过 Eino `schema.ConcatAgenticMessages` 合并，不把展示文本重新包装成历史。

| 数据 | 保存方式 |
| --- | --- |
| 用户名、会话 ID、创建/更新时间 | `agent_sessions` |
| 下一轮模型上下文（可能包含摘要） | `agent_sessions.context_messages`，原生消息数组 |
| 原始用户输入与主 Agent 输出 | `agent_turns.messages`，原生消息数组 |
| 轮次状态 | `completed` / `failed` / `canceled` |
| 队列请求与租约 | `agent_runs`，状态为 `queued` / `running` / `completed` / `failed` / `canceled` |

成功时，Eino `AfterAgent` 提供最终 state，原始轮次和该 state 在一个短事务里提交。压缩只改变模型上下文，历史接口仍能读取原始工具记录。

失败或取消时，保存已接收的完整/部分原生消息供历史展示；下一轮上下文保留之前的上下文与本轮用户输入，不包含失败轮次的不完整工具链。数据库写入失败会返回 `failed`，不会报告完成。进程被强制终止时，未提交轮次可能丢失；当前不提供中途 checkpoint 恢复或工具副作用的 exactly-once 保证。

PostgreSQL 每轮加载一次上下文，轮次期间由 Eino 内存 state 管理循环，结束时提交一次。队列 claim 使用短事务、`FOR UPDATE SKIP LOCKED`、事务级 advisory lock 和“每会话一条 running”唯一索引共同保证跨进程互斥；claim token 为 heartbeat 与最终提交提供 fencing。`RunLease` 在推理期间占用一条专用连接，但不保持长事务；`max_conns` 因此也约束可并行持有的运行数量。旧的直接运行 seam 继续使用 session advisory lock。

`database.driver: memory` 使用进程内会话与长期记忆 Store，适合开发和测试；退出即丢失数据。PostgreSQL 模式下，`database.dsn` 只保存会话历史，`memory.dsn` 是**必须独立**的长期记忆数据库。

## 用户级长期记忆

Gateway 与 TUI 都在同一 `Stored` Runtime 中，在每次模型调用前从独立记忆库检索最多 5 条 active 记忆，并只把这些结构化参考数据注入 Agent。被实际选中的记录会递增 `call_count`。排序严格为 `0.5 * recency + 0.5 * normalized_log_call_count`：recency 使用 `last_seen_at` 的 90 天半衰期。记忆内容不能覆盖系统/开发者策略或当前用户请求。

长期记忆库的 `user_long_term_memories` 包含类别、内容、标签、重要性、置信度、来源会话/轮次、调用次数、首次/最后观测时间、最后调用时间、过期时间、创建/更新时间、归档时间、状态、版本及 1–5 的 profile slot。部分唯一索引从数据库层保证每个用户最多五条 active 画像；被替换的记录会归档而非静默删除。

每天按 `memory.timezone` 的 `memory.daily_at`（默认 03:00）运行独立的无工具记忆 Agent：它只读取已完成的原始轮次，按 `memory.batch_turns` 分批提取有来源证据的候选，做全局排名取 Top 5，再和当前五槽画像对比、合并或淘汰。成功写库并物化文件后才推进 checkpoint，失败会在后续运行重试。

数据库是唯一事实来源；`Storage/Long-term Memory/User profile/u-<sha256(user-id)>/` 是可重建物化视图，包含 `Agent.md`、`Memory.md`、`Experiment.md`、`Error.md`、`Preferences.md`、`Style.md`、`Prompts.md`、`Constraints.md` 和 `Metadata.md`。目录使用用户 ID 的 SHA-256，避免将原始用户名作为路径或暴露在目录列表中。

## 上下文预算

`agent.context_tokens` 是主模型的**输入预算**，必须为模型输出和供应商协议开销留出余量。例如模型总窗口为 32k 时，可以从 24k 输入预算开始。

每次模型调用前，包括每次工具返回后，Eino summarization 中间件都会检查消息和工具定义。通过原生 `TokenCounter` 扩展，以 UTF-8 JSON 字节数作保守文本/工具估算，同时参考模型返回的 `TokenUsage`；这不是精确 tokenizer，通常会提前压缩。增加图片、音频或特殊供应商工具后，应替换为该供应商的计数实现。

摘要提示词、摘要整理及状态替换均复用 Eino 原生 summarization。压缩后仍超预算、摘要为空或摘要模型报错，会明确失败，不截断原始历史。摘要模型需要足够的上下文窗口来容纳待压缩内容及摘要指令；单条巨大工具结果可能仍需业务工具自身分页或限制返回量。

## 启动

需要 Go 1.25+、支持 Tool Calling 的 OpenAI-compatible 模型。默认使用 PostgreSQL；所有命令在 `backend` 目录运行。

```powershell
Copy-Item .env.example .env
# 编辑 .env，填写 MODEL_API_KEY、SUBMODEL_API_KEY。
# DATABASE_URL 和 MEMORY_DATABASE_URL 分别指向会话库与独立记忆库。
docker compose up -d postgres
go run . -user alice
```

`.env` 仅补充当前环境中未设置的变量。已有 `.env` 时请直接补齐字段。

```dotenv
MODEL_API_KEY=your-main-model-key
SUBMODEL_API_KEY=your-summary-model-key
DATABASE_URL=postgres://easygo:easygo@127.0.0.1:5432/easygo?sslmode=disable
MEMORY_DATABASE_URL=postgres://easygo:easygo@127.0.0.1:5432/easygo_memory?sslmode=disable
```

会话库和记忆库启动时分别自动执行 `internal/conversation/schema.sql` 与 `internal/conversation/memory_schema.sql` 的幂等建表，多个进程的初始化通过事务锁串行化。示例 Compose 会创建两个独立数据库，凭证仅用于 loopback 本地开发；若沿用已有 volume，请先执行 `CREATE DATABASE easygo_memory`。

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
memory:
  enabled: true
  dsn: "{MEMORY_DATABASE_URL}" # 独立于 database.dsn
  max_conns: 4
  daily_at: "03:00"
  timezone: "Asia/Shanghai"
  batch_turns: 20
  top_k: 5
  storage_root: "Storage/Long-term Memory/User profile"
http:
  address: 127.0.0.1:8080
queue:
  max_pending: 100
  max_workers: 4
  poll_interval: 250ms
  lease_ttl: 30s
sandbox:
  enabled: false
  base_url: http://127.0.0.1:8787
  auth_token: "{SANDBOX_CONTROLLER_TOKEN}"
  request_timeout: 11m
  max_output_bytes: 65536
```

省略整个 `subagent` 块时，摘要 Agent 使用主模型配置；仍是独立 Agent。配置严格校验未知字段；模型 key、数据库 DSN 和启用后的 Controller token 必须使用 `{ENV}` 引用。

## Docker 沙箱

可选的本地 Docker 沙箱把逻辑申请和运行容器分开。每个 Agent 会话最多持有一个 `application_id` 和一个独立命名卷；容器停止、丢失或重建时 `/workspace` 代码仍然存在。默认最多同时运行三个容器、最多保留 30 个总申请，因此休眠工作区也绝不会超过 30 个，但 active/starting 申请同样占这 30 个名额。空闲容器会按指数增长的保温时间自动停止，容量紧张时也可提前休眠。Agent 主动销毁或申请达到五小时硬期限后，容器和命名卷都会删除。

沙箱 Controller 位于独立的 `sandbox` Compose profile，普通的 `docker compose up -d postgres` 不会启动它，Compose 也不会预先启动任何 Runtime 容器。先运行 `openssl rand -hex 32`，把结果填入 `.env` 的 `SANDBOX_CONTROLLER_TOKEN`。Linux 默认使用 `/var/run/docker.sock`；Docker Desktop 使用其他 socket 时同步修改 `DOCKER_SOCKET_PATH`。然后构建固定的编译镜像并启动 Controller：

```bash
docker compose --profile sandbox-image build sandbox-runtime
docker compose --profile sandbox up -d --build sandbox-controller
curl http://127.0.0.1:8787/healthz
```

然后把 `configs/config.yaml` 中的 `sandbox.enabled` 改为 `true`。Agent 可调用 `sandbox_apply`、`sandbox_create`、`sandbox_exec`、`sandbox_write_file`、`sandbox_read_file`、`sandbox_status`、`sandbox_release` 和 `sandbox_destroy`。这些能力只作为 Agent Tool 暴露，不会加入用户 HTTP Gateway。

Runtime 镜像包含 Go 1.25、Python 3.11、Node.js 22、GCC/G++、make、git 和常用 shell 工具。运行容器使用 `network=none`，且 Go、pip、npm 均配置为离线模式；外部依赖必须随工作区上传或提前 vendor。镜像预置由 UID/GID 1000 持有的 `/workspace` 骨架，Docker 首次挂载空命名卷时将其 copy-up，无需高权限卷初始化。PID 1 使用独立的非 root UID 1001，模型可控执行和文件操作固定为 UID/GID 1000，因此 Controller 可以在每次命令后清除该 UID 的所有后台进程而不影响容器 supervisor。根文件系统只读，不挂载宿主目录、凭证或 Docker socket，并有 CPU、内存、PID 和输出限制。`/tmp` 是 `noexec` 的受限 tmpfs，编译器临时目录指向受配额监控的 `/workspace/.tmp`，因此 Go 测试等需要执行临时产物的操作仍可运行。

Controller 是唯一可访问 Docker socket 的可信进程，监听端口只发布到宿主 loopback，并要求独立 Bearer token。Docker socket 权限通常等同宿主 root 权限，因此不要把 Controller 端口暴露到公网，也不要把 socket 传给 Runtime 容器。Controller 的完整资源和生命周期配置位于 `configs/sandbox-controller.yaml`。

启动、销毁和并发成本的官方资料与本机 benchmark 方法见 [Docker 沙箱调研](doc/docker-sandbox-research.md)。真实 Docker 集成测试和 benchmark 都是显式启用的；脚本结束前会删除自己的测试容器与命名卷，不会留下常驻沙箱。

Docker daemon 已由操作者启动后，可以运行完整黑盒验证。脚本使用独立的 `easygo-agent-it` namespace 和 Compose project；它不会启动或关闭 Docker Desktop，并通过退出 trap 删除测试 Controller、状态卷以及所有带该测试 namespace 的容器和工作区卷：

```bash
./scripts/test-sandbox-docker.sh
```

测试覆盖 Python、Go、Node.js、C++ 编译/执行、运行时禁网、UID 1000、只读根文件系统、CPU/内存/PID/`shm`/`tmpfs` 限制、无端口/设备/宿主挂载、后台进程清理，以及工作区跨 stop/start 和容器重建保留、destroy 后彻底消失。普通 `go test ./...` 不接触 Docker；也可在已有测试 Controller 前显式设置 `EASYGO_SANDBOX_INTEGRATION_URL` 后单独运行 `go test ./integration/sandbox -v`。

100 个并发申请和 waiter 取消属于额外压力轮，显式设置 `SANDBOX_RUN_STRESS=1` 启用。测试配置仍保持最多 30 个申请和 3 个运行容器：超出的并发申请必须返回结构化 `application_limit_reached`，已获批申请的运行容器采样值不得超过 3；等待容量的请求被取消后必须可以重新进入队列。退出时再次验证该测试 namespace 下容器和卷均为零。

```bash
SANDBOX_RUN_STRESS=1 ./scripts/test-sandbox-docker.sh
```

需要采集本机时延分布时，在同一次隔离运行中显式打开 benchmark：

```bash
SANDBOX_RUN_BENCHMARK=1 \
SANDBOX_BENCH_OUTPUT=sandbox-benchmark.json \
./scripts/test-sandbox-docker.sh
```

默认先丢弃 10 次全生命周期预热，再对并发度 1/3/6 和 `noop`、Python、Go、Node.js、C++ 负载各执行 100 次，并分别覆盖 active reuse、stop/start 和容器丢失后复用原 volume 重建三条路径。v4 JSON 同时保留原始样本和汇总：queue/create/start/ready/exec/stop/remove/destroy 的时延、错误率，exec 窗口内 Docker cgroup CPU time、采样峰值 memory/PIDs（附带 block I/O），以及各宿主生命周期 phase 的 Docker daemon/Desktop backend CPU time delta、峰值 RSS 和匹配 PID 数。宿主字段为 `host_cpu_time_ms`、`host_peak_rss_bytes`、`host_peak_pids`、`host_stats_samples`、`host_observation_method` 和 `host_missing_reason`；发现或采样不可靠时数值字段缺失而不是补 0。`queue` 由 Controller 精确记录从进入容量队列到取得运行槽的时间，包含为腾出槽位而执行的 LRU stop，不包含前置空间检查、`max_starting` 信号量等待或 Engine create/start；cold `create` 则是 HTTP 请求开始到宿主收到 Docker create event 的上界，包含前置检查、排队和事件传输，不会伪装成纯 Engine create 时间。Docker socket 由测试脚本显式传入；单独运行命令时可用 `-docker-host`、`-namespace`，或复用 `DOCKER_SOCKET_PATH`。这些指标必须在目标机器上实测，不能由其他机器的数据代替；benchmark 只读取进程表和已运行的 Docker API，绝不会自行启动或停止 Docker。

停止正式 Controller 时，先停止服务，再运行一次受信任的 cleanup 模式，可立即删除该 namespace 下的所有申请、容器和命名卷。该操作会永久删除沙箱工作区，不会删除 PostgreSQL 数据卷：

```bash
docker compose --profile sandbox stop sandbox-controller
docker compose --profile sandbox run --rm --no-deps sandbox-controller -config /etc/easygo/sandbox-controller.yaml -cleanup
docker compose --profile sandbox rm -f sandbox-controller
```

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

CLI 启动时展示该会话的原始历史。TUI 顶部固定显示居中的 `EASY GO` banner 和 `running / queued` 计数；`Enter` 会立即把输入写入持久化队列，即使当前任务仍在运行也可以继续输入。队列区显示序号、短 run ID、输入摘要和状态。`Tab` 切换输入框与队列焦点，队列焦点下用 `↑/↓` 选择项目，`Delete` 取消 queued 项；运行中 `Ctrl+C` 取消当前任务，空闲且没有待处理任务时退出。TUI 启动会恢复数据库中的 queued/running 项，终端尺寸变化时队列区会按可用高度截断。

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
| `POST /v1/users/{user}/sessions/{id}/runs` | 持久化并排队一轮，立即返回 `202`；请求体 `{"input":"..."}`，可带 `Idempotency-Key` |
| `GET /v1/users/{user}/sessions/{id}/runs` | 返回当前 queued/running 任务 |
| `GET /v1/users/{user}/sessions/{id}/runs/{run_id}` | 查询 run 状态、队列位置、结果和关联 turn |
| `DELETE /v1/users/{user}/sessions/{id}/runs/{run_id}` | 取消 queued/running run；终态幂等返回当前资源 |
| `GET /v1/users/{user}/sessions/{id}/runs/{run_id}/events` | SSE：先发当前状态，再发同进程实时事件，终态后关闭 |

`limit` 为 1–100。历史的 `after` 是上次返回的轮次 ID；`messages` 字段直接返回 Eino 原生消息。POST runs 默认返回包含 `run_id`、`status`、`position` 和 `created_at` 的 `202` 资源；客户端断连不会取消已经接受的 run。队列满返回 `429`，正在关闭或存储不可用返回 `503`。请求体最大 64 KiB。无论 Gateway 或 TUI，每轮都走相同的长期记忆检索和注入路径。

PowerShell 示例：

```powershell
$session = Invoke-RestMethod -Method Post http://127.0.0.1:8080/v1/users/alice/sessions
$runUrl = "http://127.0.0.1:8080/v1/users/alice/sessions/$($session.id)/runs"
Invoke-RestMethod -Method Post $runUrl -ContentType application/json -Headers @{"Idempotency-Key"="client-key"} -Body '{"input":"计算 12 * 34"}'
Invoke-RestMethod "http://127.0.0.1:8080/v1/users/alice/sessions/$($session.id)/messages"
```

SSE 的每个事件包含 `event: <kind>` 和 JSON `data`，首先是 `queued` 或当前终态，随后可能包括：

- `text_delta`：`text` 文本增量。
- `tool_started` / `tool_finished`：`tool` 名称。
- `compressing` / `compressed`：摘要状态。
- `completed`：本轮完整展示文本，且已持久化。
- `failed` / `canceled`：运行终态。

同一会话严格 FIFO，不同会话可以并行，默认最多 4 个 worker。running 租约每 10 秒 heartbeat，30 秒过期后可恢复；应用关闭会取消 running、保留 queued。SSE 的文本与工具增量只在执行进程内实时分发，其他进程上的订阅会轮询 durable run 状态并在终态可见后结束。`GET /messages` 只返回审计历史，不混入队列元数据。

无效请求返回 400，会话不存在或用户名不匹配返回 404，幂等 key 与输入冲突返回 409。SSE 已开始后的错误通过终态事件传递。Gateway 和 CLI 共享同一数据库时，可访问同一用户名下的同一会话。

## 模块位置

- `internal/agent/deepagent.go`：构造 Eino 主 Agent、摘要 Agent、上下文预算中间件。
- `internal/agent/runtime`：QueueManager、已 claim run 的执行、流合并、运行生命周期和展示事件。
- `internal/conversation`：PostgreSQL 与内存存储、`agent_runs` 队列和租约提交。
- `internal/usermemory`：独立记忆库、时间/调用次数 50/50 排名、03:00 consolidation Agent 和 Storage 物化。
- `internal/tui`：banner、队列面板、历史展示、流式输出和取消。
- `internal/gateway`：HTTP JSON / SSE 接口。
- `internal/sandbox`：Docker Engine 适配、申请/容量/TTL 状态机、bbolt 对账和内部鉴权 HTTP。
- `cmd/sandbox-controller`：可信本地 Controller；包含健康检查与显式 cleanup 模式。
- `cmd/sandbox-bench`：显式启用的端到端沙箱时延 benchmark。
- `internal/app`：配置、依赖组装和模式切换。

## 运行结束与资源释放

`Start` 只预留当前 runtime，首次 `Next()` 才加载历史并调用 Agent。`Cancel()` 非阻塞地请求取消；runtime 会自动提交已接收的原生审计、关闭消息流并释放会话锁，即使调用方已经停止读取事件。父 context 取消也会触发同样的收尾。

退出时调用 `Run.Close()`：它请求取消并等待收尾，返回最终 `Event`，可与 `Next()` 并发或重复调用。`Close()` 不消费展示事件，也不会把已经完成的结果改为取消；提交失败返回 `failed`。HTTP、单次 CLI 与 TUI 共用这一规则，TUI 的关闭错误会返回应用入口。

收尾提交使用独立的 10 秒超时 context；运行中的模型、工具和 Store 应遵守传入 context 的取消信号。成功终态只在提交和释放完成后可见。首次 `Next()` 前取消会释放预留，不调用模型或创建审计轮次。

## 验证

```powershell
go test ./...
go vet ./...
# 200 个实际 Eino Agent / Runtime 的用户记忆端到端验收（必须 100%，门槛为 95%）。
go test ./internal/agent/deepagent.go -run TestUserMemoryEndToEndAcceptanceAtLeastNinetyFivePercent -count=1 -v
# 需要启用 CGO 且安装 C 编译器。
go test -race ./...
# 使用专用测试数据库。测试只清理自己的随机用户名记录。
$env:EASYGO_TEST_DATABASE_URL = 'postgres://easygo:easygo@127.0.0.1:5432/easygo?sslmode=disable'
go test ./internal/conversation -run TestPostgresContract -v -count=1
```

模型测试使用 fake model 或本地 OpenAI-compatible 测试服务器，不调用真实模型。涵盖工具循环、原生流合并、跨轮恢复、摘要成功/失败、摘要超预算、最大迭代、取消、提交失败、会话隔离、分页、HTTP 断连和单次 CLI 启动。PostgreSQL 集成测试未配置 `EASYGO_TEST_DATABASE_URL` 时明确跳过。

Eino 参考：[Summarization middleware](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_summarization/)。实际实现以 `go.mod` 固定的 Eino v0.9.13 源码为准。
