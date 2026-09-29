# P1 集成清单（工头）

## 合并
- 基线是 `feat/agent-cluster` 的 e8c5635，它已经包含 CLI 解耦 58f57f6 和设计稿 §9。
- 在它上面依次合入：W-3 eb46347、W-3b 3adec82、W-1 39863f1 + a2e0b50 + 570ff08、W-2 aa41e65、W-4 be7dac0、W-5（待交付）；L-1 78d893f、L-2 76530b2、L-3 efacf6d + 24fe1ac。
  - 做法：以 `feat/acl-workshop` 的最新提交（以及 w5）和 `feat/acl-loop-l3` 为准，用 merge 保留历史。
- 09-29 的 dry-run：CLI 与文档这一侧和 P1 快照没有碰同一个文件。

## 工头补的文档（单独一笔提交）
- `contracts/rpc-v1.md`：删掉 calculator 那句（L-1 施工图留给工头）。
- `doc/workshop.md`：夹具 op 表补上 `echo_input`（W-1 终审 P3）。
- `doc/agent-cluster-design.md`：
  - §5.3 的前提改成"Docker 模式下四种 CLI 都有 shell；host 模式只有 Codex"（W-4）；
  - 新增"已知限制"：PID 下限 16 不够用（通道场景用了 128）、`npm test` 类检查不能写只读工作区、F-1、W-5 修好之前回收脆弱；
  - §12 追加 P1 验证记录。
- 提交 `scripts/test-harness-live.mjs`（付费，只由工头跑），在 `services/README.md` 里注明它不进 CI。
- 把班子的板子归档到 `doc/crew/acl-530e/`：brief、契约、施工图、终审报告、followups、live summary，并做凭证扫描；worklog 与 hr 按需摘要。

## 回归（工头亲自跑，串行）
1. `cd services/agent-loop && npm ci && npm run typecheck && npm test`
2. 下面四处各自 `go test -race ./... && go vet ./...`：
   - `services/workshop`，要设 `EASYGO_DOCKER_TEST_*`，这样容器测试不会被跳过；
   - `services/ai-gateway`；
   - `packages/rpc-go`；
   - 仓库根模块。
3. `node scripts/test-services.mjs`、`node scripts/test-platform.mjs`、`node scripts/test-platform-faults.mjs`
4. `node scripts/test-harness.mjs`，跑两遍。
5. 如果 W-5 改了回收路径，再跑一次真实模型样本（codex + 其他任选一个），确认没有回退。
