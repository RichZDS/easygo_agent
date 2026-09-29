# 审查 · W-3b · 3adec82aad036aee686284d093bc8666cd64c9e0

结论：**通过**。W-3 的 P2 已关闭。

提交在 `feat/acl-workshop`，父提交 `aa41e65`（`eb46347` 是祖先）。本笔 diff 只有 `services/workshop/workshop/docker.go` 和 `cancel_exit_race_test.go`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD `3adec82`。没有改施工者分支，也没有提交。破坏性补丁已复原，`git diff` 为空。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

原先的 P2 是：最终扫描的 30 秒预算到期时返回 `context.DeadlineExceeded`，调用方若正在取消，任务会被记成 `cancelled`。本笔把它包成 `ErrDiskQuotaScanFailed`，只在扫描错误和 `quotaCtx` 自身都是 `DeadlineExceeded` 时包装。

### P3 · 无

## 已验证

1. **包装条件只认扫描自己的预算。** `DockerRunner.Run` 仍用 `context.WithTimeout(context.WithoutCancel(ctx), budget)`（`docker.go:565-570`）。`budget` 默认 30 秒；`finalQuotaTimeout` 为零时不改这个值。`CheckDiskQuota` 失败时，只有 `errors.Is(quotaErr, context.DeadlineExceeded)` 且 `errors.Is(quotaCtx.Err(), context.DeadlineExceeded)` 才返回 `disk_quota_scan_failed: final scan: …`，原因用 `%w` 保留（`docker.go:571-575`）。其它配额错误原样返回。`service.go` 的状态顺序不在本笔 diff 里：配额错误仍在 `Cancelling` 之前（`service.go:694-697`），产物仍只在 `Succeeded` 时写入（`service.go:711-713`）。
2. **调用方取消不会触发这层包装。** `-race` 下 `TestCancelExitRace` 两支各 200 次通过。超额分支是 `failed`，原因前缀 `disk_quota_exceeded `，产物 0。未超额分支是 `cancelled`，产物 0。扫描在目录循环里读的是传入的 `quotaCtx`（`disk_quota_linux.go:63-64`）。
3. **预算到期时，取消与不取消都是 failed，无产物。** 测试把预算注入成 1 纳秒。`-race` 下 `TestFinalQuotaBudgetFailure` 两支各 200 次通过。状态都是 `failed`，原因前缀 `disk_quota_scan_failed:`，正文含 `context deadline exceeded`，产物长度 0。
4. **去掉包装后，新测试立刻判红。** 取消分支第 0 次是 `cancelled` / `cancelled by caller` / 产物 0。不取消分支第 0 次是 `failed` / `context deadline exceeded` / 产物 0。这就是原来的 P2。`docker.go` 已 `git checkout` 复原。

## 没覆盖到

- 没有新建镜像，没有设置 `EASYGO_DOCKER_TEST_*`，没有起真实容器。这两支测试用的是假 Docker。
- 没有重跑 `go test -race ./...` 和 `go vet ./...`。本笔只判断扫描超时分类，按用例跑了上述两支。施工者的全量日志只作线索，不作为本结论的依据。
- 没有造接近 1000 万文件的目录。1 纳秒预算是施工图要求的注入方式。
- 没有单独把工作流截止时间打到比扫描预算更早。分类顺序上，已包装的扫描失败先于 `ctx.Err()` 的 `DeadlineExceeded`。这一支只看了代码。
- 父提交里的 W-2 不在本笔审查范围。

## 证据

目录：`/tmp/crew/acl-530e/review/3adec82/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `green-race.log` | `go test -race ./workshop -run 'TestFinalQuotaBudgetFailure\|TestCancelExitRace' -count=1 -v`，四支各 200 次 | 0（闸门 22 秒） |
| `sab-unwrap.log` | 去掉包装后只跑 `TestFinalQuotaBudgetFailure` | 1（闸门 2 秒） |

命令在 `services/workshop` 下执行，Go 是 `/home/ubuntu/sdk/go/bin/go`。两次都经 `CREW_SEAT=acl-review-530e /tmp/crew/acl-530e/bin/heavy`。
