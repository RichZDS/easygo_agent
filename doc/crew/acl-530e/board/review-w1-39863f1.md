# 审查 · W-1 · 39863f11ae348d1d8e1fb569b3c1a14938e13e14

结论：**通过**。

提交在 `feat/acl-workshop`，父提交正好是 W-3 `eb46347605a4096c0907683f35153baea6cd18b4`，说明是 `feat(workshop): add scoped crew messaging, worker reports and CLI`，时间 `2026-09-29 12:26:13 +0800`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD。diff 共 28 个文件，+1691/−62。没有改施工者分支，也没有提交。探针是未跟踪文件，已从工作区删除。`git status --short` 为空，HEAD 仍是 `39863f11ae348d1d8e1fb569b3c1a14938e13e14`。

`packs/base/roles/worker.md:17-19` 写了问过之后再发 `blocked`。这是工头施工图自身的矛盾，W-1b 另派修正，本笔不记为问题。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 诊断事件在截断窗口里可以留下 token 的 53 字符前缀

`runNative` 先把 stderr 收进 `diagnosticLimit + maxSecretLength` 的缓冲，再整段替换 secret，最后截到 `diagnosticLimit`（`runner.go:22`，`runner.go:177-185`，`runner.go:237-244`）。secret 是 64 个十六进制字符。缓冲上限是 16384+64=16448 字节。

探针构造的 stderr 正好填满这个上限：开头是完整 secret，末尾是同一 secret 的前 63 个字符。开头那份被换成 `[REDACTED]`（10 个字符），缓冲缩短 54 字节，截断再丢掉末尾 10 字节。第二份因此留下 53 个字符。`probe-race.log` 打印 `W1_PREFIX full=false longest=53`。完整的 64 字符不在事件 JSON 里，断言没有失败。

这条红actor 和「进程错误改写成 `engine process failed (see bounded diagnostic events)`」在父提交 `eb46347` 的 `runner.go` 里已经存在。Docker 路径当时就把同一个 relay key 传给 `runNative`（父提交 `docker.go` 的 `[]string{key}`）。本提交把这把 key 同时放进 `EASYGO_CREW_TOKEN`，没有第二把秘密。子进程本来就拿得到完整 token。`ConstantTimeCompare` 要的是整段 64 字符，53 字符对不上。普通路径（stderr 上单独一份 token、模型正文里的 token、`docker create` 的错误、CLI 的 stdout/stderr、host 进程输出、平台写进工作区的文件和 argv）都没有留下完整 token，也没有留下这段前缀。

复现：`/tmp/crew/acl-530e/review/39863f1/probe_test.go` 的 `probeTokenAndEnv`。`probe-race.log` 同时打印 `W1_PROBE_OK`，`PROBE:0`。

## 已验证

1. **旧 run 的 token 写不了新 run，也进不了新 relay。** 每次 `startModelRelayOn` 读 32 字节再编成 hex（`relay.go:45-50`）。处理函数在路由之前做 `ConstantTimeCompare`，Bearer 和 `X-Api-Key` 都走这一次比较，失败是固定的 `unauthorized` / 401（`relay.go:61-68`）。比较通过之后，路径以 `/crew/` 开头才进 `serveCrew`；这次监听没有 CrewChannel 时是 404（`relay.go:69-75`）。`serveCrew` 只接受 POST、空 query、且路径正好是 `/crew/messages` 或 `/crew/inbox`，否则 404（`crew_relay.go:12-15`）。错误正文是 `invalid crew request`、`invalid crew message`、`claims only allowed for submit`、`invalid inbox request`、`crew request rejected`，状态码按 `ErrInvalid` 400、`ErrConflict` 409、`ErrFull` 429、其余 503（`crew_relay.go:46-47`，`crew.go:328-338`）。

   通道绑在当次执行的 run 上：`execute` 构造 `runCrew{..., runID: run.ID}`（`service.go:633`），Docker 把它传进 `startModelRelayOn`（`docker.go:465`）。`Post` / `Inbox` 要求任务仍是 `Running`，并且最新一条 run 的 ID 等于通道上的 runID，否则 `ErrConflict`（`crew.go:115-123`）。resume 之后最新 run 变了，旧通道即使监听还在，也是 409。新监听有新 token，旧 token 过不了比较。没有把旧通道改绑到新 runID 的路径。

   探针 `old_token`：token1 在 relay1 上 POST 200；任务结束后同一 token 409，from_worker 仍是 1 条。Resume 后 relay2 的 token2 与 token1 不同，长度都是 64。token1 打 relay2 的 `/crew/messages` 和 `/v1/responses` 都是 401，网关调用次数保持 0；`X-Api-Key: token1` 也是 401。token1 打还活着的 relay1 是 409；token2 打 relay1 是 401。响应正文不含两把 token。带正确 token 的 query string 是 404。随后 token2 发 `blocked` 得到 200，条数变为 2，outcome 为 `blocked`。证据 `probe-race.log` 的 `TestW1ReviewProbe/old_token`。

