# L-3 第二段报审 · 2026-09-29

1. 单号与提交：L-3 验收段，`24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da`，分支 `feat/acl-loop-l3`；工作树干净、未 push。第二段实现只有这一笔 commit；第一段为 `efacf6d`。
   - 按工头简令合入 W-2 `aa41e657c65807072d66766b0d27cbde9b5bb7fa`，保留 upstream/merge 历史，未自行修改 workshop 文件。
2. 改动文件：
   - `scripts/test-harness.mjs`：补齐 S3/S4/S5/S9，默认执行全部九场景；验收输出经 loop 工具 64 字节分页并与独立工坊 RPC 全文比对。
   - `services/README.md`：九场景断言表、fixture-check/临时受信 pack 的要求、子集运行、证据与封存目录清理说明。
   - S4 用 Node 独立遍历工作区，读取文件 bytes、目录 lstat size，按 UTF-8 路径排序并编码 JSON lines 后 sha256；不调用 Go 实现。证据保存逐项 manifest，且确认 .workshop-home/ignored-proof 真实存在、未进入 hash。
   - S5 静态构建检查程序到临时 pack/checks/verify，平台封存后从 /pack/checks/verify 执行。施工者尝试改 /pack/checks/verify、controller snapshot 绝对路径、镜像里的 fixture-check，并在工作区伪造同名 checker/marker；实际检查仍读到受信 marker，隔离探测通过，前后受信程序 sha256 相等。
   - S9 等 acceptance=running 且检查容器 State.Running=true、实际 command=sleep 60 后，通过模型发 workshop_cancel，验证 task/acceptance cancelled 和无残留检查容器。
3. 检查与真实退出码：
   环境：`EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go EASYGO_DOCKER_TEST_BINARY=/tmp/easygo-platform-docker/docker/docker EASYGO_DOCKER_TEST_ENDPOINT=unix:///tmp/easygo-platform-docker/docker.sock EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:acl-loop`。
   - 专用 docker `build --network host -f deploy/runtime/Dockerfile.fixture -t easygo-sandbox-fixture:acl-loop .` → 0（W-2 镜像含 fixture-check）。
   - 同环境 `node scripts/test-harness.mjs --evidence /tmp/crew/acl-530e/loop/l3-acceptance-development --scenarios S3,S4,S5,S9` → 0，四项联调通过。
   - 同环境 `node scripts/test-harness.mjs --evidence /tmp/crew/acl-530e/loop/l3-all-green1` → 0，9/9。
   - 同环境 `node scripts/test-harness.mjs --evidence /tmp/crew/acl-530e/loop/l3-all-green2` → 0，9/9（代码未变，连续两遍）；每遍 76 次本机夹具模型请求，deferred=[]。
   - 反向断言：临时把 S3 的 acceptance_state 预期从 failed 改成 passed，用 `--scenarios S3 --evidence /tmp/crew/acl-530e/loop/l3-s3-negative-assertion` → 1（实际 failed / 预期 passed）。随后恢复原文件，坏断言未提交。
   - `npm --prefix services/agent-loop run typecheck` → 0。
   - `node --check scripts/test-harness.mjs` → 0；`git diff --check` → 0。
4. 证据：
   - `/tmp/crew/acl-530e/loop/l3-all-green1/`、`l3-all-green2/`：每场景 JSON、report.json、构建/服务日志。两遍均三个服务 code 0、remaining_containers=[]、state_removed=true。
   - S4 独立树哈希：`05bde4a1b37dcd8165e7344da5b5ed564fba1ba09af238a38ae8ec64b9d18163`；manifest 包含 artifact.txt、nested 目录、nested/proof.txt，与平台 evidence 完全一致。
   - S5 JSON 的 trusted_checker 保存前后哈希；RPC evidence 文本明确包含 network denied: true、两个写拒绝、no credentials or relay: true。
   - S9 JSON 保存 running_check（ID/运行态/命令/本次 owner label），RPC 输出包含 sleeping check 与 platform: check cancelled；remaining_checks=[]。
   - 反向断言证据 `l3-s3-negative-assertion/`、变异/恢复记录 `l3-s3-negative-mutation.json`。
   - 两遍绿及恢复后的脚本 SHA-256：`cd1fa2c17cbf6f83b35a2a88ba757d923786d5226a5ec3f0c5a3c6ce4182606a`。
   - 镜像 ID：`sha256:3f430f3ef2beeae199207a6590b821101390270d771d419db873c3abbd4d9320`；构建日志 `l3-image-w2-build.log`、类型检查 `l3-stage2-typecheck.log`。
