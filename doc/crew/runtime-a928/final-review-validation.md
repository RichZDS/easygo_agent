# 固定提交终审

审查提交：`e34e8b075985b0cb7059c4441e90fe85ef9e6b75`，Verify runtime protocols, harden native responses and preserve CLI sessions。

只读检查 `/home/ubuntu/Projects/easygo-runtimes`；确认 HEAD 等于固定提交且工作区干净。没有 cherry-pick、Git 写入或网络/监听测试。发现以下两个问题。

## 1. [P1] 成功 Responses 中的 error:null 被 Native 拒绝

位置：`services/ai-gateway/gateway/native.go:145-146`。

`validateNative.check` 将 error 键“存在”视为错误，而不是判断其值非 null。因此带 `error:null` 的成功 Responses JSON，以及 response.completed SSE 内的同一对象，都返回 upstream_error；经 gateway.native/relay 进入的正常 CLI 生成会失败。现有 `services/ai-gateway/gateway/response.go:49` 的 providerError 正确检查 `o["error"] != nil`，所以普通 Complete 可以接受同一个成功响应，形成新增 Native 路径的兼容性回归。

已执行不创建 socket 的复现：`/tmp/crew/runtime-a928/final-review-repro/main.go`。程序通过自定义内存 http.RoundTripper 将固定响应交给真实 Gateway.Native/Complete，绝无 provider 网络访问；只使用 dummy model。完整输入：

```json
{"status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}
```

从上述目录运行：

```text
GOPROXY=off GOWORK=off GOCACHE=/tmp/crew/runtime-a928/validation-go-cache /home/ubuntu/sdk/go/bin/go run .
```

真实进程退出 **0**，输出如下（退出 0 表示复现程序正常跑完，不是产品断言通过）：

```text
application/json Native error: upstream_error: native provider error
same JSON Complete error: <nil>
text/event-stream Native error: upstream_error: native provider error
```

修复应只区分“null/没有错误”和“真实错误对象”，保留已有 malformed/truncated/error SSE 拒绝。建议补 JSON 与 completed SSE 两个成功回归，并保留非 null error 的失败断言。复现模块 replace 指向固定工作区；后续若 HEAD 改变，运行结果对应新代码，应记录新 hash。

## 2. [P2] agent.run.start 省略 workshop_runtime 的重试不能返回原选择

位置：`services/agent-loop/src/store.ts:88`，入口 `services/agent-loop/src/server.ts:77`。

按本终审工单的“omitted selection on retry returns original choice”合同：首次使用 key=K、input=X、workshop_runtime=caller-choice，后续相同 session/key/input 省略该字段，应返回原 run 和原选择。实际入口把省略值变成空字符串，Store.start 与已保存的非空 runtime 严格比较，必然抛 -32009/idempotency_conflict。对应的 workshop Submit 已在 `service.go:248` 用 `req.Runtime != ""` 区分省略值，因此两层语义不一致。

源码复现序列：

```text
session = createSession("tenant")
a = start("tenant", session.id, "input", "K", "caller-choice")
start("tenant", session.id, "input", "K")
# 实际代码：idempotency_conflict；工单期望：a
```

同值显式重试正确；显式不同值 conflict 也正确，不建议放宽这两项。修复应在现存 key 分支只比较明确提供的选择，并增加“选过后省略”测试。现有 runtime-selection.test.mjs 只覆盖显式同值与不同值，漏掉此 case。

提供 `/tmp/crew/runtime-a928/final-review-repro/store-repro.mjs`，内含上述 case、旧 SQLite schema 升级、重启保留选择的检查。**没有动态证明这项：**本地启动脚本真实退出 **1**，停在 `spawnSync git EPERM`（读取固定源码的子进程阶段）。保留原限制、不改权限或绕过；foreman 可运行此脚本或用现有 TS harness 复现。

若产品刻意要求 agent.run.start 每次重试完整携带原字段，则此项属于工单合同与 API 合同冲突，需要明确；本报告依据给定终审合同判定，不把省略值等同显式不同选择。

## 指定测试调整结论