2. **`client_id` 幂等和 200 条上限在同一把锁、同一次 bolt 更新里。** `Post` 持有 `s.mu`，里面一次 `db.Update`（`crew.go:125-170`）。计数只算本 run、`from_worker` 的消息。相同 `client_id` 且 JSON 相等时，在上限判断之前返回已存储的 id 和 sequence，所以第 200 条仍可重放。相同 id、内容不同是 `ErrConflict`，不插入。新 id 且计数已经 ≥200 是 `ErrFull`。比较时 `json.Marshal` 的错误被丢掉（`crew.go:156`）；两边都是普通字符串和 map，探针里没有出现编不出的内容。

   探针 `cap_is_transactional`：先写入 180 条。对已有的 `p0` 改内容得到 `ErrConflict`，条数不变。接着 40 个不同 `client_id` 并发，正好 20 个成功、20 个 `ErrFull`，条数停在 200。重放第一条仍返回原回执。再来一个新 id 是 `ErrFull`。

3. **resume 拼接未读消息和 `crew.read` 同一次提交。** `Resume` 在 `s.mu` 下先拒绝 `len(task.Runs) >= 256`（`service.go:488`）。Docker 扫描会暂时放锁，回来重新读取，并再次拒绝 256（`service.go:526`），同时核对状态、工作区、session 和 run 条数（`service.go:529-531`）。然后在内存里追加新 run，一次 `db.Update` 里依次做 `resumeCrewInput`、`putTask`、以及 `state=queued` 事件（`service.go:538-548`）。更新失败会归还 slot，任务不会入队（`service.go:549-551`）。

   `resumeCrewInput` 把全部未读接到新 run 的 Input 末尾，格式是 `\n\nUnread messages from the foreman:\n- [id] text`，并写一条 `crew.read`，这里没有 inbox 的 50 条上限（`crew.go:288-303`）。未读的定义是 `to_worker` 且 id 不在任何已有 `crew.read` 里（`crew.go:266-286`）。`appendEvent` 自己不写 task，只给事件盖上 sequence、时间和当前最后一条 run 的 ID（`service.go:371-387`）。新 run 已经在内存切片里，所以这条 `crew.read` 的 `run_id` 是新 run。task 和事件一起提交或一起回滚。

   `Inbox` 同样是一把锁加一次更新：`sequence > after` 且 `to_worker`，最多 50 条；id 列表非空才写一条 `crew.read`（`crew.go:172-210`）。过滤按序号，不按已读。同一次 HTTP 丢了响应、用相同 `after` 再读，会再拿到同样的消息，并再写一条 `crew.read`，不会多出一条 `from_worker`。这和契约的序号语义一致，不记问题。客户端自己把 `after` 往前推之后崩溃，那些消息已经标成已读，resume 不会再拼，契约也是这样写的。

   探针 `resume_read_and_run_ids`：51 条 `workshop.message`。inbox 第一页 50 条。resume 只拼上第 51 条，正文里没有第 1 条的 id，也没有 `Acceptance checks`。新 run 的 `crew.read` 排在该 run 的 `state=queued` 之前。第二次 resume 的 input 正好是 `third`，没有 `Unread`。`Summary.RunIDs` 是 `[id0,id1,id2]`。运行中的 summary JSON 没有 `"outcome"`，`acceptance_state` 为 `skipped`。`ListPage` JSON 没有 `run_ids`，也没有 `"outcome"`，占位是 skipped / false / 0。结束后 outcome 为 `none`，`run_ids` 不变，`crew.read` 共 2 条。

   探针 `rejected_resume_does_not_splice`：终态之后先留一条 note，再在锁内把 runs 填到 256。`Resume` 返回 `ErrRunLimit`，长度仍是 256，状态仍是 `Succeeded`，没有任何 run 的 input 含这条 note 的 id 或 `do-not-apply`，事件条数不变。`Summary` 的 `run_ids` 长度 256，顺序与切片一致。`ErrRunLimit` 是包了 `ErrConflict` 的错误（`types.go:17`）。RPC 在 `ErrConflict` 之前把它映射成 `-32009` / `run_limit`（`server.go:210-213`）。`server-race.log` 里 `TestRunLimitDomainError` 通过。

