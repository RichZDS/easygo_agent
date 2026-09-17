# 框架改造与验收

基线：`e1e3f0d`。目标：效率、可读性、项目结构、agent loop 可观察性。

采用 codebase-design 的 deep module 原则：保持 CLI/TUI/HTTP 依赖的 QueueManager / Run interface，复杂的租约提交、消息聚合和订阅实现各归一处；观测通过 context 传递，不能靠 UI 继续消费事件才能完成。

## 实施范围

- Runtime：分离公开运行契约、迭代器消费、持久化收尾、订阅分发和 worker 生命周期。
- 性能：每条流消息只聚合一次；预算内路径只测量一次；按 run 定位订阅，避免每个 token 扫描全部运行。
- 观测：JSON 日志关联 session/run/execution/span，记录阶段开始、结束、耗时、模型 usage、工具失败与恢复、压缩前后估算、队列与持久化错误。默认不记录完整消息内容，debug 可显式开启。
- 文档：说明新模块职责、日志查询和验证命令，修正过时启动/运行说明。

## 不变量

- 仍使用 Eino v0.9.13 的原生消息；历史不从展示文本重建。
- 同会话 FIFO、跨会话并行；租约 fencing、取消与失败审计保持原语义。
- 完成事件只能在最终事务提交和资源释放后出现。
- 观测不能消费或丢弃模型流，不能把恢复为工具结果的错误误报为成功。
- 新 execution ID 区分同一 durable run 的不同执行尝试，不记录 claim token。
- 未报告的 provider usage 不伪装成零；输入预算估算与实际 token 使用量分开。

## 验收

1. 所有现有测试、`go vet ./...`、`go test -race ./...`。
2. 相同机器/工具链运行改造前后 benchmark，报告具体负载及局限。
3. 实际 Eino fake-model loop 验证主模型、工具、压缩、异常和并发的观测关联。
4. 取消/无人继续读事件、提交失败与重试只有一个正确终态日志。
5. 提供无需模型密钥的自动化验证和日志阅读方式。

不把旧的 `agent-run-log-audit.md` 中数据库 raw ledger / SaaS trace 建议视为本次已实现能力。本次基于已有本地 JSON 日志建立可用的运行观测，不新增数据库表或外部上报。

## 模块分工

| 修改位置 | 职责与维护入口 |
| --- | --- |
| `runtime/contracts.go`、`events.go` | Run / Event 契约、终态与展示事件转换 |
| `runtime/runtime.go`、`input.go` | 消费 Eino 迭代器、聚合原生流；准备输入和记忆检索 |
| `runtime/lifecycle.go` | 取消、原子提交、失败重试与资源释放，只有一处决定最终结果 |
| `runtime/queue.go`、`worker.go` | 请求接收与查询；claim、执行、heartbeat、过期恢复 |
| `runtime/subscription.go`、`handle.go` | 按 run 定位订阅、授权检查、缓冲和断开；请求 handle |
| `telemetry/trace.go`、`model.go` | execution/span 身份、顺序、幂等收尾；透明模型适配器 |
| `deepagent/compression.go`、`safetool.go` | 在真实预算、压缩和工具调用处记录结果，不从 UI 事件推算耗时 |
| `runtime/debug.go`、`logger/file.go` | 原生 Eino 快照归 runtime；通用 logger 只负责 JSON 文件输出 |

没有增加新的 QueueManager / Run 方法，也没有新增框架总线或假设未来供应商的接口。CLI、TUI、HTTP 继续跨同一个 seam，内部复杂度按责任集中。维护者可从 `lifecycle.go` 检查提交不变量，从 `subscription.go` 检查订阅授权与缓冲，不必在一个队列文件中来回跳转。

TUI 同时修复了一个取消反馈缺口：首个文本尚未被接收时，取消也显示 `assistant (canceled)`，不再让任务无提示消失。

## 效率结果

Go 1.25.14 / linux amd64 / AMD EPYC 9754 / GOMAXPROCS=4。同一机器先运行基线 `e1e3f0d`，再运行修改版，各三次；未同时运行 race 编译或其他本任务测试。下表为三次中位数。基线使用相同 benchmark 输入，仅将订阅 map 初始化适配回旧的 `subscriptionKey` 类型。

| 基准负载 | 基线 | 修改后 | 解释 |
| --- | ---: | ---: | --- |
| Fit：40 条消息，每条 40 次 `context `，输入预算 24000 | 76,033 ns/op | 38,907 ns/op | 耗时下降约 49%，预算内路径复用已有测量 |
| 同上，内存 | 33,287 B/op，80 allocs/op | 16,644 B/op，40 allocs/op | 避免二次 JSON 测量，分配次数减半 |
| 发布到无订阅的 run，另有 1 个 run key | 60.77 ns/op | 26.30 ns/op | 直接按 run key 查找 |
| 发布到无订阅的 run，另有 1000 个 run key | 8,690 ns/op | 28.02 ns/op | 消除每次发布的全表遍历 |

三次耗时原始值（ns/op）：Fit 基线 `76033 / 75684 / 77115`，修改后 `38907 / 38283 / 39383`；1000 key 发布基线 `8690 / 8784 / 8633`，修改后 `29.69 / 28.02 / 27.88`。

发布基准隔离了“无关 run key 数量”的开销，不包含有订阅者时的入队、消费、网络和数据库成本。以上不是模型端到端延迟提升比例。另两项直接去重：原生流只保留一份 chunk 列表，完整消息只合并一次供持久化和 debug 共用；成功构造 Agent 时不再分配未使用的迭代器管道。新增阶段日志有 JSON 写盘成本，但没有每 token 的 Info 日志；模型观测只维护计数，不复制或缓冲流。

