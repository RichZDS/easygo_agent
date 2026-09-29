# 交接 · loop · 2026-09-29

> **工头代写。** loop 席（Codex）在收尾时撞上了 Codex 周额度上限，要到 10-05 才恢复，没法自己写。本交接根据它的 worklog、分支状态和终审报告整理。它的工作全部已交付并签字，这份交接可以采信。

1. **在做哪张单**：没有在做的。L-1 `tasks/l1-registry.md`、L-2 `tasks/l2-tools.md`、L-3 `tasks/l3-scenarios.md` 都已完成。
2. **做到哪一步**：
   - L-1 78d893f、L-2 76530b2 在 `feat/acl-loop` 上；L-3 efacf6d + 24fe1ac 在 `feat/acl-loop-l3` 上，这个分支合入了 W-1c 和 W-2。
   - 三笔都由 R1 终审通过，已合进 `feat/agent-cluster`（1907c15），集成回归全绿（51dadc9）。
   - worktree `/home/ubuntu/Projects/easygo-acl-loop` 是干净的，没有 push。
3. **动了哪些文件**：
   - `services/agent-loop/src/tools/{registry,index,roles,workshop,harness,messages,receipts}.ts`；
   - `src/{loop,server,types}.ts`、`src/platform/server.ts`；
   - 测试 `test/{registry,harness-tools,platform,service}.test.mjs`；
   - `services/agent-loop/Dockerfile`；
   - `packs/base/roles/assistant.md`；
   - `scripts/{configure-platform,test-harness,test-services}.mjs`；
   - `services/README.md`。
4. **没来得及做的**：
   - F-1：call id 超过 91 字节时，`workshop_reply`/`workshop_submit` 的幂等 key 会超过契约的 128 字节。修法是对 call id 取 sha256。
   - F-4：`test-platform*.mjs` 和 `test-services.mjs` 的授权名单缺 `workshop.message`/`workshop.evidence`。
5. **坑**：
   - **`workshop.events` 要先整批严格校验，再截断分页。** 只校验塞进页里的前缀，尾部的伪造事件就会被放过。L-2 终审用对照实验证明过这一点。
   - **mutating 工具（submit/resume/cancel/reply）结果不确定时，必须终止 run，不能交还模型重试。** 只有明确的预接受拒绝码 `-32602/-32004/-32003/-32009/-32029` 才可恢复。
   - `pack_dir` 和内联 `system_prompt` 互斥。`assistant.md` 上限 16 KiB，必须是合法 UTF-8。
   - harness 需要专用 Docker 和一个含 fixture-check 的夹具镜像。夹具 `echo_input` 只解析 `User input:` 之后的第一个 JSON 值，resume 追加的未读消息在它后面。
   - 断言要做反向验证：故意改坏，确认退出码非 0。L-3 的 S3/S4/S9 都这样验过。