4. **`run_ids` 按追加顺序，只出现在 get。** `summarize` 按 `task.Runs` 的切片顺序填 `TaskSummary.RunIDs`，只把最新一条放进 `Runs`（`views.go:108-132`）。`ListPage` 用的 `TaskMetadata` 没有 `run_ids` 字段（`views.go:59-68`，`views.go:145-176`）。`Outcome` 的 JSON 标签是 `omitempty`（`types.go:105`，`views.go:41`）。结束时先把 outcome 写成 `none`，再由 `finishCrewOutcome` 按该 run 最后一条匹配的 `from_worker` 覆盖：`submit` → `submitted`，`blocked` → `blocked`，`ask` → `asked`，`report` 跳过（`crew.go:305-327`）。四个收尾点都调用了它：执行结束（`service.go:681`）、排队中取消（`service.go:454`）、启动恢复（`service.go:218`）、`Close`（`service.go:765`）。运行中 outcome 仍是空，序列化时省掉。验收字段在本提交固定为 `skipped`，`false_green` 和 `evidence_count` 为零值。契约 §5 属于 W-2，本笔不审验收计算。

5. **完整 token 不进平台自己的输出、错误和事件。** Docker 只要拉起 relay，就设置 `EASYGO_CREW_URL=http://127.0.0.1:18080/crew` 和 `EASYGO_CREW_TOKEN=key`（`docker.go:474-475`），这把 key 同时是 `EASYGO_RUNTIME_API_KEY`，并作为 `[]string{key}` 交给 `runNative`（`docker.go:555`）。`DockerRunner.output` 失败时只返回 `Docker operation failed: ` 加上 `args[0]`（`docker.go:222-226`），create 错误带不走 `--env` 里的 token。docker 客户端进程的环境是固定的 `PATH` / `HOME` / `DOCKER_CONFIG`（`docker.go:206-209`）。

   Host 只在 `GatewayModel != ""` 时启动 relay，并写入同样两个变量，返回的 secrets 是 `[]string{key}`（`runtime_config.go:55-62`，`runtime_config.go:74`）。`RuntimeSpec` 为空，或者走静态 API key，都不注入 crew 环境。`EASYGO_CREW_` 前缀在保留环境里（`runtime_config.go:36-37`）。

   `runNative` 先替换再截断。子进程返回的错误被丢掉，换成固定句子（`runner.go:200-201`）。`easygo-crew` 的失败行和成功 stdout 都把 token 换成 `[REDACTED]`（`cmd/easygo-crew/main.go:23`，`main.go:105`）。URL 必须是 `http`，不能有 userinfo、query、fragment（`main.go:30-32`）。非 200 不重试，只打印状态码。网络错误不打印响应体。`fixture-cli` 的 crew 输出同样替换 `EASYGO_CREW_TOKEN`（`cmd/fixture-cli/crew.go:31`）。`task-shim` 听 `127.0.0.1:18080`，把所有路径（含 `/crew`）反代到 `EASYGO_RELAY_SOCKET`，这个值必须是 `/run/easygo-relay/model.sock`；失败正文是 `model relay unavailable`（`cmd/task-shim/main.go:39-56`）。子进程继承 `os.Environ()`，里面有 token；平台侧红actor 盖住 stdout/stderr。子进程自己把 token 写进工作区文件，是子进程的行为，不记成平台泄漏。

   探针 `token_redaction_and_env`：stderr 和模型 `agent_message` 里的 64 字符 secret，结果正文是 `[REDACTED]`，事件、结果和错误的 JSON 不含 secret。`execute` 返回 `boom ` 加 secret 时，对外错误是 `engine process failed...`，事件里没有 secret。`output("create", "--env", "EASYGO_CREW_TOKEN="+secret)` 的错误正好是 `Docker operation failed: create`。Host 用一个名叫 `codex` 的脚本引擎：子进程看到的 URL 以 `/crew` 结尾，token 长度为 64；平台事件、结果和错误不含 token，工作区遍历不含 token。`GatewayModel` 为空、静态 key 为 `static-secret-value` 时，两个 crew 环境变量都是空，secrets 只有那把静态 key。

