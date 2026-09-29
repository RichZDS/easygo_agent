# 交接 · workshop2 (acl-workshop2-530e) · P1 收尾

班子已解散（工头签字，11 笔终审通过，集成回归绿在 `51dadc9`，真实模型复测通过）。以下是 workshop2 席位的收尾状态。

## 1 在做哪张单

无在做的单。最后一张是 W-7（诊断截断处不留 secret 前缀 + W-5 测试 d 补强），已交付、已终审通过、已合入集成分支。此后没有再派新单。

## 2 做到哪一步（可验证事实）

按交付顺序，均在 `feat/acl-workshop-w5` 上：

- **W-5**（容器回收在高负载下的韧性）：首次提交 `1e1e78c`；返工提交 `fd480ab`（工头初核退回三处后另起一笔，非 amend）。
- **W-6**（修 W-2 终审两条 P3：readonly 挂载子串匹配、false_green 未在 finish 重算）：提交 `c97378b`。终审通过，R2 提过一条轻微测试缺口（取消子用例没把 false_green 重算单独锁住），工头/R2 判定不修，已合入。
- **W-7**（诊断截断处不留 secret 前缀 + W-5 测试 d 补强）：提交 `021326e`。四项破坏验证（`confirmCleanup` 三个活跃状态判断各去掉一次、`redactAndBound` 去掉砍前缀逻辑）均精确判红且互不掩盖；`go test -race ./... && go vet ./...` 全量 exit 0。

所有交付的测试状态：截至 W-7 交付时最后一次全量 `-race`，`services/workshop/...` 全部 PASS，`go vet` 无警告。之后 W-7 又经终审、合并进集成分支，工头报告集成回归全绿——这一步的具体日志在集成分支侧，workshop2 本地没有留存。

worklog 全量记录（含每笔的测试命令、真实退出码、破坏验证过程）：`/tmp/crew/acl-530e/board/worklog-workshop2.md`。证据日志：`/tmp/crew/acl-530e/workshop2/w5-*.log`、`w6-*.log`、`w7-*.log`。

## 3 动了哪些文件

- `services/workshop/workshop/docker.go` — W-5：`confirmCleanup`/`deferCleanup`/`clearPendingCleanup`/`cleanup`/`sweepPendingCleanup`、`stoppedStatus`、相关 sentinel/consts；W-6：`containerOptions` 加 `workspaceReadOnly bool` 参数，构造时直接决定 `,readonly`。
- `services/workshop/workshop/acceptance_docker.go` — W-5：`runCheck` 返回值加 `duration`；W-6：`checkContainerOptions` 改用新签名，去掉子串匹配。
- `services/workshop/workshop/acceptance.go` — W-5：`Evidence.DurationMS` 用 `runCheck` 返回的 duration。
- `services/workshop/workshop/crew.go` — W-6：`finishCrewOutcome` 重算 `run.Acceptance.FalseGreen`。
- `services/workshop/workshop/service.go` — W-6：两处 `interrupted` 事件字面量补 `FalseGreen`（不在原归属表，施工图明确要求，已在偏差里说明）。
- `services/workshop/workshop/runner.go` — W-7：新增 `redactAndBound`、`secretPrefixSuffixLength`、`minSecretSuffixLength`；`runNative` 的诊断截断改用它。
- `services/workshop/workshop/docker_test.go` — W-5/W-6/W-7：fake Docker harness 扩展、`mountFields`/`findMount`、`TestDockerCleanupFailsClosedWhenStillActive`（W-7 从单场景拆成三个独立子用例）等。
- `services/workshop/workshop/acceptance_test.go`、`acceptance_integration_test.go`、`crew_test.go`、`disk_quota_test.go`、`docker_integration_test.go`、`docker_native_test.go` — W-5/W-6 相关新测试与机械性签名更新（含 `TestAcceptanceDockerCleanupResilienceRealContainer` 的 `t.Cleanup` 兜底清理）。
- `services/workshop/workshop/runner_redact_test.go`（新增）— W-7 的正式探针复现测试 + 边界表驱动测试。
- `doc/workshop.md` — W-5：新增"容器回收在高负载下的韧性"小节。

