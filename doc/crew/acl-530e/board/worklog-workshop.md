# Workshop 工作记录

## W-4 报审

报审 delivery_id：`d8f2cae5-ad04-4587-8c94-f9bd8ca57a51`，verified=pending。

1. 单号 W-4；提交 `be7dac06a3ab24551e1d749632f1951c3de917a3`，分支 feat/acl-workshop，未 push。
2. 文件：docker.go（仅 Docker 路径接线）、docker_runtime.go（参数/配置重写）、docker_runtime_test.go（模式/策略/CLI/resume 矩阵）、doc/runtime-provider-compatibility.md、doc/workshop.md（模式边界与实测证据）。
3. 验证：固定 runtime 的版本命令、三 CLI --help、OpenClaw config schema/config validate/exec-policy show 全部退出 0；定向 `go test -race ./workshop -run '^TestDockerAndHostShellTools$' -count=1 -v` 及最终复核退出 0；全量 `go test -race ./...` 退出 0；`go vet ./...` 退出 0；git diff --check 退出 0。Go cwd services/workshop，二进制 /home/ubuntu/sdk/go/bin/go。
4. 证据 `/tmp/crew/acl-530e/workshop/w4-*`：versions.log、{claude,pi,openclaw}-help.log、openclaw-schema.json、openclaw-exec-schema.json、openclaw-validate.log、unit.log、unit-final.log、race.log、vet.log。具体命令形态和参数结论见下节。
5. 偏差：无 host 权限扩展、无 MCP/网络工具扩展、未重建 runtime、未调模型。Claude 根据该固定版本 help 保留 --restricted（显式 --tools Bash 是例外）和 --bare，添加 --allowedTools Bash 避免等待无人能答的权限提示。OpenClaw 使用 schema 和 effective policy 实测认可的 gateway/full（容器内本地执行，ask off）。
6. 终审重点：只有 ready DockerRunner 调用重写函数；host 原函数未改；read-only 仍是只读挂载；三 CLI resume 参数/配置不丢失；OpenClaw plugins 继续禁用。

真实模型仍待工头验证：各原生 CLI 是否实际选择 shell、执行 easygo-crew 获回执、运行测试，以及 resume 后权限是否按预期生效；特别关注 Claude restricted 对具体 Bash 命令的实际权限判定。额外前提：已有 easygo-task-runtime:platform 实测缺少 /usr/local/bin/easygo-crew（w4-runtime-crew-presence.log），真实模型前请叠入 W-1 二进制或使用更新 runtime 镜像；本席只用这个旧镜像取离线 help，未误报真实工具执行。

当前所有已派 workshop 工单均已提交报审；等工头/终审反馈。

## W-4 验证

- 工头已裁定原 ASK 属 P1，按 `/tmp/crew/acl-530e/board/tasks/w4-runtime-shell.md` 实施；原待判断项已转 W-4。
- 仅 DockerRunner 调用 dockerRuntimeArgs/dockerRuntimeConfig：Claude 添加 Bash 和 --allowedTools Bash；Pi 添加 bash；OpenClaw 添加 exec 与 tools.exec={host:gateway,mode:full}。host 文件未改，Codex 原 override 未改；两种策略均保留外层挂载/网络/资源边界。
- 固定已有镜像 easygo-task-runtime:platform，全程专用 daemon、--network none、只读根、临时 HOME、无凭证、未调用模型：版本 claude 2.1.281 / pi 0.87.1 / openclaw 2026.9.6 (eb377ac)，版本命令和三份 help 均退出 0（w4-versions.log / w4-{claude,pi,openclaw}-help.log）。
- Claude help 明确 restricted 只移除未在 --tools 命名的命令工具；--bare 不影响内置功能；故保留 restricted/bare，使用显式 Bash。Pi help 的 Built-in Tool Names 列出 bash。
- OpenClaw config schema --json 退出 0；tools.exec schema 验证 gateway/full 枚举。实测 config validate --json = valid；exec-policy show --json 显示 effective mode/full、security/full、ask/off，且没有既有 approvals store。证据 w4-openclaw-schema.json / w4-openclaw-exec-schema.json / w4-openclaw-validate.log。
- `go test -race ./workshop -run '^TestDockerAndHostShellTools$' -count=1 -v` 退出 0（w4-unit.log）；最终小整理后同一命令不带 -v 再跑退出 0（w4-unit-final.log）。12 子测试＝3 CLI×2策略×初次/续跑，各子测试同时比较 Docker/host（共 24 种模式组合），并断言未初始化 runner 拒绝、外层 mount/network/UID 不变。
- 文档 doc/runtime-provider-compatibility.md 和 doc/workshop.md 已补两种模式差别、固定版本证据和仍待真实模型证明的项目。
- 全量 workshop -race/vet 均退出 0（w4-race.log / w4-vet.log），开始通知 delivery_id `913cd325-88a1-4ae9-9e02-77b33ed3118e`。

