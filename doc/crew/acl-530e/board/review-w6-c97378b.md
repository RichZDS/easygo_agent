# 审查 · W-6 · c97378be105c36a3429ae18a2fbf83fff8f380b8

结论：**通过**。

队列 #10。提交 `c97378b`，父提交 `fd480ab`。审查范围是 `fd480ab..c97378b`。检出是 `/home/ubuntu/Projects/easygo-acl-review2` 的 detached HEAD `c97378b`。没有改施工者分支。破坏验证改过的 `docker.go`、`crew.go` 已复原。审查探针已删。`git diff` 为空。

P0–P2 无。P3 一条：取消那一支子用例没有把 false_green 重算单独锁住。代码里的重算还在，重启、Close、failed 三条会判红。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 取消子用例删掉重算仍然通过

`TestFinishCrewOutcomeRecomputesFalseGreen/normal-cancelled-path-unaffected`（`crew_test.go:466-467`）把种子 `FalseGreen` 设成 false。临时删掉 `crew.go` 里调用 `acceptanceFalseGreen` 的那段之后，interrupted 和 `normal-failed` 判红，重启测试和 Close 探针也判红，这个取消子用例没有出现在失败列表里。日志 `red-drop-falsegreen.log`，退出码 1。

它因此锁不住「重算把已经为 true 的假绿清掉」。取消、interrupted、failed 都走同一个 `finishCrewOutcome`，后两条已经把函数锁住。这是测试缺口，不是产品行为错误。

## 已验证

1. **只读在构造时写进挂载串。** `containerOptions` 增加 `workspaceReadOnly`（`docker.go:259-271`），为真时直接在挂载串末尾加 `,readonly`。`checkContainerOptions` 恒传 true（`acceptance_docker.go:15`），原来的 `,dst=/workspace,` 子串循环已删除，循环只改 `--entrypoint`。`taskContainerOptions` 传 `policy == "read-only"`（`docker.go:281`），read-only 时另挂 `.workshop-home`，这个子挂载不加 readonly（`docker.go:282-284`）。`Initialize` 探针传 false（`docker.go:549`）。
2. **字段集合断言，挪动 dst 仍绿。** `findMount` 按精确字段 `dst=<路径>` 找挂载，再看字段集合里有没有 `readonly`（`docker_test.go:187-203`）。检查容器、read-only 任务、`.workshop-home` 可写、workspace-write、以及带 session 的第二次 `Run`，都在 `TestContainerOptionsWorkspaceReadOnlyByFieldSet`。`TestAcceptanceContainerOptions` 同样改成字段集合。临时把 `dst` 挪到最后一个字段（只读时字符串变成 `...,readonly,dst=/workspace`，不再含 `,dst=/workspace,`）：上述新测试和 Initialize 探针仍然通过，`red-move-dst.log`，退出码 0。旧的 `TestDockerReadOnlyWorkspaceAllowsOnlyNativeHomeWrites` 仍要求只读后缀，这次挪动下判红（`red-move-dst-suffix.log`，退出码 1，`docker_test.go:456`）。当前提交的字段顺序下它是绿的。
3. **去掉只读会判红。** 临时删掉 `workspaceReadOnly` 分支里的 `,readonly` 追加：`TestAcceptanceContainerOptions` 在 `acceptance_test.go:321` 失败，`TestContainerOptionsWorkspaceReadOnlyByFieldSet` 在 `docker_test.go:469` 失败。`red-drop-readonly.log`，退出码 1。已复原。
4. **真容器写拒绝。** 专用 daemon，镜像 `easygo-sandbox-fixture:acl-workshop`（`sha256:4eefe6d2…`），没有重建。`TestAcceptanceDockerIntegration/pass`：检查容器 `write denied /workspace/check-write: true`，pack 同样被拒绝，无 relay。`TestDockerIntegration` 的 readonly 模式：`workspace_readonly: true`、`workspace_write_denied: true`、`home_writable: true`。同一测试的 first/resume 是 workspace-write，`artifact_written` 和 `home_written` 为 true。`docker.log`，退出码 0。
5. **false_green 按同一张真值表重算。** `finish` 仍只改状态，不改 `FalseGreen`（`service.go:737-746`）。紧随其后的 `finishCrewOutcome` 在重算 outcome 之后调用 `acceptanceFalseGreen`（`crew.go:334-340`）。真值表仍是 submitted、`tests=pass`、state `failed` 三者同时成立（`acceptance.go:68-69`）。`New` 的重启恢复（`service.go:230-246`）和 `Close`（`service.go:804-820`）发出的 interrupted 事件带上重算后的 `a.FalseGreen`。执行收尾（`service.go:705-720`）和排队取消（`service.go:475-477`）也调用 `finishCrewOutcome`，这两处本来就不发 acceptance 事件，这次没有新加事件。
6. **重启和 Close 都会清掉陈旧假绿。** `TestAcceptanceRestartRecomputesFalseGreen`：bolt 里是 failed + `false_green=true`，另有一条 submit `tests=pass`，任务 Running。重启后是 interrupted，记录和事件里的 `false_green` 都是 false。自写的 Close 探针走 `Service.Close` 而不是直接关库，结果相同（`unit.log` 里 `TestReview2W6CloseRecomputesFalseGreen` PASS）。删掉重算后，这两条和 failed 子用例都判红，记录里留下 `FalseGreen:true`（`red-drop-falsegreen.log`）。
7. **Initialize 探针和 workspace-write 保持可写。** 探针的真实 create 参数里，工作区挂载没有 `readonly`（`TestReview2W6InitializeProbeStaysWritable`，`unit.log` PASS）。workspace-write 的字段集合断言没有 `readonly`。真容器 first 模式能写工作区。
8. **t.Cleanup 只清本测试的 owner。** `TestAcceptanceDockerCleanupResilienceRealContainer` 在 `s.Close()` 之后用原始 docker CLI，按 `ai.easygo.workshop.owner=<本测试 owner>` 做 `rm --force`（`acceptance_integration_test.go:192-202`）。测试前用另一个 owner `review2-w6-not-the-test` 建了已创建容器 `easygo-review2-w6-foreign`。测试结束后它还在（`docker-leftovers.log`），`easygo-platform-r3-*` 也还在。随后只删了这只外来容器。测试自身的 owner 下没有残留。
9. **提交说明里那句 W-5 测试加强不是这笔做的。** `c97378b` 的 `docker_test.go` 只加了 `mountFields`、`findMount` 和 `TestContainerOptionsWorkspaceReadOnlyByFieldSet`，并改了一处 `containerOptions` 调用。两支 confirmCleanup 测试的状态修正在父提交 `fd480ab`。按队列说明核对过，不算问题。这笔确实加了 t.Cleanup。
10. **全量。** 重负载都经 `CREW_SEAT=acl-review2-530e /tmp/crew/acl-530e/bin/heavy`。按用例 `-race`：`unit.log`，退出码 0。全量一次：`cd services/workshop && go test -race -count=1 -timeout 300s ./...`，`TEST_EXIT:0`（workshop 包 62.481 秒）。`go vet ./...`，`VET_EXIT:0`。全量没有设置 `EASYGO_DOCKER_TEST_*`，真容器是上面第 4、8 条另跑的，没有 `-race`。开跑前没有别的 `go test`。

