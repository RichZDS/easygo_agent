> 此文记录 `eba47cf` 的 Go 本地应用阶段。当前三服务 RPC 部署以 [services/README.md](../services/README.md) 和 [RPC 契约](../contracts/rpc-v1.md) 为准；旧服务命令和 Bearer 接线不适用于新端口。

# CLI workshop

Workshop is a standalone Go service for running a small catalog of operator-defined
Claude Code or Codex CLI workflows. It stores an immutable workflow snapshot per
task, creates a private workspace, records native session IDs and durable events,
and retains every run attempt. It does not depend on the AI gateway or native loop.
The CLI engine owns its internal agent loop; the native loop can submit a workshop
task as a tool, then poll or resume it through the service API.

## Run

Requires Go 1.25 and Linux for the native subprocess runner. Build and edit the
example's workflow model, executable path and environment allowlist for your
installed CLI. No provider credentials or bearer values belong in the JSON file.

```bash
go build -o /tmp/workshop ./cmd/workshop
# Set WORKSHOP_BEARER_TOKEN through your operator secret environment.
# For Codex, CODEX_HOME may point to a dedicated, already authenticated CLI home.
/tmp/workshop -config configs/workshop.example.json -listen 127.0.0.1:8091
```

The default listener is `127.0.0.1:8091`. The executable refuses a non-loopback
listener without a configured bearer token. An empty `bearer_token_env` permits
unauthenticated local operator access; a named but unset variable fails startup.
TLS and network access controls belong at the operator's reverse proxy when the
service is exposed remotely.

Each workflow requires `name`, `version`, `instructions`, `engine`, `model`,
`policy`, and `timeout_seconds`. `artifacts` is an optional list of exact relative
file paths; declared artifacts must exist after successful execution. HTTP callers
can select only a configured workflow and supply input. They cannot select an
executable, command options, timeout, policy or workspace path.

## HTTP contract

Task routes start at `/v1/tasks`; `GET /v1/workflows` discovers the configured
catalog under the same bearer authentication. It returns a name-sorted array with
only `name`, `version`, `engine`, `model`, `policy`, `timeout_seconds` and `artifacts`.
Instructions, executable paths and environment configuration are omitted. The
catalog is shared across namespaces. The bearer is an **operator credential** and
authorizes all namespaces. `?namespace=...` selects a trusted caller-owned scope;
omitting it selects `operator`. This is service-to-service scoping, not end-user
authentication. When no bearer is configured, namespace selection is limited to
`operator`. The request body must omit `namespace`; unknown JSON fields are rejected.
An agent tool wrapper must derive namespace privately from the runtime user and
session rather than accepting it from model arguments.

```bash
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  'http://127.0.0.1:8091/v1/workflows'

curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"workflow":"write-note","input":"Explain the release checklist","idempotency_key":"release-note-1"}' \
  'http://127.0.0.1:8091/v1/tasks?namespace=my-session'

# Use the returned task ID for the commands below.
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID?namespace=my-session"
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  'http://127.0.0.1:8091/v1/tasks?namespace=my-session'
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID/events?namespace=my-session&after=0"
curl -X POST -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID/cancel?namespace=my-session"
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  -H 'Content-Type: application/json' -d '{"input":"Add rollback instructions"}' \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID/resume?namespace=my-session"
```

Create/resume return 202 and a task record. Get/list/cancel/events return 200.
Unknown workflow or malformed request returns 400, namespace mismatch and missing
task return 404, state or idempotency conflict returns 409, and capacity exhaustion
returns 429. Events are durable JSON records, paginated up to 1000 per response;
pass the last `sequence` as the next exclusive `after` cursor. This is polling,
not SSE. Artifacts appear on each run as relative path, byte size and SHA-256.
There is no HTTP filesystem download endpoint.

### Bounded views and complete result pages

Without `view`, operator task endpoints keep returning complete task records and
run histories. Add `view=summary` to create/get/cancel/resume to receive
`{id, namespace, status, run_count, runs}` with only the latest attempt in `runs`.
The attempt includes:

- `id`, `status`, an error preview with `error_truncated`.
- `text`, `text_bytes` (full stored byte length), and `text_truncated`.
- `artifact_count`, an `artifacts` preview, and `artifacts_truncated`.

Text previews are valid UTF-8 prefixes of at most 16 KiB; errors are limited to
4 KiB. Artifact previews contain at most 32 entries with complete paths no longer
than 1024 bytes. Oversized paths are omitted, never shortened into different paths.
Counts and truncation flags always describe what was omitted. These bounds keep a
task summary below 512 KiB even with JSON escaping. Full artifact manifests remain
available from the raw operator task endpoint.