## W-3b 报审

报审 delivery_id：`897231a6-0865-4b62-9ca2-558627926231`，verified=yes。

1. 单号 W-3b；提交 `3adec82aad036aee686284d093bc8666cd64c9e0`，分支 feat/acl-workshop，未 push。
2. 文件：docker.go（最终扫描超时分类与私有测试预算 seam；默认仍 30 秒）；cancel_exit_race_test.go（极小预算两支回归）。服务状态优先级未动。
3. 测试（cwd services/workshop，Go `/home/ubuntu/sdk/go/bin/go`）：修复前 `go test ./workshop -run '^TestFinalQuotaBudgetFailure$' -count=1 -v` 退出 1，两支第 0 次均命中旧行为；修复后 `go test -race ./workshop -run 'TestFinalQuotaBudgetFailure|TestCancelExitRace' -count=1 -v` 退出 0，新取消/未取消各 200 次、原超额/未超额各 200 次，共 800 次；`go test -race ./...` 退出 0；`go vet ./...` 退出 0；git diff --check 退出 0。
4. 证据 `/tmp/crew/acl-530e/workshop/w3b-{red,green,race,vet}.log`。
5. 偏差：无；测试用 1 纳秒 seam 避免创建数百万文件/等待 30 秒，不改变公开配置或生产预算。仅扫描自身 DeadlineExceeded 触发包装，保留原始 cause。普通调用方取消仍按原 W-3 未超额分支为 cancelled，无产物。
6. 终审重点：WithoutCancel 清除调用方截止，quotaCtx 有独立预算；包装同时检查扫描错误和 quotaCtx 错误，确保只有扫描预算耗尽被分类成 ErrDiskQuotaScanFailed。

W-3b 报审后按最新裁定开始 W-4。

## 待工头判断：非 Codex 运行时工具许可

现有 Claude/Pi/OpenClaw 工具配置排除了 shell，可能限制真实 easygo-crew 调用；未擅自扩大权限。具体位置/建议范围见 `/tmp/crew/acl-530e/workshop/runtime-crew-tool-question.md`。ASK delivery_id `e3090481-b26f-42af-91e7-79c6d3e8db40`（verified=yes）。不影响 W-3b 继续验证。

## W-3b 验证中

已读终审报告；最终扫描新增私有预算注入 seam（默认仍 30 秒），只在 quotaCtx 自身 DeadlineExceeded 且扫描返回 DeadlineExceeded 时包装 ErrDiskQuotaScanFailed，同时保留 cause。修复前两支首轮均失败（w3b-red.log），新两支各 200 次 + 原 W-3 两支各 200 次 -race 退出 0，共 800 次（w3b-green.log）。最终全量 -race/vet 执行中（w3b-race.log / w3b-vet.log），开始通知 delivery_id `d470ad91-8e34-4cad-b1e8-3ffebef777fd`。ACK delivery_id `4ed0506f-3502-469f-ba68-bc075d3833c3`。

## W-2 报审

报审 delivery_id：`1cc5f363-5eab-4400-8d8b-a122d0fee063`，verified=yes。

