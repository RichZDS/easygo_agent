# 交接 · acl-review2-530e（workshop 侧终审）

席位：`acl-review2-530e`。工头：`acl-foreman-530e`。工作区：`/home/ubuntu/Projects/easygo-acl-review2`（只检出，不改施工分支）。工作日志：`/tmp/crew/acl-530e/board/worklog-review2.md`。证据根：`/tmp/crew/acl-530e/review2/`。

工头 09-29 收尾令 `499896d6-c78d-4d33-958e-cf3acd05f532`：11 笔终审通过，集成回归 `51dadc9` 全绿，真实模型复测通过，工头已签字，班子解散。`51dadc9` 和真实模型复测是工头签的，本席没有重跑。

## 1. 在做哪张单

没有进行中的单。本席最后一笔是队列 #10 W-6（`c97378b`），结论通过，工头已接受那条 P3 并不修。

本席审过的只有 workshop 侧：#4 W-1、#5 W-2、#8 W-4、#9 W-5、#10 W-6。#6 W-3b 和 #11 W-7 归 R1（`acl-review-530e`），本席没有出报告。队列在解散前写明 R1、R2 都已空。

## 2. 做到哪一步

每笔都是独立检出命名提交、自己跑测试、做一次破坏验证、复原到 `git diff` 为空，再写报告并 herdr-msg 工头。施工者日志只当线索。

| 单 | 提交 | 结论 | 报告 | 通知 delivery_id |
|---|---|---|---|---|
| W-1（含 W-1b、W-1c） | `39863f11ae348d1d8e1fb569b3c1a14938e13e14`、`a2e0b505fe5dd8432f8ae1d24a0ae0268cba4d39`、`570ff08d9f71a70bd48a2e1c4d160f0473398f7f` | 通过。P3：`doc/workshop.md` 夹具表缺 `echo_input`，留给工头在集成时补 | `board/review-w1-570ff08.md` | `64ce855b-a98b-43d3-bdb7-ffb4173293de`（verified=yes） |
| W-2 | `aa41e657c65807072d66766b0d27cbde9b5bb7fa` | 通过。P3 两条，都转成 W-6：只读靠 `,dst=/workspace,` 子串；interrupted 留下旧的 false_green | `board/review-w2-aa41e65.md` | `1253e470-0d2c-405e-8e34-fab414f95132`（verified=yes） |
| W-4 | `be7dac06a3ab24551e1d749632f1951c3de917a3` | 通过。P0–P3 无 | `board/review-w4-be7dac0.md` | `d004ce92-1c75-42a1-ba0d-ced9a6250ea4`（verified=pending，peer working，未重发） |
| W-5 | `1e1e78ca5afe4a94a44f016dc5211ccc69539f6a` 与返工 `fd480ab0d686106e82ee61c567bb8628c209e485`（审 `be7dac0..fd480ab`） | 通过。P3：测试 d 没单独锁住 Running。工头并入 W-7 | `board/review-w5-fd480ab.md` | `9e3582b8-9e41-45ed-b964-4ea3cf2afa45`（verified=yes） |
| W-6 | `c97378be105c36a3429ae18a2fbf83fff8f380b8`（审 `fd480ab..c97378b`） | 通过。P3：取消子用例没锁住「把已为真的 false_green 清掉」。工头接受不修（`deaf7150-c20e-4b4f-b105-60847819ed6c`） | `board/review-w6-c97378b.md` | `9a62d1e4-8604-473d-ac6e-15924fecd49a`（verified=yes） |

W-6 收尾时工作区是 detached HEAD `c97378b`，`git diff` 为空。本交接回合没有再检出，也没有跑测试或构建。

W-6 自己取的证（都在 `/tmp/crew/acl-530e/review2/c97378b/`）：

- `unit.log`：按用例 `-race`，EXIT:0。含字段集合、重启、Close 探针、Initialize 探针。
- `red-move-dst.log` EXIT:0：把 `dst` 挪到挂载串最后，新测试仍绿。
- `red-move-dst-suffix.log` EXIT:1：同一次挪动下，旧的后缀测试判红。
- `red-drop-readonly.log` EXIT:1：去掉 `,readonly` 追加后判红。
- `red-drop-falsegreen.log` EXIT:1：去掉重算后，重启、Close、failed 判红；取消子用例不在失败列表里。
- `docker.log` EXIT:0：`TestAcceptanceDockerIntegration/pass`、`TestAcceptanceDockerCleanupResilienceRealContainer`、`TestDockerIntegration`。检查容器 `write denied /workspace/check-write: true`。readonly 模式 `workspace_readonly` 与 `workspace_write_denied` 为 true，`home_writable` 为 true。
- `race.log` TEST_EXIT:0（workshop 包 62.481 秒）。`vet.log` VET_EXIT:0。全量没有设 `EASYGO_DOCKER_TEST_*`。

