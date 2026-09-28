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

内置 calculator 与 workshop_catalog/submit/get/list/cancel/resume/result。模型不能指定 namespace、RPC URL、私钥或任意可执行程序。工坊 namespace 从运行记录注入；submit 的幂等 key 使用可信 run_id + tool_call_id。CLI 工坊拥有引擎内部循环；TS Loop 只管理外部任务。

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
