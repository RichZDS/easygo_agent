# 审查 · L-2 · 76530b2a0989a6fe5f5bb481650b94ea94ff041a

结论：**通过**。

提交在 `feat/acl-loop`，父提交 `78d893f7558d70c039a9ea4e9c0f76a9f64a87ba`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD。diff 共 12 个文件，+394/−5。没有改施工者分支，也没有提交。临时改过 `messages.ts` 做对照，已经 `git checkout` 复原，工作区相对 `76530b2` 没有已跟踪改动。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 长 call id 会让 reply 的幂等 key 超过契约的 128 字节

`workshop_reply` 的 key 是 `` `${run.id}:${call.id}` ``（`services/agent-loop/src/tools/harness.ts:24`）。run id 由 `randomUUID()` 生成，36 个字符（`store.ts:97`）。模型给的 call id 在 `modelResponse` 里用 `string()` 收下，上限是 128 字节（`loop.ts:17`，`validation.ts:14-16`）。拼出来最长 36+1+128=165 字节。契约 §4 写的是 `idempotency_key(1..128)`。工具在发出前没有再量这串 key 的长度。

探针用 100 字节的 call id，假工坊收到的 key 是 137 字节，而且 RPC 已经发出（`probe.log` 的 `reply_key_exceeds_contract`，`rpc: 1`）。套件里的短 id（`loop-run:call`）落在 128 以内，`harness-tools.test.mjs:22` 能通过。按契约实现的工坊会把超长 key 判成 `-32602`。这是明确的预接受拒绝，`callWorkshop` 会把它标成可恢复，交还模型，不会走 `uncertain_tool_outcome`。

`workshop_submit` 从 L-1 起就是同一种拼法（`workshop.ts:41`）。这次是 reply 又抄了一次。常见的短 call id 不受影响。

复现：`/tmp/crew/acl-530e/review/76530b2/probe.mjs`。`probe.log` 打印 `L2_PROBE_OK`，`probe.rc` 为 0。

## 已验证

1. **`workshop_messages` 先拿 `get.run_ids`，再整批校验 events，截断发生在校验之后。** `readMessages` 先调 `workshop.get`（`messages.ts:61-68`）：`run_ids` 必须是数组、长度 ≤256、元素是不重复的字符串；摘要里如果有 `runs`，每个 `run.id` 必须落在这个集合里。然后 `workshop.events` 的整个数组在 `callWorkshop` 的回调里走 `validateEvents`（`messages.ts:69`）。分页、32 KiB 截断和 `text_truncated` 都在这次 `await` 返回之后（`messages.ts:70-99`）。任何一条不合格，回调抛错，函数不会返回已经截过的页。

   校验内容和契约 v1.2/v1.3 对得上：序号是安全整数，严格大于 `after`，并且每条至少比上一条大 1（允许空洞）；`run_id` 必须属于 `run_ids`；kind 含原生的 `state/session/text/result/diagnostic` 以及 `crew.message/crew.read/acceptance`；可选的 `namespace`、`task_id` 必须和请求一致；`message`/`read`/`acceptance` 只在对应 kind 上出现；`to_worker` 的 kind 只能是 `note`；`claims` 只随 `submit`。过滤只影响返回给模型的页，不过滤掉的事件也先过校验。原生 `text/session/result/diagnostic` 不进页，但游标照样前进。上游恰好 1000 条时 `truncated=true`，和现有工坊 `Events` 最多 1000 条一致（`services/workshop/workshop/service.go:405-419`）。

   套件 ok 11（`harness-tools.test.mjs:141-142`）用 10 条 8000 字节的合法消息再加一条 `run_id:'foreign'`，断言整批拒绝。这 10 条本身就超过 32768 字节，如果只校验塞进页里的前缀，这条会放行。ok 8 覆盖游标把被截断的多条消息分完、不重复。ok 9、ok 10 覆盖单条超预算预览和满页 1000 条原生输出。

   探针另补了套件没单列的形状，都在校验通过后才谈得上分页：
   - 8 条 8000 字节消息后面跟一条不可见的 `text`，`run_id` 为 `foreign`。整批拒绝，而且 `workshop.get` 和 `workshop.events` 都已经调用。
   - 合法消息后面跟一条 `text: 4`，以及序号重复的 `session`，都拒绝。
   - 工坊把 `sequence == after` 的事件原样返回时拒绝。序号 1 然后 3 的空洞允许，`next` 为 3。
   - `run_ids` 256 条接受，257 条拒绝。
   - `submit` 不带 `claims`、`crew.read` 的 id 重复，都拒绝。

   对照实验（已复原）：把全批校验挪到分页之后，并且只校验 `sequence <= page.next` 的事件。单条伪造仍然抛错；10 条 8000 字节消息加一条 foreign 尾事件会**返回一页**，4 条事件，`next=4`，`truncated=true`，页内没有 foreign。证据 `which-case.log` 的 `TAIL_RESOLVED 4 4 true false`。同一改动下套件这条测试失败（`sabotage2-test.log`，退出码 1）。复原后这条测试通过（`restored2-test.log`，`# pass 1`），`messages.ts:69` 仍是全批回调。

   单条合法消息如果 JSON 转义后超过 32768 字节，会保留身份、标 `text_truncated` 或 `message_ids_truncated`，并把游标推过这条。8192 个 NUL 的正文留下 5418 个字符的前缀；用返回的 `next` 再读，下一页是空的，省略掉的字节不会从 `workshop_messages` 里再出现。`assistant.md:17-19` 写了这个标记是预览。这是在 32 KiB 上限下避免游标卡死的做法，尾部非法事件不走这条路径。

