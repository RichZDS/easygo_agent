# EasyGo 独立服务 RPC 契约 v1

部署服务只有三个：`services/ai-gateway`（Go）、`services/agent-loop`（TypeScript / Node 22.23+）、`services/workshop`（Go）。Go 通用 RPC/TLS 实现放在 `packages/rpc-go`，不单独部署。各服务有自己的依赖清单、配置、启动入口和 Dockerfile；运行时不导入其它服务实现，不共享数据库。

## 身份与权限

- 仅监听 HTTPS，强制双向 TLS，最低 TLS 1.3。不提供明文端口或 Bearer 降级。
- 每个服务/客户端各有独立证书和私钥；私钥只挂给本人。CA 私钥不进入服务容器。
- 服务端先验证证书链，再将连接实际叶证书 DER 的 SHA-256 与配置中的公钥证书精确匹配。证书主题、请求 Header、RPC 参数不能自报身份。
- 权限表按证书确定 `id`、允许的 RPC 方法和 namespace。方法和 namespace 精确匹配；只有显式的 `"*"` 才表示全部。
- 客户端验证服务端证书链、主机名及配置的服务端叶证书指纹。证书续期/撤销通过替换公钥权限表和重启/滚动发布完成；保留旧新两条授权可实现轮换窗口。
- `/healthz` 也要求 mTLS，且调用证书有 `health` 权限。无 namespace 要求。

公共 TLS 配置：

```json
{
  "tls": {
    "cert_file": "/run/easygo/identity/tls.crt",
    "key_file": "/run/easygo/identity/tls.key",
    "ca_file": "/run/easygo/trust/ca.crt"
  },
  "authorization": [{
    "id": "agent-loop",
    "cert_file": "/run/easygo/trust/agent-loop.crt",
    "methods": ["health", "gateway.generate", "gateway.models"],
    "namespaces": ["*"]
  }]
}
```

RPC 调用连接配置为 `{ "url": "https://ai-gateway:8441/rpc", "peer_certificate_file": "/run/easygo/trust/ai-gateway.crt" }`；客户端使用自身公共 TLS 配置的证书、私钥和 CA。日志只记录 RPC ID、已验证身份、方法、namespace、耗时和错误码，不输出请求正文、私钥或凭证。

## 传输格式

`POST /rpc`，`Content-Type: application/json`。使用 JSON-RPC 2.0 请求/响应信封的受限 profile：id 必须是非空字符串（最多 128 字节），params 必须是对象，不支持 batch 或 notification。非法信封不会执行任何方法。

```json
{"jsonrpc":"2.0","id":"request-uuid","method":"gateway.generate","params":{"namespace":"demo","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]},"stream":false}}
```

成功：`{"jsonrpc":"2.0","id":"request-uuid","result":...}`。
失败：`{"jsonrpc":"2.0","id":"request-uuid","error":{"code":-32000,"message":"Request failed","data":{"code":"upstream_error"}}}`。未知/非法 id 用 null。

错误码：JSON 解析 -32700；非法信封 -32600；未知方法 -32601；非法参数 -32602；内部错误 -32603；无权限 -32003；不存在 -32004；状态/幂等冲突 -32009；容量耗尽 -32029；上游/执行错误 -32000。无权限返回 HTTP 403；正常 RPC 成功及业务失败使用 HTTP 200；信封/参数错误可使用 HTTP 400。客户端始终解析 RPC error，不能只凭 HTTP 200 判断成功。

所有业务 params 都包含 `namespace`（1–128 个 ASCII 字符，正则 `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`），先授权再访问数据或调用上游。未知字段应拒绝，取消要传递到上游 HTTP 或子进程。RPC 客户端不自动重试有副作用的方法。

`gateway.generate` 的 `stream:true` 使用 SSE：

- `event: delta`，data 为 `{"jsonrpc":"2.0","method":"gateway.delta","params":{"id":"request-uuid","event":<ai.Event>}}`。
- 最后仅一次 `event: result`，data 为普通成功响应信封，result 是完整 `ai.Response`。
- 出错为 `event: error`，data 为普通错误响应信封。未开始流时也可以直接返回 JSON error。
- 增量是临时输出，断流或缺少完整 result 都是失败。SSE 是本 profile 的扩展，不是额外业务 RPC。

模型 request/response/block 格式沿用已有 `ai` 契约，ProviderState 必须完整保留；只有网关解释厂商格式。自定义 JSON 映射使用显式非流式请求。

