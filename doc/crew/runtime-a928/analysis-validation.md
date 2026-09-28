# Runtime adapters：独立源码分析与测试方案

作者：runtime-validation-a928。基线：`dc63aa3d4da97e664f98329cf1bb941f72658e63`，worktree `/home/ubuntu/Projects/easygo-runtime-test`，分支 `test/runtime-adapters`。

本轮只读，依据完整读取的 onboarding、brief 和 tasks/validation.md；没有实施新接口或代替 foreman 做兼容性决策。以下“建议验收”是供施工图采用的测试约束，尚非实现已满足。未读取真实凭证，未运行真实模型调用。

## 1. 当前调用路径与需要覆盖的接缝

| 接缝 | 当前源码事实 | 新功能测试关注点 |
|---|---|---|
| agent-loop 模型选择 | `services/agent-loop/src/loop.ts:71` 在普通调用与压缩调用使用 `config.model ?? 'chat'`；`types.ts:25` Config 只有模型 alias，没有 runtime/profile | 主模型 alias 如何成为任务默认模型，必须来自可信配置，不能从模型工具参数伪造；默认与显式覆盖均需跨服务实证 |
| agent-loop workshop 工具 | `services/agent-loop/src/tools.ts:12` 工具表只接受 workflow/input；`:42` 从 run 赋 namespace；`:48` 用 run ID + tool-call ID 生成幂等键 | 工具 schema、运行期 fields 校验、RPC DTO 都需同时支持有限的选择 ID；禁止 command/env/url/auth 等自由参数 |
| RPC 认证 | `packages/rpc-go/server.go:245` 先检查证书对应 methods/namespaces，再调用方法；`packages/rpc-go/json.go:77` 严格解码；`services/workshop/server/server.go:84` submit 要求非空 key | 新 catalog/selection 不可绕过 mTLS 与 namespace；需验证 raw JSON 重复键与未知字段 |
| workflow 固定绑定 | `services/workshop/workshop/types.go:20` Workflow 内含 Engine/Model；`service.go:146` 只认可 codex/claude；`runner.go:38` 引擎表也只接受两者 | 四 runtime 的名称验证不能只改一个 switch；允许 runtime/model 组合的检查必须在产生任务与子进程副作用前完成 |
| 进程适配 | `runner.go:64` 把引擎映射为固定 argv；`:127` 直接 exec.CommandContext，输入走 stdin；stdout 解析硬编码为 Claude 或 Codex | 分离选择解析、argv/env 构造、事件归一化；fixture 必须捕获实际 argv/env/stdin，不能仅测拼接函数 |
| 持久化与恢复 | `types.go:109` Task 保存 Workflow 快照；`service.go:223` submit 持久化后排队；`:365` resume 使用既有 task/session/workspace；`:453` Invocation 使用持久化 Workflow | 新 profile 的实际模型、协议、端点、配置版本等非秘密身份是否快照；恢复是否错误重用当前默认；秘密值不得持久化 |
| 主模型 provider 配置 | `services/ai-gateway/gateway/gateway.go:35` 模型 alias 映射 protocol/endpoint/model；`config.go:29` 从 env 引用解析密钥 | 复用配置不等于 CLI 支持相同协议；需要经过兼容性判定；不能将 gateway 的任意参数强行映射到 CLI |
| 独立旧 task 系统 | `internal/app/bootstrap.go:137` 主模型来自 cfg.Model；`:158` worker 来自 cfg.SubAgent；`internal/task/engine.go:136` 用 checkpoint format/version 防漂移 | 旧 internal/task 不是 workshop 的持久化实现；不要用其测试通过来证明三服务 workshop 新功能正确 |
| 部署 | `compose.services.yaml` 独立 ai-gateway/agent-loop/workshop；`services/workshop/Dockerfile:16` 镜像只安装 Codex/Claude | 增加 runtime 后镜像可用性与服务数分别验证；不可把源码支持或 catalog 可见当成 CLI 已安装/已验证 |

## 2. 当前约束与风险，区分事实和待证假设

### 2.1 环境隔离存在配置层逃逸口（源码可证，非远程利用结论）