`GET /v1/tasks?view=summary&offset=0&limit=20` returns a page object
`{tasks, offset, next_offset}`. `tasks` contains metadata only: task identity,
status, total run count, and the latest run's identity/status/text byte count and
artifact count. It has no result text, error strings or artifact arrays. The order
is ascending creation time then task ID. `next_offset` is `null` at the end;
otherwise pass it as the next offset. Limit defaults to 20 and must be 1–100.
Offsets must be nonnegative and no greater than the current task count.

`GET /v1/tasks/{id}/result?run_id=...&offset=0&limit=8192` returns
`{task_id, run_id, text, offset, next_offset, total_bytes, eof}`. Omit `run_id` to
select the latest attempt; use the returned run ID for all later pages so a resume
cannot change which result is being read. Historical run IDs remain readable.
Offsets are byte cursors at UTF-8 boundaries. Limit defaults to 8192 and must be
4–32768 bytes. The page end moves back to a rune boundary if needed; concatenate
`text` and follow `next_offset` until `eof` is true. An offset equal to total length
returns empty text and `eof: true`. Unknown task/run or wrong namespace returns 404;
invalid cursor, limit, duplicate paging parameter or view returns 400. An unfinished
run's stored result may still be empty; use task status to distinguish completion.

```bash
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID?namespace=my-session&view=summary"
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  'http://127.0.0.1:8091/v1/tasks?namespace=my-session&view=summary&offset=0&limit=20'
curl -H "Authorization: Bearer $WORKSHOP_BEARER_TOKEN" \
  "http://127.0.0.1:8091/v1/tasks/$TASK_ID/result?namespace=my-session&offset=0&limit=8192"
```

The application's workshop tools always request summaries while retaining their
1 MiB HTTP response defense. `workshop_get` exposes preview flags;
`workshop_list` accepts optional `offset`/`limit` and returns the metadata page;
the read-only `workshop_result` tool accepts `task_id`, optional `run_id`, and
optional `offset`/`limit` to read complete text. Ownership comes from trusted runtime
identity and is never a model argument. Raw endpoints and database schemas are
unchanged; pagination bounds responses, not the storage required for old attempts.

Within a namespace, submitting the same nonempty idempotency key with the same
workflow name and input returns the original task, including after restart or a
workflow catalog update. Reusing that key with different input or workflow is a
conflict. Different namespaces can reuse the same key independently. Resume is
explicitly a new attempt, not an idempotent create: it requires a terminal task
with a saved native session ID and retains the same snapshot and workspace.

## Embedding

```go
service, err := workshop.New(config, nil) // nil selects CommandRunner
if err != nil { return err }
defer service.Close()

task, err := service.Submit(workshop.SubmitRequest{
    Namespace: trustedRuntimeScope,
    IdempotencyKey: operationID,
    Workflow: "write-note",
    Input: userInput,
})
// Get(scope, id), Cancel(scope, id), Resume(scope, id, input)
// List(scope), Events(scope, id, after)
// Workflows() []WorkflowMetadata returns independent metadata copies.
// Summary(scope, id), ListPage(scope, offset, limit)
// Result(scope, id, runID, offset, limit)
```

`Runner.Run(ctx, Invocation, emit)` is the small substitution point for tests or
other trusted executors. It must honor cancellation, return after its processes
stop, propagate event persistence errors, and report success only after a native
terminal success. `emit` persists events before returning; a session event also
persists the native session ID immediately. `Task.Runs` exposes each attempt's
status, result text, usage, errors, timestamps and artifacts.

## Execution and recovery

The service uses bbolt at `<root>/workshop.db` with an exclusive process lock.
Workspaces live at `<root>/workspaces/<generated UUID>`. Admission is bounded by
`concurrency + queue_capacity`, including active work; only `concurrency` runners
execute simultaneously. Cancellation of queued work prevents its execution.
Running cancellation first records `cancelling`, then kills the process group and
records `cancelled`. Deadlines produce `timed_out`. Graceful service shutdown joins
workers and marks unfinished work `interrupted` before releasing storage.

On restart, both queued and running attempts become `interrupted`. They are never
automatically replayed. A saved native session can be explicitly resumed after the
operator checks any external effects. A task that never received a native session
cannot resume; submit a new task deliberately. Native CLI session storage must
remain available for resume; a stored ID alone is not a backup of CLI history.
An external supervisor should terminate the service's process tree on abrupt
service death; restart recovery records task state, not external side-effect
rollback or reattachment to orphan processes.

The supported CLI arguments were checked against the installed help on
2026-09-25:

- Codex: `exec --json --skip-git-repo-check --ignore-user-config --ignore-rules`,
  explicit `approval_policy="never"` and `sandbox_mode="read-only"` or
  `"workspace-write"`, `--model`, prompt on stdin using `-`. Resume uses
  `exec resume ... <native-session-id> -`; configuration overrides preserve the
  sandbox policy because the resume subcommand has no `--sandbox` flag.
- Claude: `--print --output-format stream-json --verbose --bare --restricted
  --strict-mcp-config --permission-prompts none`. Read-only allows only
  `Read,Glob,Grep` with `dontAsk`; workspace-write additionally allows `Edit,Write`
  with `acceptEdits`. Restricted mode confines file tools to the workspace.
  Shell/code execution and MCP tools are not enabled. Resume adds `--resume <id>`.
  Bare mode uses explicitly supplied API credentials rather than ambient OAuth.

Older CLI releases without these flags fail explicitly; there is no fallback to
permission bypass. Zero exit status alone never means success: Codex requires
`turn.completed`, Claude requires a successful `result`, and a native session ID
must be present. Malformed JSONL, terminal failure, nonzero exit, missing terminal
result, event persistence failure, or excess stdout output fail the task.

Only operator-allowlisted environment variables are inherited. The default HOME,
CODEX_HOME and CLAUDE_CONFIG_DIR are private under the task workspace, and PATH is
minimal. Add an existing CLI config home or custom PATH only deliberately. Known
passed credential values are redacted from normalized output and bounded stderr
diagnostics. No environment dump or raw JSONL is stored. Stderr diagnostics are
bounded and visibly marked if truncated; stdout exceeding `max_output_bytes`
(default 4 MiB per attempt) fails execution rather than silently dropping content.

The service uses argument arrays and `exec.CommandContext`, never a shell. Linux
cancellation kills the process group, including ordinary inherited descendants;
it is not containment for a hostile process that creates a different session.
For that threat model use an external container/cgroup supervisor. Artifact reads
use Go's rooted filesystem API to reject traversal and symlink escapes. Only
regular files up to 256 MiB each are accepted; FIFOs/devices are rejected without
blocking. The service does not claim that CLI tool permissions are a complete OS
security boundary. Operator configuration, local storage and the Runner are trusted.

## Verification

```bash
go test ./pkg/workshop ./cmd/workshop
go test -race ./pkg/workshop ./cmd/workshop
go vet ./pkg/workshop ./cmd/workshop
```

The native tests launch a real child executable fixture that emits Claude/Codex
JSONL. They verify argv, stdin, cwd, environment isolation, terminal classification,
bounded output, credential redaction, cancellation with descendants, deadlines,
resume, durable restart recovery, namespace isolation/idempotency, artifact
containment, queue/concurrency and authenticated HTTP behavior. They make no live
LLM calls. The restart test seeds an interrupted durable run to verify startup
recovery; it does not claim to simulate a machine power loss.

Design references were read only: reference runner C's engine profiles, subprocess launch and
resume logic, normalized activity and workflow models. The implementation avoids
its business workflow state machine, deployment hooks and compatibility layers.


## P1 施工者通道

配置 `pack_dir` 可加载 `<pack_dir>/roles/worker.md`（可选，UTF-8，最多 16 KiB）。controller 镜像内置 `/opt/easygo/packs/base`。工坊在工作流说明之前插入施工者说明，保留 `User input:\n` 输入分隔符；未配置 pack 时沿用原输入。

任务容器和带模型 relay 的 host 任务会收到 `EASYGO_CREW_URL`、`EASYGO_CREW_TOKEN`。`easygo-crew` 复用该 relay，通过 `report` 汇报、`ask` 提问、`blocked` 报卡住、`submit --tests pass|fail|not_run` 报审，用 `inbox [--after N]` 收取工头消息。每次提交有持久化 id/sequence 回执；网络失败最多重试三次，复用 client_id。没有通道时命令退出 2。