5. 偏差与未做：
   - 无施工图偏差；工作区哈希独立取证需要读取自己临时任务工作区，这是 S4 的预定路径。S5/S9 的额外 host 读数只用于程序哈希和本次 owner 的容器信息，业务判定仍以 RPC 证据为准。
   - 新建的临时受信 pack 包含静态 checker，所以清理前需在所有服务/容器停止后解除本次 state 内目录的封存权限；只改本次临时目录，未触碰其它成员资源。
   - 所有任务和检查保持 128 PID、64 MiB、0.1 CPU、1 MiB tmpfs，串行执行。Go 编译 -p 1、GOMAXPROCS=2。无付费调用，无真实凭证，无 race 并发测试，无生产部署。
   - 未重跑原有全量 TS/Go 测试：本任务只改 harness/README，W-2 合入后已由工头确认 TS 128/128、workshop build+vet；本地独立构建与本单 E2E、typecheck 已通过。
6. 终审重点：
   - S3 确认 CLI 成功与 acceptance 失败分离，claims.pass 不会被 oracle 当通过；false_green 与真实失败输出一致。
   - S4 的 manifest、独立树扫描及 .workshop-home 排除，特别是目录 size 与 UTF-8 路径排序和 JSON 编码格式。
   - S5 实际 command 指向 /pack/checks/verify，受信 binary 哈希不变，工作区伪造副本无影响；隔离结论来自平台执行的证据输出。
   - S9 在实际检查容器运行后取消，平台证据和容器清理一致；全部九场景连续两遍、S3 错断言的退出码以及脚本恢复哈希。

状态：L-3 九场景已完成报审，等待终审反馈。开工回执 delivery_id `a6b490dc-efaf-459e-a4dc-4ac5ec3f26a8`。第一段报审 delivery_id `a1470067-f769-4254-b27e-c741e9ee7e49`。

# L-3 第一段报审 · 2026-09-29

1. 单号与提交：L-3 通道段，`efacf6de7412782bdc0662ad680cd48beed76ecb`，分支 `feat/acl-loop-l3`；工作树干净、未 push。本任务仅此一笔实现 commit。
   - 基线 `int/acl-p1-s1` / `762d66a`。
   - 按工头简令合入 W-1c `570ff08d9f71a70bd48a2e1c4d160f0473398f7f`；保留合入历史 `689c503`，没有自行修改 workshop 文件。
2. 改动文件：
   - `scripts/test-harness.mjs`：S1/S2/S6/S7/S8 各一个场景函数；真正经过模型夹具→gateway→loop 工具→workshop→Docker fixture-cli/easygo-crew→事件→loop 工具读回。判定读取 RPC 的持久化工具记录和工坊数据，不信任最终 assistant 文字。
   - `services/README.md`：构建/运行方式、选择子集、两遍独立证据目录、资源配置、清理和暂缓场景。
3. 测试命令与真实退出码：
   环境：`EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go EASYGO_DOCKER_TEST_BINARY=/tmp/easygo-platform-docker/docker/docker EASYGO_DOCKER_TEST_ENDPOINT=unix:///tmp/easygo-platform-docker/docker.sock EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:acl-loop`。
   - 专用 docker `build --network host -f deploy/runtime/Dockerfile.fixture -t easygo-sandbox-fixture:acl-loop .` → 0；W-1c 合入后重新 build → 0。
   - 上述环境下 `node scripts/test-harness.mjs --evidence /tmp/crew/acl-530e/loop/l3-channels-green1` → 0，5/5 通过。
   - 同环境 `node scripts/test-harness.mjs --evidence /tmp/crew/acl-530e/loop/l3-channels-green2` → 0，5/5 通过（连续两遍、代码未变）。每遍 34 次夹具模型请求，三个服务均 code 0 退出，无本次 owner 的残留容器，state_removed=true。
   - 反向断言：临时将 S2 的终态 outcome 预期从 none 改为 submitted，使用 `--scenarios S2 --evidence /tmp/crew/acl-530e/loop/l3-negative-assertion` → 1（实际 none / 预期 submitted）；随后 finally 恢复原文件，坏断言未提交。
   - `npm --prefix services/agent-loop run typecheck` → 0。
   - `node --check scripts/test-harness.mjs` → 0；`git diff --check` → 0。