6. **Docker 和 host 的 env 注入分叉与上面一致，并且真容器跑通了。** 作者测试 `TestDockerRunUsesUDSOnly`、`TestCrewHostEnvironmentAndPackInvocation` 在第一次 workshop race 的 `-run` 里，包退出码 0（`workshop-race.log`）。那次没有设置 `EASYGO_DOCKER_TEST_*`。随后单独跑 `TestCrewDockerIntegration`，镜像是现场标签 `easygo-sandbox-fixture:acl-workshop`，id `sha256:4eefe6d2c30a665b7299c73282c0fdd24c34bb147252dab84c4dda22e181b258`，创建于 `2026-09-29T04:34:15Z`。日志有 report、ask、一次重复 `client_id` 的 report、工头 note、对应 `crew.read`、带 `claims.tests=pass` 的 submit，以及 `real container: report/ask/submit, duplicate receipt, live foreman inbox, submitted outcome, artifact, cleanup verified`。`CONTAINER:0`。跑完后按 `easygo.workshop.owner` 过滤，没有残留容器。没有重建镜像。

   工人说明从 `PackDir` 经 `os.Root` 读 `roles/worker.md`，缺文件得到空串，普通文件、UTF-8、最多 16 KiB（`pack.go:11-36`）。`New` 在启动时加载（`service.go:119`），`execute` 放进 `WorkerInstructions`。提示是工人说明、空行、workflow、空行、`User input:\n`、input（`pack.go:38-44`）。本提交的提示里没有 acceptance-check 块。探针核对了 `worker\n\nworkflow\n\nUser input:\ninput` 这个精确字符串。

7. **`easygo-crew` 的重试用同一份 body，不会多写一条消息。** body 在重试循环之前编一次（`main.go:66-68`），注释写明每次网络重试共用同一个 `client_id`。循环是 `attempt < 4`，也就是 1 次加上最多 3 次重试（`main.go:68`）。新进程会生成新的 `client_id`。作者测试 `TestCrewRetriesSameClientID` 在 `crew-cli-race.log` 里，包退出码 0。

   探针 `cli_retry_against_real_post`：代理先把请求转到真 relay，`Post` 已经提交，再 Hijack 并关掉连接；第二次正常转发。客户端看到两次尝试、同一个 `client_id`，stdout 含已存储的 message id，stdout 和 stderr 都不含 token，库里正好一条 `from_worker`。真容器里重复的 `client_id=dedup` 也只留下一条 report。

`go vet ./workshop/ ./server/ ./cmd/easygo-crew/ ./cmd/fixture-cli/ ./cmd/task-shim/` 无输出，`vet.log` 为 `VET:0`。

## 没覆盖到

- 没有重建完整运行时镜像。容器测试用的是已经存在的 fixture 标签。施工者工作日志里的镜像 id `a65b69e52f90` 已经不在这个标签上。现场 id 是上面的 `4eefe6d2c30a`。从该镜像抽出的 `/usr/local/bin/codex` 里没有 `echo_input`，所以不是后来的 W-1c。抽出的二进制已删除。集成测试连的是本检出的 workshop 服务。
- 没有跑整个模块的 `go test -race ./...`。磁盘余量当时大约 1.3G。跑过的是 workshop 包里点名的测试、`TestRunLimitDomainError`、`easygo-crew`，以及单独的容器集成。
- Docker 扫描放锁之后再查 256 的那段（`service.go:507-531`）是读过的。探针把 256 填在锁内、扫描之前的那次拒绝上，没有用一次很慢的扫描去撞第二次检查。
- 没有调用付费模型。工头 note 的条数在契约里不设上限，因为 resume 要拼上全部未读；没有记成缺陷。`json.Marshal` 比较失败被忽略，见上面第 2 条，没有单独开问题。

## 证据

目录：`/tmp/crew/acl-530e/review/39863f1/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `workshop-race.log` | `-race`，`-run` 含探针、`TestCrew`、`TestDockerRunUsesUDSOnly`、`TestWorkerPack`。未设 docker 测试环境 | 0 |
| `probe-race.log` | `-race -v -run TestW1ReviewProbe$`。`W1_PREFIX full=false longest=53`，`W1_PROBE_OK` | 0 |
| `probe_test.go` | 上面六个子测试的源码 | — |
| `server-race.log` | `-race -run TestRunLimitDomainError` | 0 |
| `crew-cli-race.log` | `-race ./cmd/easygo-crew/` | 0 |
| `vet.log` | `go vet` 上述五个包 | 0 |
| `container.log` | `TestCrewDockerIntegration`，镜像 `easygo-sandbox-fixture:acl-workshop` | 0 |

工作区相对 `39863f11ae348d1d8e1fb569b3c1a14938e13e14` 没有已跟踪改动，也没有未跟踪的探针。
