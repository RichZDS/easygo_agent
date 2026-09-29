# worklog · workshop2 (acl-workshop2-530e)

## 2026-09-29 W-7 报审：诊断截断处不留 secret 前缀 + W-5 测试 d 补强

单号：W-7 · 提交：`021326e`（`feat/acl-workshop-w5`，单独一笔，基线 `c97378b`）

### 1. 单号与提交号
W-7，commit `021326e`。

### 2. 改了哪些文件
- `services/workshop/workshop/runner.go`：新增 `minSecretSuffixLength = 4` 常量、`secretPrefixSuffixLength(text, secrets, minLen) int`（找 text 末尾能和某个 secret 前缀精确匹配的最长一段，只看 ≥ minLen 的匹配）、`redactAndBound(raw, redact, secrets, limit) (text string, truncated bool)`（先替换、按 limit 截断、再按 `secretPrefixSuffixLength` 把截断后暴露出来的那段 secret 前缀砍掉）。`runNative` 原来"整段替换→按 diagnosticLimit 截断"的两行内联代码改成调用 `redactAndBound`。检查过其它做替换的地方（streamParser 逐片替换、`runtime_events.go`、`views.go` 的 `prefix()`）都是对已拼好的完整片段做替换，不存在"替换后再截断"的顺序，不需要改。
- `services/workshop/workshop/runner_redact_test.go`（新增）：`TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary` 把终审探针的构造（开头一份完整 64 位 hex secret，末尾放同一 secret 的前 63 个字符）写成正式测试，跑 `runNative` 断言诊断事件的 JSON 里不出现该 secret 任意长度 ≥4 的前缀，同时诊断文本没有被过度截断（保留了大部分正常文本）。`TestRedactAndBoundEdgeCases` 表驱动覆盖：secret 恰好跨在截断点上、两个不同 secret 都要检查、没有配置 secret、缓冲刚好等于 limit、缓冲比 limit 少一字节。
- `services/workshop/workshop/docker_test.go`：把 `TestDockerCleanupFailsClosedWhenStillRunning`（只把 `Running` 设 true，`Status` 留空字符串）换成 `TestDockerCleanupFailsClosedWhenStillActive`，三个子用例 `Running`/`Paused`/`Restarting`，每个子用例都先把 `Status` 设成 `"exited"`，再单独锁一个布尔字段为 true，断言 `cleanup` 失闭、`cleanupFailure` 被设置、`Run` 被拒绝。

### 3. 每条测试命令和真实退出码
全部通过 `CREW_SEAT=acl-workshop2-530e /tmp/crew/acl-530e/bin/heavy` 闸门执行：
- `go build ./... && go vet ./...` → 两条都 exit 0。
- 按用例：`go test ./workshop/... -run 'TestDockerCleanupFailsClosedWhenStillActive|TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary|TestRedactAndBoundEdgeCases' -v -count=1` → exit 0，8 个子用例全 PASS。
- 破坏验证（四项，均已恢复，`git status` 干净）：
  - `docker.go` 的 `confirmCleanup` 判断依次去掉 `st.Running ||`、`st.Paused ||`、`st.Restarting ||` 三次，每次单独跑 `TestDockerCleanupFailsClosedWhenStillActive`：去掉哪个，就只有对应的子用例判红（`expected fail-closed, got container confirmed stopped; removal deferred`），另外两个子用例仍然 PASS——三次都精确对应，没有互相掩盖。
  - `runner.go` 的 `redactAndBound` 去掉 `secretPrefixSuffixLength` 那段砍前缀的逻辑：`TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary` 判红（诊断事件里出现了 4 字节的 `[REDACTED]` 之后紧跟的 secret 前缀残留，日志里能直接看到那段污染文本），`TestRedactAndBoundEdgeCases` 里 `straddles-truncation-point`、`multiple-secrets-both-checked` 两个用例判红，其余三个（无 secret、恰好等于 limit、比 limit 少一）不受影响仍是绿的，符合预期（这三个场景本来就不触发砍前缀逻辑）。
- 全量（跑前确认无人在跑，只跑一次）：`go test -race -count=1 ./... && go vet ./...` → 两条都 exit 0。

### 4. 证据路径
全部在 `/tmp/crew/acl-530e/workshop2/`：
- `w7-build-vet.log`
- `w7-unit-tests.log`
- `w7-destructive-running.log`
- `w7-destructive-paused.log`
- `w7-destructive-restarting.log`
- `w7-destructive-redact.log`
- `w7-full-race.log`

