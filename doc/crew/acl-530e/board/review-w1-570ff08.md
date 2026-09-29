# 审查 · W-1 · 570ff08d9f71a70bd48a2e1c4d160f0473398f7f

结论：**通过**。

队列 #4，交付 W-1 + W-1b + W-1c。三笔提交是 `39863f1`、`a2e0b50`、`570ff08`，父链 `eb46347`（W-3，已通过）← `05b5670`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review2` 的 detached HEAD `570ff08`。没有改施工者分支，也没有提交。生产文件已复原；复现探针留在证据目录，不在仓库里。`git diff` 为空。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · `doc/workshop.md` 的夹具表没有 `echo_input`

`services/workshop/cmd/fixture-cli/crew.go:61` 有 `echo_input`。`doc/workshop.md:262-269` 的 op 表仍只有 crew、write、inbox、duplicate、silent、exit。

复现：读这张表，再对照 `crew.go` 里的 `switch`。

W-1c 点名要更新的是 `/tmp/crew/acl-530e/workshop/w1-fixture-interface.md`，那份第 16 行已经写了这个动作。仓库文档没跟着改。容器测试用这个动作通过，通道行为不受影响。

## 已验证

1. **旧 run 的 token 不能写新 run。** 探针把第一次执行的 relay 留着，再 resume 并另起一个 relay。Bearer 和 `X-Api-Key` 都能过；两个头都在时以 `X-Api-Key` 为准，错误的 key 得到 401（`relay.go:61-67`）。旧 token 打新 relay 是 401，新 token 打旧 relay 是 401。旧 relay 在 resume 之后再写是 409（`crew.go:120`，run 已不是当前 run）。空白和键序不同的同一条 JSON 得到同一张回执。两个 token 都不在事件里，也不在 relay URL 里。第一次 run 先 report 再 ask，outcome 是 `asked`。
2. **200 条上限在同一把锁、同一次 bolt 更新里。** 已有 199 条时，10 个不同 `client_id` 并行提交，结果是 1 次成功、9 次 `ErrFull`。上限处换正文是 `ErrConflict`（先对上已有 `client_id`，`crew.go:154-165`），原文重放仍成功。下一 run 可以再用 `m000`。把 `count >= 200` 临时改成 `count >= 1000000` 后，这条探针变成 `10 0 0`，施工者的 `TestCrewPostIdempotencyCapacityAndScope` 报 `201st message accepted`。随后 `git checkout -- services/workshop/workshop/crew.go`，`git diff` 为空。
3. **resume 把未读笔记和 `crew.read` 放在同一次提交里。** 配额扫描放锁期间用 `workshop.message` 写入的笔记，出现在新 run 的 Input 里，事件里有带这个 id 的 `crew.read`。施工者测试同时确认：inbox 按 `sequence > after` 返回，已经拼进 resume 的笔记用更早的游标还能再读到，并再记一条 `crew.read`（契约 §1，不是未读过滤）。
4. **resume 被接受时就记已读。** 排队、尚未开跑的 resume 已经把笔记拼进 Input。取消这次 run 之后，下一次 resume 不再拼接同一条。原 `crew.message` 还在。这和契约 §4 一致。
5. **`run_ids` 从旧到新，第 257 次拒绝。** 探针做了 255 次真实 Resume，加上首次 Submit 共 256 个 run。`workshop.get` 的 `run_ids` 与 `task.Runs` 顺序一致。再 Resume 得到 `ErrRunLimit`。`domainError` 先判断 `ErrRunLimit` 再判断 `ErrConflict`，返回 -32009、`run_limit`（`server.go:210-213`）。list 的 JSON 里没有 `run_ids`（`views.go:168-173`）。摘要里 `acceptance_state=skipped`、`false_green=false`、`evidence_count=0`，这是 W-1 留给 W-2 的默认值。
6. **token 不进 host 的参数、URL、结果和事件。** 假 codex 把环境写进证明文件：token 长度 64，参数和 URL 都不含 token，URL 以 `/crew` 结尾。stdout 把 token 放进 `agent_message`，结果文本是 `secret [REDACTED]`，发出的事件里是 `[REDACTED]`。注入点是 `runtime_config.go:61-62`，遮蔽是 `runner.go:237-243`。没有 relay 时不注入，由 `TestCrewHostEnvironmentAndPackInvocation` 覆盖，该测试在这次 `-race` 里通过。Docker 路径固定 `EASYGO_CREW_URL=http://127.0.0.1:18080/crew`，token 与 runtime key 相同（`docker.go:474-475`），并作为 `runNative` 的 secret（`docker.go:555`）。
7. **easygo-crew 的网络重试不产生第二条消息。** 代理在上游 relay 已经提交之后掐掉第一次响应。第二次用同一 `client_id` 拿到同一张回执，事件里只有一条 `from_worker`，stdout 和 stderr 都不含 token。客户端在循环外生成一次 body（`cmd/easygo-crew/main.go:66-68`），非 200 不重试。
8. **worker.md 把 ask 回执当作合法停手。** `packs/base/roles/worker.md:17-19`：停手前持有 submit、blocked 或 ask 之一的回执；ask 之后停，同一问题不再发 blocked。这是 a2e0b50 的唯一文件。
9. **W-1c 的续跑输入能被夹具解析，并回到 result。** `fixture-cli` 只解码 `User input:\n` 之后的第一个 JSON 值（`cmd/fixture-cli/main.go:39-41`）。`echo_input` 把这段原文（最多 64 KiB，UTF-8 边界）放进结果。真实容器：`TestCrewDockerIntegration` 在 `easygo-sandbox-fixture:acl-workshop` 里走完 report、ask、重复 client_id、inbox、submit，outcome `submitted`，有产物，容器已清理。`TestCrewDockerResumeInputIntegration` 的第二次 result 含回复 id 和 `Unread messages from the foreman:`。镜像是现成的 `sha256:4eefe6d2c30a`，没有重建 runtime 镜像。
10. **重启后 outcome 还在。** 运行中写入 ask，Close 后 bolt 里是 `Interrupted` 且 outcome `asked`。把状态拨回 `Running` 并清掉 outcome 再 `New`，恢复逻辑重新写成 `Interrupted` / `asked`。pack 的符号链接、目录、非法 UTF-8 都不能把包外的 `worker.md` 读进来（`pack.go:15-34`）。