4. 证据：
   - `/tmp/crew/acl-530e/loop/l3-channels-green1/` 和 `l3-channels-green2/` 各含 S1/S2/S6/S7/S8.json、report.json、构建与服务日志。
   - 负例 `l3-negative-assertion/`、变异与恢复记录 `l3-negative-mutation.json`；其中原始与恢复 SHA-256 都为 `e845d27fcbe7e7901ecfb6ddd3bf35d700fa09e5cb80a417833090b5cff43056`，与两遍绿证据一致。
   - 当前夹具 image id：`sha256:e02430a98fcc3189e8b5f6a1c49ffb843860e808002bc00eaa47218f0884062b`。
   - 构建日志 `l3-image-build.log`、`l3-image-w1c-build.log`，类型检查 `l3-typecheck.log`。
5. 偏差与观察 / 未做：
   - S6 原夹具无法处理 resume 的未读消息后缀，也不能取证输入；已报告并按 W-1c 修正后的 echo_input + workshop_result 实证。S6 明确校验 asked、回复 id、第二个 run、该 id 的 crew.read、新 run 接收的输入含精确 id 与正文。
   - 资源观察：配置最低 16 PID 下首次 S1 failed（曾发出首条 report），重测仍 failed。为消除镜像差异，用同一 W-1c image 单变量对照：32 PID S1 退出 0（`l3-pids32-comparison/`）；16 PID S1 退出 1（`l3-pids16-w1c-comparison/`，diagnostic 为 crew command failed）。不是对 32 PID 可用于真实 CLI 的保证。按工头裁定正式使用默认 128 PID，64 MiB 内存、0.1 CPU、1 MiB tmpfs 保持；P1 不改资源下限。
   - 开发途中 S7 有一次脚本局部变量 submit 遮蔽函数导致 ReferenceError，已改名 submission；该失败在 `l3-channels-run1/` 保留，后续两遍完整通过。
   - 仅实现第一段五个通道场景。S3/S4/S5/S9 在 report.deferred 明确列出；没有运行验收类、付费模型或 race 测试，没有改钱包/生产部署。
   - 所有本次测试 state 已删除；保留证据和命名夹具镜像供复核，没有清理其它成员的容器/镜像。
6. 终审重点：
   - 所有 submit/reply/resume 和消息读回均由真实 fixture 模型发 tool call，经 loop 执行；直接工坊 RPC 只承担独立读数与等待，无旁路写操作。
   - S1 顺序/id 唯一/重读一致/游标空页；S6 跨两轮归属、输入和已读证据；S7 reply 时 running，容器 inbox 确认 id 后才 submit；S8 夹具重复 POST 先校验回执相等，再由 RPC 确认仅一条事件。
   - 反向断言必须确实退出非 0；最终脚本哈希与两遍通过报告一致。独立检查资源配置和所有退出/清理状态。

状态：第一段停手报审，等待工头终审反馈或 W-2 集成后的第二段简令。L-3 开工回执 delivery_id `527e33da-e6a1-46df-92e6-1a9ea08aad7e`；W-1c/资源通知 `98a29f1c-982d-4489-afff-fe687510547f`；128 PID 裁定回执 `d9b979c8-0af4-4724-9394-c71ac485683d`。

# L-2 报审 · 2026-09-29