`runner.go:114` 先设置工作区专属 HOME/CODEX_HOME/CLAUDE_CONFIG_DIR，但 `:116-118` 将 EnvAllowlist 里的宿主变量直接覆盖这些值；`:53` 只验证环境变量名称正则。故可信 operator 配置若把 HOME 等放入白名单，临时 home 约束会被覆盖。当前示例没有这样配置，也不能由现有模型工具参数直接选择白名单。

建议在 profile 配置复用工作中纳入保留变量测试。另 `:117` 对缺失环境变量静默忽略；若新 profile 声明必需 API key，建议明确在执行前报错，避免 fallback 到未知认证路径。PATH、NODE_OPTIONS、LD_PRELOAD、XDG 配置路径及新增 runtime 的目录变量应按施工图界定边界。

### 2.2 Workflow 快照不包含 runner 配置（源码事实）

`service.go:90` 在启动时构造 runner；`runner.go:102` 每次执行按 engine 名取当前 EngineConfig。旧任务恢复保留 workflow 的 model/version，却没有冻结 binary、环境配置或未来 profile 参数。单纯保存一个 profile ID，若其映射在重启后改变，就可能导致原 session 在另一个模型/端点运行。

建议让测试明确区分：非秘密身份不漂移；凭证轮换是否允许；配置删除/禁用后的 resume 行为。最终语义由 foreman 决定，不能凭“immutable snapshot”注释推定新 profile 已安全。

### 2.3 幂等性当前只比较 workflow 名和 input（源码事实，当前不是 bug）

`service.go:184-205` 先按 namespace/key 找旧任务，然后只比较 previous.Workflow.Name 与 req.Workflow，以及 Input；匹配直接返回旧 task，发生在读取当前 workflow 前。`workshop_test.go:349` 明确覆盖重启后同 key 仍返回旧 v1 workflow。

新选择字段必须参加语义比较，否则同 key 改 runtime/model 会悄悄返回先前任务。反过来，若每次先解析当前默认，再比较旧快照，原来省略选择的重试可能因管理员改默认而失去幂等。建议在施工图中定义省略值、显式默认、主模型 alias 与 profile 版本的 canonical identity；不能仅比较原始 JSON 字符串，也不能把 API key 值作为身份。

### 2.4 namespace 权限不等于 runtime/profile 权限（源码事实）

当前 catalog 跨 namespace 共享，`service.go:46` 明确说明这一点；RPC 只限制 method/namespace。现有 workflow 是 operator 定义，不是任意命令，因此当前没有模型选择任意 executable 的入口。新设计若仅声明全局允许组合，测试应验证它；若承诺逐 workflow/namespace 选择授权，则必须额外测试，不能宣称 mTLS 已自然提供。

### 2.5 协议与策略不能凭名称推断兼容（未验证兼容性）

当前 gateway 支持 chat_completions/responses/anthropic/custom（`gateway.go:121`），runner 只有 --model 与环境白名单。此次分析没有调用真实 CLI/provider，也没有对 DeepSeek、Pi、OpenClaw 的协议/策略/恢复语义做外部验证。应由研究证据和最终施工图给出支持矩阵；fixture 测试只证明适配器遵守该矩阵，不证明真实供应商兼容。尤其不能因“OpenAI compatible”便认定 Responses 与 Chat Completions 可互换。

`runner.go:68` 和事件解析目前都要求 UUID session；新 runtime 若用文件路径或非 UUID 标识，需各自验证，不能为通用化而让调用者指定任意宿主路径。

### 2.6 取消与成功必须保持现有证据标准（源码事实）

`process_linux.go:13` 使用独立进程组并 SIGKILL；`runner.go:141-174` 在退出后清理、检查解析/退出码/成功终态/session；`service.go:487-496` cancellation 优先于成功。stdout parser 保留有限输出并传播持久化失败。

agent-loop 取消当前 RPC socket 与取消已接受 workshop task 不是同一个动作：`tools.ts:68` 不确定 mutation 会阻止自动重试；`loop.ts:65` close 只 abort 活动工作。请施工图明确是否增加跨服务传播，避免测试错误要求现有架构自动取消已提交的独立任务。

