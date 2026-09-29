# L-3 · harness 场景库

assignee: loop · 状态: done（efacf6d + 24fe1ac；工头已签 51dadc9）（分两段：W-1 集成后先做 S1/S2/S6/S7/S8；W-2 集成后做 S3/S4/S5/S9。工头发简令开工）

## 现场 / 锚点
- `scripts/test-platform.mjs`：它会起真实的三个服务、专用 daemon 和夹具任务镜像，照它的写法来。
- `scripts/platform-model-fixture.mjs`：模型夹具，用来按脚本发出工具调用。
- fixture-cli 的 `crew` 模式和 `fixture-check`：由 workshop 席在 W-1、W-2 提供。脚本接口说明：通道类看 `/tmp/crew/acl-530e/workshop/w1-fixture-interface.md`，验收类（fixture-check 子命令、write-denied、acceptance 配置）看 `/tmp/crew/acl-530e/workshop/w2-fixture-interface.md`。都以集成后的代码为准。

## 修法
新建 `scripts/test-harness.mjs`，每个场景一个函数，各自写证据文件到 `--evidence` 目录，最后输出汇总 `report.json`。

**调用链必须完整走一遍**：模型夹具 → loop 工具 → 工坊 → 任务容器 → easygo-crew → 事件 → loop 工具读回。判定用 RPC 直接取回的数据，不看模型输出的文字。

至少包括这些场景：

| # | 场景 | 断言 |
|---|---|---|
| S1 | 施工者依次发 report、ask、report、submit(pass) | loop 读到 4 条，顺序对、id 唯一，不重不漏 |
| S2 | 静默退出（exit 0，什么都没报） | `outcome=none` |
| S3 | 坏产物，并且 submit 自报 pass | acceptance=failed、false_green=true；能用 `workshop_evidence` 读到失败输出 |
| S4 | 好产物 | acceptance=passed；证据里的 `workspace_sha256` 与测试脚本独立重算的结果一致 |
| S5 | 施工者试图改检查脚本，检查容器试图连网 | 检查结果不受影响；证据里写明连网失败 |
| S6 | 施工者 ask 后退出 | `outcome=asked`；模型 `workshop_reply` 加 `workshop_resume` 之后，新 run 的输入里有该消息 id，并且记为已读 |
| S7 | 运行中由 loop 回复 | 容器里 inbox 能收到，施工者据此 submit |
| S8 | 同一个 client_id 发两次 | 只记一条事件 |
| S9 | 检查期间取消 | 任务 cancelled，acceptance cancelled，没有残留的检查容器 |

## 明令不做
- 不接真实模型。
- 不把场景写成只测工坊或只测 loop 的单测，这些各自的单测里已经有了。

## 测试
- 在工头给的集成快照上连续跑两遍，都要全绿。
- 故意改坏一个断言，比如让 S3 期望 passed，确认脚本判红。改坏的版本不提交，只在报审里写明做法和结果。

## 交付
- 在 `feat/acl-loop` 上单独提交一笔，同时在 `services/README.md` 里写上运行方式。
- worklog 报审，证据放在 `/tmp/crew/acl-530e/loop/`。
- 然后 herdr-msg 通知工头。