1. 单号 W-2；提交 `aa41e657c65807072d66766b0d27cbde9b5bb7fa`，分支 `feat/acl-workshop`，未 push。
2. 文件：新增 acceptance.go / acceptance_docker.go / acceptance_pack.go / acceptance_types.go / evidence.go 及 acceptance 单测/真实容器测试；修改 service/types/views/pack、server RPC/mTLS 测试、两份 config 示例、fixture-cli；新增 fixture-check、base checks/npm-test.sh；修改 fixture Dockerfile、契约和 workshop 使用文档。完整清单 `git show --stat aa41e65`。
3. 所有命令见「W-2 验证」：定向测试、-race、真实容器七场景、RPC 均退出 0；最后全量 `/home/ubuntu/sdk/go/bin/go test -race ./...` 退出 0、`go vet ./...` 退出 0（cwd services/workshop）；`git diff --check` 退出 0。
4. 证据 `/tmp/crew/acl-530e/workshop/w2-{unit,unit-race,rpc,image-build,fixture-update,container,race,vet}.log`；夹具接口 `/tmp/crew/acl-530e/workshop/w2-fixture-interface.md`。
5. 偏差/未做：无契约行为偏离。按契约分离 CLI 终态和 acceptance，验收 failed/error 时 CLI 成功任务仍为 succeeded；这不是假绿漏判。fixture 镜像首次按 Dockerfile 构建，此后仅叠加新静态 fixture-cli（插单 W-1c 和 W-2 write-denied），以控制磁盘；没有构建完整 runtime。未做 P2 终审/跨任务挂载/检查重跑。原始检查日志、metadata、分页有不同字节计数（完整收到数 vs 实际保留数），文档已说明。源码最后仅把取消时的附加平台诊断从 infrastructure failed 改为 check cancelled；真实容器运行已验证取消状态/回收，最终全量检查覆盖当前代码。
6. 终审重点：pack 拷贝/复用拒绝符号链接且校验内容与权限；检查参数无 relay/凭证、只读工作区/pack；超时清理实际容器；false_green 只按最终结果；取消和重启 acceptance 状态；证据 scope/UTF-8/截断；各检查共享执行前树哈希且排除 native home。

按工头插单安排，W-2 报审后开始 W-3b。

## W-2 验证

- 实现 Docker-only acceptance 配置、可信 checks 快照与权限、只读无网无凭证检查容器、逐检查 evidence（1 MiB 截断）、workspace 树哈希、false_green、取消/重启状态、workshop.evidence 列表/分页与身份字段；加入 fixture-check 和 base npm-test.sh。
- `go test ./workshop -run 'TestAcceptance|TestPackSnapshot|TestEvidence' -count=1` 退出 0（w2-unit.log）。
- `go test -race ./workshop -run 'TestAcceptanceConfig|TestPackSnapshot|TestEvidence|TestAcceptanceRestart|TestAcceptanceContainer' -count=1` 退出 0（w2-unit-race.log）。
- `go test ./server -run '^TestAllWorkshopMethodsOverTLS$' -count=1` 退出 0（w2-rpc.log）；包含 evidence mTLS 身份、列表默认值、参数边界和跨 namespace 拒绝。
- 专用 daemon 构建 Dockerfile.fixture（--network host）退出 0（w2-image-build.log）；后因插单 W-1c 与 write-denied 更新，静态编译 codex 并叠加小层（w1c-image-build.log / w2-fixture-update.log），均退出 0。只清理本次自建、无标签的 builder IDs f048174f34b6 / 21022d31ea6c（对应 cleanup.log），未清理共享 daemon 其它镜像。
- `go test ./workshop -run '^TestAcceptanceDockerIntegration$' -count=1 -v`，显式专用 endpoint/binary/image，退出 0（w2-container.log）。七场景：passed、坏产物 failed/false_green、timeout failed/timed_out、检查实际启动后 cancel、1 MiB 截断、infra error、CLI 失败 skipped。每场景核对所属容器为 0。
- passed 场景还证明：检查工作区/pack 写拒绝、外网拒绝、无凭证/relay；worker 对 /pack/checks 和 controller snapshot 路径均写失败；修改原始 pack 后检查仍读取快照原内容。source pack 与 snapshot 都不在工作区内。
- stdout/stderr 可读分页（含 UTF-8 边界和无效字节），output_bytes 记完整收到数，分页 total_bytes 记保留数；配置和日志证据均不含真实凭证。
- 全量 workshop -race/vet 均退出 0（w2-race.log / w2-vet.log）；通知 delivery_id `64585a91-585b-41a9-be42-90e1955b63bf`。L-3 夹具接口 `/tmp/crew/acl-530e/workshop/w2-fixture-interface.md`，通知 delivery_id `8044881b-d537-405a-affa-d94e9a915893`。

## W-1b / W-1c 报审（插单）