```bash
go test ./internal/agent/deepagent ./internal/agent/runtime \
  -run '^$' -bench 'Benchmark(FitWithinBudget|PublishUnrelatedRuns)$' -benchmem -count=3
```

## 如何解释日志

- `run_id` 是 durable 请求；`execution_id` 是本进程的一次执行尝试。恢复可能出现相同 run ID、不同 execution ID。worker 编号只在所属进程内有意义。
- `sequence` 在单个 execution 内单调递增，`span_id` / `parent_span_id` 关联调用。主模型和工具是 run 的子阶段，摘要模型是 compression 的子阶段。不要把不同 execution 的 sequence 混排。
- `duration_ms` 是墙钟耗时。模型流阶段包含等待/消费流的时间；`first_chunk_ms` 是拿到首个 chunk 的时间，可能是元数据，不保证首个用户可见文本。并行工具和父子阶段会重叠，不能相加得到 run 耗时。
- `queue_wait_ms` 来自 durable `CreatedAt → StartedAt`。恢复后的再次 claim 会包含自最初入队以来的总等待；不是各次排队耗时之和。
- `usage_reported` 标明 provider 是否返回用量；未报告时省略 token 字段。同一调用的流式累计用量取各字段最大值，与固定版本 Eino 合并规则一致，不逐 chunk 相加。不计算费用，也不把估算当实际用量。
- budget 的 `estimate_before` / `estimate_after` 来自内容计数，`provider_usage_before` 是历史 provider usage 下界；`skill_shrunk`、`dropped_tools`、`summarized_messages` 描述 Fit 的压缩决策。它们不是精确 tokenizer。最后检查仍超预算会失败，不调用主模型。
- `tool` 的 `recovered` 表示工具确实报错，但被写为工具结果交给模型继续处理；它不等于 run 失败。超时作为 `failed` 带错误，显式 context 取消为 `canceled`。
- `persist` 的 `target_status` 是尝试写入的 run 状态；其自身 `status` 表示提交是否成功。取消竞争与失败状态回写可产生多条 persist 阶段，但 root run 只结束一次。
- root run 结束记录在持久化尝试、租约释放后产生。取消回调可能让子阶段结束日志稍晚于 root；按 span 配对分析，不以“文件最后一行”为终态判断依据。已取消模型流的 usage 只反映当时接收到的部分。

默认 Info 不写完整消息。`-debug` 开启 native 消息和工具参数/结果；现有评测入口仍显式使用 Debug。错误文本、工具名称来自真实执行，未做通用内容脱敏。本地日志没有新增轮转、数据库 raw ledger、跨进程事件重放或外部 SaaS 上报。独立评测/每日记忆任务没有进入 Run 时不生成运行 trace。仅关闭模型 reader 且不取消 context 时，Eino reader 没有 Close 回调，阶段要到 EOF、错误或 context 取消才收尾。

## 验证覆盖

| 要求 | 自动化证据 |
| --- | --- |
| native 流原样透传、累计 usage 不重复统计 | `telemetry/TestModelStreamPreservesMessagesAndCountsCumulativeUsageOnce` |
| 模型启动/生成/流中失败；未消费流取消后只结束一次 | `telemetry/TestModelErrorsAndUnconsumedCancellationFinishOnce` |
| 并发 execution 身份、顺序、幂等 finish | `telemetry/TestConcurrentExecutionsHaveIndependentIdentityAndOrderedPhases` |
| 实际 Eino 主模型→可恢复工具错误→主模型→提交的父子关联 | `deepagent/TestLoopTraceCorrelatesModelToolRecoveryAndCommit` |
| 实际 Eino 摘要 provider 失败可定位，主模型不继续调用 | `deepagent/TestFailedCompressionTracesSummaryModelWithoutCallingMain` |
| 两个实际 Eino loop 并行、内容和 trace 不串会话 | `deepagent/TestSharedAgentRunsIndependentUsersConcurrently` |
| 流式工具正常结束、启动/流中错误恢复、取消、nil reader | `deepagent/TestStreamingToolReportsRecoveryAndCancellation` |
| 无继续消费、取消、提交失败重试的 root 终态 | `runtime/TestTerminalObservationFollowsPersistenceEvenWithoutConsumer` |
| 日志级别、JSON 元数据可读、Debug 正文开关 | `logger/TestFileLevelKeepsMetadataAndGatesFullMessages` |
| TUI 首个文本前取消仍可见且回到 idle | `tui/TestQueueCancellationWithoutOutputIsVisible`、`TestQueueTUICancelStopsRunning` |

原有测试继续覆盖原生历史、压缩成功/失败/超预算、200 个记忆验收用例、会话 FIFO、lease fencing、慢订阅不丢事件、提交前取消竞争、HTTP/SSE 与 CLI 行为。上述测试使用内存 Store、真实 Eino 运行逻辑及 fake provider，无需模型密钥。

最终验证（2026-09-17，Go 1.25.14）：`go test ./...`、`go vet ./...`、`go test -race ./...` 均通过；`git diff --check` 与修改文件的 gofmt 检查通过。真实 PostgreSQL、独立记忆库和 Docker 集成测试需显式配置对应环境，本次未配置的测试跳过；不据此宣称验证过真实模型延迟或外部服务。