## RPC 方法

除下表字段外，params 都带 namespace。分页 offset/limit 为非负整数；具体上限由对应服务校验。

| 服务 | 方法 | params 字段 | result |
|---|---|---|---|
| gateway | `gateway.models` | 无 | `{models:string[]}` |
| gateway | `gateway.generate` | request: ai.Request, stream?: boolean | ai.Response / SSE |
| workshop | `workshop.workflows` | 无 | 已有公开工作流 metadata 数组 |
| workshop | `workshop.submit` | workflow,input,idempotency_key（必填） | TaskSummary |
| workshop | `workshop.get` | task_id | TaskSummary |
| workshop | `workshop.list` | offset?:0,limit?:20 | TaskPage |
| workshop | `workshop.cancel` | task_id | TaskSummary |
| workshop | `workshop.resume` | task_id,input | TaskSummary（不自动重试） |
| workshop | `workshop.result` | task_id,run_id?,offset?:0,limit?:8192 | ResultPage |
| workshop | `workshop.events` | task_id,after?:0 | 已有 Event 数组 |
| agent | `agent.session.create` | 无 | `{id,namespace,created_at}` |
| agent | `agent.session.list` | offset?:0,limit?:20 | `{sessions,next_offset}` |
| agent | `agent.session.history` | session_id,after?:0,limit?:100 | `{messages,next_after}` |
| agent | `agent.run.start` | session_id,input,idempotency_key（必填） | Run |
| agent | `agent.run.get` | run_id | Run |
| agent | `agent.run.cancel` | run_id | Run |
| agent | `agent.run.events` | run_id,after?:0,limit?:100 | `{events,next_after}` |

Run 至少有 `id,namespace,session_id,status,created_at,result?,error?`；status 为 queued/running/completed/failed/canceled/interrupted。Event 至少有 `seq,run_id,kind,data`。TaskSummary、TaskPage、ResultPage 复用工坊已有的有界视图；RPC 不返回宿主路径或原生 CLI 凭证。

## TypeScript Loop 执行约束

TS 服务拥有自己的持久化会话、队列、原始消息与事件。按会话 FIFO，不同会话可有限并行。同一数据卷只允许运行一个服务实例并有实际启动互斥。进程中断后不自动重放已开始的工具副作用，running 记为 interrupted；未开始的 queued 可以恢复。

每次模型完整输出先落库，再执行工具；工具结果落库后才执行下一个工具或模型。流式增量不当作已提交消息。使用数据库事务提交终态与最终上下文，失败不污染后续请求的工具链。提供上下文预算与独立无工具压缩请求，保留原始历史。到工具轮数上限后额外允许一次禁工具收尾；取消/超时停止上游请求，不能转成成功。

工具由注册表按角色提供；assistant 角色内置 workshop_catalog/submit/get/list/cancel/resume/result，以及 P1 新增的 workshop_messages/reply/evidence。calculator 已删除。模型不能指定 namespace、RPC URL、私钥或任意可执行程序。工坊 namespace 从运行记录注入；submit 的幂等 key 使用可信 run_id + tool_call_id。CLI 工坊拥有引擎内部循环；TS Loop 只管理外部任务。

## 运行配置入口

- Gateway：`services/ai-gateway/cmd/server`；配置 `{listen,tls,authorization,models:<gateway.FileConfig.models>,...existing gateway bounds}`，默认端口 8441。RPC listener 不使用原 Bearer 配置。
- Workshop：`services/workshop/cmd/server`；配置 `{listen,tls,authorization,workshop:<workshop.Config>}`，默认端口 8443。工坊旧 Bearer 不参与 RPC 鉴权。
- Agent：`services/agent-loop/src/server.ts`，构建后 `dist/server.js --config FILE`；配置 `{listen,tls,authorization,database,gateway:{url,peer_certificate_file},workshop:{url,peer_certificate_file},model,streaming,max_steps,context_bytes,concurrency,system_prompt}`，默认端口 8442。database 为 SQLite 文件，模型密码只在网关。

所有服务只通过 RPC 调用其它服务。Docker 容器分别挂配置、自身 identity、公钥 trust；Agent/Workshop 各挂独立数据卷。模型服务凭证只传给网关，CLI 凭证只传给工坊。没有 Docker socket 的工坊仍可运行配置好的 CLI；Docker 沙箱控制器不是这三个服务的必要依赖。

