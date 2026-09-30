> 历史文档：这里描述的代码已于 2026-09-30 从主分支移除，完整实现保留在 git tag `legacy-go-app-final`。

# 分层 skill 与可恢复后台任务

主 agent 继续使用 Go/Eino Deep Agent、会话 FIFO、长期记忆和原有沙箱。后台任务使用独立的 Eino AgenticModel 执行循环，以便在模型输出和工具副作用之间持久化检查点。旧配置未声明 `tasks` 时默认关闭；仓库示例启用。

## 运行

- `go run ./cmd/easygo-agent -mode gateway`：HTTP、会话队列、后台 worker。
- `go run ./cmd/easygo-agent`：TUI、会话队列、后台 worker；展示任务进度和自动汇总。
- `go run ./cmd/easygo-agent -mode worker`：常驻后台 worker 与主会话队列，处理任务与汇总。
- 单次 `-input` CLI 只提交和执行主会话；持久化子任务随后由常驻进程接手。

PostgreSQL 是重启恢复的事实来源。Memory 支持相同接口与进程内恢复流程，退出进程后数据不保留。各服务应使用相同数据库、skill 内容和角色配置。启用后台任务时，`database.max_conns` 至少为 `queue.max_workers + 2`，为任务落盘和通知保留连接，避免主会话持有全部连接时死锁。默认每个进程两个后台 worker；多进程可增加总体并发，同一任务由租约和 claim token 排他执行。

`subagent` 提供后台模型配置；摘要仍使用单独构造的模型与 agent。默认执行版本最多 32 次模型请求、从首次执行开始 15 分钟。模型请求前预留步数，截止时间和步数上限落盘，重启不重置；停机时间也计入该版本期限。显式续跑增加版本并开启新预算。

## Skill 与工具

目录只展示名称和触发描述。支持标准 YAML 多行描述、显式 `$skill-name` 和多 skill 组合。`list_skills(query)` 可重新发现完整目录，`load_skill(name)` 加载正文，`read_skill_resource(name,path)` 读取所属 skill 的 UTF-8 文本，限制 64 KiB；目录、二进制、越界路径和逃逸符号链接返回错误。

工具注册表统一保存 schema、组和恢复策略，`call_tool` 兼容入口使用同一个注册表。压缩保留技能发现、任务控制和未完成任务摘要；只有仍能通过 dispatcher 访问的非必要原生工具才可暂时移除 schema。

`analyst` 只允许读取和计算。`executor` 使用配置中的工具白名单，示例包括创建迷宫；沙箱和任务控制工具始终拒绝进入子 agent，`call_tool` 也不开放。子任务只收到简报、指定资料和自身历史，不能递归委派。现有主 agent 沙箱保持原有能力。

## 任务控制

主 agent 工具：`spawn_subagent`、`get_task`、`list_tasks`、`resume_task`、`cancel_task`、`update_plan`。身份来自可信 run 上下文，不能通过工具参数指定用户或会话。

`spawn_subagent` 参数示例：

```json
{"role":"analyst","goal":"验证给定数据的合计","constraints":["只使用提供的资料"],"acceptance":["给出计算结果及证据"],"materials":["数据：2、3"],"idempotency_key":"sum-check"}
```

幂等键作用域是发起 run。同键不同简报返回冲突。返回 task ID 表示受理。状态为 `queued/running/blocked/completed/failed/canceled`。

`update_plan` 可更新排队和终态任务；执行中的子 agent 用 `report_progress` 持久化自己的计划。`finish_task` 要求结构化 summary、evidence、unmet，存在未满足标准时进入 blocked。

正常结束父轮不会取消子任务。用户取消父 run 时级联取消当前执行的子任务；服务停止释放子任务租约。工具续跑绑定当前用户 run，HTTP 显式续跑不绑定已取消的旧父 run。

## 恢复协议

1. 完整原生模型输出和整批调用先保存。
2. 每个工具调用在执行前登记，执行后保存原始结果、错误与稳定幂等键。
3. 恢复先补齐工具批次，复用已保存结果；完整配对调用 ID 后才继续模型。
4. 只读工具可重试；声明幂等的工具必须实际使用上下文中的稳定幂等键。未知策略按不确定写操作处理。
5. 已开始但结果不确定的写操作进入 blocked，显示调用 ID。明确选择 retry，或补录已核实的原始结果，才可续跑。

```json
{"instructions":"继续验收","decisions":{"call-123":{"action":"result","result":"{\"ok\":true}"}}}
```

重试使用 `{"action":"retry"}`。格式版本、提示词、skill 内容和工具 schema/恢复策略的版本不兼容时明确阻塞；恢复旧配置后再续跑，不自动切换流程。执行记录、逐步事件和原始工具结果都保留在数据库，可导出 Markdown；不依赖沙箱文件。

## HTTP 与汇总

基础路径 `/v1/users/{user}/sessions/{id}/tasks`：

| 方法与路径 | 行为 |
| --- | --- |
| GET 基础路径 | 列出该会话任务 |
| GET `/{task_id}` | 任务详情、检查点和执行历史 |
| GET `/{task_id}?format=markdown` | 导出简报、证据与事件 |
| GET `/{task_id}/events?after=0` | 递增事件序号、最多 100 条及 next_after，断线后继续轮询 |
| DELETE `/{task_id}` | 取消任务 |
| POST `/{task_id}/resume` | 明确恢复决策和续跑要求，返回新执行版本 |

终态与 outbox 在同一事务保存，每个 task/version 至多一份通知。通知幂等进入原会话 FIFO，保留已排队用户输入的顺序。它具有独立的 `task_notification` 来源，以内部消息进入模型，不能启动或续跑委派。汇总读取处理时的最新对话约束；提交汇总 turn 与通知确认在同一事务中。Memory 使用同一把锁提交通知 run 的终态。停机中断的内部汇总重新排队；模型失败仍作为失败 run 和错误记录展示。

## 验证

```bash
go test ./...
go test -race ./internal/task ./internal/conversation ./internal/agent/runtime ./internal/agent/deepagent ./internal/app ./internal/tools ./internal/toolregistry ./internal/skill ./internal/gateway ./internal/tui
go vet ./...
EASYGO_TEST_DATABASE_URL='postgres://.../disposable?sslmode=disable' go test ./internal/task ./internal/conversation -count=1 -v
go run ./cmd/eval/skill -config configs/eval/skill.yaml -out doc/eval/skill-framework.md
```

PostgreSQL 故障测试使用独立临时 schema，验证检查点、工具结果、失效租约、双 worker、outbox 与汇总确认边界。真实模型评测需要 `MODEL_API_KEY` 和 `SUBMODEL_API_KEY`，报告保留实际加载的 skill 名、工具调用次数、模型调用次数和输入序列化字节；字节不是计费 token。使用 `-baseline previous.md.json` 比较同名用例。
