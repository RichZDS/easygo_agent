# 审查 · L-1 · 78d893f7558d70c039a9ea4e9c0f76a9f64a87ba

结论：**通过**。

提交在 `feat/acl-loop`，父提交 `05b567045f398642a277a8a0fd0e3013086be220`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD。diff 共 13 个文件，+213/−51。没有改施工者分支，也没有提交。`contracts/` 不在 diff 里。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 超过 64 KiB 的 knowledge 参数，reason 变成 `tool_arguments_too_large`

父提交里 knowledge 工具直接进 `Knowledge.execute`，不经过 `executeTool` 的 65536 字节检查（`78d893f^:services/agent-loop/src/loop.ts:158-159`）。新注册表在查找条目前就按 JSON 字节数拒绝（`services/agent-loop/src/tools/registry.ts:30`）。

仍走 knowledge 自己校验的输入，分类和旧代码一致：

- 64 字节的技能名：`-32602` / `invalid_string`（`validation.ts:14-16`，`knowledge/index.ts:25`）
- 含空格的名字：`-32602` / `invalid_skill_name`
- 多出来的 `namespace`：`-32602` / `unknown_field`，`Knowledge.execute` 没有被调用
- 缺失技能：`-32004` / `skill_not_found`（`knowledge/index.ts:147`）

70000 个字符的 `name` 在进入 `Knowledge.execute` 之前失败，reason 是 `tool_arguments_too_large`。错误码仍是 `-32602`，`recoverable` 仍为真，run 会把错误交还模型，不会改去重试一次 mutating 调用。旧路径上同一输入会先落到 `invalid_string`，也是可恢复的 `-32602`。变的是这条超限边上的 reason 字符串。

复现：`/tmp/crew/acl-530e/review/78d893f/probe.mjs` 用真实 `Knowledge` 走 `createToolRegistry`。`probe.log` 打印 `L1_PROBE_OK`，退出码 0。

## 已验证

1. **mutating 工具的不确定结果仍然终止 run，不交还模型。** `callWorkshop` 仍只把明确的预接受拒绝码 `-32602/-32004/-32003/-32009/-32029` 标成可恢复；其余 mutating 失败是 `uncertain_tool_outcome`，`recoverable=false`（`tools/workshop.ts:74-79`）。`ToolRegistry.recoverable` 把分类交给条目自己的函数（`registry.ts:37-39`）。这三个写工具挂的是 `recoverableToolError`（`workshop.ts:23-26,57-61`）。`loop.ts:164-172` 在 `recoverable` 为假时，于下一次工具或模型调用之前把错误抛出。幂等键仍是 `` `${run.id}:${call.id}` ``（`workshop.ts:40`）。`npm test` 里这三条断链用例通过：`workshop_submit/resume/cancel accepted then disconnected`（`npm-test.log` ok 91–93）。内部错误和畸形回包（ok 94，源码 `service.test.mjs:508-527`）以及五个预接受拒绝码（ok 95）也通过。网关夹具每次都回同一组工具调用，断言 `gateway.calls.length === 1`，所以第二次模型回合过不了这组测试。

2. **角色过滤同时约束展示和执行。** 本期工具的 `roles` 都是 `assistant`。`request()` 只把 `definitions('assistant')` 放进模型请求（`loop.ts:106`），`execute()` 固定用 `'assistant'`（`loop.ts:164`）。`foreman` 只出现在类型上。真实注册表对 `foreman` 的定义是空数组；以 `foreman` 执行 `workshop_submit` 得到 `unknown_tool`，执行函数调用次数是 0。`load_skill` 对 `foreman` 同样是 `unknown_tool`。套件 ok 50 用两个角色的合成条目覆盖了同一条路径。calculator 已从定义里删除；模型调用它得到 `unknown_tool` 并且 run 仍完成（ok 87，`service.test.mjs:447-451`）。

3. **`pack_dir` 的上限、冲突、缺失、非 UTF-8。** `systemPrompt` 用同一个 fd 读，缓冲区 16385 字节，超过 16384 即抛 `assistant role exceeds 16 KiB`，再用 fatal UTF-8 解码（`tools/roles.ts:8-15`）。目录不是普通文件时抛 `assistant role must be a file`。`parseConfig` 和 `systemPrompt` 都拒绝与 `system_prompt` 并存（`server.ts:35-37`，`roles.ts:7`）。`Loop` 构造函数在监听前读提示词（`loop.ts:41`，`server.ts:67`）。套件 ok 52 覆盖缺失（ENOENT）、恰好 16384 字节、多字节超限、`0xff`、以及和空 `system_prompt` 冲突。ok 56 确认 `packs/base/roles/assistant.md` 进入模型请求，且缺失目录让 `startServer` 失败。探针另覆盖：目录、指向非 UTF-8 文件的符号链接、20000 字节文件。符号链接被 `openSync` 跟随；`pack_dir` 和原来的 `system_prompt` 一样由操作员配置。`assistant.md` 写了接受不等于完成、要看 outcome 和验收、不采信施工者自报、`asked` 时用回复加续跑（`packs/base/roles/assistant.md:3-14`）。本单没有新增回复工具，这和施工图一致。

