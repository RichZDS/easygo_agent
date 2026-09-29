# P1 契约 · 施工者通道、结束判定、验收检查（v1.3）

修订记录：
- v1.1（09-29，loop 席提出）：`workshop.message` 和 `workshop.evidence` 的响应补上 namespace、task_id、run_id 等身份字段，供 loop 独立校验回执；evidence 列表改为对象包裹。
- v1.2（09-29，loop 席提出）：`workshop.events` 的响应形状**不变**（仍是 Event 数组，Web 在用）。它的身份靠请求作用域：mTLS 固定对端、namespace 和 task_id 由请求限定。loop 读消息时必须：先调 `workshop.get`（带身份校验）拿到该任务的 run id 集合；再调 `workshop.events`，逐条严格校验结构与枚举，序号严格递增且大于 after，run_id 必须属于该集合，任何一条不合格就整体拒绝。
- v1.3（09-29，loop 席提出）：`workshop.get` 的摘要只含最后一个 run，拿不到全部 run id。补充两条：(1) 每个任务最多 256 个 run，`workshop.resume` 超出时返回 -32009（reason `run_limit`）；(2) `workshop.get` 的摘要新增 `run_ids`：该任务全部 run id，按时间从旧到新（≤256 个）。v1.2 里"run_id 必须属于该集合"就以 `run_ids` 为准。

作者：工头。这是 workshop 和 loop 之间的接口，两边都照这个实现。发现契约有漏洞或做不到，**停下来问工头**，不许自行改动。workshop 席在提交时把本契约并入 `contracts/rpc-v1.md`，新开一节「P1 Harness 扩展」。

## 1. 容器内通道

- 工坊在每次执行的 relay 上新增 `/crew/` 路由，模型路由不变。
  - 鉴权和模型路由一样，用同一个 per-run token，放在 `Authorization: Bearer` 或 `X-Api-Key` 里。
  - 只接受 `POST`，不接受查询串，其它路径一律 404。
- **容器环境变量**新增两个：
  - `EASYGO_CREW_URL=http://127.0.0.1:18080/crew`
  - `EASYGO_CREW_TOKEN=<本次执行的 relay token>`

  host 模式的 `CommandRunner` 有 relay 时，用 relay 的地址同样注入；没有 relay 就不注入。token 已经在 runNative 的 secrets 里，输出里继续被遮蔽。
- **`POST /crew/messages`**
  - 请求体（严格 JSON，未知字段拒绝，请求体不超过 16 KiB）：
    ```json
    {"client_id":"<1..64，[A-Za-z0-9_-]>","kind":"report|ask|blocked|submit","text":"<1..8192 字节 UTF-8>","claims":{"tests":"pass|fail|not_run"}}
    ```
  - `claims` 只有 `submit` 必须带，其他 kind 不能带。
  - `client_id` 在同一次执行内幂等：
    - 重复发送相同内容，返回同一个 `id`/`sequence`，不重复记事件；
    - 同一个 `client_id` 但内容不同，返回 409。
  - 每次执行最多 200 条，超出返回 429。
  - 成功时响应 `200 {"id":"<uuid>","sequence":<事件序号>}`。
- **`POST /crew/inbox`**
  - 请求体：`{"after":<uint64，缺省 0>}`。
  - 响应 `200 {"messages":[{"id","sequence","text","time"}],"next":<uint64>}`。
  - 只返回发给本任务（`direction=to_worker`）且 `sequence > after` 的消息，最多 50 条。
  - 返回的列表非空时，记一条 `crew.read` 事件，里面带这些消息的 id。

## 2. 事件（经 `workshop.events` 读取）

`Event` 增加三个可选字段，原有字段不变：

| 字段 | Event.kind | 内容 |
|---|---|---|
| `message` | `crew.message` | `{"id","direction":"from_worker\|to_worker","kind":"report\|ask\|blocked\|submit\|note","text","claims"?,"client_id"?}`。`to_worker` 的 kind 固定为 `note` |
| `read` | `crew.read` | `{"message_ids":[...]}` |
| `acceptance` | `acceptance` | `{"state":"running\|passed\|failed\|error\|cancelled\|interrupted","check"?,"exit_code"?,"evidence_id"?,"false_green"?}` |

事件照旧按 `run_id` 归属，序号单调递增。

## 3. run 字段

`Run` 增加两个字段：

- **`outcome`**：`submitted | blocked | asked | none`。
  - run 结束时判定：看本 run 最后一条 kind 属于 {submit, blocked, ask} 的 `from_worker` 消息。report 不算。
  - 本 run 没有这类消息，就是 `none`。
  - run 还没结束时不出现这个字段。
- **`acceptance`**：
  ```json
  {"state":"skipped|running|passed|failed|error|cancelled|interrupted","false_green":false,
   "evidence":[{"id","check","command":[],"exit_code","timed_out","duration_ms","output_bytes","output_truncated","workspace_sha256","time"}]}
  ```

Task 顶层不加字段，看最后一个 run 即可。`workshop.get` 和 `workshop.list` 的每个 run 摘要增加 `outcome`、`acceptance_state`、`false_green`、`evidence_count`。

## 4. 工坊 RPC（授权给 agent-loop 证书）

- **`workshop.message {namespace, task_id, text(1..8192 字节), idempotency_key(1..128)}`**
  - 返回 `{"namespace","task_id","id","sequence"}`，其中 namespace、task_id 与请求一致（v1.1）。
  - 追加一条 `direction=to_worker` 的 `crew.message` 事件。任务处于任何状态都可以收。
  - 同一个 key、相同内容时幂等；同一个 key、不同内容，返回 -32009。
