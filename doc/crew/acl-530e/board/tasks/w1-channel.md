# W-1 · 施工者通道、结束判定、`easygo-crew`

assignee: workshop · 状态: done（39863f1 + W-1b a2e0b50；工头已签 51dadc9）；W-1c 570ff08 · 契约：`board/contract-p1.md` §1–4、§6、§7

## 现场 / 锚点
- **relay**：`services/workshop/workshop/relay.go` 里的 `startModelRelayOn`。每次执行一个随机 token，现在只放行一条模型路径。
- **容器环境变量与输入**：`docker.go` 的 `Run`。`env` 表在这里组装，拼给 CLI 的 stdin 是 `in.Workflow.Instructions + "\n\nUser input:\n" + in.Input`。
- **host 模式**：`runner.go` 的 `CommandRunner.Run`，以及 `runtime_config.go` 里的 `configureRuntime` 和 `--message` 拼接。
- **事件与任务**：`types.go` 的 `Event`/`Run`/`Task`/`Invocation`；`service.go` 的 `appendEvent`、`execute`、`Resume`。
- **视图**：`views.go`。
- **RPC**：`server/server.go` 的方法表。
- **容器 PID 1**：`cmd/task-shim/main.go`，它会把 `127.0.0.1:18080` 的所有请求转发到 relay。
- **夹具**：`cmd/fixture-cli/main.go` 解析 `User input:` 之后的 JSON。
- **镜像**：`deploy/runtime/Dockerfile` 和 `Dockerfile.fixture`。

## 修法
1. **relay**：加 `/crew/messages` 和 `/crew/inbox`，严格按契约 §1 实现。
   - relay 需要能写本任务、本 run 的事件，也要能查收件箱。可以通过 `Invocation` 传一个窄接口，由 Service 实现（比如 `Crew{Post, Inbox}`）。
   - relay 本身不直接拿 bolt。
2. **幂等与上限**：`client_id` 幂等、同 id 不同内容返回 409、每个 run 最多 200 条，全部在 Service 的同一个事务里判定。
3. **结束判定**：`execute` 收尾时按契约 §3 计算 `run.outcome` 并落库。视图摘要增加 `outcome`，以及留给 W-2 填的 `acceptance_state`、`false_green`、`evidence_count`（先给默认值）。
4. **RPC**：新增 `workshop.message`，按契约 §4 实现，授权给 agent-loop。`workshop.resume` 把未读消息拼进新 run 的输入，并记 `crew.read`。
5. **`pack_dir`**：这一单只管 `roles/worker.md` 的读取和输入拼接（契约 §6）。checks 的复制留给 W-2。
6. **`cmd/easygo-crew`**：新建，按契约 §7 实现，加进两个 runtime Dockerfile。同时建 `packs/base/pack.json` 和 `packs/base/roles/worker.md`，worker.md 用英文写。worker.md 要讲清楚：
   - 什么时候用 report、ask、blocked、submit；
   - 停手前必须 submit 或 blocked；
   - 没实际跑过测试不许 `--tests pass`；
   - 施工图有疑问就 ask 后停下；
   - 用 `inbox` 收工头消息。
7. **fixture-cli 新增 `crew` 模式**：输入里带一个脚本数组，按顺序执行各种动作，供 W 自测和 loop 席的场景库使用。动作至少包括：
   - 调 easygo-crew 的各子命令；
   - 写文件；
   - 轮询 inbox 直到收到消息或超时；
   - 用同一个 client_id 发两次；
   - 静默退出；
   - 非 0 退出。
8. **契约文档**：把契约（v1.1）并入 `contracts/rpc-v1.md` 的新节，`doc/workshop.md` 补使用说明。同时把 rpc-v1.md「TypeScript Loop 执行约束」节里"内置 calculator 与 workshop_catalog/submit/get/list/cancel/resume/result。"这句改成："工具由注册表按角色提供；assistant 角色内置 workshop_catalog/submit/get/list/cancel/resume/result，以及 P1 新增的 workshop_messages/reply/evidence。calculator 已删除。"（工头 09-29 追加）
10. **run 上限与 run_ids（契约 v1.3，工头 09-29 追加）**：每个任务最多 256 个 run，`Resume` 超出时返回冲突（RPC -32009，reason `run_limit`）；`summarize()` 给 `workshop.get` 的摘要加 `run_ids`（全部 run id，从旧到新）。`workshop.list` 的元数据不用加。补对应单测。
9. **controller 镜像带上 base 包**：`services/workshop/Dockerfile` 把仓库的 `packs/base` 复制到 `/opt/easygo/packs/base`（loop 席的 configure-platform 会给工坊配置写 `pack_dir: "/opt/easygo/packs/base"`）。只复制，不在镜像里做别的处理（工头 09-29 追加）。