更早几笔的证据目录：`review2/570ff08/`、`review2/aa41e65/`、`review2/be7dac0/`、`review2/fd480ab/`。W-1 要引用 `probe-race-2.log`（EXIT:0）。同目录的 `probe-race.log` 被后来一次已删除探针的超时盖掉了，不要当结论。

## 3. 动了哪些文件

产品仓库没有留下修改。破坏验证和审查探针都已 `git checkout` 或删除。

本席写下的文件：

- `/tmp/crew/acl-530e/board/review-w1-570ff08.md`
- `/tmp/crew/acl-530e/board/review-w2-aa41e65.md`
- `/tmp/crew/acl-530e/board/review-w4-be7dac0.md`
- `/tmp/crew/acl-530e/board/review-w5-fd480ab.md`
- `/tmp/crew/acl-530e/board/review-w6-c97378b.md`
- `/tmp/crew/acl-530e/board/worklog-review2.md`
- `/tmp/crew/acl-530e/board/handoff-review2.md`（本文件）
- `/tmp/crew/acl-530e/review2/` 下上表那些证据日志

没有写 `review-w3b-3adec82.md`，也没有写 `review-w7-021326e.md`。那两份是 R1 的，检出在 `/home/ubuntu/Projects/easygo-acl-review`。`review2/3adec82/race-quota.log` 是改派前误跑的片段，不是 W-3b 结论。

## 4. 没来得及做的

- 没有审 W-3b、W-7、L-1、L-2、L-3。W-7 的 53 字符 token 前缀和 W-5 测试 d 的 Running/Paused/Restarting 补强是 R1 关的，报告在 `review-w7-021326e.md`。
- 没有跑工头签收的集成回归 `51dadc9`，也没有做真实模型抽样。
- W-4 报告里的「没覆盖到」仍然成立：没有亲自证明 Claude 在 `--restricted --bare` 下真的跑通 Bash、Pi 的 bash 在 `--print` 里不弹确认、OpenClaw `host=gateway` 加 `agent --local` 能在容器里 exec。本席核对的是 help、schema 和参数拼装。
- W-5 没有把 daemon 真拖满 20 秒。W-6 没有在验收跑到一半时停服务，验收集成也没有重跑 bad/timeout/cancel/output。
- 已关闭的报告没有回改。

## 5. 坑

下一个班子最容易踩的是下面这些。报告里的行号是审查当时的提交，合进 `51dadc9` 之后要重新对行。

**只读挂载不要改回子串。** W-6 之前，`checkContainerOptions` 和 `taskContainerOptions` 在生成挂载串之后搜索 `,dst=/workspace,`，找到才在末尾加 `,readonly`。字段一换顺序，只读会悄悄丢掉，而旧测试锁的是整段字面量。现在 `containerOptions` 用 `workspaceReadOnly` 在构造时写上 `,readonly`。检查容器恒为 true，任务容器只在 `policy == "read-only"` 时为 true，Initialize 探针传 false，`.workshop-home` 子挂载不加 readonly。新测试 `findMount` 认的是字段集合。旧测试 `TestDockerReadOnlyWorkspaceAllowsOnlyNativeHomeWrites` 仍要求只读出现在字符串末尾：把 `dst` 挪到最后时，新测试是绿的，这支旧测试是红的。修旧测试时不要把产品改回子串匹配。

**false_green 只有一张表。** `submitted` 且 `claims.tests=pass` 且 acceptance state 为 `failed` 才是 true。`finish` 可以把 state 改成 `interrupted`，但自己不重算；必须靠紧随其后的 `finishCrewOutcome` 调用 `acceptanceFalseGreen`。`New` 的重启恢复和 `Close` 两处 interrupted 事件要带重算后的值。执行收尾和排队取消也调用 `finishCrewOutcome`，这两处本来不发 acceptance 事件。`AcceptanceEvent.FalseGreen` 是 `omitempty`：false 在 JSON 里会被省掉，按结构体解出来才是 false。不要把「事件里没有这个字段」当成「沿用上一次的 true」。W-6 那条 P3 工头已接受：取消子用例的种子本来就是 false，删掉重算它仍然绿；interrupted 和 failed 已经锁住同一个函数。不要再开一单去修这个测试，除非真值表本身改了。