### 5. 偏差与没做的事
- 没有偏差：只改了归属表里 W-7 那一行列出的 `runner.go` 诊断/截断/secret 替换部分，以及对应测试；`docker_test.go` 的改动是施工图明确写的"追加"部分，并进同一笔提交。
- 没有动 relay 鉴权、token 生成方式，没有改 W-5、W-6 的既有逻辑（`confirmCleanup` 本身的判断顺序和内容一个字没动，只是测试从一个子用例拆成三个）。
- 没有改诊断上限 `diagnosticLimit`、`[REDACTED]` 标记、错误改写文案。

### 6. 终审建议重点看
- `secretPrefixSuffixLength` 的匹配策略：对每个 secret 从最长可能的重叠长度往下试到 `minLen`，取所有 secret 里最长的一次匹配整体砍掉一次——如果两个 secret 在文本末尾有不同长度的重叠，只砍最长的那一次是否总是够（因为最长匹配的后缀本身就包含了任何更短的匹配，所以理论上只需一次，已在函数注释里写明这个前提，值得终审确认这个前提在多 secret 场景下依然成立）。
- `minSecretSuffixLength = 4` 这个阈值本身是否合适（施工图写"只看长度 ≥ 4 的前缀"，直接照抄的数字，没有额外论证）。
- `TestDockerCleanupFailsClosedWhenStillActive` 三个子用例是否真的互相独立——破坏验证已经证明去掉任一判断只影响对应子用例，但终审可以再看一眼三个子用例的 fixture 构造有没有隐藏的共享状态。

单号：W-6 · 提交：`c97378be105c36a3429ae18a2fbf83fff8f380b8`（`feat/acl-workshop-w5`，单独一笔）

### 1. 单号与提交号
W-6，commit `c97378b`。

### 2. 改了哪些文件
- `services/workshop/workshop/docker.go`：`containerOptions` 加 `workspaceReadOnly bool` 参数，在构造挂载串时直接决定是否带 `,readonly`；`taskContainerOptions` 传 `policy == "read-only"`；`Initialize` 探针容器传 `false`（行为不变）；删掉原来靠 `,dst=/workspace,` 子串匹配追加 `readonly` 的写法。
- `services/workshop/workshop/acceptance_docker.go`：`checkContainerOptions` 改传 `containerOptions(..., true)`（检查容器恒只读），删掉子串匹配循环，删掉不再用的 `strings` 导入。
- `services/workshop/workshop/crew.go`：`finishCrewOutcome` 在重算 `run.Outcome` 之后，用 `acceptanceFalseGreen(tx, task, run.Acceptance.State)` 重算 `run.Acceptance.FalseGreen`（`run.Acceptance` 非 nil 时）。
- `services/workshop/workshop/service.go`：`New()` 重启恢复、`Close()` 优雅关闭两处 `AcceptanceEvent{State:"interrupted"}` 字面量补上 `FalseGreen: a.FalseGreen`（用重算后的值）。这个文件不在 brief 归属表里，但改动是施工图第 2 条明确要求的（"事件里的 false_green 也必须是重算后的值"），只动了这两处字面量，已在偏差里说明。
- `services/workshop/workshop/docker_test.go`：新增 `mountFields`/`findMount` 辅助函数（解析 `--mount` 字段集合，不锁字面量顺序）；新增 `TestContainerOptionsWorkspaceReadOnlyByFieldSet`（覆盖检查容器/只读任务容器/写任务容器/resume 四种场景）；四处直接调用 `containerOptions` 的旧测试补上新增的 bool 实参。
- `services/workshop/workshop/acceptance_test.go`：`TestAcceptanceContainerOptions` 改成用 `findMount` 断言语义（挂载在字段集合里有没有 `readonly`），不再锁 `,dst=/workspace,bind-propagation=rprivate,readonly` 整段字面量；新增 `TestAcceptanceRestartRecomputesFalseGreen`，把终审探针场景写成正式测试（bolt 里种 acceptance `failed`+`false_green=true`+一条 `submit tests=pass` 消息，任务 Running，重启后断言 `interrupted`+`false_green=false`，且事件里的 `false_green` 也是 false）。
- `services/workshop/workshop/crew_test.go`：新增 `TestFinishCrewOutcomeRecomputesFalseGreen`，表驱动覆盖三种场景：重启导致的 interrupted 清掉旧的假绿；正常 failed 路径不受影响（假绿该是 true 还是 true）；正常 cancelled 路径不受影响（false）。
- `services/workshop/workshop/disk_quota_test.go`、`docker_integration_test.go`（2 处）、`docker_native_test.go`：`containerOptions` 签名改了之后的机械性调用点更新，全部传 `false`（保持原语义，这几处都不关心只读）。
- `services/workshop/workshop/acceptance_integration_test.go`：给 W-5 的真容器测试 `TestAcceptanceDockerCleanupResilienceRealContainer` 加 `t.Cleanup`，跑在 `s.Close()` 之后，按 owner 标签用原始 docker CLI 兜底删除残留容器，即使测试中途 `t.Fatal` 也会执行（工头 09-29 追加的要求，开发时留下过一个已停止的检查容器）。

