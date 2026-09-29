# 交接 · acl-review-530e

席位：常驻终审 R1。只向工头 `acl-foreman-530e` 汇报，不联系 `acl-review2-530e`。审查工作区 `/home/ubuntu/Projects/easygo-acl-review`。工头 2026-09-29 说 11 笔终审都已通过，集成回归 `51dadc9` 全绿，真实模型复测通过，已签字，班子解散。本席没有自己重跑集成回归，也没有跑真实模型。

## 1. 在做哪张单

没有正在做的单。队列已空。最后一张是 #11 W-7（诊断截断不留 secret 前缀，并入 W-5 测试 d），已审完并通知工头。

本席名下审过的是：#1 W-3、#2 L-1、#3 L-2、旧简令上的 W-1 复审、#7 L-3、#6 W-3b、#11 W-7。W-1 的正式终审、W-2、W-4、W-5、W-6 是 R2 的，本席没有重审。

## 2. 做到哪一步

工作区现在是 detached HEAD `021326eee7688fbaa03ad33113c3d21ac4ea4d65`，`git status --short` 为空。这是 W-7 的提交，不是集成提交 `51dadc9`。没有提交，没有 push。

| 单 | 提交 | 结论 | 报告 | 本席最后一次自测 |
|---|---|---|---|---|
| W-3 | `eb46347605a4096c0907683f35153baea6cd18b4` | 通过，有一条 P2（后由 W-3b 关闭） | `board/review-w3-eb46347.md` | `services/workshop` 下 `go test -race ./...` 与 `go vet ./...` 退出码 0。证据 `/tmp/crew/acl-530e/review/eb46347/` |
| L-1 | `78d893f7558d70c039a9ea4e9c0f76a9f64a87ba` | 通过，P3 一条 | `board/review-l1-78d893f.md` | `npm test` 115/115 退出码 0。证据 `/tmp/crew/acl-530e/review/78d893f/` |
| L-2 | `76530b2a0989a6fe5f5bb481650b94ea94ff041a` | 通过，P3 一条 | `board/review-l2-76530b2.md` | `npm test` 128/128 退出码 0。证据 `/tmp/crew/acl-530e/review/76530b2/` |
| W-1 复审 | `39863f11ae348d1d8e1fb569b3c1a14938e13e14` | 通过，P3 一条（已立为 W-7） | `board/review-w1-39863f1.md` | 探针 `W1_PREFIX full=false longest=53`。正式报告以 R2 的 `board/review-w1-570ff08.md` 为准 |
| L-3 | `efacf6de7412782bdc0662ad680cd48beed76ecb` 与 `24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da` | 通过，P0–P3 无 | `board/review-l3-24fe1ac.md` | 九场景一遍退出码 0。S3/S4/S9 改坏断言后退出码 1，脚本已复原。证据 `/tmp/crew/acl-530e/review/24fe1ac/` |
| W-3b | `3adec82aad036aee686284d093bc8666cd64c9e0` | 通过，P2 关闭，P0–P3 无 | `board/review-w3b-3adec82.md` | `-race` 下 `TestCancelExitRace` 与 `TestFinalQuotaBudgetFailure` 四支各 200 次通过。去掉包装后第 0 次判红。没有重跑全量。证据 `/tmp/crew/acl-530e/review/3adec82/` |
| W-7 | `021326eee7688fbaa03ad33113c3d21ac4ea4d65`（父提交 `c97378b`） | 通过，P3 一条 | `board/review-w7-021326e.md` | 全量 `go test -race ./... -count=1` 退出码 0，`go vet ./...` 退出码 0，各一次。证据 `/tmp/crew/acl-530e/review/021326e/` |

通知工头且 `verified=yes` 的有：L-3 `d59f0d4e-0983-45d5-ac97-803f4c4192bb`，W-3b `f330b886-be98-462b-a204-e0472fc30f01`，W-7 结果 `f6df9a22-8595-4a59-88bc-0f85ced09a28`。`verified=pending` 的按纪律视为已送达，不要重发。完整名单在 `board/worklog-review.md`，新的在上面。

## 3. 动了哪些文件

产品仓库没有留下改动。审查时临时加过的 `zz_review_*.go` 和改过的 `runner.go`、`docker.go`、`scripts/test-harness.mjs` 都已复原或删除。

写下的文件都在 `/tmp/crew/acl-530e/`：

- 报告：`board/review-w3-eb46347.md`、`review-l1-78d893f.md`、`review-l2-76530b2.md`、`review-w1-39863f1.md`、`review-l3-24fe1ac.md`、`review-w3b-3adec82.md`、`review-w7-021326e.md`
- 工作日志：`board/worklog-review.md`
- 证据：`review/eb46347/`、`review/78d893f/`、`review/76530b2/`、`review/39863f1/`、`review/24fe1ac/`、`review/3adec82/`、`review/021326e/`
- 本交接：`board/handoff-review.md`

R2 的报告（`review-w1-570ff08.md`、`review-w2-aa41e65.md`、`review-w4-be7dac0.md`、`review-w5-fd480ab.md`、`review-w6-c97378b.md`）不是本席写的。

## 4. 没来得及做的