1. 单号与提交：L-2，`76530b2a0989a6fe5f5bb481650b94ea94ff041a`，分支 `feat/acl-loop`；未 push。基于 L-1 `78d893f7558d70c039a9ea4e9c0f76a9f64a87ba`。
2. 文件清单：
```
M	packs/base/roles/assistant.md
M	scripts/configure-platform.mjs
M	services/agent-loop/src/loop.ts
M	services/agent-loop/src/platform/server.ts
A	services/agent-loop/src/tools/harness.ts
M	services/agent-loop/src/tools/index.ts
A	services/agent-loop/src/tools/messages.ts
A	services/agent-loop/src/tools/receipts.ts
M	services/agent-loop/src/tools/workshop.ts
A	services/agent-loop/test/harness-tools.test.mjs
M	services/agent-loop/test/platform.test.mjs
M	services/agent-loop/test/service.test.mjs

```
3. 检查与真实退出码：
- `npm --prefix services/agent-loop run typecheck` → 0。
- `npm --prefix services/agent-loop test` → 0（128 tests，128 pass，0 fail）。新测覆盖三工具参数边界、伪造 namespace/task/run/evidence 身份、非法枚举/类型、分页/整批拒绝/历史轮次、reply 接受后断链终止 run、Web 两层白名单及 HTTP 认证/CSRF/namespace 绑定。
- `node --test services/agent-loop/test/harness-tools.test.mjs` 定向开发检查 → 0（当时 10 项；最终新增第 11 项被上述全量覆盖）。
- `git diff --check` → 0。
4. 证据：`/tmp/crew/acl-530e/loop/l2-typecheck.log`、`l2-tests.log`、`l2-focused.log`。全部使用假工坊或本机 HTTPS/HTTP 夹具，无真实模型；测试临时目录自行清理。
5. 偏差与未做：
- 按工头契约 v1.1→v1.2→v1.3 实现；messages 先校验 get.run_ids（≤256），随后全批校验 events，再过滤/分页；不改 events RPC 形状。v1.3 回执 delivery_id `0cd318a2-f11a-434a-ba0d-4186d3081fea`。
- 消息 JSON 页严格 ≤32768 字节；原生输出被过滤但游标继续前进，上游满 1000 条提示 continuation。单条合法消息因 JSON 转义超预算，或单条 read 包含大量 ID 时，保留事件身份并标记 `text_truncated` / `message_ids_truncated` 预览，游标推进；这些极端预览不承诺完整正文，可用原始 events RPC 取全文。
- evidence 文本服从 4..32768 字节 limit；全响应（包含 metadata/JSON 转义）最大 256 KiB，越界按不可信上游拒绝；列表最多 8 个 check。
- 实际 Web 还有 `PUBLIC_METHODS` 白名单，因此同时补了该入口和 loop.workshopCall，未改 UI 或钱包。生成部署配置也补齐 workshop.message/evidence 对 agent-loop 的授权。
- 本单没有真实 workshop v1.3 集成、Docker/真实模型测试，也未开 L-3；等待工头集成快照和 L-3 简令。
6. 终审重点：消息全批校验先于截断，不能用截断掩盖尾部非法事件；多 run 历史校验基于 run_ids；reply 的 mutating 不确定结果不能回模型重试；evidence 验证身份和 UTF-8 字节游标；HTTP 白名单实际可达且 namespace 不可由客户端伪造。

L-1 报审投递已记录：delivery_id `0133ec6d-8bcc-422c-9be5-4dd60b8611f8`（verified=pending）。

# L-1 报审 · 2026-09-29

1. 单号与提交：L-1，`78d893f7558d70c039a9ea4e9c0f76a9f64a87ba`，分支 `feat/acl-loop`；未 push。
2. 文件清单：
```
A	packs/base/roles/assistant.md
M	scripts/configure-platform.mjs
M	scripts/test-services.mjs
M	services/agent-loop/Dockerfile
M	services/agent-loop/src/loop.ts
M	services/agent-loop/src/server.ts
A	services/agent-loop/src/tools/index.ts
A	services/agent-loop/src/tools/registry.ts
A	services/agent-loop/src/tools/roles.ts
R072	services/agent-loop/src/tools.ts	services/agent-loop/src/tools/workshop.ts
M	services/agent-loop/src/types.ts
A	services/agent-loop/test/registry.test.mjs
M	services/agent-loop/test/service.test.mjs

```
3. 检查与真实退出码：
- `npm --prefix services/agent-loop run typecheck` → 0。
- `npm --prefix services/agent-loop test` → 0（115 tests，115 pass，0 fail）。覆盖重名、角色隔离、knowledge 错误、包加载/冲突/字节上限/缺失/入模，以及原有 mutating 不确定结果不重试全套用例。
- `EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go node scripts/test-services.mjs` → 0（真实三进程、mTLS、夹具模型与 CLI，无付费调用）。
- `git diff --check` → 0。
4. 证据：`/tmp/crew/acl-530e/loop/l1-typecheck.log`、`l1-tests.log`、`l1-services.log`、`l1-services-evidence/report.json` 和服务日志。原临时状态 `/tmp/easygo-rpc-e2e-FfARY4` 已清理，报告中的 state 为原路径。
5. 偏差与未做：没有新增 L-2 工具；没有改钱包；没有 build Docker 镜像。workshop controller Dockerfile 和 rpc-v1 calculator 文案由工头分配 W-1 8/9，未越权改。knowledge 注册提前到监听前以在启动时检查重名，但后台启动仍在 ready 后，保持原时机。L-2 身份字段歧义经工头 v1.1 修订解决（回执 delivery_id be105ce0-0902-42ae-abd7-b2cd31ec044b）。
6. 终审重点：注册表后不确定写结果仍终止 run；role 过滤同时约束展示与执行；pack_dir 读取不超过 16 KiB，配置冲突及缺失阻止监听；部署配置依赖 W-1 的 workshop pack 支持。

下一步：按工头放行直接开始 L-2，按契约 v1.1 用假工坊验证。