工头用 `workshop.message` 写入任务收件箱，用 `workshop.events` 读取 `crew.message` 和 `crew.read`。消息支持幂等键；运行中可读取，任务停止后收到的未读消息在下次 resume 输入中补入并标记已读。每个 run 的施工者消息最多 200 条，收件箱每页最多 50 条。JSON 字段、大小和鉴权要求见 [P1 契约](../contracts/rpc-v1.md#p1-harness-扩展v13)。

run 结束后 `outcome` 按最后一条 submit/blocked/ask 判定为 submitted/blocked/asked；没有则为 none，report 不改变判定。进行中的 run 不输出 outcome。任务最多 256 个 run，get 的 `run_ids` 提供按时间排列的完整 id 集合。

### 离线 crew 夹具

`fixture-cli` 接受 `{"mode":"crew","script":[...]}`。脚本按顺序执行：

| op | 字段与作用 |
|---|---|
| crew | `args`：easygo-crew 参数数组，如 `["report","started"]` 或 `["submit","--tests","pass","ready"]` |
| write | `path` 为工作区相对路径，`text` 为文件内容 |
| inbox | 轮询至收到消息；`text` 可指定匹配片段，`after` 缺省 0，`timeout_ms` 缺省 5000、上限 60000 |
| duplicate | 用相同 `client_id`/`kind`/`text` 连续 POST 两次，核对同一回执；kind 缺省 report，submit 时另带 tests |
| echo_input | 回显完整 User input 原文到本 run result，UTF-8 边界截断到 64 KiB |
| write-denied | 尝试写 `path`，仅写入被拒绝才通过，用于检查脚本隔离证明 |
| silent | 只发 native 成功终止事件，不发 crew 消息，然后退出 |
| exit | `exit_code` 为 0 时 native 成功退出，为 1..125 时失败退出 |

命令回执和 inbox 响应作为 native 文本事件输出，便于场景库检查。脚本结束默认 native 成功退出。`silent` 在空脚本或只有此动作时产生 outcome=none。


## P1 平台验收与证据

Docker 模式的工作流可以配置：

```json
{
  "acceptance": {
    "checks": [
      {"name":"npm-test","command":["/pack/checks/npm-test.sh"],"timeout_seconds":300}
    ]
  }
}
```

检查数为 1..8，名字唯一、由小写字母/数字/连字符组成且最多 32 字符；命令首项须为绝对路径，超时为 1..1800 秒。host 模式配置 acceptance 会拒绝启动。声明检查时必须配置 `pack_dir`，并有 `checks/` 目录。

启动时把可信 `pack_dir/checks` 复制到 `<root>/packs/<sha256>/checks`；拒绝符号链接，目录 0555，文件去掉写权限并保留执行位。该副本在任务工作区之外，修改原始 pack 不会改变当前服务已选定的副本。base 包的 npm-test.sh 要求 package.json 存在 test 脚本，以离线模式运行 npm test。

CLI 成功并收集产物后，任务继续保持 running。平台计算工作区树哈希，再按顺序在新容器执行每个检查：工作区和 pack 副本只读、无网络、无 relay 和凭证，沿用任务资源限制；超时或取消会强制回收容器。树哈希按相对路径排序，对路径、类型、大小、文件内容摘要进行确定性编码；排除根层 `.workshop-home`，符号链接只记录链接目标。

全部退出 0 为 passed；非 0 或超时为 failed；容器/存储等基础设施故障为 error。CLI 失败或缺少产物时为 skipped；检查期间取消为 cancelled，重启未完成检查为 interrupted。run 的执行状态与 acceptance 分开：CLI 和产物阶段成功时，即使验收 failed/error，任务执行终态仍按契约为 succeeded。报审 submit 自称 tests=pass，而最终验收 failed 时标记 false_green。

每项实际执行的检查记录命令、退出码、超时、耗时、输出字节数和工作区摘要，输出写入 `<root>/evidence/<task_id>/<evidence_id>.log`，最多保留 1 MiB。`output_bytes` 是收到的完整字节数，`output_truncated` 表示截断；分页的 total_bytes 是实际保存的字节数。UTF-8 分页不拆开有效字符；无效字节在返回 text 时替换，offset 仍按原始已存字节计数。

`workshop.evidence` 不带 evidence_id 时返回有 namespace/task_id/run_id 的列表对象；指定 evidence_id 时返回有完整身份字段的输出页。摘要提供 acceptance_state、false_green、evidence_count。读取只限所属 namespace，检查输出不经施工者转述。


## Docker 与 host 的 shell 工具

Docker 模式下，Claude、Pi、OpenClaw 分别开放 `Bash`、`bash`、`exec`，用于调用 easygo-crew 和执行测试。只读工作流同样开放 shell，工作区写保护由只读挂载执行；无网络、UID/资源限额和只读根文件系统继续由外层容器保证。未增加 MCP 或网络工具。

Claude 显式配置 `--tools …,Bash --allowedTools Bash`；固定版本允许在 `--restricted` 中显式加入 Bash，因此保留 restricted/bare。OpenClaw 的 exec 目标固定为容器内本地执行，mode=full 避免无交互环境等待许可。Pi 只在已有工具白名单中加入 bash。以上改写只由初始化通过的 DockerRunner 执行。

host 模式保持原有工具范围：Claude/Pi/OpenClaw 只有文件工具，不能调用 easygo-crew；Codex 可执行 shell，但仍需配置模型 relay 才有 crew 通道。参数与固定版本的离线取证见 [运行时兼容性说明](runtime-provider-compatibility.md#p1-docker-shell-tools-2026-09-29)。
