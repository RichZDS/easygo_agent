# 审查 · L-3 · 24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da

结论：**通过**。

两段都在 `feat/acl-loop-l3`。第一段 `efacf6de7412782bdc0662ad680cd48beed76ecb`（父提交 `689c503d26747dcb79e513eed6533875088331b6`，说明 `test(harness): prove P1 crew channels through real task containers`）只改 `scripts/test-harness.mjs` 和 `services/README.md`，+310/−1。第二段 `24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da`（父提交 `24d78bfec43d14f8dbbd0e59f84b53b2e425bc9f`，说明 `test(harness): verify acceptance evidence, isolation and cancellation`）仍是这两个文件，+171/−19。中间的 `24d78bf` 是合入 W-2 `aa41e65` 的 merge，workshop 实现不在这两笔自己的 diff 里。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD，停在 `24fe1ac`。没有改施工者分支，也没有提交。故意改坏断言的三次都已 `git checkout` 复原，脚本 SHA-256 回到 `cd1fa2c17cbf6f83b35a2a88ba757d923786d5226a5ec3f0c5a3c6ce4182606a`，`git status --short` 为空。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 无

## 已验证

1. **九个场景走的是模型夹具、loop 工具、工坊和容器，判定不靠助手正文。** `tools()` 先 `agent.session.create` / `agent.run.start`，把计划放进用户输入的 `harness_plan`；夹具只有在请求工具名已注册时才回 tool call，看到 tool 结果后才结束（`scripts/test-harness.mjs:98-110`，`scripts/test-harness.mjs:348-364`）。loop 对同一次回复里的工具是逐个 `await`（`services/agent-loop/src/loop.ts:161-164`），所以 S6 的 reply 先于 resume。断言读的是 `agent.session.history` 里的工具结果，以及工坊 RPC。客户端证书只授了 `health`、`workshop.get`、`workshop.events`、`workshop.evidence`（`scripts/test-harness.mjs:377`）。`workshop.get` 返回的是摘要（`services/workshop/server/server.go:95-100`）。

   本席这一遍：`model_requests` 76，`agent.run.start` 38，`agent.session.history` 38。直接打到工坊的只有 `workshop.get` 49 次、`workshop.events` 13 次、`workshop.evidence` 9 次。没有直接的 `workshop.submit`、`workshop.message`、`workshop.resume`、`workshop.cancel`。`paid_models` 为 false。九个场景都 `pass: true`，`deferred` 为空。三个服务退出码都是 0，`remaining_containers` 为空，`state_removed` 为 true。证据 `green/report.json`，`HARNESS_RC:0`。

2. **S1、S2、S6、S7、S8 对得上通道语义。** 容器里的 `crew` / `inbox` 调的是 `/usr/local/bin/easygo-crew`；`duplicate` 用同一份 body 对 crew URL 发两次，两次回执必须相同（`services/workshop/cmd/fixture-cli/crew.go:44-49`，`crew.go:116-149`）。镜像 `easygo-sandbox-fixture:acl-loop` 的 id 是 `sha256:3f430f3ef2beeae199207a6590b821101390270d771d419db873c3abbd4d9320`，和报审一致。没有重建镜像。镜像里的 `codex` 把 `echo_input`、`write-denied` 拆进比较指令里，完整字面量不连在一起；S5、S6 能通过，说明这些 op 在这只镜像里是活的。

   绿跑里 `workshop.get` 的终态：S1 `outcome=submitted`，`acceptance_state=skipped`。S2 `outcome=none`，没有 crew 消息（`scripts/test-harness.mjs:225-231`）。S6 两条 `run_ids`（`b1b66898-7a36-4dd1-9fd2-32c537c58910`、`de262a7f-2e26-495e-bb25-39e68fae3539`），`outcome=submitted`。S8 只有一条 `client_id=S8-same-id` 的事件，sequence 6，正文 `S8 exactly once`。S7 在状态已是 `running` 之后才经模型发 `workshop_reply`，再要求 inbox 输出里出现这条 id，并且 submit 的 sequence 晚于对应的 `crew.read`（`scripts/test-harness.mjs:298-309`）。

3. **S3 把自报 pass 和验收失败分开。** 任务状态 `succeeded`，`outcome=submitted`，`acceptance_state=failed`，`false_green=true`，`evidence_count=1`。证据命令是 `fixture-check file /workspace/artifact.txt good`，`exit_code=1`（`green/S3.json` 里的 `workshop.get` / `workshop.evidence`）。`falseGreen` 只在 submitted、tests=pass、验收 failed 时为真（`services/workshop/workshop/acceptance.go:68-69`）。

   把 S3 的预期从 `failed` 改成 `passed` 后，`--scenarios S3` 退出码 1。报告里的实际值仍是 `failed`，错误在 `scripts/test-harness.mjs:236`。`sab-s3/report.json` 的 `pass` 为 false，容器清干净，state 已删。脚本已复原。