## 4 没来得及做的

- 没有遗留任务。W-6 终审时 R2 提的"取消子用例没锁 false_green 重算"这条轻微缺口，工头/R2 已明确决定不修（其他子用例已覆盖同一函数），不是遗留，是已拍板的取舍。
- 工头在 pane 里提到过 F-4（生产权限清单未覆盖两个新 P1 workshop 方法，四个端到端脚本仍用旧权限列表）、F-5（生产 PID/内存限额 128/1GiB 未用真实模型验证过，只验证过 256/2GiB）——这两条是工头记的后续跟进项，**不在 workshop2 的施工图范围内**，未涉及、也没有能力核实是否已处理，转告下一个班子留意。

## 5 坑（下一个班子最该注意的）

- **"先替换再截断"的顺序是一类系统性漏洞**，不止 `runNative` 一处；这次审出来的是诊断缓冲区，检查别的地方（结果 text、stdout 预览、任何对已做过 secret 替换的字符串再做长度裁剪的代码）时要用同一个思路排查：替换会缩短字符串，缩短会挪动截断点，截断点可能正好落在一个还没被完整匹配上的 secret 局部重复片段里。这次的修法（`redactAndBound`/`secretPrefixSuffixLength`）只在 `runner.go` 一处生效，不是全局兜底。
- **测试里用零值当"未激活"状态极易造成假阳性测试**：`fakeContainer.State.Status` 留空字符串、`Running`/`Paused` 等留 false，看起来是"没激活"，但如果被测代码本身的判断逻辑被删掉一部分，测试可能因为零值本身就满足"安全"分支而继续判绿——即测试测的不是你以为的那条逻辑。这类测试必须显式把无关字段设成一个明确的"正常但仍需要那一条判断才能救回来"的值（这次是 `Status="exited"` + 单独一个布尔字段 true），并且必须做破坏验证（删掉判断，确认变红）才能相信它。W-5 初审、W-7 施工图追加，都是同一个坑第二次被抓出来，值得写进通用测试规范。
- **资源闸门是硬约束**：主机 4 核 15GB 无 swap，根盘紧张；任何 `go test`/容器测试/`npm`/`docker build` 必须经 `CREW_SEAT=<seat> /tmp/crew/acl-530e/bin/heavy <cmd>`，细则见 `/tmp/crew/acl-530e/board/resource-rules.md`。每笔交付的全量 `-race` 只跑一次，真容器测试和全量分开跑，跑完清理 `/tmp` 临时目录——不遵守会把主机拖到不可用。
- **不要用 `AskUserQuestion` 等交互式工具等待后台任务**——pane 是无人值守的，交互式提问框会把 pane 卡在 `blocked`，只能工头手动 Esc 解除。等后台任务用 `Monitor` 或被动的任务通知；有疑问用 `herdr-msg` 问工头。这是本班子实际踩过的坑，不是假设。
- **提交说明只写本笔改了什么**，不要在新提交里描述之前提交做过的事——曾经在 W-6 的提交信息里错误地把 `fd480ab` 做的事写进了 `c97378b` 的说明，工头指出后没有 amend（正确做法是保持既有提交不变，下次写准确），下一个班子也应遵守"不 amend、只在下一笔写清楚"的规则。
- **改了归属表之外的文件要在 worklog"偏差"里明确说明**（如 W-6 改了 `service.go` 两处字面量），不要默默扩大改动范围；施工图"明令不做"的边界（如本次不动 relay 鉴权/token 生成方式）要逐条对照，不要因为顺手就多改。
- **真容器测试要留意计数器按什么维度累积**：曾经在 W-5 的真容器测试里用"按容器名"计数注入失败次数，但一个测试跑两个任务会各自创建新名字的容器，导致第二个任务意外继承了"未被注入过失败"的状态而使断言失败——如果类似测试要模拟"前 N 次失败"，用跨整个测试的全局计数器，不要按名字/按对象分别计数，除非确实是有意如此。

发生了什么、坑在哪，就到这里。今天的活儿谢谢工头。