- **`workshop.evidence {namespace, task_id, run_id?, evidence_id?, offset?, limit?}`**
  - 不带 `evidence_id`：返回 `{"namespace","task_id","run_id","acceptance_state","false_green","evidence":[...]}`，evidence 结构同 §3。不带 `run_id` 时取最后一个 run，响应里的 `run_id` 是实际取的那个（v1.1）。
  - 带 `evidence_id`：返回这项检查的输出分页 `{"namespace","task_id","run_id","evidence_id","text","offset","next_offset","total_bytes","eof"}`。`limit` 取 4..32768，缺省 8192（v1.1）。
  - 响应里的 namespace、task_id 必须与请求一致；run_id、evidence_id 在请求带了时也必须一致。loop 会逐项校验。
- **`workshop.resume`**
  - 新 run 的输入 = 调用方给的 input，加上所有未读的 `to_worker` 消息（如果有）：
    ```
    

    Unread messages from the foreman:
    - [<id>] <text>
    ```
  - resume 被接受时，这些消息记为已读（写一条 `crew.read` 事件）。

## 5. 验收检查

- **工作流字段**：新增 `acceptance`，格式为 `{"checks":[{"name":"[a-z0-9-]{1,32}","command":["<绝对路径>","..."],"timeout_seconds":1..1800}]}`。
  - 1 到 8 条，name 不能重复。
  - 只能在 Docker 模式下用，否则配置校验失败。
- **工坊配置**：新增可选的 `pack_dir`，是运维给的可信路径。启动时：
  - 读取 `<pack_dir>/roles/worker.md` 作为施工者说明。这个文件可选，最大 16 KiB。
  - 把 `<pack_dir>/checks/` 复制到 `<root>/packs/<内容 sha256>/checks/`：目录权限 0555，文件保留可执行位、去掉写位。检查容器把这个目录只读挂到 `/pack/checks`。
  - 有工作流声明了 acceptance，但没配 `pack_dir` 或 checks 目录缺失，启动失败。
- **什么时候跑**：run 的 CLI 成功结束、产物也收集成功之后，如果工作流有 acceptance：
  - 任务保持 `running`，依次跑每项检查；
  - 全部结束后才进入终态 `succeeded`；
  - 其它情况 `acceptance.state=skipped`。
- **检查容器**：
  - 用同一个 runtime 镜像（`sandbox.image`）；`--entrypoint` 取 `command[0]`，其余作为参数；
  - `--network none`、只读根文件系统、UID 1000、cap-drop ALL、no-new-privileges；PID、内存、CPU 限额与任务容器相同；`/tmp` 用 tmpfs；
  - 工作区**只读**挂到 `/workspace`，`/pack/checks` 只读挂载；
  - **不挂 relay，不注入任何 `EASYGO_*` 凭证**；
  - 到 timeout 就杀掉。
- **证据**：
  - 每项检查一条 evidence。
  - stdout 和 stderr 合并存到 `<root>/evidence/<task_id>/<evidence_id>.log`，最多 1 MiB，超出就截断并标记。
  - `workspace_sha256` 是跑检查之前工作区的树哈希，排除 `.workshop-home`：按路径排序，对每个 `(相对路径, 类型, 大小, 内容 sha256)` 依次做 sha256。
- **结果**：
  - 全部退出码为 0 → `passed`；
  - 任何一项非 0 或超时 → `failed`；
  - 基础设施失败 → `error`。
- **假绿**：同时满足以下三条，就把 `false_green` 记为 true，并在 acceptance 事件里标出：
  - `run.outcome=submitted`；
  - submit 带的 `claims.tests=pass`；
  - acceptance 结果是 `failed`。
- **取消与重启**：
  - 检查期间取消：停止检查，acceptance 记 `cancelled`，任务记 `cancelled`。
  - 重启：running 的任务照旧转成 `interrupted`，acceptance 记 `interrupted`。
- **产物登记**：只有任务终态是 `succeeded` 时才登记产物。这是修竞态的一部分，见任务单 W-3。

## 6. 施工者说明与输入拼接

- 工坊交给 CLI 的输入，按顺序拼接：
  1. `worker.md` 的内容，后面接 `\n\n`；
  2. `workflow.instructions`；
  3. 如果有 acceptance，加上：
     ```
     

     Acceptance checks the platform will run after you finish:
     - <name>: <command 用空格连接>
     ```
  4. `\n\nUser input:\n` 加上 input。
- 没配 `pack_dir` 时，输入和现在完全一样。
- 分隔符 `User input:\n` 保持不变，夹具靠它解析输入。

## 7. `easygo-crew` 命令

这是一个 Go 静态二进制，放进 runtime 镜像和夹具镜像的 `/usr/local/bin/easygo-crew`。

- 子命令：
  - `easygo-crew report|ask|blocked <text>`
  - `easygo-crew submit --tests pass|fail|not_run <text>`
  - `easygo-crew inbox [--after N]`：把响应 JSON 打印出来。
- 发消息时自动生成 `client_id`。网络失败时用同一个 `client_id` 最多重试 3 次。
- 成功时打印 `{"id","sequence"}`，退出码 0。
- 失败时退出码非 0，在 stderr 写明原因，不能打印 token。
- 缺少 `EASYGO_CREW_URL` 或 `EASYGO_CREW_TOKEN` 时，退出码 2，提示 `crew channel unavailable`。