### 3. 每条测试命令和真实退出码
全部通过 `CREW_SEAT=acl-workshop2-530e /tmp/crew/acl-530e/bin/heavy` 闸门执行：
- `go build ./... && go vet ./...` → 两条都 exit 0
- 按用例：`go test -v -race -run 'TestContainerOptionsWorkspaceReadOnlyByFieldSet|TestAcceptanceContainerOptions|TestDockerReadOnlyWorkspaceAllowsOnlyNativeHomeWrites|TestCodexInnerPolicyChangesOnlyAfterDockerInitialization|TestFinishCrewOutcomeRecomputesFalseGreen|TestAcceptanceRestartRecomputesFalseGreen|TestAcceptanceRestartInterruption|TestDockerCleanup|TestDockerSweep|TestDockerRunCheck' -count=1 ./workshop/` → exit 0，全部 PASS
- 破坏验证（测试项 2，已写进证据）：
  - 临时把挂载串里 `dst` 挪到 `bind-propagation` 之后：`TestContainerOptionsWorkspaceReadOnlyByFieldSet` 仍然判绿（不依赖字段顺序）；
  - 临时去掉 `,readonly` 追加：同一测试判红（`check container workspace not readonly`）；
  - 两处均已恢复，`git status` 干净。
- 真容器（专用 daemon，复用 `easygo-sandbox-fixture:acl-workshop`，未新建镜像）：`go test -run 'TestAcceptanceDockerIntegration|TestAcceptanceDockerCleanupResilienceRealContainer|TestDockerIntegration' -v -count=1 -timeout 180s ./workshop/` → exit 0；`TestDockerIntegration` 的 readonly 子场景里 `workspace_readonly`、`workspace_write_denied` 均为 true，证明重构后真实容器里工作区确实只读。
- 全量（每笔交付只跑一次，跑前用 `ps -eo args | grep -c '[g]o test -race'` 确认无人在跑，闸门本身也保证全班同一时刻只跑一个）：`go test -race -count=1 ./... && go vet ./...` → 两条都 exit 0。

### 4. 证据路径
全部在 `/tmp/crew/acl-530e/workshop2/`：
- `w6-build-vet.log`
- `w6-unit-tests.log`
- `w6-destructive-verification.log`
- `w6-real-container.log`
- `w6-full-race.log`

### 5. 偏差与没做的事
- 改了 `service.go` 两处 `AcceptanceEvent` 字面量，不在 brief 归属表里列出的文件范围内；这是施工图第 2 条明确要求的最小改动（只加 `FalseGreen: a.FalseGreen` 字段），没有改这两处以外的任何逻辑。如果工头认为需要补进归属表，请告知。
- 没有改 `finish` 把 acceptance 改写成 `interrupted` 的既有选择，只做一致性重算，符合施工图"明令不做"。
- 没有改 W-1~W-5 的其它行为。
- 真容器测试环节额外确认：跑完之后用 `ps -aq --filter label=ai.easygo.workshop.owner=<owner>` 核对过，测试自身的 owner 下没有残留容器（见 w6-real-container.log 里 `owned containers=0` 那几行）。

