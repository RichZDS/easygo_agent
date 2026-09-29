# P1 验证记录：Harness 基础

- 日期：2026-09-29
- 集成分支：`feat/agent-cluster`，验证提交：`51dadc9`
- 设计依据：`doc/agent-cluster-design.md` §4、§6、§11 P1
- 接口契约：`doc/crew/acl-530e/board/contract-p1.md`（v1.3）
- 施工方式：crew 班子 `acl-530e`，工头 1 人、施工 3 人、终审 2 人。每笔交付都经过独立终审，由工头亲自跑回归后签字。班子现场存档在 `doc/crew/acl-530e/`。

## 1. 交付与终审

| 单号 | 内容 | 提交 | 终审结论 |
|---|---|---|---|
| W-3 / W-3b | 取消与退出竞态：一律 failed，非 succeeded 不登记产物，最终扫描 30 s 有界 | eb46347、3adec82 | 通过；W-3 的 P2 由 W-3b 关闭 |
| W-1 / W-1b / W-1c | 施工者通道：`/crew/*`、`easygo-crew`、outcome、resume 带未读消息、256 run 上限；worker.md；夹具 | 39863f1、a2e0b50、570ff08 | 两位终审各审一遍，都通过；截断处可能留下 token 前缀的 P3 由 W-7 修复 |
| W-2 | 验收检查容器、pack 快照、证据、假绿 | aa41e65 | 通过；两条 P3 由 W-6 修复 |
| W-4 | Docker 模式下给 Claude/Pi/OpenClaw 开放 shell | be7dac0 | 通过（P0–P3 无） |
| W-5 | 高负载下容器回收：重试、确认已停止后延迟回收，状态不明时失败即关闭 | 1e1e78c、fd480ab | 通过；测试 d 补强并入 W-7 |
| W-6 | 只读挂载在构造时决定；重启时重算 false_green | c97378b | 通过（P3 一条接受：取消子用例未单独锁住重算，interrupted/failed 子用例已锁住同一函数） |
| W-7 | 诊断截断处不留 secret 前缀；测试 d 补强 | 021326e | 通过（P3 一条接受，见 F-6） |
| L-1 | loop 工具注册表、按角色加载、删 calculator、`pack_dir` | 78d893f | 通过（一条 P3 接受，见 F-2） |
| L-2 | `workshop_messages` / `workshop_reply` / `workshop_evidence`，Web 白名单 | 76530b2 | 通过（P3 转后续项 F-1） |
| L-3 | harness 场景库 S1–S9 | efacf6d、24fe1ac | 通过（P0–P3 无） |

终审报告都在 `doc/crew/acl-530e/board/review-*.md`。每份报告都写明了独立取证的命令和证据，以及故意改坏实现后测试判红的对照实验。

## 2. §11 P1 验收对照

| 验收项 | 证据 |
|---|---|
| 四类报告在 loop 侧按顺序读到，每条有 id，不重不漏 | S1、S8；L-2 终审验证了整批严格校验后才分页，截断不会掩盖尾部的非法事件 |
| 没报告就退出记为 `outcome=none` | S2 |
| 做坏的产物被检查拒绝，证据可查 | S3；W-2 终审的真实容器坏产物用例 |
| 自报通过但检查失败记为假绿 | S3；W-2 真值表单测（四种 outcome × 四种 tests × 六种 state） |
| 施工者改不了检查脚本 | S5；W-2 的 pack 快照（内容哈希、0555、拒绝符号链接） |
| 检查容器无网络、工作区只读 | S5；W-2 真实容器用例；W-6 改为在构造挂载时就决定只读 |
| 竞态复现用例转绿 | W-3/W-3b；把修复回退后判红，`-race` 下 200 次循环 |
| 现有测试和端到端全部通过 | 见第 3 节 |

## 3. 回归（工头亲跑，`51dadc9`）

TS、ai-gateway、rpc-go、根模块这四项在 `1907c15` 上跑；其余在 `51dadc9` 上跑。两次提交之间只改了 `services/workshop/`（W-6、W-7）。全部经班子的资源闸门串行执行（nice、`GOMAXPROCS=2`、`-p=2`）。日志在 `/tmp/crew/acl-530e/foreman/regress/`。

