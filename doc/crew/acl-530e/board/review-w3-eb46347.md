# 审查 · W-3 · eb46347605a4096c0907683f35153baea6cd18b4

结论：**通过**。

提交在 `feat/acl-workshop`，父提交 `05b5670`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD，没有改施工者分支，也没有提交。生产文件已复原；复现探针留在证据目录，不在仓库里。

## 问题

### P2 · 30 秒扫描超时不是配额错误，取消进行中时会被记成 cancelled

`DockerRunner.Run` 的最终扫描用 `context.WithoutCancel` 加 30 秒超时（`services/workshop/workshop/docker.go:562`）。`scanDiskUsage` 在每个目录项前看 `ctx.Err()`（`disk_quota_linux.go:63`）。超时返回的是 `context.DeadlineExceeded`，不是 `ErrDiskQuotaExceeded`，也不是 `ErrDiskQuotaScanFailed`。

`execute` 的状态顺序是：配额错误 → `Cancelling` → 服务关闭 → 工作流截止 → 其它 `runErr`（`service.go:649-658`）。因此：

- 进程已经成功、调用方没有取消：超时使 run 失败，原因是 `context deadline exceeded`，不登记产物。这是失败关闭。
- 调用方已经取消：超时排在 `Cancelling` 后面，任务记 `cancelled` / `cancelled by caller`，未扫完这件事被丢掉。产物仍然不登记，因为 `runErr != nil`，收集被跳过（`service.go:636`）。
- 工作流自己的截止时间如果也到了，父 ctx 的 `DeadlineExceeded` 排在通用 `runErr` 前面，状态会是 `timed_out` 而不是扫描失败。这一支只对过代码，没有单独打桩。

测速（同一台机器，4 万个空文件，`scanDiskUsage`）：

- 40001 项，147.3 ms，约 271592 项/秒。
- 默认文件限额 200000：外推约 0.74 秒，30 秒够用。
- 配置上限 10000000（`docker.go` 校验）：外推约 36.8 秒，超过 30 秒。扫描在数到限额之前就会停。上限附近的超额树，在取消竞态里会得到 `cancelled` 而不是 `failed` / `disk_quota_exceeded`。

30 毫秒预算在这棵树上停在 8056 项，错误仍是 `context deadline exceeded`。把生产代码里的 30 秒临时改成 1 纳秒后走真实 `DockerRunner.Run`：取消分支是 `cancelled` 且产物为空；不取消分支是 `failed` / `context deadline exceeded`，产物为空。探针跑完已把 `docker.go` 复原，`git diff` 为空。

这不否定本次要修的窗口：限额内能扫完的超额，仍然先于 `Cancelling` 记 `failed`。默认限额落在扫得完的一侧。

### P3 · 无

## 已验证

1. **超额且取消：状态 failed，原因是配额，产物为空。** 测试把文件限额临时设成 8，轮询 60 秒，避免周期扫描兜底。容器启动回调里先取消，再写超额文件。启动时工作区只有 4 项，低于 8，所以启动前的两次检查都放行了；最终扫描才拒绝（`used_files=9`）。`TestCancelExitRace/over_quota=true` 在 `-race` 下 200/200 通过。
2. **未超额且在成功返回后取消：状态 cancelled，产物为空。** 取消发生在 `DockerRunner.Run` 返回 nil 之后、Service 登记之前。`-race` 下 200/200 通过。`timed_out`（工作流 1 秒，runner 在 ctx 结束时返回 nil）和 `interrupted`（置 `s.closed` 再取消 active ctx）两条也都不登记产物。
3. **两处修改各自都被测试卡住，不靠 sleep。** 把最终扫描改回「`ctx.Err()==nil` 才检查」且恢复「任何终态都写 `r.Artifacts`」后，`TestCancelExitRace` 第 0 轮就失败：超额分支是 `cancelled` 且产物 0（`runNative` 在 `runner.go:194` 已经返回 `context.Canceled`，收集被跳过）；未超额分支是 `cancelled` 且产物 1。只回退扫描时，超额分支失败、未超额 200 次通过。只回退产物门闩时，超额 200 次通过、未超额第 0 轮产物为 1。
4. **状态优先级没有被这次改动重排。** 配额判断仍在 `Cancelling` 之前。产物只在 `status == Succeeded` 时写入（`service.go:663-665`）。仓库里给 `Run.Artifacts` 赋值的生产代码只有这一处。
5. **全量与 vet。** 探针移出仓库后，`cd services/workshop && go test -race ./... -count=1` 退出 0（cmd/server 2.3s，server 6.3s，workshop 59.0s）。`go vet ./...` 退出 0。Go 是 `/home/ubuntu/sdk/go/bin/go`，go1.25.14。

## 没覆盖到

- 没有起真实 Docker。集成测试在未设置 `EASYGO_DOCKER_TEST_*` 时跳过。W-3 要求的是受控竞态，不是容器证明。
- 没有真正创建 800 万以上文件。1000 万限额的 37 秒是按 4 万文件线性外推的。本机空闲 inode 不够做那次全量扫描。
- 配额失败仍会写下 `SessionID`（探针里失败 run 的 session 是夹具线程 id）。这行不在本次 diff 里。`*DockerRunner` 的 resume 会再扫配额；这次没有跑 resume。
- 没有跑 `services/ai-gateway`、`packages/rpc-go` 和仓库根模块。这次 diff 不涉及它们。

## 证据

目录：`/tmp/crew/acl-530e/review/eb46347/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `green.log` | `-race` 下 `TestCancelExitRace` 两支各 200 次，加上启动项计数、超时/扫描探针 | 整次命令是 1，因为当时仓库里还有一份写错的中断探针（Close 之后读已关闭的 bolt）。`TestCancelExitRace` 本身通过 |
| `interrupt.log` | 修正后的中断探针：`interrupted`，产物 0 | 0 |
| `red-both.log` / `red-docker.log` / `red-service.log` | 回退旧代码后测试判红，以及每一支单独回退 | 都是 1 |
| `patched-bound.log` | 最终扫描预算临时改为 1 纳秒 | 0 |
| `race-all.log` | 探针删除后的 `go test -race ./...` | 0 |
| `vet.log` | `go vet ./...` | 0 |
| `w3_review_probe_test.go` | 审查探针原文 | |
| `restored.diff` | 实验结束后 `docker.go` / `service.go` 的 diff，空文件 | |

审查命令都在 `services/workshop` 下执行。回退和 1 纳秒补丁都已 `git checkout` 掉。