## 没覆盖到

- 没有在验收检查跑到一半时停掉服务。执行收尾那条路径只对照了代码：它调用 `finishCrewOutcome`，不另发 acceptance 事件。
- 全量 `-race` 没有带专用 Docker。真容器那三支没有 `-race`。
- 验收集成只跑了 `pass`。bad、timeout、cancel、output、infrastructure、skip 没有重跑。
- `t.Cleanup` 在 `ps` 失败时丢掉错误，这次 daemon 是通的，没有单独制造 `ps` 失败。

## 证据

| 文件 | 内容 |
|---|---|
| `/tmp/crew/acl-530e/review2/c97378b/unit.log` | 按用例 `-race`，含字段集合、重启、Close、Initialize 探针，EXIT:0 |
| `/tmp/crew/acl-530e/review2/c97378b/red-move-dst.log` | dst 挪到末尾，新测试仍绿，EXIT:0 |
| `/tmp/crew/acl-530e/review2/c97378b/red-move-dst-suffix.log` | 同一挪动下旧后缀测试判红，EXIT:1 |
| `/tmp/crew/acl-530e/review2/c97378b/red-drop-readonly.log` | 去掉 readonly 判红，EXIT:1 |
| `/tmp/crew/acl-530e/review2/c97378b/red-drop-falsegreen.log` | 去掉重算后重启、Close、failed 判红，EXIT:1 |
| `/tmp/crew/acl-530e/review2/c97378b/docker.log` | 真容器 pass、回收韧性、Docker 集成，EXIT:0 |
| `/tmp/crew/acl-530e/review2/c97378b/docker-leftovers.log` | 外来 owner 容器还在，平台容器未动 |
| `/tmp/crew/acl-530e/review2/c97378b/race.log` | 全量 `-race`，TEST_EXIT:0 |
| `/tmp/crew/acl-530e/review2/c97378b/vet.log` | vet，VET_EXIT:0 |