- 没有检出 `51dadc9`，没有重跑工头的集成回归，没有跑真实模型。这两件事以工头签字为准。
- W-7 的 P3 只记录，没有改代码：相邻两段 secret 前缀只剥最后一段。探针结果是 `fedcba98`，见 `review/021326e/green.log` 的 `W7_STACKED`。
- L-1 的 P3（knowledge 参数超过 64 KiB 时 reason 变成 `tool_arguments_too_large`，仍是可恢复的 `-32602`）和 L-2 的 P3（较长 call id 拼出的 reply 幂等 key 超过契约 128 字节）都接受了，没有再开单。
- W-3b 没有重跑 workshop 全量 `-race`。L-3 没有跑第二遍全量 harness，没有跑 `npm test`，没有跑 workshop 全量 `-race`，没有重建镜像。S4 的破坏断言先撞上「必须排除 `.workshop-home`」，没有走到哈希相等那一行。
- 没有审 R2 名下的 W-2、W-4、W-5、W-6。W-1 不要用本席的 `review-w1-39863f1.md` 覆盖 R2 的 `review-w1-570ff08.md`。

## 5. 坑

1. 重命令必须走闸门，不要自己绕。命令是 `CREW_SEAT=<席位> /tmp/crew/acl-530e/bin/heavy <命令>`。算重负载的包括任何 `go test`、`go build ./...`、真容器测试、`node scripts/test-*.mjs`、`npm test`、`npm ci`、`docker build`。全班同时只放一个，`nice 15`，`GOMAXPROCS=2`。负载 1 分钟 ≥ 8、可用内存 < 3000 MB、根盘剩余 < 1200 MB 时会打印 `[heavy] hold`，这是正常等待。细则是 `board/resource-rules.md`。这台机器 4 核、15 GB、没有 swap。根盘曾掉到约 1.2 GB。闸门等超过 20 分钟再告诉工头。全量 `-race` 和真容器不要同时开。每笔全量 `-race` 只跑一次。跑完删测试 state 和 `/tmp/egh-*`。别人的容器不要删。
2. Go 不在 `PATH` 上，用 `/home/ubuntu/sdk/go/bin/go`。workshop 是 `services/workshop` 里的独立模块。测试要 `cd services/workshop` 再跑 `./workshop` 或 `./...`。在仓库根跑 `./services/workshop/workshop` 会报 module 里没有这个包。
3. 真容器用专用客户端 `/tmp/easygo-platform-docker/docker/docker`，套接字 `unix:///tmp/easygo-platform-docker/docker.sock`。环境变量是 `EASYGO_DOCKER_TEST_BINARY`、`EASYGO_DOCKER_TEST_ENDPOINT`、`EASYGO_DOCKER_TEST_IMAGE`。不设置这些变量时，集成测试会跳过，不要为此去起容器。不要 `docker build`。夹具镜像 `easygo-sandbox-fixture:acl-loop` 是 `FROM scratch`，没有 `/bin/sh`，`docker run --entrypoint /bin/sh` 会失败；要看里面的文件用 `docker create` 再 `docker cp`。Go 1.25 会把字符串比较拆开，对镜像里的二进制 `grep echo_input` 得到 0，不代表没有这个 op。
4. L-3 harness 很重，全量只跑一遍。命令是 `node scripts/test-harness.mjs --evidence <新目录> [--scenarios S1,...]`。证据目录里如果已经有 `report.json`，它会拒绝。状态目录是 `/tmp/egh-*`，跑完要确认删掉。破坏断言之后必须把 `scripts/test-harness.mjs` 复原。本席复原后的 sha256 是 `cd1fa2c17cbf6f83b35a2a88ba757d923786d5226a5ec3f0c5a3c6ce4182606a`。跑之前设 `EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go`。
5. 开跑全量 `-race` 之前用 `ps -eo args | grep -c '[g]o test -race'`。如果这条命令自己的参数里就写着 `go test -race`，计数会把当前 shell 算进去，不等于有别人在跑。
6. 用 `~/.smux/bin/herdr-msg`，先 `herdr agent get`。对方 `blocked` 就不要发。退出码 4 且 `verified=pending` 算已送达，不要重发。正文写了「不用回复」就不要回，哪怕标题要你加载 smux。只给工头发。长内容写文件，消息里给路径。
7. 审查结论只认自己跑出来的证据。施工者的 `w7-*.log` 一类只是线索。队列文件 `board/review-queue.md` 取代更早的简令。旧简令让本席审过 W-1，后来分工改了，正式 W-1 报告是 R2 那份。
8. 诊断截断在 `runner.go` 的 `redactAndBound`：先把完整 secret 换成 `[REDACTED]`，再截到 16384，然后只剥末尾一段长度 ≥ 4 的 secret 前缀。W-1 那种「开头一份完整 secret、末尾 63 字符」会留下 53 字符；W-7 已把这一段剥掉，16321 字节的普通填充还在。两条前缀挨在一起时只剥最长的一段，短的那段会留下来。3 字符的后缀是故意不剥的。stdout 超限是直接失败，不是再截一刀。
9. 最终磁盘扫描只有 `quotaCtx` 自己超时（`context.DeadlineExceeded`）才包成 `ErrDiskQuotaScanFailed`。调用方取消不会包进去。30 秒预算没有改。超额且取消仍是 `failed` / `disk_quota_exceeded`，未超额且取消仍是 `cancelled`，两种都不登记产物。
10. 这个工作区是 detached HEAD，和施工者共享对象库。不要在这里提交，不要 push，不要改 `feat/acl-workshop` 或 `feat/acl-workshop-w5`。临时测试文件放进去之后，`EXIT` trap 还没跑时 `git status` 仍会看见它。交接时工作区是干净的 `021326e`。下一个班子如果要看集成结果，自己检出 `51dadc9`，不要把当前 HEAD 当成集成分支。
