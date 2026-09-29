# 审查 · W-5 · fd480ab0d686106e82ee61c567bb8628c209e485

结论：**通过**。

队列 #9。两笔提交：`1e1e78c`（父提交 `be7dac0`）和返工 `fd480ab`。审查范围是 `be7dac0..fd480ab`。检出是 `/home/ubuntu/Projects/easygo-acl-review2` 的 detached HEAD `fd480ab`。没有改施工者分支。红测改过的 `docker.go` 已复原。`git diff` 为空。

P0–P2 无。P3 一条：测试 d 没有把「仍在运行」单独锁住。代码里的判断还在。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 测试 d 删掉 Running 判断仍然通过

`TestDockerCleanupFailsClosedWhenStillRunning`（`docker_test.go:565-571`）只把 `State.Running` 设成 true，`Status` 仍是空字符串。空状态本来就不在 `created`/`exited`/`dead`/`removing` 里。把 `docker.go:370` 的 `st.Running ||` 临时拿掉，这支测试仍然通过，日志 `red-running.log`，退出码 0。然后已复原。

`Paused`、`Restarting` 也没有单独的测试去锁。正常 Docker 状态里，正在跑的容器 `Status` 是 `running`，同样会被 `stoppedStatus` 拒掉；这个缺口只覆盖「Status 已是停止、布尔量却仍为真」这种不一致。

## 已验证

1. **三次有界重试。** `cleanup` 最多 3 次 `remove`，每次独立 20 秒，中间退避 1 秒、2 秒（`docker.go:406-422`）。测试把退避收成 1 毫秒。前两次 `rm` 失败、第三次成功：不置位，容器被删掉。前一次 `rm` 已经生效、下一次 `ps` 为空：同样成功，不置位。
2. **确认后才延迟回收。** 3 次都失败后，独立 20 秒做 `confirmCleanup`（`docker.go:340-373`）。`ps` 出错直接 `return false, err`（`341-344`），不再往下 `inspect`。`ps` 成功且为空算成功。否则 `inspect` 必须恰好一个对象，三个 owner 标签全匹配，并且 `Running`、`Paused`、`Restarting` 都为 false，`Status` 属于 `created`/`exited`/`dead`/`removing`，才返回 `errCleanupDeferred`、写入待回收集合、不置位。其余情况置位 `cleanupFailure`。
3. **返工的两处破坏验证。** 把 `ps` 出错改回「继续 inspect」：`TestDockerCleanupFailsClosedWhenConfirmationTransportFails/ps` 失败，得到的是 `errCleanupDeferred` 而不是失败即关闭（`red-ps.log`，退出码 1，`docker_test.go:605`）。删掉 `confirmCleanup` 里的标签循环：标签不符被当成待回收（`red-label.log`，退出码 1，`docker_test.go:630`）。两处都已复原。inspect 传输失败那一支在未改代码时是绿的，假容器的 `Status` 是 `exited`。
4. **上限、清扫、调用方。** 集合里已经有 32 个再来一个已停止容器：置位，集合大小不变。清扫用一把总预算 20 秒，失败的名字留在集合里；第二个并发清扫在第一个还卡在 `rm` 时直接返回（`docker.go:451-474`，`TestDockerSweepPendingCleanupOnlyOneAtATime`）。`runCheck` 遇到哨兵错误保留退出码、`timedOut` 和 `err`（`acceptance_docker.go:53-59`）：退出码 0 仍是 `(0, false, nil)`；超时仍是 `(-1, true, nil)`；失败即关闭仍盖成 `check container cleanup failed`。`Run` 的延迟回收不追加 `admission disabled`（`docker.go:658-661`）。`Initialize` 的探针把哨兵错误当成成功，不 join 进初始化错误（`docker.go:536-538`）。孤儿循环仍直接 `remove`（`docker.go:509`）。
5. **时长不含回收。** `runCheck` 在 `start --attach` 返回时记下 duration，回收在 defer 里（`acceptance_docker.go:63-68`）。`acceptance.go:170-171` 把这个值写入 `duration_ms`。
6. **真容器。** 专用 daemon，镜像 `easygo-sandbox-fixture:acl-workshop`，没有重建。检查容器的前 3 次 `rm` 被注入失败。第一笔验收 `passed`，`duration_ms < 10000`，`cleanupFailure` 为空，待回收集合为 1。下一笔任务能进来，准入清扫把那个容器删掉，owner 标签下 `ps` 为空。`docker.log`，2.24 秒，退出码 0。
7. **文档。** 回收三层写在 `doc/workshop.md:302-310`。W-4 的标题 `## Docker 与 host 的 shell 工具` 在第 312 行，正文还在。
8. **全量。** `cd services/workshop && go test -race -count=1 -timeout 300s ./...` 退出 0（workshop 包 64.149 秒）。`go vet ./...` 退出 0。这次没有设置 `EASYGO_DOCKER_TEST_*`，真容器测试在全量里 Skip；它是上面第 6 条单独跑的。跑之前没有别的 `go test`。

## 没覆盖到

- 没有把 daemon 真的拖满 20 秒。重试用的是注入失败和缩短的退避。
- 全量 `-race` 没有带专用 Docker，真容器那一支没有 `-race`。
- `Paused`、`Restarting` 没有单独用例。测试 d 的缺口见上面的 P3。
- 没有重跑 W-2 那一组验收容器子测试。

## 证据

| 项 | 路径 |
|---|---|
| 假 docker 单测 a–k | `/tmp/crew/acl-530e/review2/fd480ab/unit.log`（EXIT:0） |
| ps 出错继续 inspect 会判红 | `red-ps.log`（EXIT:1） |
| 去掉确认阶段标签循环会判红 | `red-label.log`（EXIT:1） |
| 去掉 Running 判断，测试 d 仍绿 | `red-running.log`（EXIT:0） |
| 真容器 | `docker.log`（EXIT:0） |
| 全量 -race / vet | `race.log`（TEST_EXIT:0），`vet.log`（VET_EXIT:0） |