### 6. 终审建议重点看
- `containerOptions` 新参数 `workspaceReadOnly` 是否在全部三个调用点（`taskContainerOptions`、`checkContainerOptions`、`Initialize` 探针）都传对了值。
- `findMount`/`mountFields` 这两个测试辅助函数本身的正确性——它们现在是好几条断言的共同基础。
- `finishCrewOutcome` 重算 `FalseGreen` 时复用 `acceptanceFalseGreen` 是否会在某些 outcome/tests 组合下产生和原来 `acceptanceState()` 路径不一致的结果（正常的 `running`→`failed`/`passed` 路径完全没有经过 `finishCrewOutcome`，只有 finish() 参与的四个收尾点才走这条重算，逻辑上不应冲突，但值得终审单独确认一遍真值表）。
- `service.go` 那两处补的 `FalseGreen: a.FalseGreen` 是否真的只加了这一个字段，没有牵动其它字段或逻辑。

### 附：资源管控与工具使用两点说明
- 09-29 14:15 起的资源管控令（`board/resource-rules.md`）已收到并整篇读完，本单起所有重命令都经 `CREW_SEAT=acl-workshop2-530e /tmp/crew/acl-530e/bin/heavy` 闸门执行，全量 `-race` 只跑了一次，真容器测试与全量分开跑，跑完已清理 `/tmp` 下的测试临时目录。
- 工头指出我误用了 `AskUserQuestion`（交互式提问工具）去等待后台任务，导致 pane 显示 blocked，工头已手动 Esc 处理。已确认：以后等后台任务只用 Monitor 或后台通知，有疑问改用 herdr-msg 问工头，不再使用交互式工具。

## 2026-09-29 W-5 返工报审（工头初核 1e1e78c 退回三处）

单号：W-5 · 返工提交：`fd480ab`（在 `feat/acl-workshop-w5` 上另起一笔，未 amend；基线是本单第一笔 `1e1e78ca5afe4a94a44f016dc5211ccc69539f6a`）

### 1. 单号与提交号
W-5，返工 commit `fd480ab`（`git log` 全 hash 见下方证据日志或直接 `git show fd480ab`）。

### 2. 改了哪些文件
- `services/workshop/workshop/docker.go`：`confirmCleanup` 里 `ps` 失败不再继续走 `inspect`，改成立刻 `return false, err`（失败即关闭）；`ps` 成功且为空才判 gone，成功且非空才走 `inspect`。
- `services/workshop/workshop/docker_test.go`：`TestDockerCleanupFailsClosedWhenConfirmationTransportFails` 的两个子用例（ps/inspect）和 `TestDockerCleanupFailsClosedOnLabelMismatchWithoutSendingRm` 都把假容器的 `State.Status` 设成 `"exited"`（其余字段零值已经是 false），让失败即关闭只能来自被测的那个原因（传输失败/标签不符），不能来自零值状态本身。
- `doc/workshop.md`：恢复被误删的 `## Docker 与 host 的 shell 工具` 标题，清掉多出来的空行。

### 3. 每条测试命令和真实退出码
- `cd services/workshop && go build ./... && go vet ./...` → 两条都 exit 0
- `go test -v -race -run 'TestDockerCleanup|TestDockerSweep|TestDockerRunCheck' -count=1 ./workshop/` → exit 0，全部 PASS
- 破坏验证（工头点名要求，已写进证据）：
  - 临时把 `confirmCleanup` 改回"`ps` 出错仍继续 `inspect`"（旧 bug）：`TestDockerCleanupFailsClosedWhenConfirmationTransportFails/ps` 判红（`expected fail-closed, got container confirmed stopped; removal deferred`）；恢复后重跑判绿。
  - 临时删掉 `confirmCleanup` 里的 owner 标签校验循环：`TestDockerCleanupFailsClosedOnLabelMismatchWithoutSendingRm` 判红（同样报 `errCleanupDeferred` 而不是失败即关闭）；恢复后重跑判绿。
  - 复核 d（`TestDockerCleanupFailsClosedWhenStillRunning`）、g（`TestDockerCleanupFailsClosedWhenPendingSetFull`）：d 靠 `Running=true` 判红，与 `Status` 是否零值无关；g 的假容器本来就是 `Status:"exited"`，两条都没有工头点的那个问题，未改动。
- 真容器测试重跑（本次改动不影响它，确认未被破坏）：`EASYGO_DOCKER_TEST_BINARY=... EASYGO_DOCKER_TEST_ENDPOINT=... EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:acl-workshop go test -run TestAcceptanceDockerCleanupResilienceRealContainer -v -count=1 ./workshop/` → exit 0
- 全量（跑前确认无人在跑全量）：`cd services/workshop && go test -race ./... && go vet ./...` → 两条都 exit 0