## 明令不做
- 验收检查、evidence、`workshop.evidence` 都留给 W-2。
- 不改模型路由的行为。
- 不给 relay 开新的监听地址。
- 不让工坊回调 loop。
- 不重建完整的 runtime 镜像，见 brief 红线。

## 测试
- **Go 单测**：
  - 鉴权：无 token 401、错 token 401。
  - 严格 JSON、各字段边界、claims 规则。
  - 幂等与 409、第 201 条返回 429。
  - inbox 的游标与 `crew.read`。
  - outcome 的四种取值。
  - `workshop.message` 的幂等和 -32009。
  - resume 的输入拼接与已读标记。
  - `easygo-crew` 的重试不产生重复消息（用假 server 让首次响应丢失）。
  - `easygo-crew` 缺少环境变量时退出码为 2。
- **真实容器测试**（专用 daemon + 你自己构建的夹具镜像 `easygo-sandbox-fixture:acl-workshop`）：容器里的 fixture-cli 通过 easygo-crew 发 report、ask、submit，工坊事件按顺序出现；运行中用 `workshop.message` 发一条，容器里 inbox 能收到。
- **全量**：`cd services/workshop && go test -race ./... && go vet ./...`。

## 交付
- 在 `feat/acl-workshop` 上单独提交一笔。
- worklog 报审写清：提交号、文件清单、每条测试命令的退出码、真实容器测试的证据路径（放 `/tmp/crew/acl-530e/workshop/`）、偏差说明。
- 然后 herdr-msg 通知工头。

## W-1b · 工头更正（09-29）
原施工图第 6 条自相矛盾（"停手前必须 submit 或 blocked"与"有疑问就 ask 后停下"），是工头的错。按契约 §3，`ask` 本身就是合法的停手交接（outcome=asked，表示"等回答"；blocked 表示"卡在障碍上"）。worker.md 改为：
- 有疑问：`easygo-crew ask` 拿到回执后就可以停手，平台会记为 asked，工头的回复在续跑时带进输入；**同一个问题不要再发 blocked**。
- 停手前必须拿到 submit、blocked 或 ask 三者之一的回执。
只改 `packs/base/roles/worker.md`，单独一笔提交（不要混进 W-2），测试与文档里如有对应描述一并改。

## W-1c · 夹具补丁（09-29，loop 席在 L-3 S6 发现）

- **现场**：`cmd/fixture-cli/main.go` 把 `User input:\n` 之后的整段文本做 `json.Unmarshal`。resume 时工坊会追加 "Unread messages from the foreman: ..."（契约 §4），带未读回复的 crew 脚本因此必然解析失败。另外，夹具从不回显自己收到的输入，而 `workshop.get` 摘要里又没有 input，场景库就无法用 RPC 证明续跑输入里带着回复 id。
- **修法**：
  1. fixture-cli 只解码 `User input:\n` 之后的**第一个 JSON 值**，可以用 `json.Decoder`，后面跟的文本忽略；
  2. crew 脚本加一个动作（名字你定，比如 `echo_input`）：把收到的完整 `User input:` 之后的原文，放进本次 run 的最终结果文本，截断到 64 KiB。这样 `workshop.result` 就能读到。
- **明令不做**：不改生产代码和契约，不改 easygo-crew。
- **测试**：单测覆盖"JSON 后跟未读消息后缀"能够解析，以及 echo 动作的输出；再在真实容器里做一次 resume，确认结果文本包含回复 id。
- **交付**：单独一笔提交，不混进 W-2，然后更新 `/tmp/crew/acl-530e/workshop/w1-fixture-interface.md`。