2. **`workshop_reply` 的不确定结果不交还模型。** 工具 `mutating: true`，`callWorkshop` 的第五个参数也是 `true`（`harness.ts:21-24`）。namespace 来自 `run.namespace`。参数上多出来的 `namespace` 或 `idempotency_key` 在发 RPC 之前被 `additionalProperties: false` 拒绝（`harness-tools.test.mjs:33-34`，`calls.length` 保持 0）。回执的 namespace、task_id、id、sequence 不合法时，`validateHarnessReceipt` 抛错（`receipts.ts:26-33`）。这个错不是预接受拒绝码，mutating 分支收成 `uncertain_tool_outcome`，`recoverable=false`（`workshop.ts:72-82`）。`loop.ts:172` 在下一次工具或模型调用之前把不可恢复的错误抛出。明确拒绝码仍是 `-32602/-32004/-32003/-32009/-32029`。

   套件 ok 103：`workshop_reply` 和 submit/resume/cancel 放在同一组里，工坊在收到请求后 `res.destroy()`。断言 run 状态 `failed`，错误码 `uncertain_tool_outcome`，工坊只被调用 1 次，网关只被调用 1 次，后面的工具没有执行，历史最后一条是这个错误码（`service.test.mjs:488-507`）。单元测试覆盖伪造回执和七个上游码（`harness-tools.test.mjs:44-52`）。正文按 UTF-8 字节卡在 8192：8192 个 ASCII 通过，2731 个「字」（8193 字节）在本地 `-32602` / `invalid_string`，不发 RPC（`harness-tools.test.mjs:32,41`）。探针复测了 2730 个「字」（8190 字节）会发出。

3. **evidence 的身份和分页。** 列表和分页都核对 namespace、task_id（`receipts.ts:29`）。请求带了 `run_id` 或 `evidence_id` 时，回执必须一致。分页要求 `text` 的 UTF-8 字节数 ≤ `limit`，`offset` 等于请求，`next_offset = offset + 字节数`，`next_offset ≤ total_bytes`，`eof` 与 `next_offset === total_bytes` 一致，并且空页只允许出现在 eof（`receipts.ts:39-45`）。整份回执 JSON ≤256 KiB。列表要求 `acceptance_state`、布尔 `false_green`、最多 8 条 evidence、id 不重复、check 符合 `^[a-z0-9-]{1,32}$`、`command[0]` 以 `/` 开头、exit_code 为 int32。`limit` 缺省 8192，范围 4..32768（`harness.ts:32`）。

   套件 ok 7 用正文「好」和 `next_offset: 3` 钉住 UTF-8 字节，并拒绝伪造身份、坏枚举、重复 evidence id、offset/next/total/eof 不一致、以及超过默认 limit 的正文（`harness-tools.test.mjs:55-63`）。探针复测了跨页的「好好」（offset 0 然后 3）、空页只有 eof 时接受、空页不在结尾时拒绝、`next_offset` 比字节数多 1 时拒绝、相对路径命令拒绝。`evidence_count` 缺省和 0..8 接受，9 拒绝（`receipts.ts:23`，与契约 §5 最多 8 条检查一致）。`workshop_get` / `workshop_list` 对 `outcome`、`acceptance_state`、`false_green`、`evidence_count` 是出现才校验（`receipts.ts:17-24`，`workshop.ts:96-107`）。套件 ok 12 覆盖非法枚举和类型。

