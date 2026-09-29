# 审查 · W-2 · aa41e657c65807072d66766b0d27cbde9b5bb7fa

结论：**通过**。

队列 #5。提交 `aa41e65`，父提交是已审的 `570ff08`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review2` 的 detached HEAD `aa41e65`。没有改施工者分支，也没有提交。生产文件已复原；探针留在证据目录。`git diff` 为空。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 工作区只读靠挂载串里的 `,dst=/workspace,`

`checkContainerOptions` 在 `containerOptions` 生成的参数上找 `,dst=/workspace,`，找到才在末尾追加 `,readonly`（`acceptance_docker.go:17-19`）。当前生成式是 `dst=/workspace,bind-propagation=rprivate`（`docker.go:259`），所以会命中。

复现：把这一段改成 `bind-propagation=rprivate,dst=/workspace`（`dst` 在最后，后面没有逗号），再跑 `TestAcceptanceContainerOptions`。测试在 `acceptance_test.go:257` 失败，日志是 `red-mount.log`，退出码 1。然后 `git checkout -- services/workshop/workshop/docker.go`。

现在的容器确实是只读的：真实检查里写 `/workspace/check-write` 和 `/pack/checks/check-write` 都被拒绝（`docker.log` 的 pass 子测试）。施工者的测试锁的是这一整段字面量，字段顺序一变就会红。它没有单独断言「任意挂载写法下工作区都只读」。

### P3 · 重启把已经记成 failed 的检查改成 interrupted 时，留下 `false_green`

`finish` 在任务仍是 Running/Cancelling、终态却是 Interrupted 时，无条件把 acceptance.state 写成 `interrupted`（`service.go:737-739`），不重算 `false_green`。检查已经在 bolt 里写成 `failed` 且 `false_green=true`、进程在写终态之前退出，就会留下 `interrupted` + `false_green=true`。

复现：探针 `TestReview2InterruptedFinishKeepsFalseGreen` 直接调用 `finish`。通过，日志 `false-green-probe.log`。契约要求重启时把还在跑的任务记成 interrupted；这个标志是多出来的。正常取消路径会按最终状态重算，取消子测试里 `false_green=false`。

## 已验证

1. **检查容器的当前参数。** 无网络、UID 1000、cap-drop ALL、no-new-privileges、entrypoint 换成检查命令。参数里没有 `EASYGO_`，没有 relay 挂载。工作区和 `/pack/checks` 都带 readonly。host 模式配了 acceptance，`New` 返回 `ErrInvalid`（`service.go:140-142`，`TestAcceptanceConfig`）。
2. **真实容器隔离。** 镜像 `easygo-sandbox-fixture:acl-workshop`（`sha256:4eefe6d2c30a`，没有重建 runtime 镜像）。pass：产物内容正确、pack 副本仍是 `trusted-original`（施工开始前改过源 pack）、写工作区和 pack 被拒绝、连 `1.1.1.1:80` 失败、没有 `EASYGO_*` 和 relay socket。acceptance `passed`，`false_green=false`。owner 标签下容器为 0。
3. **假绿真值表和两条真实失败。** `falseGreen` 只在 submitted + `claims.tests=pass` + acceptance `failed` 时为真（`acceptance.go:68-69`），单测覆盖了四个 outcome、四种 tests、六种 state。坏产物：任务 `succeeded`，acceptance `failed`，`false_green=true`。检查超时 1 秒对上 sleep 5 秒：`timed_out=true`，同样是 failed 且假绿。CLI 自己失败（exit 9）时检查不跑，state `skipped`，任务 `failed`，不是假绿。
4. **取消、截断、基础设施失败。** 检查已经打出 `sleeping check` 再取消：任务 `cancelled`，acceptance `cancelled`，证据里有 `platform: check cancelled`，产物不登记。1048600 字节输出被截到 1 MiB，`output_truncated=true`，`output_bytes=1048600`。缺少的入口程序：acceptance `error`，任务仍是 `succeeded`（检查阶段的错误记在 acceptance 上，`runAcceptance` 自己返回 nil）。这是施工者测试锁定的行为。
5. **pack 快照。** 按内容哈希复制，源文件改了之后副本不变，内容一变就换目录。符号链接拒绝复制。目录 0555、文件去掉写位，由 `verifySealedChecks` 检查。副本不在 `workspaces/` 下。施工脚本写 `/pack/checks/source.txt` 和快照路径都被拒绝，pass 子测试才能成功。
6. **树哈希。** 排除工作区根上的 `.workshop-home`。改内容、改路径、把普通文件换成符号链接，哈希都变（`TestPackSnapshotAndWorkspaceHash`）。检查前算一次，写进每条 evidence。
7. **证据 RPC。** 缺省 limit 8192（`server.go:118`）。列表和分页都带回 namespace、task_id、run_id。别的 namespace、错误 run、未知 evidence id 是 not found。偏移切在 `界` 的中间被拒绝。非法 UTF-8 用替换符读出。超过 1 MiB 的文件按 `ErrConflict` 拒绝；写入侧先截断。
8. **重启。** 把 state 为 `running` 的任务直接写入 bolt 再 `New`，恢复成 `interrupted`，并有一条 acceptance 事件（`TestAcceptanceRestartInterruption`）。
9. **全量。** 探针移出后，`cd services/workshop && go test -race -count=1 ./...` 退出 0。这次没有设置 `EASYGO_DOCKER_TEST_*`，容器测试在全量里 Skip。`go vet ./...` 退出 0。Go `go1.25.14`。

## 没覆盖到

- 没有在 `easygo-task-runtime` 里跑 `packs/base/checks/npm-test.sh`。那份脚本要求 `package.json` 里有 test 脚本，然后 `npm test --offline`。夹具镜像里没有 node。按红线没有重建 runtime 镜像。
- 校验只要求 `command[0]` 是绝对路径（`acceptance_types.go:55`），不要求落在 `/pack/checks`。工作流配置是受信的，施工者改不了它。这次没有把命令指到 `/workspace` 下再跑。
- 全量 `-race` 没有开 Docker 环境变量。容器证据是另一次不带 `-race` 的 `TestAcceptanceDockerIntegration`。
- 没有跑 `services/ai-gateway`、`packages/rpc-go` 和仓库根模块。

## 证据

目录：`/tmp/crew/acl-530e/review2/aa41e65/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `unit-v.log` | `TestAcceptance*`、`TestPackSnapshot*`、`TestEvidence*`（未设 Docker 环境变量，容器测试 Skip） | 0 |
| `docker.log` | 专用 daemon 上 `TestAcceptanceDockerIntegration` 的七个子测试 | 0 |
| `red-mount.log` | `dst` 改到挂载串末尾后，`TestAcceptanceContainerOptions` 判红 | 1 |
| `false-green-probe.log` | `finish` 把 failed 改成 interrupted 时留下 `false_green` | 0 |
| `race-all.log` | 探针删除后的 `go test -race -count=1 ./...` | 0 |
| `vet.log` | `go vet ./...` | 0 |
| `acceptance_review2_probe_test.go` | 假绿探针原文 | |
| `restored.diff` | 实验结束后的 `git diff`，空文件 | |

审查命令都在 `services/workshop` 下执行。
