# Eino Agent 原始运行日志入库与可观测性推荐指南

> 调研日期：2026-09-09
> 仓库基线：easygo-agent，HEAD 312476e；当前 go.mod 使用 github.com/cloudwego/eino v0.9.13。
> 文档性质：基于当前工作树审计、Eino v0.9.13 源码和 CloudWeGo/CozeLoop 一方资料形成的设计建议。本审计代理只新增本文档，未修改业务代码；当前工作树另有未提交的队列/runtime 等改动，本文将“已实现”和“建议落地”分开说明。

## 1. 给决策者的结论

用户要的是“只把 Agent 运行产生的原始日志写进数据库，并能按唯一 ID + Agent/Graph/Chain 名称查询”。推荐采用两条互补管道：

1. 在业务入口和 Agent 执行入口建立应用自己的 ID 层级。
2. 用自定义 Eino Callback Handler 加运行时兜底，在 handler/runtime 自己收到 Eino 原生事件时立即复制成不可变快照，写入 PostgreSQL outbox/事件表。这是本地事实源；若必须保证先于第三方 parser，需在同一分发层显式串接，不能依赖多个全局 callback 的注册顺序。
3. 对支持目标消息类型的 ADK Agent 和 subagent 走 adk.TypedRunner（或等价的 flow wrapper），不要只依赖当前 runtime 中的直接 agent.Run；当前项目使用 `*schema.AgenticMessage`，其完整多 Agent/cancel/retry 能力仍需按 Eino 版本验证。
4. CozeLoop 作为可选的远程 Trace、延迟、Token 和模型分析副本，与本地 handler 双写；不要把它当作原始日志表、事务审计或 subagent 唯一来源。
5. 默认使用 strict_agent 过滤，落 Agent/AgenticAgent 生命周期、AgentEvent，以及 Agent 子树中 Graph/Chain/Workflow 的轻量 envelope（名称、scope、状态，不含模型/工具大 payload）；需要诊断内部模型、工具和节点细节时再打开 agent_tree。

一句话回答“Eino 有没有办法抓 subagent”：有。每个 Agent/AgentTool 子 Agent 需要自己的 callback 生命周期、scope_id 和 parent_scope_id；父 Agent 的 EmitInternalEvents 只是展示转发，不能代替子 Agent 的审计记录。Eino 的 RunPath 能说明路径，但不是唯一 ID。

对本仓库要特别注意：当前运行时的类型是 `adk.TypedAgent[*schema.AgenticMessage]`。在 Eino v0.9.13 中它只保证 single-agent；如果 subagent/取消监控/retry 是硬需求，应评估改用 `*schema.Message` 的 flow Agent 或升级并验证兼容性，不能把“换成 TypedRunner”当成自动补齐。

### 1.1 本文中的术语假设

- “agentloop”可能指 Eino ADK TurnLoop，也可能指 DeepAgent/ReAct 的内部迭代；本文分别覆盖 TurnLoop 的 OnAgentEvents 和普通 Agent callback/runtime 事件。
- “sunagent/agentoop”按上下文解释为 subagent、AgentTool 或多 Agent workflow。当前仓库的 Deep Agent 配置包含 WithoutGeneralSubAgent: true，因此主路径暂时没有通用 subagent；方案仍为后续启用 AgentTool/工作流预留。
- Graph、Chain、Workflow、Agent 的 Name 是查询维度，不是主键。相同名称可以在不同请求、不同嵌套层级反复出现。

## 2. 当前仓库审计

### 2.1 已有日志和运行链路

- 文件日志：internal/logger/file.go 的 logger.New 只建立按日的 Zap JSON 文件 sink（logs/yyyy-mm-dd.log），并替换全局 zap logger；没有数据库 sink、Agent 过滤器或运行 ID。
- Agent 日志辅助：internal/logger/agent.go 的 DebugEinoEvent 只把 TypedAgentEvent 做成日志快照；StreamChunkLog 先聚合 chunk 再打印，因此不会保留每个 chunk 的接收时间和边界。
- 运行生命周期：internal/agent/runtime/runtime.go 的 StartContext 保留一次运行，initialize 读取历史并启动 Eino，next 消费 ADK 事件，receiveMessageChunk 消费消息流，finalize 负责终态提交和资源释放。这些位置天然适合作为 run/scope/event 的边界。
- 当前 Eino 调用：initialize 仍直接调用 session.agent.Run(ctx, input)，没有使用 TypedRunner，也没有注入 Agent callback。即使底层模型、工具或 compose 节点可能触发自己的 callback，也不能据此推断 Agent 生命周期 callback 一定被触发。
- 压缩 Agent：internal/agent/deepagent.go/compression.go 也有独立的 context-compressor 直接 Run 路径；若它属于审计范围，必须显式复用同一 ID 和 sink。
- 代码审计没有发现业务层实际构建 compose Graph/Chain、ADK TurnLoop 或 AgentTool 的入口；本文对这些对象的说明是 Eino 能力边界和后续接入方案，不代表当前版本已经产生这类运行事件。
- HTTP/TUI：当前工作树的 app.Run 已构建并把 QueueManager 传给默认 gateway、CLI 和 TUI；现有按 run 的 SSE 路由只返回 QueueManager 的状态/实时语义 Event，不是 `agent_run_events` 的历史 raw 查询。仍保留不传 manager 的 gateway.New 兼容路径，此时会走 direct runtime，Event/eventPayload 没有可持久化的 run_id；上线前要明确禁止或补齐这个兼容入口。

### 2.2 当前数据库语义

- agent_sessions.id 是会话 UUID，生命周期跨多次运行。
- agent_turns.id 是终态提交时生成的 bigserial 轮次 ID；messages 保存用户输入和输出审计，适合对话历史，不适合实时事件流。
- 当前工作树新增的 agent_runs 表是队列/请求生命周期表：包含 queued/running/completed/failed/canceled、幂等键、取消标记、worker lease、result/error 和 turn_id。它解决“排队和领取哪一个请求”，不是不可变 Eino event ledger。
- 当前 app.buildQueue 已有队列/worker 组装代码，app.Run 的默认 gateway、CLI 和 TUI 已接入该队列；但测试、嵌入方或直接调用 gateway.New(store, agent) 仍可能走 direct runtime。两种入口必须共用同一套 run/execution/event sink。
- direct runtime 还存在存储实现差异：Memory.Begin 会拒绝同一 session 中已有 queued/running run；Postgres.Begin 当前只拿 session advisory lock 并读取会话，不检查 agent_runs 的 queued/running 行，因此直连 PostgreSQL 可能绕过队列/FIFO 约束。若保留两种入口，应统一语义，或明确生产环境只允许 queue-only。
- NewQueueManager 启动时会立即调用 RecoverExpired，并另起按 LeaseTTL/3（上限 10 秒）的周期恢复 goroutine；当前恢复错误没有进入指标/告警，且恢复事务仍只重置 agent_runs，尚未写入 attempt 终态。同一逻辑 run 可能被重新 claim，日志模型必须能区分 execution attempt。
- 当前 PostgreSQL pgRunLease.Heartbeat 在持有同一 mutex 后再次 Lock，成功续租路径存在自死锁风险；在修复并增加 PostgreSQL heartbeat 回归测试前，不应把当前队列实现视为稳定的监控承载层。本文只记录问题，不修改该业务代码。
- 队列基线风险曾包括 `agent_runs.status` 的默认值/入队写入不一致；当前工作树已补上 `DEFAULT 'queued'`，且 PostgreSQL `Enqueue` 显式写入 `status='queued'`。仍应把这项作为 migration 回归测试，确认已有数据库也完成迁移。
- 因此不要把 callback 事件直接塞进 agent_turns.messages，也不要让一个 agent_runs 行承载所有嵌套 Agent、Graph、Chain 和 stream chunk。应在现有 agent_runs 旁增加 scope 表和 append-only event 表。
- PostgreSQL 使用 pgx pool，Lease 在一次对话提交期间持有连接；日志 writer 若复用很小的连接池，长推理可能和会话提交互相争抢，需要独立连接配额或独立 pool。

