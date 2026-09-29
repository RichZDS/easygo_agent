# 交接 · workshop · 2026-09-29

> **工头代写。** workshop 席（Codex）收尾时撞上了 Codex 周额度上限，要到 10-05 才恢复。本交接根据它的 worklog、分支状态和终审报告整理。它的工作全部已交付并签字，这份交接可以采信。

1. **在做哪张单**：没有在做的。W-3/W-3b、W-1/W-1b/W-1c、W-2、W-4 都已完成（`tasks/w3-race.md`、`w1-channel.md`、`w2-acceptance.md`、`w4-runtime-shell.md`）。
2. **做到哪一步**：
   - 提交都在 `feat/acl-workshop`（be7dac0）上：eb46347、3adec82、39863f1、a2e0b50、570ff08、aa41e65、be7dac0。
   - 全部终审通过，W-1 由两位终审各审过一遍。已合进 `feat/agent-cluster`，集成回归全绿（51dadc9）。
   - worktree `/home/ubuntu/Projects/easygo-acl-workshop` 是干净的，没有 push。
   - W-5 到 W-7 由 workshop2 在 `feat/acl-workshop-w5` 上接着做完。
3. **动了哪些文件**：
   - `services/workshop/**`：`crew.go`、`crew_relay.go`、`relay.go`、`pack.go`、`acceptance*.go`、`evidence.go`、`docker.go`、`docker_runtime.go`、`runner.go`、`runtime_config.go`、`service.go`、`types.go`、`views.go`、`server/server.go`；`cmd/easygo-crew`、`cmd/fixture-cli`、`cmd/fixture-check`；以及对应的测试；
   - `deploy/runtime/Dockerfile{,.fixture}`、`services/workshop/Dockerfile`；
   - `packs/base/{pack.json,roles/worker.md,checks/npm-test.sh}`；
   - `contracts/rpc-v1.md`、`doc/workshop.md`、`doc/runtime-provider-compatibility.md`。
4. **没来得及做的**：自己名下没有。相关后续项：
   - F-5：生产配置 `pids_limit` 128 / 内存 1 GiB 还没用真实模型验证；
   - host 模式不开放 shell，这是设计决定。
5. **坑**：
   - **验收失败时，任务状态照样是 `succeeded`。** CLI 的终态和验收是分开记的，看质量要看 `acceptance_state` 和 `false_green`，不能看 task status。
   - **PID 下限 16 不够用。** task-shim、CLI、easygo-crew 都是进程，再加上 shell 和 npm。夹具场景用 128，真实样本用 256。
   - **relay 的 Unix socket 路径不能超过 107 字节**，state 根目录要短，比如 `/tmp/eglive`。
   - **Claude 2.1.281：** 保留 `--restricted` 和 `--bare` 时，显式的 `--tools Bash` 仍然生效；但还要加 `--allowedTools Bash`，否则会卡在等人批准。OpenClaw 要配 `tools.exec={host:gateway,mode:full}`。
   - 夹具镜像用 `docker build --network host` 构建（daemon 是 `--bridge=none`）。不要重建约 2 GB 的完整 runtime 镜像，在上面叠一层就够了。
   - 检查容器里工作区是只读的：会往工作区写缓存的测试框架会失败，输出要写到 `/tmp`。