**容器回收的哨兵不能盖掉结果。** W-5：`remove` 最多 3 次，每次独立 20 秒，退避 1 秒、2 秒。三次都失败才 `confirmCleanup`。`ps` 出错必须立刻失败关闭，不能继续 `inspect`。只有确认已停止（Running、Paused、Restarting 都为 false，且 status 属于 created/exited/dead/removing，owner 标签全匹配）才返回 `errCleanupDeferred` 并放进待回收集合，上限 32。这个哨兵在 `runCheck` 里要保留退出码、超时和原错误；在 `Run` 里不要追加 admission disabled；在 Initialize 探针里不要 join 进初始化错误。其它不清楚的状态要置位 `cleanupFailure`，之后 `Run` 和 `runCheck` 都拒绝。`sweepPendingCleanup` 失败不阻挡准入，也不置位。`duration_ms` 不含回收。W-5 真容器测试的 `t.Cleanup` 只用本测试 owner 标签、走原始 docker CLI，在 `s.Close()` 之后。删容器之前先看 owner。`easygo-platform-r3-*` 是别人的，本席核对过它们还在，不要 prune。

**Docker shell 只加在容器路径。** W-4：`dockerRuntimeArgs` 只从 `DockerRunner.Run` 走进去。Claude 追加 `Bash` 并加 `--allowedTools Bash`，保留 `--bare`、`--restricted`、`--strict-mcp-config`。Pi 追加的工具名是 `bash`（`--no-approve` 不是自动批准命令）。OpenClaw 追加 `exec`，并设 `tools.exec.host=gateway`、`mode=full`。host 的 `CommandRunner.configureRuntime` 不走这套。工作区只读仍靠上面的构造参数，开了 shell 也不许把只读拿掉。运行时镜像 `easygo-task-runtime:platform` 当时测得的 id 是 `sha256:8cc696a4b2b8b5ab1097a556a63a8f032c03657c47acfad66a1d49ebb96e9ff8`，大约 2 GB。不要重建它。夹具镜像是 `easygo-sandbox-fixture:acl-workshop`，id `sha256:4eefe6d2c30a665b7299c73282c0fdd24c34bb147252dab84c4dda22e181b258`。专用 daemon 是 `unix:///tmp/easygo-platform-docker/docker.sock`，客户端是 `/tmp/easygo-platform-docker/docker/docker`。

**诊断截断和 token。** crew token 是 32 字节 hex。`Authorization: Bearer` 和 `X-Api-Key` 都可以，非空的 `X-Api-Key` 优先。token 不应进输出、错误或事件。R1 在 W-1 上找到过截断错误能留下 53 字符前缀，W-7（`021326e`，R1 审）修了。W-7 还有一条工头接受不修的 P3：相邻两段前缀只剥掉最后一段，记在 followups F-6。不要在新的日志路径里先截断再替换。

**提交说明会撒谎。** `c97378b` 的说明写「又加强了两支 W-5 测试」。那两支 confirmCleanup 的 Status 修正在父提交 `fd480ab`。这笔 diff 只加了挂载字段集合测试和 t.Cleanup。以 `git diff <parent>..<sha>` 为准。

**资源和命令。** 重负载走 `CREW_SEAT=<席位> /tmp/crew/acl-530e/bin/heavy`。包括任何 `go test`、真容器、`docker build`。闸门在负载、内存或磁盘不够时打印 `[heavy] hold`，那是正常等待。全量 `-race` 和真容器分开跑。每笔全量只跑一次。主机当时约 4 核、15 GB、无 swap，根盘只剩约 1.7 GB。Go 不在 PATH 上，用 `/home/ubuntu/sdk/go/bin/go`。队列里那句 `ps -eo args | grep -c '[g]o test -race'` 会把自己的命令行算进去。要看有没有别人在跑，用进程名：`ps -eo comm,args | awk '$1=="go" && index($0,"test")'`。

**审查工作区不要和 R1 搞混。** 本席是 `easygo-acl-review2`，日志是 `worklog-review2.md`。R1 是 `easygo-acl-review` 和 `worklog-review.md`。复原文件用 `git -C /home/ubuntu/Projects/easygo-acl-review2 checkout -- <从仓库根开始的路径>`。先 `cd services/workshop` 再 `git checkout -- services/workshop/...` 会匹配不到，破坏会留在树上。`go test | tee` 不加 `pipefail` 时，外壳退出码是 tee 的，测试失败会被看成成功。看日志里的 `EXIT:` 或 `TEST_EXIT:`。

**herdr。** 只给工头发消息。`verified=pending` 且带 delivery_id（对方正在 working）算送到，不要重发。对方 `blocked` 就不要发。长内容写文件，消息只留一行指向。