| 项 | 命令 | 结果 |
|---|---|---|
| TS | `cd services/agent-loop && npm ci && npm run typecheck && npm test` | `npm ci`、typecheck 退出码 0；`npm test` 128/128 |
| ai-gateway | `go test -race -count=1 ./... && go vet ./...` | 退出码 0 |
| rpc-go | 同上 | 退出码 0 |
| 根模块（含 CLI） | 同上 | 退出码 0 |
| workshop | 同上，带 `EASYGO_DOCKER_TEST_*`，所以容器测试不会被跳过 | 退出码 0；容器测试 0 个跳过 |
| 三进程 | `node scripts/test-services.mjs` | 退出码 0 |
| 平台端到端 | `node scripts/test-platform.mjs` | 退出码 0 |
| 故障注入 | `node scripts/test-platform-faults.mjs` | 退出码 0 |
| harness | `node scripts/test-harness.mjs`，跑两遍 | 两遍都是 9/9，退出码 0；跑完没有残留容器 |

## 4. 真实模型样本（工头，付费）

- 脚本：`scripts/test-harness-live.mjs`，只由工头手动运行，不进 CI。
- 模型：deepseek-flash，经 ai-gateway 调用；key 只存在于网关进程的环境变量里。
- 镜像：`easygo-task-runtime:platform` 加上 easygo-crew。
- 任务：让施工者实现一个 Node ESM 的 slugify 项目，并用 `node --test` 写测试。平台在独立的检查容器里跑 `/pack/checks/npm-test.sh`。

| CLI | outcome | 验收 | 自报 tests | false_green | 输入 / 缓存 / 输出 tokens |
|---|---|---|---|---|---|
| codex | submitted | passed | pass | false | 80066 / 77824 / 1216 |
| claude | submitted | passed | pass | false | 3636 / 21248 / 1837 |
| openclaw | submitted | passed | pass | false | 51789 / 44672 / 1501 |
| pi | submitted | passed | pass | false | 20114 / 18944 / 1097 |

- 每次运行结束都对整个 state 目录做 key 扫描，四次（r-wd7ynQ、r-mY4dwU、r-zV8VxK、r-ZBeabT）结果都是 0。
- 第一次跑 pi 时，检查已经以退出码 0 结束，但负载 17.9 下删除检查容器超过了 20 s。工坊因此永久停止接收任务，openclaw 被拒绝。这个问题由 W-5 修复。
- W-5、W-6、W-7 合入之后，在 `51dadc9` 上又跑了一次（codex、pi），报告 r-ZBeabT：两者都是 submitted、passed，false_green=false；检查耗时 614 ms / 540 ms（W-5 之后不含回收）；key_leaks=0；没有残留容器。脚本结束后只保留报告、日志和配置（32 KB）。

## 5. 已知限制

1. **host 模式下只有 Codex 能用 `easygo-crew`。** Claude、Pi、OpenClaw 的 shell 只在 Docker 模式开放（W-4），因为工具白名单不是 OS 隔离。设计稿 §5.3 已按此更正。
2. **真实模型下的假绿、`ask`/`blocked` 和 resume 带未读消息，目前只由夹具场景证明**（S3、S6、S7）。真实模型样本里的每次运行都是诚实通过。
3. **生产配置的资源下限没有经过真实模型验证。** 生产配置是 `pids_limit` 128、内存 1 GiB，样本用的是 256 / 2 GiB。另外，PID 下限 16 对"CLI + shell + easygo-crew"不够用，夹具场景用了 128。见 F-5。
4. **检查以只读方式运行工作区。** 如果测试框架要往工作区写缓存或覆盖率文件，检查会失败。pack 作者要把输出写到 `/tmp`，这是 tmpfs。
5. **回收的最坏耗时。** 回收最多重试 3 次，每次 20 s，再加一次 20 s 的确认，最坏情况下任务结束会拖长约 83 s。确认仍在运行或状态不明时，照旧失败即关闭，需要运维重启恢复。
6. **检查命令只要求是绝对路径，不强制放在 `/pack/checks` 下。** 工作流配置由运维提供，被视为受信。

## 6. 后续项

见 `doc/crew/acl-530e/board/followups.md`。

- **F-1**：call id 过长时，reply/submit 的幂等 key 会超过 128 字节。常见 provider 的 call id 不受影响。
- **F-2**：接受，不修。
- **F-4**：端到端脚本的授权名单还没加上 `workshop.message`/`workshop.evidence`。和 P2 的 Web 看板一起补。
- **F-5**：见第 5 节第 3 条。
- **F-6**：截断处相邻两段 secret 前缀只剥掉最后一段；接受不修，理由见 followups。