## 验收

三进程真实 mTLS 调用链：客户端→TS Loop→Go 网关→本机模型夹具；Loop→Go 工坊→真实子进程产物→Loop 最终回答。覆盖无证书、不可信 CA、错误服务证书、可信但未授权客户端、越权方法/namespace、取消、重复提交、重启恢复及原始错误传播。不得把本机夹具说成付费模型验证，也不得把静态 Dockerfile 校验说成容器运行验证。

参考：[JSON-RPC 2.0](https://www.jsonrpc.org/specification)、[Node TLS](https://nodejs.org/docs/latest-v22.x/api/tls.html)、[Node SQLite](https://nodejs.org/docs/latest-v22.x/api/sqlite.html)、[Go TLS](https://pkg.go.dev/crypto/tls)。


## Runtime selection extension

`agent.workshop.catalog({namespace})` returns the bounded workshop workflow catalog under caller namespace authorization. `agent.run.start` accepts optional `workshop_runtime` (nonempty ID, <=128 UTF-8 bytes), persists it and includes it in idempotency identity. The trusted selection overrides the model's `workshop_submit.runtime` argument.

`workshop.submit` accepts optional `runtime` profile ID, restricted to the workflow default/allowed list. Explicitly changing it for an existing key conflicts; omission on a retry retains the original task. Summary/list add runtime, engine, model. Workflow catalog adds nonsecret runtime choices. Resume retains the stored runtime/profile; no cross-framework session migration.

`gateway.native({namespace,model,protocol,body})` is separately authorized for Workshop's certificate. It posts to the configured generation endpoint with the configured model and credentials, preserving matching native API format. Returns `{content_type,body}` where body is base64 bytes, <=8MiB decoded. SSE is buffered and validated, not incremental RPC streaming. Only responses/chat_completions/anthropic matching the configured route are accepted; no caller URLs/headers/credentials. Existing `gateway.generate` remains the neutral protocol for the TS Loop.


## P1 Harness 扩展（v1.3）

本节为 P1 契约。施工者通道、outcome 和 pack 角色说明由 W-1 实现；acceptance、evidence 和检查容器由 W-2 实现。

`workshop.events` 保持 Event 数组形状，以 mTLS 对端和请求 namespace/task_id 限定身份。读取方先校验 `workshop.get` 身份，再以其 `run_ids` 验证每条事件归属，并校验结构、枚举和严格递增且大于 after 的序号；任一条不合格则整体拒绝。

`workshop.get` 新增 `run_ids`（全部 run id，从旧到新）；每任务最多 256 个 run，超限 Resume 返回 -32009，错误 data.code 为 `run_limit`。

### 1. 容器内通道

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

### 2. 事件（经 `workshop.events` 读取）

`Event` 增加三个可选字段，原有字段不变：

| 字段 | Event.kind | 内容 |
|---|---|---|
| `message` | `crew.message` | `{"id","direction":"from_worker\|to_worker","kind":"report\|ask\|blocked\|submit\|note","text","claims"?,"client_id"?}`。`to_worker` 的 kind 固定为 `note` |
| `read` | `crew.read` | `{"message_ids":[...]}` |
| `acceptance` | `acceptance` | `{"state":"running\|passed\|failed\|error\|cancelled\|interrupted","check"?,"exit_code"?,"evidence_id"?,"false_green"?}` |

事件照旧按 `run_id` 归属，序号单调递增。

### 3. run 字段

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

### 4. 工坊 RPC（授权给 agent-loop 证书）

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

### 5. 验收检查

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

### 6. 施工者说明与输入拼接

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

### 7. `easygo-crew` 命令

这是一个 Go 静态二进制，放进 runtime 镜像和夹具镜像的 `/usr/local/bin/easygo-crew`。

- 子命令：
  - `easygo-crew report|ask|blocked <text>`
  - `easygo-crew submit --tests pass|fail|not_run <text>`
  - `easygo-crew inbox [--after N]`：把响应 JSON 打印出来。
- 发消息时自动生成 `client_id`。网络失败时用同一个 `client_id` 最多重试 3 次。
- 成功时打印 `{"id","sequence"}`，退出码 0。
- 失败时退出码非 0，在 stderr 写明原因，不能打印 token。
- 缺少 `EASYGO_CREW_URL` 或 `EASYGO_CREW_TOKEN` 时，退出码 2，提示 `crew channel unavailable`。