4. **knowledge 错误分类与旧行为一致。** 旧门闩是 `recoverableToolError`（`-32602`）或 knowledge 专用的 `-32004/-32009`（父提交 `loop.ts:167`）。新条目的谓词是这三个码（`tools/index.ts:13`）。`-32029`、`-32601` 和普通 `Error` 不可恢复，与旧式一致。`-32009` 来自版本和完成记录，不是 `load_skill` 抛的；谓词保留它，避免把旧的特殊情况收窄。探针用真实库导入技能 `review`，`load_skill` 按 run 的 namespace 取回正文；另一个 namespace 得到 `skill_not_found` 且可恢复。`list_skills` 带多余字段时是 `unknown_field`。重名在 `ToolRegistry` 构造时抛出（`registry.ts:17`，套件 ok 49）。`attachKnowledge` 在 `listen` 之前（`server.ts:135-138`），所以重名进不了监听；`knowledge.start()` 仍在 `ready=true` 之后（`server.ts:147-148`，`loop.ts:62`）。

5. **三进程验收。** `EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go node scripts/test-services.mjs` 退出 0，约 41 秒。九项检查都通过，包含 `workshop_get` 空参数 → `invalid_string` → 模型给出最终答复。`paid_models` 为 false。没有使用 `-race`。临时状态 `/tmp/easygo-rpc-e2e-WAaYTM` 已删除，报告副本在证据目录。

6. **本机套件。** `npm ci`、`npm run typecheck`、`npm test` 都在这次审查里跑过。测试日志：115 通过，0 失败，28211 ms。类型检查日志没有编译错误。

7. **工坊 `pack_dir` 按简令不记缺陷。** `configure-platform.mjs:29` 给工坊配置写入 `pack_dir`。本提交的 `workshop.Config`（`services/workshop/workshop/types.go:53-64`）没有这个字段。`LoadConfig` 调用 `rpc.ReadConfig`（`services/workshop/server/server.go:23`），`Decode` 使用 `DisallowUnknownFields`（`packages/rpc-go/json.go:89,176-182`）。因此这份 Compose 配置在 W-1 之前会让工坊启动失败。loop 自己的配置只写了 `pack_dir`，内联 `system_prompt` 已去掉，两边不会同时出现。Dockerfile 在 `USER 1000:1000` 之前把 `packs/base` 复制到 `/opt/easygo/packs/base`（`services/agent-loop/Dockerfile:24-26`）。没有构建镜像。

## 没覆盖到

- 没有构建 agent-loop 镜像，也没有用 `configure-platform.mjs` 生成配置去启动工坊。
- 套件没有一次带 knowledge 的模型回合去跑 `load_skill`。`knowledge.test.mjs` 直接调用 `Knowledge.execute`。注册表路径和 `loop.ts` 的门闩是分开取证的。
- 没有把 `callWorkshop` 改成一律可恢复再看不确定结果测试变红。上面第 1 条的断言本身要求只发生一次模型调用。
- `contracts/rpc-v1.md` 里 calculator 那句按施工图留给工头，这次没有改，也没有审那份文档。
- 分支上 L-1 之后的提交不在 `78d893f` 里，不在这次审查范围。

## 证据

目录：`/tmp/crew/acl-530e/review/78d893f/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `npm-ci.log` | `npm ci` | 0 |
| `typecheck.log` | `npm run typecheck` | 0 |
| `npm-test.log` | `npm test`，115/115 | 0 |
| `probe.mjs` / `probe.log` / `probe.rc` | 角色、knowledge 分类、pack 目录/符号链接/超限 | 0 |
| `services.log` / `services.rc` | 三进程 `test-services.mjs` | 0 |
| `services-report.json` | 上述脚本的检查清单；`paid_models: false` | |

`node_modules` 和 `dist/` 在审查工作区里，已被 gitignore。工作区相对 `78d893f` 没有已跟踪改动。