4. **S4 的独立树哈希和平台一致，并且排除 `.workshop-home`。** 平台用 `treeEntries(..., pack=false)` 跳过根上的 `.workshop-home`，目录保留 `lstat` 的 size，文件和符号链接分别做 sha256，再按路径字节排序；`json.Encoder` 每条一行，默认做 HTML 转义（`services/workshop/workshop/acceptance_pack.go:46-50`，`acceptance_pack.go:142-155`）。脚本里的 Node 实现不调用这段 Go，用同样的字段顺序、排序，并把 `<>&` 和 U+2028/U+2029 转成 `\uXXXX`（`scripts/test-harness.mjs:175-194`）。

   脱离场景、在同一棵临时树上对过：文件名 `a<b&c.txt`（内容含 `<>&` 和 U+2028）、`nested/独立.txt`、指向该文件的符号链接，以及 `.workshop-home/secret`。Go `workspaceTreeHash` 和这段 Node 都得到 `8cc4401a582d982ff5b72f5bf1519b465c3a7d3d1334ef2ee040ea3defe9d4f4`。临时测试文件已从工作区删除。

   绿跑的真实工作区只有 `artifact.txt`、`nested`、`nested/proof.txt`，没有 `.workshop-home` 路径。独立哈希 `05bde4a1b37dcd8165e7344da5b5ed564fba1ba09af238a38ae8ec64b9d18163`，与证据里的 `workspace_sha256` 相同，检查 `exit_code=0`。脚本在哈希之前读到了 `.workshop-home/ignored-proof` 的内容 `excluded`（`scripts/test-harness.mjs:250-255`）。

   删掉「跳过 `.workshop-home`」之后，`--scenarios S4` 退出码 1。失败点是「每条路径都不得以 `.workshop-home` 开头」（复原前的行号 253，复原后是 254）。容器和 state 已清。脚本已复原。

5. **S5 跑的是封存后的检查程序，工作区里的伪造副本没有被执行。** 检查命令两条都是 `/pack/checks/verify`，`exit_code` 都是 0。marker 输出是 `file content matches`。isolation 输出是工作区写入被拒、pack 写入被拒、`network denied: true`、`no credentials or relay: true`。封存文件 `.../checks/verify` 前后 sha256 都是 `cee7ca49ab1aec3275681b299450e568a845ac9c85eb9b4c6e606bb00b6989cb`。检查容器把 `/pack/checks` 只读挂上，并把 entrypoint 设成命令的第一个参数（`services/workshop/workshop/acceptance_docker.go:15-26`）。`fixture-check isolation` 自己探测这两个写路径、`1.1.1.1:80`、`EASYGO_` 环境和 relay socket（`services/workshop/cmd/fixture-check/main.go:58-82`）。

6. **S9 是在检查容器已经跑起来之后取消的。** 先等到任务 `running` 且 `acceptance_state=running`，再等到名为 `easygo-check-` 的容器 `State.Running=true`，命令正好是 `/usr/local/bin/fixture-check sleep 60`（`scripts/test-harness.mjs:320-325`，容器名规则在 `acceptance_docker.go:48`）。取消走 `workshop_cancel` 工具，不走 docker kill。终态 `status=cancelled`，`acceptance_state=cancelled`，`false_green=false`，`evidence_count=1`。随后同一 owner 的检查容器为 0。上下文取消时验收状态写成 cancelled（`acceptance.go:147-150`，`acceptance.go:188-189`）。

   把终态预期从 `cancelled` 改成 `succeeded` 后，`--scenarios S9` 退出码 1。实际值是 `cancelled`，错误在 `scripts/test-harness.mjs:326`。清理仍然完成。脚本已复原。

7. **清理。** 正常结束和三次改坏断言的结束都是 `remaining_containers=[]`、`state_removed=true`。脚本先停服务，再按本次 `owner` 标签确认容器，然后只对本次 state 目录解除封存并删除（`scripts/test-harness.mjs:408-421`）。没有新建镜像，没有调用付费模型。

## 没覆盖到

- 没有重建夹具镜像，用的是已经存在的 `easygo-sandbox-fixture:acl-loop`（`3f430f3ef2be`）。
- 没有再跑 agent-loop 的 `npm test`，也没有跑 workshop 的 `go test -race`。这两笔自己的 diff 只有 harness 和 README。W-2 的实现随 merge 进来，那一笔已有单独终审。
- 全量九场景跑了一遍，没有连跑第二遍。报审里的第二遍只作对照，不代替这一遍。
- S4 那次改坏先撞上「路径不得进入 `.workshop-home`」的断言，没有再往下执行哈希相等那一行。哈希相等由绿跑里的相同 digest，以及脱离场景的那次 Go/Node 对照保住。
- 施工者对宿主机上那条 pack 绝对路径的 `write-denied`，失败原因是容器里看不到这条路径。封存没有被改掉，靠的是前后 sha256 和实际命令 `/pack/checks/verify`。

## 证据

目录：`/tmp/crew/acl-530e/review/24fe1ac/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `green-run.log` / `green/report.json` | 九场景一遍。76 次夹具请求，镜像 id 与报审一致 | 0 |
| `green/S1.json` … `green/S9.json` | 各场景 RPC 和 S4 独立清单、S5 封存哈希、S9 运行中的检查容器 | 场景均为 pass |
| `hash-go.log` / `hash-node.out` | 含 `<>&`、U+2028、符号链接和 `.workshop-home` 的同一棵树，两边哈希相同 | Go 测试 0 |
| `sab-s3-run.log` / `sab-s3/report.json` | S3 预期改成 passed，实际仍是 failed | 1 |
| `sab-s4-run.log` / `sab-s4/report.json` | 不再排除 `.workshop-home`，断言判红 | 1 |
| `sab-s9-run.log` / `sab-s9/report.json` | 终态预期改成 succeeded，实际是 cancelled | 1 |

工作区相对 `24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da` 没有已跟踪改动。