- W-1b：`a2e0b505fe5dd8432f8ae1d24a0ae0268cba4d39`，仅 worker.md，改为 ask 回执即可停手，同问不补 blocked。`git diff --check` 退出 0。报审 delivery_id `83a64726-3cc4-43be-8a2b-3a0dcbadd4ef`。
- W-1c 报审 delivery_id `db16dba2-7fb0-490f-b254-1638125e6455`（verified=yes）。提交：`570ff08d9f71a70bd48a2e1c4d160f0473398f7f`，fixture 解析首个 JSON 值、echo_input（64 KiB UTF-8 截断），补单测和真实容器 resume 证明。`go test ./cmd/fixture-cli -count=1 -v` 退出 0；专用 daemon `go test ./workshop -run '^TestCrewDockerResumeInputIntegration$' -count=1 -v` 退出 0，result 完整匹配输入 JSON + 未读回复 id/正文。证据 w1c-unit.log、w1c-container.log；镜像仅叠加新静态 codex 夹具层（w1c-image-build.log），未重建完整 runtime。接口说明已更新。
- 两笔均未混入 W-2，未 push。

## W-1 报审

报审 delivery_id：`c3054edb-820c-45b9-9cca-59b9e16ba956`，verified=pending。

1. 单号 W-1；提交 `39863f11ae348d1d8e1fb569b3c1a14938e13e14`，分支 `feat/acl-workshop`，未 push。
2. 文件：workshop 的 crew.go/crew_relay.go/pack.go、types/service/views/relay/docker/runner/runtime_config 及 crew/Docker 回归测试；server RPC 和 mTLS 测试；cmd/easygo-crew（含测试）、fixture-cli crew 脚本；两个示例配置；controller 和两个 runtime Dockerfile；packs/base/pack.json、roles/worker.md；contracts/rpc-v1.md、doc/workshop.md。完整清单 `git show --stat 39863f1`。
3. 验证：下面「W-1 验证」逐项命令退出均 0；最后 `cd services/workshop && /home/ubuntu/sdk/go/bin/go test -race ./...` 退出 0，`/home/ubuntu/sdk/go/bin/go vet ./...` 退出 0；`git diff --check` 退出 0。全量覆盖 W-3 各 200 次竞态和现有下载测试，无复跑才绿的问题。
4. 证据目录 `/tmp/crew/acl-530e/workshop/`：w1-unit.log、w1-unit-race.log、w1-image-build.log、w1-container.log、w1-race.log、w1-vet.log。真实容器证据记录每条 crew 事件与 id/sequence/run_id/read。
5. 偏差：契约采用最新 v1.3；run_limit 映射到现有 RPC 错误形状的 `data.code=run_limit`。acceptance/evidence 的行为按工单留 W-2，摘要暂为 skipped/false/0；文档已收录完整 P1 契约并明确 W-2 实现范围。未构建完整 runtime 镜像，只构建约 27 MB 的夹具镜像；controller Dockerfile 仅增加 COPY。
6. 终审重点：同 run client_id 幂等事务、旧 run capability 隔离、resume 与 crew.read 原子性、get.run_ids 顺序与 256 上限、token 不进入错误输出、真实容器跨 relay 通信证据。worker.md 的 ask 后停手同时以 blocked 说明等待回复，符合停手须 submit/blocked 的要求。

W-1 报审后按任务单开始 W-2。

## W-1 验证（契约 v1.3）

- 增加 task/run 绑定的 CrewChannel；消息、幂等/限额、inbox/read、resume 未读消息、outcome 均在 Service 的事务中处理。
- relay 共用 per-run token/监听；Docker 和有 relay 的 host 注入 EASYGO_CREW_URL/TOKEN；同 token 在 native 输出继续遮蔽；无 relay 时不注入。
- `workshop.message` 响应 namespace/task_id/id/sequence；get.run_ids 按旧到新；Resume 上限 256（-32009，data.code=run_limit）。
- 新 easygo-crew 静态客户端；离线 fixture 脚本接口见 `/tmp/crew/acl-530e/workshop/w1-fixture-interface.md`。接口通知 delivery_id `de1f19c9-3c3a-4278-a131-12bbcf800323`。
- 角色文件使用 writing-for-agents 技能；新增 base pack、controller 镜像 COPY、契约文档及使用说明。
- 定向 `go test ./workshop -run 'TestCrew|TestWorkerPack' -count=1` 退出 0（w1-unit.log）。
- 定向 `go test -race ./workshop ./server ./cmd/easygo-crew -run 'TestCrew|TestWorkerPack|TestAllWorkshopMethodsOverTLS|TestRunLimitDomainError' -count=1` 退出 0（w1-unit-race.log）。
- 补充 host/pack/注入验证 `go test ./workshop -run 'TestCrewHost|TestDockerRunUsesUDSOnly' -count=1` 退出 0。
- 专用 daemon 镜像构建（--network host，Dockerfile.fixture，tag easygo-sandbox-fixture:acl-workshop）退出 0（w1-image-build.log），image a65b69e52f90。
- 真实容器 `go test ./workshop -run '^TestCrewDockerIntegration$' -count=1 -v`（显式专用 endpoint/binary/image）退出 0（w1-container.log）：report/ask/report/submit 顺序、重复回执、运行中 inbox 回信及 read 事件、submitted outcome、产物、容器回收均验证。
- 全量 -race/vet 均退出 0；开始通知 delivery_id `6b90b113-0a80-4ff2-bef0-8c0b2d0c9d44`。

