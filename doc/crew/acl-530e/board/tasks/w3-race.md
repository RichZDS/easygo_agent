# W-3 · 取消与退出竞态（先做）

assignee: workshop · 状态: done（eb46347，终审通过 review-w3-eb46347.md，工头签字 09-29；工头已签 51dadc9）；W-3b review（3adec82）

## 现场 / 锚点
- `services/workshop/workshop/docker.go` 的 `DockerRunner.Run` 末尾有这段：
  ```go
  if ctx.Err() == nil { if quotaErr := r.CheckDiskQuota(ctx, in.Workspace); ... }
  ```
  调用方一取消，最后这次配额检查就被跳过了。原生进程如果恰好已经成功退出，`runNative` 返回 nil。
- `services/workshop/workshop/service.go` 的 `execute`：`runErr == nil` 时会收集产物。状态判定 switch 里 `current.Status == Cancelling` 会给出 `Cancelled`，但产物照样登记进 `r.Artifacts`。
- 上一班子终审的复现：`/tmp/crew/platform-b928/review-553b958/cancel-exit-race.log`，同目录下有复现代码。用真实 Service、只把 Docker 命令换成受控假实现，12 次里观测到 1 次任务 Cancelled、artifacts=1、工作区条目 1001 > 1000。

## 修法
1. 最终配额检查**总是执行**：用 `context.WithoutCancel(ctx)` 加一个有界超时，比如 30 s。超额时返回配额错误。service 里配额错误的判定本来就排在 Cancelling 前面，所以超额一律记 Failed。
2. `execute` 只在最终状态是 `Succeeded` 时写 `r.Artifacts`，其他终态一律不登记产物。
3. 行为改动限于这两处。不要改变其它状态判定的顺序。

## 明令不做
- 不重写配额监控循环。
- 不改轮询间隔的默认值。
- 不顺手做 P1 其它单。

## 测试
- 新增确定性单测：用假 runner 或假 docker 制造"原生成功退出 + 调用方取消"同时发生，并让工作区超额。断言：
  - 最终状态是 `failed`，原因是配额；
  - 没有登记产物。
- 再测不超额的情况：取消与退出竞态时，状态可以是 `cancelled`，但产物列表必须为空。
- 用同一条件循环至少 200 次，结果必须稳定。不能靠 sleep 赌时序，要用 channel 或钩子精确卡住时点。
- `cd services/workshop && go test -race ./... && go vet ./...` 退出 0。

## 交付
- 在 `feat/acl-workshop` 上单独提交一笔。
- 在 worklog 顶部写报审：提交号、改了哪些文件、测试命令和真实退出码、竞态测试的循环次数和结果。
- 然后用 herdr-msg 通知工头，附 delivery_id。

## W-3b · 终审 P2 修正（09-29，W-2 报审后再做）

- **现场**：终审报告 `review-w3-eb46347.md` 的 P2。最终扫描的 30 秒上限到期时返回 `context.DeadlineExceeded`，它不属于配额错误；如果此时调用方正在取消，任务会记成 `cancelled`，"没扫完"这件事就丢了。按终审实测的每秒约 27 万项，文件数上限 1000 万时约需 37 秒，会超出 30 秒。
- **修法**：最终扫描因为自己的 30 秒预算到期而失败时，把错误包装成 `ErrDiskQuotaScanFailed`，保留原因文字。这样它排在 `Cancelling` 之前，任务一律记 `failed`（扫描失败），做到失败即关闭。调用方的取消本身不能触发这个包装，只有扫描预算到期才包装。
- **明令不做**：不改 30 秒这个预算，不改状态优先级。
- **测试**：用终审的做法，把预算临时注入成极小值。取消分支和不取消分支都必须得到 `failed`，原因是扫描失败，并且不登记产物。`-race` 下两支各跑 200 次。
- **交付**：单独一笔提交。