### 2.3 直接缺口

1. 没有一次实际执行 attempt 级的不可变 execution_id（队列 run_id 不能覆盖 lease 重试）。
2. 没有从 Eino 原生 callback/event 到数据库的独立 sink。
3. 没有严格区分 Agent 日志和普通业务 logger。
4. 没有 parent-child scope，subagent 无法可靠回链。
5. 没有运行中的分页/tail 查询、写入失败重试和丢弃计数。
6. callback、runtime 语义事件、agent_turns 三种数据的去重规则尚未定义。

## 3. Eino 能力和边界

### 3.1 统一 callback Handler

Eino 的 callbacks.Handler 有五个时机：OnStart、OnEnd、OnError、OnStartWithStreamInput、OnEndWithStreamOutput。官方手册说明这些 callback 可覆盖组件、Graph 节点、Graph、Chain 和 Workflow，适合做观测和审计：[Callback User Manual](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/)。

v0.9.13 的 RunInfo 只有三类信息：

- Name：业务名称；Graph 节点通常来自 compose.WithNodeName，Graph 可用 WithGraphName。
- Type：实现类型，例如模型实现名。
- Component：类别，例如 Graph、Chain、Workflow、Tool、Agent 或 AgenticAgent。

RunInfo 不提供 request ID、run ID、trace ID、span ID 或父节点字段；名称也没有唯一性承诺。字段定义可核对 [v0.9.13 callbacks/interface.go](https://github.com/cloudwego/eino/blob/v0.9.13/callbacks/interface.go)。

同一 handler 的 OnStart 返回 context 会传给它自己的后续时机；不同 handler 之间没有保证的执行顺序，也不会自动传递 context。全局 handler 的 callbacks.AppendGlobalHandlers 必须在进程启动、任何执行之前调用一次，它不是并发安全的动态注册 API。流式 callback 收到的是框架复制的 reader，必须读取并关闭自己的副本；不要修改共享 Input/Output。相关实现见 [internal/callbacks/inject.go](https://github.com/cloudwego/eino/blob/v0.9.13/internal/callbacks/inject.go)。

### 3.2 Graph、Chain 和节点名称

Graph/Chain 适合提供拓扑和组件维度，但不适合提供唯一性：

- 构建 Graph/Chain 时为 Graph、Chain、节点设置稳定的业务名。
- 执行时通过 compose.WithCallbacks 或组件级 callback 注入 handler。
- 嵌套图用 DesignateNode、DesignateNodeWithPath 或自定义 scope path 保存上下文。
- 并行节点的 callback 到达顺序不代表因果顺序；应用必须自己生成 event_id 和每个 scope 内的 seq。

如果直接调用独立组件，按官方手册用 callbacks.InitCallbacks/ReuseHandlers 初始化 RunInfo，否则可能拿不到名称。只用名称前缀判断“是否在 Agent 内”不可靠。

### 3.3 ADK Agent、AgenticAgent 和 Runner

Eino ADK 复用同一个 Handler，但 Agent callback 的输入/输出是 ADK 专用类型：

- AgentCallbackInput 或 TypedAgentCallbackInput，包含新运行 Input，或者 checkpoint 恢复的 ResumeInfo。
- AgentCallbackOutput 或 TypedAgentCallbackOutput，包含独立的 Events 异步迭代器。
- v0.9.13 同时定义 adk.ComponentOfAgent 和 adk.ComponentOfAgenticAgent；使用 schema.AgenticMessage 的 typed Agent 可能走后者。可核对 [adk/interface.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/interface.go) 和 [adk/callback.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/callback.go)。

官方 [ADK Agent Callback](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/adk_agent_callback/) 明确说明 Agent callback 通过 Runner 执行才有完整语义；直接调用 agent.Run 不会自动触发 Agent lifecycle callback，除非自定义 Agent 自己显式调用 callback。v0.9.13 的 TypedRunner 源码将其定位为处理多 Agent 编排、callback、命名、RunPath、checkpoint 和取消的执行入口，但同版本 `TypedAgent` 注释同时限定：`M=*schema.AgenticMessage` 目前只保证 single-agent，模型流取消监控和 retry 尚未接通；需要完整多 Agent 时应评估 `*schema.Message` flow 或升级版本，并用集成测试确认：[adk/runner.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/runner.go)。

因此当前 runtime 的直接 session.agent.Run 是一个明确的观测盲区。两种补救方式：

1. P0 在 runtime 直接消费的 iterator 和 message stream 上做原始快照，保证当前代码先有事实记录。
2. P1 将执行入口迁移到 adk.NewTypedRunner，并通过 adk.WithCallbacks(rawHandler) 和 adk.WithSessionValues 注入业务 ID；但当前 `*schema.AgenticMessage` 只保证 single-agent，需先通过类型/版本集成测试，必要时切到 `*schema.Message` flow；再用 runtime 事件作为兜底。

不要把“给直接 agent.Run 加了 WithCallbacks”误认为已经得到 Runner 的 Agent lifecycle callback；必须用集成测试验证实际执行路径。

### 3.4 AgentEvent、RunPath 和错误

Agent callback 的 OnEnd 拿到 Events 迭代器时，Agent 可能仍在产生事件。每个 handler 有独立的 iterator copy，handler 必须立即启动受控 goroutine/worker drain 到关闭，否则会阻塞 Agent。Agent 错误经常作为 AgentEvent.Err 出现，不能只依赖通用 OnError；流中途错误也可能只出现在 reader 的 Recv 结果中。

RunPath 是框架维护的 Agent 来源路径，适合解释“哪个父 Agent 转给了哪个子 Agent”，但 RunStep/AgentName 仍是业务名称，不是 UUID。`RunStep.agentName` 在 Eino 中是未导出字段，不能直接对 `[]adk.RunStep` 做 JSON marshal（通常会得到空对象）；入库前应逐步调用 `RunStep.String()` 规范化为 `[]string`，再保存 JSON，并额外保存应用的 parent_scope_id。

### 3.5 AgentLoop 和 TurnLoop

如果“agentloop”指 ADK TurnLoop，应使用 TurnLoopConfig.OnAgentEvents 捕获每一轮事件，并由应用自行维护 turn_seq/turn_index，同时记录 Preempted、Stopped、StopCause；checkpoint/resume 细节应在 GenResume/ResumeParams 和 TurnLoop.Wait 返回的 TurnLoopExitState 中补录，不能假定都由 TurnContext 直接提供。官方快速开始见 [Agent cancel and TurnLoop](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_cancel_and_turnloop_quickstart/)。

一个 session 可能有多次 turn、重试或 resume，所以 session_id 不能代替 execution_id。若同时使用 Agent callback 和 OnAgentEvents，给事件标注 source，并用 event_id 或 payload hash 去重，避免同一个 AgentEvent 写两次。

### 3.6 Subagent、AgentTool 和 EmitInternalEvents

官方 [Agent Collaboration](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_collaboration/) 推荐用 AgentAsTool/AgentTool 组合协作（具体可用能力取决于 Agent 消息类型）：

- 子 Agent 是独立的 Agent 生命周期，应生成自己的 scope_id 或 agent_run_id。
- parent_scope_id 指向调用它的父 Agent；parent execution_id 通常保持不变，除非子任务是异步新请求。
- ToolsConfig.EmitInternalEvents=true 只把子 Agent 事件转发到父输出，主要用于终端用户显示。
- 官方明确说明 forwarded internal events 不写入父 runSession，也不改变父状态/checkpoint。因此只消费父输出流会漏掉子 Agent 原始事件。
- 每个子 Agent 的 callback handler 应自己消费 iterator；如果只能拿到父转发流，必须把 source 标为 pass_through，不得把它当成完整子 Agent 事实源。

Eino 的内置模板还提供 AgenticAgent 分支，可用 [utils/callbacks/template.go](https://github.com/cloudwego/eino/blob/v0.9.13/utils/callbacks/template.go) 的 NewHandlerHelper().AgenticAgent(...) 按组件类别分派自定义 handler。这比在 logger 文本中猜测“agent”前缀稳定得多。

## 4. ID、名称和查询模型

推荐的应用 ID 层级如下：

- request_id：一次 HTTP/RPC/消息请求的业务关联 ID；跨同一请求的重试或 resume 保持不变。
- run_id：一次逻辑请求/队列记录的用户可见 ID；当前工作树可对应 agent_runs.id。
- execution_id：一次执行边界（通常从 queue claim/lease 开始，覆盖后续 Runner.Run、Query、Resume 或 TurnLoop）；带 lease recovery 或重试时，同一个 run_id 可以有多个 execution_id。这样连“claim 后尚未成功启动 Runner”的失败也有可追踪记录。
- scope_id：一次 Agent、AgenticAgent、Graph、Chain、Workflow、Tool 生命周期；同一 execution 内可以有多个 scope。
- parent_scope_id：调用关系；subagent、AgentTool、嵌套 Graph/Chain 都用它回链。
- event_id：每条不可变事件的 UUID，作为幂等键；如果团队改用 ULID，数据库列也要改成 text/char(26)，不能和 uuid 混用。
- seq：execution 内由应用分配的全局单调“采集序号”（可另加 scope_seq）；并行事件只表达采集顺序，不伪造全局因果。
- trace_id/span_id：可选的 OTel/CozeLoop 关联字段，不能替代上述业务 ID。
- name/type/component/run_path：直接复制 Eino 语义标签，用于查询和解释。

当前工作树的 agent_runs.id 已经是 UUID，语义更接近逻辑队列 run。由于 RecoverExpired/worker lease 允许同一行被重新领取，默认不要把它当作唯一 execution_id。ClaimNext 已为每次 claim 生成唯一 claim_token，可把该 UUID 持久化为“claim 级 execution_id”，即使初始化/Runner 启动失败也保留 attempt 历史；若产品只想统计真正进入 Runner 的执行，再另加 `runner_invocation_id`，不要覆盖 claim 记录。同步 direct runtime 则在 StartContext 自己生成 execution_id，并先创建对应父 run。没有重试需求时可以暂时一一映射，但应把这个假设写入契约。不要把同一个 id 当成每个嵌套 Agent 的 scope_id。

Graph 和 Chain 不必各自发明一套主键：以 component=Graph/Chain、name=业务名称、scope_id=本次实例来表达即可。若后台查询极其频繁，再增加 graph_name/chain_name 的冗余列或物化索引；唯一性仍由 execution_id + scope_id 保证。

### 4.1 查询示例

按用户可见 run_id 回放全部 attempt：

~~~sql
SELECT x.attempt_no, e.execution_id, e.scope_id, e.occurred_at,
       e.seq, e.component, e.name, e.event_kind, e.payload, e.error_message
FROM agent_run_events AS e
JOIN agent_run_executions AS x
  ON x.execution_id = e.execution_id AND x.run_id = e.run_id
WHERE e.run_id = $1
ORDER BY x.attempt_no, e.seq;
~~~

按 `run_id + Graph/Chain 名称` 精确筛选：

~~~sql
SELECT x.attempt_no, e.execution_id, e.scope_id, e.parent_scope_id,
       e.occurred_at, e.seq, e.component, e.name, e.event_kind,
       e.payload, e.error_message
FROM agent_run_events AS e
JOIN agent_run_executions AS x
  ON x.execution_id = e.execution_id AND x.run_id = e.run_id
WHERE e.run_id = $1
  AND e.component IN ('Graph', 'Chain')
  AND e.name = $2
ORDER BY x.attempt_no, x.started_at, e.seq, e.occurred_at, e.event_id;
~~~

严格只查询 Agent 原始事件：

~~~sql
SELECT e.occurred_at, e.execution_id, e.scope_id, e.event_kind,
       e.name, e.type, e.component, e.seq, e.payload, e.error_message,
       e.source, e.capture_mode
FROM agent_run_events AS e
JOIN agent_run_executions AS x
  ON x.execution_id = e.execution_id AND x.run_id = e.run_id
WHERE e.run_id = $1
  AND e.component IN ('Agent', 'AgenticAgent')
  AND ($2::text IS NULL OR e.name = $2)
ORDER BY x.attempt_no, x.started_at, e.seq, e.occurred_at, e.event_id;
~~~

如果要跨同一业务请求的多个逻辑 run（例如一次请求触发重试或拆分任务），才使用 `request_id = $1`，并通过 `agent_run_executions.attempt_no/started_at` 排序；不要用随机 UUID 的字典序推断 attempt 顺序。

查询某次执行及其 Graph/Chain/子 Agent：

~~~sql
SELECT *
FROM agent_run_events
WHERE execution_id = $1
  AND ($2::uuid IS NULL OR scope_id = $2 OR parent_scope_id = $2)
ORDER BY occurred_at, seq, event_id;
~~~

如果要包含任意深度的子 Agent，应先对 agent_run_scopes 做 WITH RECURSIVE 树查询，再按 scope_id 过滤；不要用名称匹配推断父子关系。

以上 SQL 为内部查询示例，省略了用户/租户鉴权条件；实际 API 必须先通过 `agent_runs → agent_sessions`（或独立租户表）校验归属，再执行分页查询，不能把“知道 run_id”当作授权。

生产 API 建议提供（其中第一条是当前 queue 路由的语义说明，raw 历史查询仍需新增）：

- 当前已有 `GET /v1/users/{user}/sessions/{id}/runs/{run_id}` 和 `/events` SSE：前者返回队列状态，后者返回实时语义 Event，不等于 `agent_run_events` 历史 raw。
- 建议新增 `GET /v1/runs/{run_id}`（或沿用现有嵌套路由）返回事件汇总、状态、开始/结束时间、根 Agent。
- 建议新增 `GET /v1/runs/{run_id}/events?component=&name=&scope_id=&after_seq=&limit=`：从数据库分页/tail raw 事件。
- 按 session_id、request_id、Agent/Graph/Chain name、时间范围过滤。
- direct 兼容模式仍应补 run_id，且新接口还要返回 execution_id/attempt_no，便于区分重试；所有查询先校验 user/session/tenant 权限。

## 5. “原始日志”契约

本文将原始日志定义为：从 Eino callback、TypedAgentEvent 或 runtime 收到对象时，在本地 handler/runtime 边界立即复制出的结构化快照，与 CozeLoop/Langfuse parser、Zap 格式化和 semantic event 投影解耦。仅靠 callback 注册顺序不能保证它先于第三方 parser；需要严格先行时，应由同一 wrapper 先记录再分发。原始不等于把 Go 指针地址或不可序列化 reader 写进数据库。

如果“原始”在产品层面特指现有 Zap 的一整行字节，也可以额外保存 raw_line/text（DDL 中的 `raw_text`；若必须保留原始字节则改为 `bytea` 或对象存储引用）；但它不能替代 payload JSON。推荐同时保存结构化 envelope 和可选 raw_text：前者支持按 component/name/path 查询，后者用于问题复盘和格式升级。

建议记录以下事件类型：

- agent_start、agent_end、agent_error。
- agent_event：保留 AgentName、RunPath、Action、Err、CustomizedOutput、MessageOutput。
- component_start、component_end、component_error：Graph、Chain、Workflow、Tool 等按过滤模式选择。
- stream_chunk：每次成功接收的 AgenticMessage chunk，带 stream_index；另可保存最终聚合消息作为 derived 字段。
- runtime_recv_error、iterator_closed、turn_commit：用于解释 callback 和业务结果的差异。

对每条事件保存 canonical JSON 和 payload_sha256。不要盲目 `json.Marshal` 整个 Eino 对象：RunStep、error 和部分 Action 字段含未导出/不可序列化内容，应通过版本化 DTO 显式复制可审计字段（例如 RunPath 先转 `[]string`）；无法 JSON 化的对象保存 type_name、稳定文本摘要和 payload_encoding。输入、工具参数、工具结果和模型输出可能含 PII、token 或业务机密；脱敏是入库前的策略，须记录 redaction_version，必要时使用列级加密或受控对象存储。原始性不要求绕过合规。

若需要证明“收到的原始内容”未被改写，可在受控边界先计算 raw_payload_sha256，再对落库 payload 计算 stored_payload_sha256；不要把脱敏后的 hash 误称为原始字节校验。

在下方 DDL 中，`payload_sha256` 表示落库 canonical payload 的 hash；如果团队需要把命名严格区分为 `stored_payload_sha256`，应在迁移和 API 契约中同步改名。

示例 envelope：

~~~json
{
  "event_id": "01994b49-e640-7f20-9e89-8d33d4a5f101",
  "request_id": "req-...",
  "run_id": "a7d29767-3140-4d14-b2e9-e0ed034a27b1",
  "execution_id": "b1ae5691-cf11-42f8-8c0b-62a7bd7faf85",
  "scope_id": "db403461-602b-4241-9541-18edb04ea4fb",
  "parent_scope_id": null,
  "seq": 17,
  "occurred_at": "2026-09-09T10:20:30.123Z",
  "component": "AgenticAgent",
  "name": "deep-agent",
  "type": "DeepAgent",
  "event_kind": "agent_event",
  "agent_name": "deep-agent",
  "run_path": ["deep-agent", "research-subagent"],
  "capture_mode": "strict_agent",
  "payload": {
    "agent_name": "research-subagent",
    "action": {},
    "output": {},
    "error": null
  },
  "source": "eino_callback",
  "payload_sha256": "...",
  "schema_version": 1,
  "redaction_version": 1
}
~~~

不要只依赖现有 StreamChunkLog.Flush：它只输出 concat 后的一条消息，会丢失 chunk 边界、接收时间和中途失败位置。

## 6. PostgreSQL 数据模型

### 6.1 与现有 agent_runs 的关系

当前 agent_runs 继续承担队列、幂等、worker lease 和终态结果。新增事件表不要复用 queued/running 状态字段，也不要在 agent_runs 中放无限增长的 payload。推荐：

- agent_runs：一次逻辑队列 run 的汇总父记录；必要时补 request_id、root_name、trace_id、metadata。
- agent_run_executions：一次实际推理 attempt 一行，处理 lease recovery、重试和 resume 的执行边界。
- agent_run_scopes：一次 Agent/Graph/Chain/Tool 生命周期一行，保存嵌套关系。
- agent_run_events：append-only 原始事件，一行一个 callback/event/chunk。

若未来确认队列表和执行审计不是同一概念，可把现有表重命名为 agent_requests，再让 agent_executions 独立承载执行；第一阶段不必做破坏性重命名。

### 6.2 设计稿 DDL

以下是增量设计稿，不是本次迁移脚本；落地时请按现有 schema migration 机制拆分版本。

~~~sql
ALTER TABLE agent_runs
  ADD COLUMN IF NOT EXISTS request_id text,
  ADD COLUMN IF NOT EXISTS root_name text,
  ADD COLUMN IF NOT EXISTS trace_id text;

CREATE TABLE IF NOT EXISTS agent_run_executions (
  execution_id uuid PRIMARY KEY,
  run_id uuid NOT NULL REFERENCES agent_runs(id),
  attempt_no integer NOT NULL DEFAULT 1,
  claim_token uuid,
  status text NOT NULL CHECK (status IN ('running','completed','failed','canceled','abandoned')),
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  terminal_reason text,
  worker_id text,
  metadata jsonb NOT NULL DEFAULT '{}',
  UNIQUE (execution_id, run_id),
  UNIQUE (run_id, attempt_no)
);

CREATE TABLE IF NOT EXISTS agent_run_scopes (
  scope_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES agent_run_executions(execution_id),
  parent_scope_id uuid,
  component text NOT NULL,
  name text NOT NULL,
  type text,
  run_path jsonb NOT NULL DEFAULT '[]',
  status text NOT NULL CHECK (status IN ('running','completed','failed','canceled','abandoned')),
  started_at timestamptz NOT NULL DEFAULT now(),
  ended_at timestamptz,
  metadata jsonb NOT NULL DEFAULT '{}',
  UNIQUE (execution_id, scope_id),
  CONSTRAINT agent_run_scopes_parent_same_execution_fk
    FOREIGN KEY (execution_id, parent_scope_id)
    REFERENCES agent_run_scopes(execution_id, scope_id)
);

CREATE TABLE IF NOT EXISTS agent_run_events (
  event_id uuid PRIMARY KEY,
  run_id uuid NOT NULL,
  execution_id uuid NOT NULL,
  request_id text,
  scope_id uuid NOT NULL,
  parent_scope_id uuid,
  seq bigint NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  component text NOT NULL,
  name text NOT NULL,
  type text,
  event_kind text NOT NULL,
  agent_name text,
  run_path jsonb NOT NULL DEFAULT '[]',
  payload jsonb NOT NULL,
  payload_sha256 text NOT NULL,
  raw_payload_sha256 text,
  payload_encoding text NOT NULL DEFAULT 'json',
  raw_text text,
  error_code text,
  error_message text,
  source text NOT NULL,
  capture_mode text NOT NULL DEFAULT 'strict_agent',
  schema_version integer NOT NULL DEFAULT 1,
  redaction_version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (execution_id, seq),
  CONSTRAINT agent_run_events_execution_run_fk
    FOREIGN KEY (execution_id, run_id)
    REFERENCES agent_run_executions(execution_id, run_id),
  CONSTRAINT agent_run_events_scope_same_execution_fk
    FOREIGN KEY (execution_id, scope_id)
    REFERENCES agent_run_scopes(execution_id, scope_id),
  CONSTRAINT agent_run_events_parent_scope_same_execution_fk
    FOREIGN KEY (execution_id, parent_scope_id)
    REFERENCES agent_run_scopes(execution_id, scope_id)
);

CREATE INDEX IF NOT EXISTS agent_run_scopes_execution
  ON agent_run_scopes(execution_id, started_at);
CREATE INDEX IF NOT EXISTS agent_run_executions_run
  ON agent_run_executions(run_id, attempt_no);
CREATE INDEX IF NOT EXISTS agent_run_scopes_parent
  ON agent_run_scopes(parent_scope_id);
CREATE INDEX IF NOT EXISTS agent_run_events_run_time
  ON agent_run_events(run_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS agent_run_events_execution_seq
  ON agent_run_events(execution_id, seq, occurred_at);
CREATE INDEX IF NOT EXISTS agent_run_events_request_time
  ON agent_run_events(request_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS agent_run_events_component_name
  ON agent_run_events(component, name, occurred_at DESC);
CREATE INDEX IF NOT EXISTS agent_run_events_path
  ON agent_run_events USING gin(run_path);
~~~

并发 callback 仍应通过 execution 级原子计数器分配 seq；它表示采集顺序而非业务因果。应用层继续用 event_id 做重放幂等。当前 DDL 可直接按 occurred_at 做时间分区；若要按租户分区，需在 `agent_runs`/事件表增加明确的 `tenant_id` 分区键（不能仅凭跨表 JOIN 做 PostgreSQL 分区）。大 prompt/tool output 可只在事件表保存 hash 和对象存储引用，但要在契约中标记 payload_encoding。

`parent_scope_id` 必须指向同一 `execution_id` 内的父 scope；上面的设计稿用 `(execution_id, scope_id)` 复合唯一键/外键强化了这一约束。若现有迁移体系暂时无法添加复合外键，则必须由 writer 在入队前校验，避免跨请求串树。

如果产品明确保证一个 run 永不重试，可以省略 agent_run_executions，让 agent_runs.id 直接作为 execution_id；当前队列已有 lease recovery，默认保留这张 attempt 表更安全。

当 lease 过期或 worker 丢失时，RecoverExpired 应在同一恢复事务里把旧 execution 标为 abandoned、写 ended_at/terminal_reason，再把逻辑 run 放回 queued；下一次 ClaimNext 使用新的 claim_token/execution_id。终态更新必须用 claim_token/execution_id 做 fencing，迟到的旧 worker 事件仍可归入旧 abandoned attempt，但不能改写新 attempt 或逻辑 run 的状态。否则旧 attempt 会永久停在 running，或出现新旧 worker 互相覆盖。

当前 agent_runs.started_at 会在恢复/重新 claim 时被清空或覆盖，所以它不是可靠的“首次开始时间”。汇总页应从 agent_run_executions 计算 MIN(started_at)/MAX(ended_at)，或在 agent_runs 另存 first_started_at。

## 7. 过滤策略：只写 Agent，不污染普通日志

### 7.1 strict_agent（默认）

只持久化以下内容：

- adk.ComponentOfAgent。
- adk.ComponentOfAgenticAgent。
- 上述 Agent scope 的 start/end/error/AgentEvent，以及必要的 runtime stream chunk。
- 作为 Agent 子树边界的 Graph、Chain、Workflow envelope，以及直接 Tool 调用的轻量 start/end/error envelope（component/name/path/status/call_id）；内部 ChatModel 和 Tool 的完整 input/output payload 默认不落库。

runtime 兜底事件也必须使用根 Agent 的规范化 `component`（当前 typed 路径用 `AgenticAgent`，普通 Agent 用 `Agent`），并以 `source=runtime_fallback` 标记；不要把 `Runtime` 当成第三种组件，否则按 component 的查询会漏掉兜底事件。

普通 logger.Info、HTTP access、配置、数据库 SQL、业务 debug 不进入 agent_run_events；它们继续写现有 Zap 文件或指标系统。

生产环境可对高频 stream_chunk 做采样或按时间窗口聚合，但 agent_start、agent_end、agent_error、AgentEvent.Err 和工具调用边界不采样；采样率、聚合方式和 dropped_count 必须写入运行元数据。

### 7.2 agent_tree（诊断开关）

在 strict_agent 基础上，允许同一 execution 下的 Graph、Chain、Workflow、Tool、ChatModel 等子组件事件。每一行保留 capture_mode、source、scope_id 和 parent_scope_id，便于区分 Agent 生命周期与内部诊断。

如果“Graph/Chain 日志”指每个节点的原始输入输出，strict_agent 的轻量 envelope 不够，必须显式打开 agent_tree，并接受更高的事件量和敏感数据风险。

不要依赖全局“当前是否在 Agent 中”的可变布尔值。更稳妥的方法是：

1. 入口生成 request_id/run_id，并在每次实际 attempt 生成 execution_id。
2. 通过 adk.WithSessionValues 注入不可变的 request_id、run_id、execution_id；不要把可变的 current scope/ scope 栈放进共享 SessionValues（shared parent session 下子 Agent 可能互相覆盖），scope 状态放在 handler 自己返回的 context 或显式 child option 中。
3. Graph/Chain 显式传同一 handler，并为节点创建 scope。
4. 在 Sequential/Parallel/Loop、AgentTool、取消、resume 集成测试中验证 context 传播。

Runner 会在运行时重新建立 context，所以这些值要在每次 Run/Query/Resume 调用时注入，不能只在构造 Runner 时设置。

如果无法证明 scope 传播正确，宁可退回 strict_agent，也不要把同进程其他请求的模型/工具日志误写到 Agent 审计表。

## 8. 推荐实现方式

### 8.1 P0：先覆盖当前直接 Run

在不改变 Agent 构造的情况下，先改造 runtime 的四个边界；这些边界同时覆盖 Session.StartContext 和队列 worker 的 NewClaimed：

1. 同步 direct runtime 在 StartContext 同时生成/接收 run_id 和 execution_id；在写事件前必须先创建对应的 `agent_runs` 逻辑父行（或改用独立的 `agent_requests` 父表），否则本设计 DDL 的外键会拒绝 direct 事件。队列路径在 Enqueue 只生成 run_id，在 ClaimNext 时把 claim_token 固化为 claim 级 execution_id（若要严格区分 Runner 启动，再额外生成 runner_invocation_id）。两条路径都创建本地 EventRecorder 和 seq 分配器；更简单的生产约束是让所有入口统一走 QueueManager。
2. initialize 在调用 agent.Run 前写 execution start；同时把 request/session/user 作为 context 元数据。
3. next 每消费一个 TypedAgentEvent 就复制原始 payload；receiveMessageChunk 每收到一个 chunk 就复制并编号。callback 没有覆盖时，这一层是兜底事实源。
4. finalize 在 completed/failed/canceled、调用方停止消费和父 context 取消时只写一次终态，先关闭 stream，再在有界超时内 flush writer。

这一步直接覆盖 CLI、TUI 和 HTTP，因为三者都经过 runtime。它不能凭空补齐 Eino Agent lifecycle callback，但能保证当前主 Deep Agent 的原始运行记录不漏。

当 P1 callback 与 P0 runtime 同时启用时，先约定事实来源：组件 start/end 由 callback 负责，顶层 iterator/chunk 由 runtime 负责，或反过来但只能选一处写入；另一处只写 projection/reference。不要用“内容相同就去重”的临时逻辑代替明确的 source 和 event_id 规则。

### 8.2 P1：统一到 TypedRunner + 自定义 Handler

推荐的调用形态如下，代码仅作 API 示意，正式实现需按项目输入类型编译验证。TypedRunner 是执行边界，不是 TypedAgent 接口的同类型替换；现有 Session.agent 字段可保留，通过 adapter/runner 在调用处切换。对当前 `*schema.AgenticMessage`，这段代码不能自动获得完整多 Agent、stream cancel 或 retry 能力。

~~~go
runner := adk.NewTypedRunner(
    adk.TypedRunnerConfig[*schema.AgenticMessage]{
        Agent:           agent,
        EnableStreaming: true,
    },
)

events := runner.Run(
    ctx,
    messages,
    adk.WithCallbacks(rawHandler),
    adk.WithSessionValues(map[string]any{
        "request_id":    requestID,
        "run_id":        runID,
        "execution_id":  executionID,
    }),
)
~~~

rawHandler 应实现 callbacks.Handler 的五个时机，或用 Eino 的 NewHandlerHelper 组合 Agent、AgenticAgent、Graph、Chain 分支。关键行为：

- OnStart：读取 RunInfo，创建 scope_id，复制输入和应用自己的 ID/context 值；公开 OnStart 没有可靠的 RunPath getter，RunPath 应从 TypedAgentEvent/AgentEvent 读取并规范化，不把 Name 当唯一键。
- OnEnd：取得 TypedAgentCallbackOutput.Events，立即异步 drain；每个 AgentEvent 保留 AgentName、RunPath、Action、Output 和 Err。
- OnError：记录 Graph/Chain/Tool 等普通组件错误；Agent 的错误仍从 AgentEvent.Err 读取。
- OnStartWithStreamInput/OnEndWithStreamOutput：消费并关闭 handler 自己的 stream copy。
- callback 只做快照和入有界队列，不在模型 goroutine 中等待数据库网络 I/O。

一个 handler 的私有状态放在它自己返回的 context 中；不要依赖另一个 handler 先写 context，因为 Eino 不保证不同 handler 的顺序。

### 8.3 P2：Graph/Chain、压缩 Agent 和 subagent

- 所有 Graph/Chain 构建处设置稳定 WithGraphName/WithNodeName，并显式传 callbacks。
- context-compressor 作为独立 Agent 记录独立 scope；可用 parent_scope_id 指向主 Agent。
- AgentTool 子 Agent 在自己的 Runner/callback 中生成 scope_id；父 Agent 只记录 tool 调用和 child reference。
- EmitInternalEvents 只服务 UI；不能把父 pass-through 计为 child 的完整 raw source。
- TurnLoop 使用 OnAgentEvents，由应用写 turn_seq/turn_index 和 stop cause，并在 GenResume/退出状态处写 checkpoint/resume；与 Agent callback 使用 source + event_id 去重。

### 8.4 异步写入和 outbox

推荐结构：

~~~text
Eino callback/runtime
  └─ 复制快照、生成 event_id、放入有界队列
       └─ DB writer 批量 INSERT agent_run_events
            └─ 可选 CozeLoop/OTel 异步 exporter
~~~

建议：

- 事件 writer 使用独立连接配额，避免占满会话 Lease 的 pgx pool。
- 每个 execution 先在一个短事务中落 `agent_run_executions` 和根 `agent_run_scopes`，再允许异步批量写 event；若使用外键且子 scope 也异步创建，必须保证 scope 行先于 event，或在同一事务使用可延迟约束，不能让 callback 事件先到而永久失败。
- 关键事件（start、end、error、AgentEvent.Err）不能静默丢失；队列满时可只丢低优先级 token chunk，并递增 dropped_events。
- 使用 event_id 幂等插入和指数退避；DB 短暂不可用时保留本地 outbox/WAL 或可重放文件。
- 进程退出前 drain；flush 使用 context.WithTimeout(context.WithoutCancel(...))，不能因调用方已断开而遗留 goroutine。
- 不要把实时事件追加到 agent_turns 的终态事务；否则运行中的日志不可查且会放大提交延迟。

### 8.5 与现有 logger 的关系

不建议在现有 Zap 全局 core 上按 message 文本筛选后直接写数据库：普通业务日志和 Agent 日志共用全局 logger，字符串筛选会漏事件或误收无关日志，也无法正确处理 AgentEvent iterator 和 stream。若确实需要把少量业务日志关联到运行，可在 logger 字段中显式加入 execution_id/scope_id，再由独立的 AgentAuditHandler 写事件表；不要用 logger 行反推 Eino 拓扑。

## 9. CozeLoop 评估

### 9.1 能做什么

CozeLoop 的 Eino 集成通过 callback 把模型、工具和 Agent 的生命周期转换成远程 Trace/span，适合可视化、延迟、Token、模型调用和运营分析。官方资料：[Eino Trace 上报](https://docs.coze.cn/cozeloop_eino_trace_report)、[Trace 数据筛选](https://docs.coze.cn/cozeloop_trace-data)、[Go SDK](https://docs.coze.cn/cozeloop_go-sdk)。

典型初始化形态（实际构造参数以锁定版本 README 为准）：

~~~go
client, err := cozeloop.NewClient(/* workspace/token/base URL */)
if err != nil {
    return err
}
handler := cozeloopcb.NewLoopHandler(client)
callbacks.AppendGlobalHandlers(handler) // 进程初始化阶段只调用一次
// shutdown 时用带超时的 context 调用 client.Close(ctx)
~~~

CozeLoop client 应在进程级复用，退出时 Close/Flush，避免异步队列尚未上报的数据丢失。不要在每个请求中追加全局 handler。

### 9.2 当前版本的关键风险

本次调研通过 Go module proxy 能解析到的最高版本是 github.com/cloudwego/eino-ext/callbacks/cozeloop v0.3.1；该版本 go.mod 的依赖基线为 Eino v0.9.1，而本仓库是 v0.9.13。Go module 的最小版本选择不等于行为兼容保证，必须锁版本并做集成测试。

引入时应在隔离分支显式固定版本（示意：go get github.com/cloudwego/eino-ext/callbacks/cozeloop@VERSION），提交 go.mod/go.sum，并把 Eino、eino-ext 和 Go toolchain 版本写入兼容性矩阵；不建议直接使用浮动的 latest。本文没有执行 go get，也没有把第三方依赖加入当前工作树。

至少应从记录 ADK tracing 支持的 [v0.2.0 release note](https://github.com/cloudwego/eino-ext/releases/tag/callbacks%2Fcozeloop%2Fv0.2.0) 起评估；“有 ADK 支持”仍不等于覆盖当前的 AgenticAgent 常量。

更关键的是，v0.3.1 的 Agent 专用分支在 [tagged trace_handler.go](https://github.com/cloudwego/eino-ext/blob/callbacks%2Fcozeloop%2Fv0.3.1/callbacks/cozeloop/trace_handler.go) 和 [tagged data_parser.go](https://github.com/cloudwego/eino-ext/blob/callbacks%2Fcozeloop%2Fv0.3.1/callbacks/cozeloop/data_parser.go) 主要按 `adk.ComponentOfAgent` 判断；基础 RunInfo 的 name/type/component 仍会写入 span，但 `ComponentOfAgenticAgent` 在 span 类型映射、专用 payload、RunPath 和 AgentEvent 聚合上的行为不能想当然。Eino v0.9.13 的 schema.AgenticMessage typed Agent 会使用 `adk.ComponentOfAgenticAgent`，因此接入前三选一：

1. 选取源码明确支持 ComponentOfAgenticAgent 且与 v0.9.13 通过回归测试的版本。
2. 自定义/扩展 parser 和 handler，补齐 AgenticAgent 分支。
3. 在兼容层转换组件类型，但必须确认不会丢失 typed event。

CozeLoop 的 Exporter 接收的是规范化 span（trace/span/parent、input/output、tags、duration 等），不是逐条原始 Eino event；可核对 [coze-dev/cozeloop-go client.go](https://github.com/coze-dev/cozeloop-go/blob/main/client.go)、[entity/export.go](https://github.com/coze-dev/cozeloop-go/blob/main/entity/export.go) 和 [span.go](https://github.com/coze-dev/cozeloop-go/blob/main/span.go)。

### 9.3 推荐双写而非替换

~~~text
Eino callback
  ├─ AgentAuditHandler：原始快照 → 本地 PostgreSQL/outbox（事实源）
  └─ CozeLoop Handler：规范化 span → 远程 Trace/UI（观测副本）
~~~

CozeLoop 的网络、异步批量、平台留存和外部数据边界都与本地事务不同。Prompt、工具参数和模型输出若发送到 SaaS，须先完成脱敏、区域、权限和合规评审；远程不可用不能阻塞 Agent，也不能影响本地审计。

## 10. OTel、Langfuse 和其他后端

- OTel 适合把 request_id/execution_id 映射到跨服务 Trace，并经 Collector 转发多个后端；标准 span/event 仍不是 Eino AgentEvent 原始存储。
- Eino 的通用 OTel callback 仍有公开提案 [issue #1028](https://github.com/cloudwego/eino/issues/1028)；在 v0.9.13 上应使用自定义 Handler 或已验证的厂商 handler，不要假设有稳定内置实现。
- 本次本地核查到的 eino-ext/callbacks/langfuse/v2 模块基线约为 Eino v0.9.14；它走 OTel 并提供 Agent observation（见 [Langfuse v2 README](https://github.com/cloudwego/eino-ext/blob/main/callbacks/langfuse/v2/README_zh.md) 和 [go.mod](https://github.com/cloudwego/eino-ext/blob/main/callbacks/langfuse/v2/go.mod)）。版本会变化，若采用先在隔离分支完成 Go/Eino 升级和兼容性测试。
- 无论后端是 CozeLoop、OTel 还是 Langfuse，都只能作为远程规范化副本；本地 raw event 表仍是审计事实源。

## 11. 可靠性、安全和运营

### 11.1 可靠性

- 每个 event_id 可重放且幂等；event_seq 由应用生成，不依赖数据库插入顺序。
- 记录 writer queue length、write latency、retry count、dropped_events、serialization_error 和 remote_export_error。
- 关键终态必须最终落地；运行中事件允许按策略降级，但降级要可观测。
- 对 iterator、stream reader、runtime.Next、调用方停止消费、父 context cancel、进程优雅退出分别做资源泄漏测试。

### 11.2 数据安全

- 禁止记录 API key、Authorization header 和数据库凭据。
- Prompt、工具参数、工具结果、模型输出按字段脱敏或加密；为策略版本打 redaction_version。
- 按租户/用户做查询授权和访问审计；设置 TTL、分区、归档和删除策略。
- DDL 外键默认不带 `ON DELETE CASCADE`；做 TTL/删除时先归档并按 events → scopes → executions → runs 的依赖顺序清理，或采用分区 detach/受控级联，避免“append-only”与父记录删除互相冲突。
- 大对象设大小上限，超限保存 hash + 对象存储引用，并在 payload_encoding 中标明。

### 11.3 查询和告警

- 以 execution_id/run_id + name/component/path 查询；名称只作为过滤条件。
- 为运行中的查询提供 after_seq 游标，避免 offset 扫描。
- 失败率、取消率、平均/分位时延、AgentEvent.Err、队列积压和 CozeLoop 上报失败应进入指标和告警。

## 12. 分阶段落地路线

### Phase 0：契约和迁移

- 定义 request_id、run_id、execution_id、scope_id、parent_scope_id、event_id、seq。
- 确认 strict_agent 为默认过滤，agent_tree 为显式诊断开关。
- 以版本化 migration 增加 agent_run_executions、agent_run_scopes、agent_run_events；保留现有 agent_runs 队列语义。
- 先修复 PostgreSQL Heartbeat 的重复加锁，并决定是否增加周期性过期 lease 恢复；否则 writer/worker 的可靠性测试没有稳定基线。
- 先写内存 RawRunSink 和 payload 脱敏/哈希单测。

### Phase 1：当前 Deep Agent 可审计

- 在 StartContext/initialize/next/receiveMessageChunk/finalize 接入 raw recorder。
- HTTP/SSE 返回 run_id，增加按 run 查询 API。
- 增加 PostgreSQL 批量 writer、幂等和 shutdown drain。
- 覆盖 completed、failed、canceled、调用方不再消费、stream 中途错误。

### Phase 2：Runner、Graph/Chain、subagent

- 在消息类型和版本支持的前提下将主 Agent 和压缩 Agent 迁移到 TypedRunner；若继续使用 `*schema.AgenticMessage`，保留 runtime 兜底并单独验证 subagent/cancel/retry。
- 给 Graph/Chain/Workflow 设置稳定名称并注入 handler。
- 为 AgentTool/Sequential/Parallel/Loop 增加 child scope、parent_scope_id 和 RunPath 测试。
- 验证 ComponentOfAgenticAgent 的 callback 和 CozeLoop parser。

### Phase 3：远程观测和运营

- 本地事实源稳定后再接 CozeLoop；把业务 ID 写入 tags/baggage 以便回链。
- 有跨服务需求再引入 OTel Collector；有评估/Prompt 管理需求再评估 Langfuse。
- 建立 retention、归档、权限和成本控制。

## 13. 验收测试清单

- [ ] 同一 session 并发请求时 run_id 唯一、队列约束和终态只写一次。
- [ ] 普通 logger.Info/Error 不进入 agent_run_events；Agent/AgenticAgent 事件能按 session、run、name 查询。
- [ ] Runner.Query/Run/Resume 都触发预期 Agent callback；直接 agent.Run 的覆盖边界有明确测试。
- [ ] AgentCallbackOutput.Events 被完整 drain，且不会阻塞主 Agent。
- [ ] AgentEvent.Err、iterator error、stream Recv error 都有原始快照。
- [ ] 非流式输出、多个 stream chunk、concat 失败都保留 chunk 边界和 seq。
- [ ] Graph/Chain/Workflow 节点名称、同名节点和嵌套 path 不串数据。
- [ ] AgentTool 子 Agent 在 EmitInternalEvents=true/false 时都能独立落库；父 pass-through 有 source 标记。
- [ ] Sequential/Parallel/Loop 并发事件的 seq 语义可解释，没有重复 callback。
- [ ] 取消、checkpoint resume、调用方停止消费和优雅退出无 goroutine/连接泄漏。
- [ ] DB 短暂不可用、队列满、writer 重启时关键事件可重试或从 outbox 重放。
- [ ] 相同 event_id 重放不会重复；payload hash 可校验。
- [ ] CozeLoop v0.3.1（或选定版本）对 ComponentOfAgenticAgent 的 span 类型映射、专用 payload/RunPath/AgentEvent 聚合有回归测试，远程失败不影响本地事实源。
- [ ] 脱敏、加密、TTL/分区和查询授权通过安全评审。

## 14. 最终推荐和待确认项

### 最终推荐

对当前项目优先实施：

1. 自定义 AgentAuditHandler + runtime raw recorder。
2. PostgreSQL agent_run_executions + agent_run_scopes + agent_run_events（现有 agent_runs 继续做队列父记录）。
3. 统一 ID 和 parent-child scope；默认 strict_agent。
4. 在消息类型/版本支持并通过回归测试后，将主 Agent/压缩 Agent 迁移到 TypedRunner；当前 `*schema.AgenticMessage` 路径先保留 runtime 兜底；TurnLoop 使用 OnAgentEvents。
5. CozeLoop 作为可选双写的 Trace/UI，不作为原始日志事实源；先解决 AgenticAgent 版本兼容。

这个组合在相应 Eino 消息类型/版本通过集成测试后，可以覆盖 AgentLoop/TurnLoop、Graph、Chain、Deep Agent、subagent 和流式事件，同时保留按唯一 run_id + 名称查询的能力，不会把普通 logger 日志或第三方平台的异步/采样语义混入本地审计。

### 建议产品/安全评审确认

- “原始”是否要保留完整 prompt、tool 参数和模型输出，还是只保留结构化事件与 hash？
- ChatModel/Tool 是否默认算 Agent 子树，还是只有 agent_tree 打开时才保存？
- PostgreSQL 的租户隔离、保留期、加密和对象存储区域要求是什么？
- CozeLoop 是否允许把原始内容发送到外部 SaaS；若不允许，只接入其 callback 思路，不启用网络 exporter？
- 查询 API 是否需要实时 tail；若需要，outbox/WAL 和背压策略应在 Phase 1 一并实现。

## 15. 资料索引

### Eino 官方文档和源码

- [Callback User Manual](https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/)
- [ADK Agent Callback](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/adk_agent_callback/)
- [ADK overview](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_preview/)
- [Agent Collaboration / AgentTool](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_collaboration/)
- [Agent cancel and TurnLoop quick start](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_cancel_and_turnloop_quickstart/)
- [v0.9.13 callbacks/interface.go](https://github.com/cloudwego/eino/blob/v0.9.13/callbacks/interface.go)
- [v0.9.13 internal/callbacks/inject.go](https://github.com/cloudwego/eino/blob/v0.9.13/internal/callbacks/inject.go)
- [v0.9.13 compose/graph_call_options.go](https://github.com/cloudwego/eino/blob/v0.9.13/compose/graph_call_options.go)
- [v0.9.13 adk/interface.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/interface.go)
- [v0.9.13 adk/callback.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/callback.go)
- [v0.9.13 adk/call_option.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/call_option.go)
- [v0.9.13 adk/flow.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/flow.go)
- [v0.9.13 adk/runner.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/runner.go)
- [v0.9.13 adk/agent_tool.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/agent_tool.go)
- [v0.9.13 adk/turn_loop.go](https://github.com/cloudwego/eino/blob/v0.9.13/adk/turn_loop.go)
- [v0.9.13 utils/callbacks/template.go](https://github.com/cloudwego/eino/blob/v0.9.13/utils/callbacks/template.go)

### CozeLoop 和第三方

- [CozeLoop 数据上报总览](https://docs.coze.cn/cozeloop_trace_integrate)
- [CozeLoop Eino Trace 集成](https://docs.coze.cn/cozeloop_eino_trace_report)
- [CozeLoop Go SDK](https://docs.coze.cn/cozeloop_go-sdk)
- [CozeLoop Trace 数据筛选](https://docs.coze.cn/cozeloop_trace-data)
- [CozeLoop OTel 上报](https://docs.coze.cn/cozeloop_opentelemetry_sdk_trace_report)
- [eino-ext/callbacks/cozeloop README](https://github.com/cloudwego/eino-ext/blob/main/callbacks/cozeloop/README_zh.md)
- [eino-ext CozeLoop v0.2.0 ADK support](https://github.com/cloudwego/eino-ext/releases/tag/callbacks%2Fcozeloop%2Fv0.2.0)
- [eino-ext CozeLoop v0.3.1 go.mod](https://github.com/cloudwego/eino-ext/blob/callbacks%2Fcozeloop%2Fv0.3.1/callbacks/cozeloop/go.mod)
- [eino-ext CozeLoop v0.3.1 trace_handler.go](https://github.com/cloudwego/eino-ext/blob/callbacks%2Fcozeloop%2Fv0.3.1/callbacks/cozeloop/trace_handler.go)
- [eino-ext CozeLoop v0.3.1 data_parser.go](https://github.com/cloudwego/eino-ext/blob/callbacks%2Fcozeloop%2Fv0.3.1/callbacks/cozeloop/data_parser.go)
- [Eino native OTel proposal #1028](https://github.com/cloudwego/eino/issues/1028)
- [coze-dev/cozeloop-go client.go](https://github.com/coze-dev/cozeloop-go/blob/main/client.go)
- [coze-dev/cozeloop-go entity/export.go](https://github.com/coze-dev/cozeloop-go/blob/main/entity/export.go)
- [coze-dev/cozeloop-go span.go](https://github.com/coze-dev/cozeloop-go/blob/main/span.go)
- [coze-dev/cozeloop-go trace.go](https://github.com/coze-dev/cozeloop-go/blob/main/trace.go)

### 本仓库相关位置

- internal/logger/file.go：Zap 文件 sink。
- internal/logger/agent.go：Eino event/message 的当前日志快照。
- internal/agent/runtime/runtime.go：StartContext、initialize、next、receiveMessageChunk、finalize。
- internal/agent/deepagent.go/agent.go：主 Deep Agent 构造和名称。
- internal/agent/deepagent.go/compression.go：context-compressor 路径。
- internal/conversation/schema.sql：agent_sessions、agent_turns 以及当前工作树的 agent_runs 队列表（建议旁边增加 execution/scope/event 表）。
- internal/conversation/store.go、memory.go、postgres.go：Store、Lease 和 QueueStore 语义。
- internal/gateway/http.go：运行接口和 SSE 投影。