1. **未知计量 fixture 调整合理。** 原 `{framework_specific:true}` 不是合法 Responses 成功响应，不能用它要求 Native 放过缺失 status/output。改为 `status:"completed"` 加 `custom_tool_call` 后，仍检验合法原生工具超出中立 parser 能力时保留原始 body，且 Usage.Known/Cost.Known/HasFirstDelta 均为 false。测试没有把 unknown 偷换成免费，也没有放过失败/截断终态。此调整与问题 1 无关，问题 1 需要另补 nullable error 用例。
2. **Claude HEAD /api/hello 调整合理。** `native_runtime_test.go:73` 仅对 HEAD + 精确路径提前返回 204，不进入 generation bodies/requestErrors；其他请求仍要求 POST、fixture-model 和所选 dummy key，最终仍要求至少两次生成、resume session 相同、最后请求保留第一轮 assistant 文本。没有降低模型/凭证/恢复的断言。该 fixture 不能独立证明经 relay 的完整链路，但调整本身没有伪造 generation 成功。
3. **relay base64 字段修复合理。** wire 上 []byte 是 base64 JSON 字符串；改为 string 后再 DecodeString，保留 response envelope ID、ContentType、16MiB envelope/8MiB decoded body 上限，修正 strict Decode 把 []byte 当数组造成的错误拒绝。这里只审源码；`native-relay-root-2.log` 的通过属于 foreman 的实际运行，不是本 worker 的测试。

## 其余审查范围结论与边界

- Workshop runtime choice/default/AllowedRuntimes：新提交调用 selectRuntime，未授权 profile 被拒绝；catalog 仅列默认和允许项，无凭证值/endpoint。
- Workshop 幂等：先查旧任务；省略选择返回既有选择，显式不同选择冲突；不受后来 default/profile 映射改变影响。
- Resume：Invocation 使用已持久化 RuntimeSpec，直接 provider model/baseURL/env 名保留。凭证在执行时重新解析；gateway 路由保留 alias，远端配置可集中轮换，这是工单明确允许的语义，不作为 endpoint/key 未冻结的缺陷。
- Direct credential isolation：profile 模式跳过旧 EnvAllowlist，只注入选中 key；私有 home、保留环境变量拒绝、生成配置中只写环境变量引用。未验证真实 OS 隔离；不把无 shell 工具或独立 HOME 宣称为 sandbox。
- mTLS relay：固定 namespace/model alias/protocol、task token、本机地址、固定路径，外部 params 严格、服务证书 pin、cancel Context 都保留；Native 限长并缓冲 SSE，不是 live delta。已知原 malformed/error SSE red 在源码被显式验证覆盖，但 nullable error 回归仍见问题 1。
- TS caller override：已持久化 run.workshop_runtime 优先于模型工具偏好，重启/queued 读取经 runView 保留字段；入口先进行证书/method/namespace 授权。发现的问题限于省略选择重试。
- SQLite migration：ALTER 添加非空空字符串默认值，在 BEGIN IMMEDIATE 与 ownership 校验后执行；旧行可默认无覆盖，启动失败会 rollback。源码未发现另外可报告的迁移缺陷；本次没有声称动态迁移测试通过。

限制：没有真实 provider、CLI 或 socket 测试。没有把 foreman 的 race/native/relay 日志当独立执行。只在 /tmp 写报告与复现文件；两个问题以外不提出推测性重构建议。

---

## 闭环复核 · efc18697cceaf1a6591b5bab9be228fb48dfe377

按消息 d19d694c-913b-465f-be6b-3fd6f5b71f8c，本节仅核对上文两项发现的修复及回归，不扩大审查范围。上文 e34e8b0 的 red 记录作为历史证据保留。

**两项均已闭环：**

- P1：native.go 只将非 null 的 error 值判为错误，nullable 成功响应不再误拒绝；新增 JSON/SSE 正向用例，并保留真实错误/截断反向用例。foreman 的 null-error-repro-green.log 显示原复现三个结果全部 nil。
- P2：store.ts 的 runtime 比较增加 runtime !== '' 条件；省略重试复用原选择，显式不同选择仍拒绝。RPC 测试验证相同 ID 与 caller-choice，新增旧 schema 迁移/关闭重开/省略重试/显式冲突覆盖。foreman 的 ts-closure.log 中相应测试通过，整体 77 pass、0 fail、0 skipped。

本轮独立核对的是固定提交源码与测试断言；运行结果来自已读取的 foreman 日志，未声称自己执行。没有新增发现或未闭环 review 项。详见更新后的五节 handoff-validation.md。