## 没覆盖到

- 验收检查、`workshop.evidence`、`false_green` 真值表是 W-2。这次摘要里的 `skipped` 是 W-1 的默认值。
- 磁盘配额扫描在取消竞态里的状态归类是 W-3b。
- 全量 `go test -race ./...` 没有设置 `EASYGO_DOCKER_TEST_*`，两条容器测试在那次命令里 Skip。容器证据是另一次、不带 `-race` 的运行。
- 没有在 `docker create` 期间抓宿主机 `ps`。`docker.go:522` 把 `EASYGO_CREW_TOKEN` 放进 `--env`，和既有的 `EASYGO_RUNTIME_API_KEY` 同一条路。失败诊断只返回 `Docker operation failed: ` 加 `args[0]`（`docker.go:226`）。这次没有故意让 create 失败。
- 没有把 token 故意写进施工者消息正文。`Post` 按正文原样入库（`crew.go:167`）。平台自己的结果和诊断走 secrets 遮蔽，不改这条已提交的消息。
- `task-shim` 没有测试。它在 `cmd/task-shim/main.go:53` 把请求体放到 16 MiB；crew 的 16 KiB 在 relay（`crew_relay.go:17`）。这次没有单独打桩 shim。
- 没有跑 `services/ai-gateway`、`packages/rpc-go` 和仓库根模块。这次 diff 在 workshop、契约文档、pack 和 runtime Dockerfile。

## 证据

目录：`/tmp/crew/acl-530e/review2/570ff08/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `probe-race-2.log` | `-race` 下审查探针和施工者的 `TestCrew*`、`TestRunLimit*`、`TestWorkerPack*`、`TestCrewHost*` | 0（47.782s） |
| `red-cap.log` | 上限改成 1000000 后，容量探针和 `TestCrewPostIdempotencyCapacityAndScope` 判红 | 1 |
| `docker-integration.log` | 专用 daemon 上两条真实容器测试，镜像 `easygo-sandbox-fixture:acl-workshop` | 0 |
| `race-all.log` | 探针移出后，`cd services/workshop && go test -race -count=1 ./...`（未设 Docker 环境变量） | 0 |
| `vet.log` | `go vet ./...` | 0 |
| `crew_review2_probe_test.go` | 审查探针原文 | |
| `restored.diff` | 实验结束后的 `git diff`，空文件 | |
| `go-version.txt` | `go1.25.14 linux/amd64`，二进制 `/home/ubuntu/sdk/go/bin/go` | |

`probe-race.log` 是更早一次探针写错导致的超时，不作结论。审查命令都在 `services/workshop` 下执行。