## 3. 独立对抗测试矩阵（收到准确 implementation commit 后执行）

全部使用临时目录、生成的本机 TLS 证书、dummy keys、本地 HTTP provider 和可控子进程。先锁定 commit，再以公开 RPC → durable service → 真实 fixture 子进程为主；实现单元测试作补充。记录每个 case 的输入、预期、实际、退出码与 artifact 路径。

| ID | 操作/输入 | 独立观察与预期 |
|---|---|---|
| A1 | 对每个允许 runtime/profile 组合从 agent-loop 发起 submit | catalog、工具 schema、RPC 参数、实际 child argv、最终 summary 的选择一致；只启动一次 child |
| A2 | 未知 runtime/profile、有效但禁止组合、禁用 runtime | RPC 明确拒绝或按施工图报 unsupported；任务计数/queue/child marker 均不增加；不能静默换模型 |
| A3 | 模型参数加入 binary、command、shell、argv、env、endpoint、credential、namespace、idempotency_key | 本地字段验证拒绝；直接 RPC 同样严格；namespace/key 只由可信上下文填充 |
| A4 | 非授权证书/method/namespace；跨租户 get/resume/cancel/results | 在数据访问与 child 启动前拒绝；对方 task 不可见且未变；选择 catalog 按声明权限过滤或共享 |
| A5 | 选择字段为空/空白/null/数字/数组/对象/超长/重复或转义重复 JSON 键 | 明确一致的 validation；无 panic、未知字段落地或语义歧义 |
| I1 | 同 namespace/key 同语义请求并发提交；JSON 字段顺序不同 | 返回同 task ID、只有一次启动；跨 namespace 相同 key 得到独立任务 |
| I2 | 同 key 改 runtime/profile/model/workflow/input | conflict，不返回错误模型的旧任务，不启动新 child |
| I3 | 省略选择 vs 显式默认；服务重启改变默认或主模型 alias 后重试 | 依 canonical contract 判断；旧任务身份不被新默认改写；记录预期后再运行，不事后迁就实现 |
| I4 | credential dummy 值轮换，非秘密 profile 身份不变 | 若允许轮换，幂等身份稳定且新执行仅使用新 key；存储内无 key 值 |
| P1 | 主模型 alias 指向 dummy 第三方 profile，任务省略 model/profile | 本机 provider 接收到预期 protocol/path/upstream model/dummy auth；没有额外生产请求 |
| P2 | 同 runtime 显式切换两个兼容模型；同模型选两个支持 runtime | fixture/provider 捕获证明选择独立；不能仅比较 API 返回的标签 |
| P3 | protocol/runtime 不兼容、未知协议、URL 带用户信息/敏感 query、缺失 key | 按配置边界 fail closed；无付费请求或自动认证 fallback；错误无密钥 |
| P4 | 真实 CLI 版本的无网络 help/schema 与 adapter flags 不一致 | 将 runtime 标 unsupported/blocked；fixture 接受任意参数不能冒充 CLI 验证 |
| C1 | parent 环境放 dummy 非白名单 secret；白名单仅选所需 key | child 看不到非白名单值；不同任务 HOME 独立；resume 仅复用自己的目录 |
| C2 | 将 HOME/CODEX_HOME/CLAUDE_CONFIG_DIR/XDG 或新 runtime 私有目录加入白名单 | 根据隔离合同拒绝覆盖或保留内部路径；只用 dummy 临时 home，不读取 operator home |
| C3 | dummy secret 出现在 stdout JSON text/result、stderr、分片/超长输出与错误 | events/result/error/日志均不含 dummy 值；秘密不出现在 argv、catalog、DB 快照；检查返回成功前后的日志 |
| R1 | 完成 → 重启并修改 profile/default → resume | 保留原 runtime/model/endpoint/workspace/native session；或按明确漂移策略拒绝；绝不悄悄切换 |
| R2 | terminal 但无 session；运行中 resume；disabled/non-resumable runtime | 明确拒绝；无新 run/child；若不支持 resume，应从 catalog 能预先知晓 |
| R3 | running/queued 时服务关闭或模拟崩溃，再启动 | 未完成任务 interrupted；不自动 replay；仅显式 resume 执行且沿用已持久化身份 |
| R4 | child 输出一个 session 后 hang，cancel/timeout；child 再生子进程 | 达到 cancelled/timed_out；进程组退出；迟到 success 不覆盖；输出流和管道有界收敛 |
| R5 | native session 含路径穿越、绝对路径、控制字符、不同 runtime 的 ID | adapter 依据自身格式拒绝；不能逃出 task home；旧 UUID 合同不盲目套到所有 runtime |
| E1 | exit 0 无成功事件、重复终态、成功后非零退出、malformed JSONL、超限 stdout/stderr | 都按协议准确失败/截断；不能只看 exit 0；通用 envelope 不丢 runtime 原生错误 |
| E2 | 输出事件持久化失败；submit 已接受但 RPC 断线/伪造 receipt | child/loop 停止后续副作用；uncertain outcome 不触发模型自动重试 |
| D1 | compose config 与镜像内容核验 | 恰好既有三个服务；无 Docker socket/operator home；声明支持的 CLI 已安装且版本固定；不部署生产 |

