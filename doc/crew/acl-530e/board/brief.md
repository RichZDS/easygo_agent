# 班子 acl-530e · EasyGo P1 Harness 基础

工头：`acl-foreman-530e`（Claude Opus 5.5，herdr pane wB:p7）。没有秘书，工头兼任。
集成分支：`feat/agent-cluster`，集成检出在 `/home/ubuntu/Projects/easygo-cluster`（只有工头能动）。基线 `05b5670`。

## 目标

按 `doc/agent-cluster-design.md` 的 §4、§6、§11 P1，把"单个 Agent 可靠干活"的 harness 做出来：
- 施工者在容器里用 `easygo-crew` 汇报、提问、报卡住、报审，每条都有 id，loop 按顺序读到，不重不漏；
- 平台判定 run 的结束结果（`outcome`）；
- 平台自己跑验收检查、留证据、识别假绿；施工者改不了检查；
- loop 的工具改成注册表，按角色加载 `base` 包里的角色说明，删掉 calculator；
- 修复"取消和退出同时发生"的配额竞态；
- 一套用夹具模型、结果确定的 harness 场景库。

接口以 `board/contract-p1.md` 为准。契约有问题就停下来问工头，不许现场改。

## 验收（工头亲自复跑）

1. TS：`cd services/agent-loop && npm run typecheck && npm test` 全绿。
2. Go：`services/workshop`、`services/ai-gateway`、`packages/rpc-go` 和仓库根模块，各自 `go test -race ./... && go vet ./...` 退出 0。
3. 原有端到端 `scripts/test-platform.mjs` 和故障注入 `scripts/test-platform-faults.mjs` 仍然通过。
4. 新的 `scripts/test-harness.mjs` 场景全部通过，每个场景都有证据文件。
5. 终审对每笔交付独立取证、给出结论；阻断问题清零。
6. 工头用真实模型（codex + deepseek）跑一个样本，确认真实 CLI 能按施工者说明用 `easygo-crew submit` 报审，并且验收检查被执行。这一步只由工头跑。

## 红线（在各人礼包之外补充）

- **不 push、不 merge、不碰 `main` 和 `origin`。** 每人只在自己的 worktree 和分支上提交，由工头集成。
- 不 `git reset --hard`、不 `git clean`、不删别人的分支和 worktree。
- **不调用付费模型，不读、不打印任何 API key。** 全部用本地夹具。
- 只能用专用 Docker daemon `unix:///tmp/easygo-platform-docker/docker.sock`。不许动系统 Docker，也不许动不是自己创建的容器；已停止的 `easygo-platform-r3-*` 保持原样。
- 镜像构建要加 `--network host`（daemon 是 `--bridge=none`）。镜像 tag 带上自己的席位名，例如 `easygo-sandbox-fixture:acl-workshop`。构建后只 prune dangling 镜像。
- **不重建完整的 `easygo-task-runtime` 镜像**（约 2 GB，磁盘放不下）。改它的 Dockerfile 可以，但验证只用夹具镜像；或者用 `FROM easygo-task-runtime:platform` 叠一层小镜像来验证。
- **资源很紧**：根盘约 3 GB 空闲，内存约 3 GB 可用，机器上还有别人的会话在跑。
  - 每人净增磁盘不超过 500 MB；
  - 测试产生的 state 目录跑完就删；
  - 不要同时跑多个 `-race` 全量测试。开发时按包跑，报审前再跑一次全量。
- 不联系班子以外的 agent，也不起子 agent。

## 文件归属（避免冲突）

| 席位 | 可以改 |
|---|---|
| workshop | `services/workshop/**`、`deploy/runtime/**`、`packs/base/pack.json`、`packs/base/roles/worker.md`、`packs/base/checks/**`、`contracts/rpc-v1.md`、`doc/workshop.md` |
| loop | `services/agent-loop/**`、`packs/base/roles/assistant.md`、`scripts/**`、`services/README.md` |
| workshop2 | 仅限 W-5：`services/workshop/workshop/docker.go` 里的 `remove`、`cleanup`、`Run` 和 `runCheck` 的准入检查与延迟回收、`Initialize` 探针容器的回收；`acceptance_docker.go`；`acceptance.go` 里计时的那几行；新增的 `services/workshop/workshop/docker_cleanup*.go`，以及 `docker_test.go`、`acceptance_integration_test.go` 里 W-5 的用例；W-6：`containerOptions`、`taskContainerOptions`、`checkContainerOptions` 的挂载构造，`crew.go` 的 `finishCrewOutcome`、`service.go` 里两处 interrupted 的 acceptance 事件字面量，以及相应测试；W-7：`runner.go` 的诊断与结果截断和 secret 替换，以及相应测试；`doc/workshop.md` 里讲回收的那一段 |
| review | 不改任何仓库文件，只在自己的 worktree 或快照里放复现代码 |

W-5 施工期间（09-29 起），workshop 席不改上面 workshop2 那一行列出的函数和行。返工如果必须碰这些地方，先问工头。

要改归属之外的文件，先问工头。