### 4. 证据路径
全部在 `/tmp/crew/acl-530e/workshop2/`：
- `w5-rework-build-vet.log`
- `w5-rework-destructive-verification.log`（两处破坏验证的判红/判绿对照）
- `w5-rework-full-race.log`
- `w5-rework-vet.log`

### 5. 偏差与没做的事
- 三处返工按工头指出的原样改，没有顺手改别的。
- 没有单独给测试 e 补"确认阶段 inspect 失败也用 exited 状态"以外的新场景；`inspect` 子用例本来就是靠 `inspectFails` 让 `inspect` 调用本身报错，`Status` 设成 exited 只是排除零值歧义，逻辑不变。

### 6. 终审建议重点看
- `confirmCleanup` 现在的分支顺序：`ps` 出错→直接失败即关闭；`ps` 成功且空→gone；`ps` 成功且非空→`inspect`。确认没有第四种分支被漏掉。
- 两处破坏验证的日志（`w5-rework-destructive-verification.log`）是否真的证明了"测试名说的原因"而不是别的偶然原因。
- `doc/workshop.md` 的 diff 是否干净地只加回了标题（已用 `git diff be7dac0 -- doc/workshop.md` 自查过，W-4 原文一字未动）。

---

## 2026-09-29 W-5 报审：容器回收在高负载下不再让整个工坊停摆

单号：W-5 · 提交：`1e1e78ca5afe4a94a44f016dc5211ccc69539f6a`（分支 `feat/acl-workshop-w5`，基线 `be7dac0`）

### 1. 单号与提交号
W-5，commit `1e1e78ca5afe4a94a44f016dc5211ccc69539f6a`。

### 2. 改了哪些文件
- `services/workshop/workshop/docker.go`：`cleanup` 改为有界重试（3 次，各自独立 20s 上下文，退避 1s/2s）；新增 `confirmCleanup`（重试仍失败后的一次独立 20s 状态确认）、`deferCleanup`/`clearPendingCleanup`（受 `mu` 保护的待回收集合，上限 32）、`sweepPendingCleanup`（准入时的尽力清扫，`sweeping` 标志位保证同一时刻只有一个）、哨兵错误 `errCleanupDeferred`；`Run` 在通过 `initialized`/`cleanupFailure` 检查后调用一次 `sweepPendingCleanup`，其延迟回收 defer 对 `errCleanupDeferred` 不再追加 "admission disabled"；`Initialize` 探针容器的 defer 对 `errCleanupDeferred` 不再 `errors.Join` 进初始化错误。
- `services/workshop/workshop/acceptance_docker.go`：`runCheck` 签名新增返回值 `duration time.Duration`（只计"创建到 `start --attach` 返回"）；准入处加一次 `sweepPendingCleanup`；延迟回收 defer 对 `errCleanupDeferred` 保留原有退出码/`timedOut`/`err`，不覆盖。
- `services/workshop/workshop/acceptance.go`：仅改 170-173 行附近的计时——去掉 `startedAt`/`time.Since`，直接用 `runCheck` 新返回的 `duration`。
- `doc/workshop.md`：新增"容器回收在高负载下的韧性"一段，讲重试/确认/待回收/清扫和 `errCleanupDeferred` 的语义边界。
- `services/workshop/workshop/docker_test.go`：扩展 `fakeContainer`/`fakeDocker`（`State` 字段、`rmFailReal`/`rmAmbiguousOnce`/`psFails`/`inspectFails`）；新增测试 a-k 全部覆盖（见第 6 节）。
- `services/workshop/workshop/acceptance_integration_test.go`：新增真容器测试 `TestAcceptanceDockerCleanupResilienceRealContainer`。