## W-3 报审

报审 delivery_id：`e1148b54-b82d-4df7-b01a-26f1eba49732`，verified=pending。

1. 单号 W-3；提交 `eb46347605a4096c0907683f35153baea6cd18b4`，分支 `feat/acl-workshop`，未 push。
2. 文件：`services/workshop/workshop/docker.go`（最终扫描 WithoutCancel + 30 秒界限）；`service.go`（仅 Succeeded 登记产物）；`cancel_exit_race_test.go`（确定性回归）。状态优先级和周期监控不变。
3. 测试（cwd `services/workshop`，Go `/home/ubuntu/sdk/go/bin/go`）：
   - `go test ./workshop -run '^TestCancelExitRace$' -count=1 -v`，修复前退出 1；两个旧行为首轮复现。
   - `go test -race ./workshop -run '^TestCancelExitRace$' -count=1 -v`，退出 0；超额、未超额各 200 次，共 400 次，均符合状态和无产物断言。
   - 第一次 `go test -race ./...`，退出 1；workshop 包通过（再次覆盖各 200 次），server 既有下载测试发生客户端超时。
   - `go vet ./...`，退出 0。
   - 串行 `go test -race ./server -run '^TestArtifactDownloadLimitOverMTLS$' -count=1 -v`，退出 0，8 MiB 产物 envelope 11184983 字节。
   - 第二次 `go test -race ./...`，退出 0；server 实跑，workshop/cmd-server 复用对应未变代码缓存。
   - `git diff --check`，退出 0。
4. 证据：`/tmp/crew/acl-530e/workshop/w3-{red,green,race,vet,download-retry,race-retry}.log`。
5. 偏差：无生产行为范围偏离。测试内部 quota=8 条目以降低重复成本，公开配置边界未改；8 MiB 下载测试未改，失败和复跑结果均保留。未跑真实 Docker（W-3 要求的是受控竞态）。
6. 终审重点：取消后最终配额错误仍高于 Cancelling；真实 native 成功返回后发生取消时，Service 不能登记已收集产物。两处行为修改外只有回归测试。

报审后按工头放行直接开始 W-1（契约 v1.1）。

## W-3 验证中

- 分支：feat/acl-workshop；仅修改 docker.go、service.go，新加 cancel_exit_race_test.go。
- 确定性回归：真实 Service + DockerRunner/native parser，受控 Docker 命令和 runner 返回钩子触发取消；无 sleep 触发竞态。超额分支取消后造超额，确保周期扫描不兜底；未超额分支真实 DockerRunner 成功返回后再取消，确保覆盖 nil runErr 的产物登记问题。
- 配额测试用内部 8 条目限额降低循环成本；生产配置下限未改，已有配置边界测试覆盖。
- 红：`cd services/workshop && /home/ubuntu/sdk/go/bin/go test ./workshop -run '^TestCancelExitRace$' -count=1 -v`，退出 1，两个分支第一轮均命中旧行为。证据 `/tmp/crew/acl-530e/workshop/w3-red.log`。
- 绿：`cd services/workshop && /home/ubuntu/sdk/go/bin/go test -race ./workshop -run '^TestCancelExitRace$' -count=1 -v`，退出 0，超额/未超额各 200 次，共 400 次。证据 `/tmp/crew/acl-530e/workshop/w3-green.log`。
- 全量正在运行；server 的既有 TestArtifactDownloadLimitOverMTLS 下载 8 MiB 时超时，待复核。
- 进度投递 delivery_id：07c31571-78cd-4486-b59d-f22bd341f641；全量失败预报 delivery_id：702507cd-05ed-41c4-a38d-efb1e3c0535e；均 pending（工头忙），未重复发送。