## 4. 现有测试资产与建议执行入口

已有源码测试可复用思想，但独立验收至少另取上述边界与真实 child 捕获，避免照搬实现分支。

- `services/workshop/workshop/workshop_test.go`：真实本机子进程 fixture；success/resume/snapshot、失败与输出限制、进程组取消、namespace/key/persistence、恢复不重放、队列、artifact 越界。fixture 是测试可执行文件本身，不调用真实 CLI/model。
- `services/workshop/server/server_test.go:18`：真实 mTLS RPC + 本机 shell fixture；现有全部 workshop 方法、跨租户与 strict params。
- `services/agent-loop/test/service.test.mjs`：可信 namespace/key，取消 socket，持久化屏障、不确定 mutation 不重试；`receipt.test.mjs` 检查假回执；`strict-json.test.mjs` 检查重复键/UTF-8。
- `scripts/test-services.mjs`：三个真实服务进程及本机模型/CLI fixture，可用 `EASYGO_GO_BIN` 指定 Go；需要 agent-loop 依赖。

建议命令（未把计划当执行）：

```text
(cd services/workshop && <GO_BIN> test ./... -count=1 -timeout=120s)
(cd packages/rpc-go && <GO_BIN> test ./... -count=1 -timeout=90s)
(cd services/agent-loop && npm run typecheck && npm test)
EASYGO_GO_BIN=<GO_BIN> node scripts/test-services.mjs
```

新增适配 fixture 的参数应来自签定施工图/版本证据；兼容性矩阵由 foreman 确认后，独立写观测断言。不要用运行一次真实模型当作安全边界证明。

## 5. 本轮执行事实、限制与待明确项

- `git rev-parse HEAD` 返回上述基线，退出 0；初始 `git status --short --branch` 显示干净任务分支。
- 已完整读取指定 onboarding/brief/task 文件；当前仓库与祖先未发现可适用的磁盘 CLAUDE.md/AGENTS.md 文件。
- 尝试在 workshop 模块执行 `go test ./workshop ./server -count=1 -timeout=90s`，真实退出码 **127**，`go: command not found`。检查 `/usr/local/go/bin/go`、`/opt/go/bin/go`、`/home/ubuntu/go/bin/go` 也未找到；这不是测试通过或代码失败。
- `node --version` 为 v22.23.2；当前 worktree 未检测到 agent-loop node_modules/dist，未安装依赖、未运行 TS suite。
- 本轮没有运行 CLI/provider 兼容性探针。无额外 worktree、无源码修改、无 git commit/push、无 peer contact。
- 等待准确实现 commit/施工图后进行对抗验收。需 foreman 明确：各 runtime 支持的 protocol/policy/resume 矩阵；省略/显式默认的 canonical 幂等语义；profile 漂移/删除/凭证轮换的恢复策略；选择授权粒度；主任务取消是否影响已接受的 workshop 独立任务。
- 所有计数/测试结果若对最终用户发布，需 foreman 按 onboarding 二次核对。报告通过共享文件交付，不宣称已投递 UI，也不虚构 delivery_id。