4. **Web 两层白名单，客户端写不了 namespace。** `PUBLIC_METHODS` 和 `Loop.workshopCall` 都加上了 `workshop.message`、`workshop.evidence`，两边的其余工坊方法名单一致（`platform/server.ts:14-16`，`loop.ts:74-77`）。`/api/rpc` 先查 `PUBLIC_METHODS`，再拒绝带 `namespace` 字段的 params（`-32602` / `namespace_is_server_bound`），然后把登录账号的 namespace 填进去（`platform/server.ts:170-175`）。生产环境里这个回调进 `dispatch`，`workshop.*` 全部转给 `workshopCall`（`server.ts:80`）。HTTP 层没放行的方法到不了工坊；`workshopCall` 不认识的方法是 `-32601`。

   套件 ok 30 对两个方法都做了：无 cookie 401，恶意外站 403，客户端 `namespace:'other'` 返回 400，登录后 200，回调看到的 namespace 是账号自己的，伪造和未登录请求没有进回调（`platform.test.mjs:183-195`，`seen.length === 2`）。ok 13 直接打 `Loop.prototype.workshopCall`，两个新方法放行，`workshop.delete` 拒绝。探针把 `createPlatform` 的 rpc 接到真实的 `workshopCall`：`workshop.delete` 和 `workshop.workflows` 在 HTTP 层 400，回调次数仍是 0；登录后的 message 和 evidence 各一次，namespace 是注册返回的 `u-…`，不是 `other`（`probe.log` 的 `web_both_layers`）。模型工具路径的 namespace 只来自 run，不来自参数。

5. **说明和授权名单。** `packs/base/roles/assistant.md` 写了三个工具、截断标记、reply 本身不会 resume、evidence 要钉住 `run_id` 并按 `next_offset` 读到 eof。`configure-platform.mjs:28` 给 agent-loop 证书补了 `workshop.message` 和 `workshop.evidence`。diff 里没有 Web 界面文件。

6. **本机套件。** 在干净的 `76530b2` 上跑过。`npm run typecheck` 退出码 0。`npm test` 128 通过，0 失败，23431 ms。日志在证据目录。对照实验之后重新 build 了复原的源码，尾部测试和探针再次通过。

## 没覆盖到

- 没有连真实工坊 v1.3。本提交里的工坊还没有 `workshop.message`、`workshop.evidence` 和 `get.run_ids`。测试用的是假工坊。
- 没有构建镜像，没有跑 Docker，没有调用付费模型。
- Web 调用方自己提供 `idempotency_key`。模型工具路径不接受这个参数。mTLS 路径仍由证书上的 namespace 授权，这次没有另做证书用例。`configure-platform.mjs` 里 agent-loop 的 namespace 仍是 `*`。
- `workshop_reply` 的断线用例和 submit 放在同一组服务测试里。畸形 JSON 回包那组仍只对 `workshop_submit` 发请求；reply 的畸形回执走的是同一条 `callWorkshop` 单元测试。
- 全量 `npm test` 是在对照实验之前跑的。复原后重跑的是尾部那一条和探针，不是 128 条再跑一遍。

## 证据

目录：`/tmp/crew/acl-530e/review/76530b2/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `typecheck.log` / `typecheck.rc` | `npm run typecheck` | 0 |
| `npm-test.log` / `npm-test.rc` | `npm test`，128/128 | 0 |
| `probe.mjs` / `probe.log` / `probe.rc` | 隐藏非法尾事件、run_ids 256/257、超长幂等 key、evidence 字节游标、两层 Web | 0 |
| `which-case.log` | 校验若只覆盖游标以内，foreign 尾事件会变成成功页 | 0（脚本本身）；尾事件被放行 |
| `sabotage2-test.log` | 上述改动下套件尾部用例失败 | 1 |
| `restored2-test.log` | 源码复原后同一条用例通过 | 0 |
| `sabotage-test.log` | 完全不做 events 校验时，同一条用例失败 | 1 |

`node_modules` 和 `dist/` 在审查工作区里，已被 gitignore。工作区相对 `76530b2` 没有已跟踪改动。