### 3. 每条测试命令和真实退出码
- `cd services/workshop && go build ./...` → exit 0
- `cd services/workshop && go vet ./...` → exit 0
- `cd services/workshop && go test -v -run 'TestDockerCleanup|TestDockerSweep|TestDockerRunCheck' -race ./workshop/` → exit 0（全部 PASS，含 a-k 对应用例）
- 真容器：`EASYGO_DOCKER_TEST_BINARY=/tmp/easygo-platform-docker/docker/docker EASYGO_DOCKER_TEST_ENDPOINT=unix:///tmp/easygo-platform-docker/docker.sock EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:acl-workshop go test -run TestAcceptanceDockerCleanupResilienceRealContainer -v -count=1 ./workshop/` → exit 0（复用了 workshop 席之前已构建的夹具镜像 `easygo-sandbox-fixture:acl-workshop`，未新建镜像，未新增磁盘占用；跑完已删测试产生的 state 目录）
- 回退验证（测试项 4）：临时把 `acceptance_docker.go` 里 `errCleanupDeferred` 判断改回"任何 cleanup 失败都覆盖 err"，`TestDockerRunCheckPreservesResultWhenCleanupDeferred`/`TestDockerRunCheckPreservesTimeoutWhenCleanupDeferred`（对应测试项 h/i）判红；恢复后重跑判绿。已恢复，`git status` 干净。
- 全量（跑前用 `ps -eo args | grep -c '[g]o test -race'` 确认无人在跑全量，为 0）：`cd services/workshop && go test -race ./... && go vet ./...` → 两条都 exit 0

### 4. 证据路径
全部在 `/tmp/crew/acl-530e/workshop2/`：
- `w5-build-vet.log`：build + vet
- `w5-unit-tests.log`：a-k 对应的 -race 单测
- `w5-real-container.log`：真容器测试
- `w5-rollback-verification.log`：回退验证前后对照
- `w5-full-race.log`：全量 `go test -race ./...`
- `w5-vet.log`：全量 `go vet ./...`

### 5. 偏差与没做的事
- 施工图测试项 a-g、k 用直接构造 `fakeContainer` + 调用 `r.cleanup`/`r.remove` 的写法（不经过 `Run`），比经过完整 `Run` 流程更直接，测试名与施工图字母不是一一对应，但逐条核对内容都覆盖到了：
  - a→`TestDockerCleanupRetrySucceedsOnThirdAttempt`
  - b→`TestDockerCleanupTreatsAmbiguousRmAsSuccessOnNextEmptyPS`
  - c→`TestDockerCleanupDefersConfirmedStoppedContainerUntilSweep`（含"下一次准入清扫成功，集合变空"）
  - d→`TestDockerCleanupFailsClosedWhenStillRunning`（含"下一次 Run 被拒"）
  - e→`TestDockerCleanupFailsClosedWhenConfirmationTransportFails`（ps/inspect 两个子用例）
  - f→`TestDockerCleanupFailsClosedOnLabelMismatchWithoutSendingRm`
  - g→`TestDockerCleanupFailsClosedWhenPendingSetFull`
  - h→`TestDockerRunCheckPreservesResultWhenCleanupDeferred`
  - i→`TestDockerRunCheckPreservesTimeoutWhenCleanupDeferred`
  - j→`TestDockerRunCheckStillErrorsWhenCleanupFailsClosedDespiteZeroExit`
  - k→`TestDockerSweepPendingCleanupOnlyOneAtATime`
- 测试项 e 的"下一次 runCheck 被拒"没有单独写断言（只验证了 d 场景下"下一次 Run 被拒"），因为 `Run`/`runCheck` 对 `cleanupFailure` 的拒绝逻辑是同一段（读同一个 `r.cleanupFailure`），d 已经证明了该分支生效，e 只是走到同一分支的另一条路径，判断为不需要重复断言，如工头认为需要补一条可以再加。
- 未改 W-1 到 W-4 的其它行为；未顺手修复无关问题。
- `cleanupRetryBackoff`、`finalQuotaTimeout` 一样是测试seam字段，生产路径零值时保持 1s/2s 退避不变。

### 6. 终审建议重点看
- `docker.go` 里 `cleanup`/`confirmCleanup`/`deferCleanup`/`sweepPendingCleanup` 的状态机分支，尤其"确认阶段"判定"已停止、待回收"的四个条件（`ps` 为空 或 `inspect` 恰好一个对象+owner三标签全匹配+`Running/Paused/Restarting=false`+`Status` 属于允许集合）与"其它任何情况都失败即关闭"的边界，是否有遗漏的中间状态。
- `Run`/`runCheck`/`Initialize` 三处对 `errCleanupDeferred` 的特殊处理是否真的做到"不改变已有结果、不新增副作用"（对照测试 h/i/j 和真容器测试）。
- `acceptance.go` 的改动是否真的只在计时那两行，没有牵动其它逻辑。
- 待回收集合上限 32 这个数字是否需要做成配置项（施工图没要求，按施工图原样实现的是硬编码常量）。
