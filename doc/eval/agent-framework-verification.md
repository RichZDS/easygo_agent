# Agent framework 验证记录

日期：2026-09-18。基线：`de4c85d`（本次 fetch 的 origin/main）。分支：Agent framework 功能分支（已合并）。

## 已执行

| 检查 | 结果 |
| --- | --- |
| `go test ./...` | 通过；未配置的独立长期记忆数据库和 Docker 集成按现有约定跳过 |
| 相关包 `go test -race` | task、conversation、runtime、deepagent、app、tools、toolregistry、skill、gateway、tui 通过 |
| `go vet ./...` | 通过 |
| `git diff --check` | 通过 |
| 5 个专项 SKILL.md 的 quick_validate | 全部通过 |
| 独立 PostgreSQL 14 上显式 task/conversation 集成测试 | 通过；任务测试各自创建并清理临时 schema |

Go 工具链使用本机 `/tmp/easygo-toolchain/go/bin/go`。HTTP 测试在允许本地监听端口的环境执行。PostgreSQL 为本次任务创建的一次性本地实例，没有访问业务数据库。

## 行为证据

- 模型输出保存后中断：恢复调用原批次，保持原生调用 ID。
- 写工具返回后、结果保存前中断：进入 blocked，禁止自动重复写。
- 写工具结果保存后中断：直接复用，副作用调用次数保持 1。
- 只读和幂等工具允许恢复重试，幂等调用收到稳定键。
- 未知恢复策略视为不安全；提示词/skill/工具配置版本改变时阻塞。
- 显式补录已核实结果后，同一任务 ID 的下一版本完成；上一版本记录保留。
- 8 个并发 claim 只产生一个成功领取者，失效 claim token 不能提交。
- 运行中拒绝续跑；跨用户读取失败；父 run 取消级联取消任务。
- 两个后台 worker 的并发上限生效；主会话可以在子任务执行期间继续处理输入。
- 服务停止释放子任务租约；正常父轮完成不取消已提交任务；内部汇总被停机打断后重新排队。
- PostgreSQL 通知入队后、outbox 标记前中断：重新派发复用同一个 run。
- 汇总提交前 acknowledged=false，提交后 acknowledged=true；确认与 turn 在同一事务。
- 端到端确定性模型测试：主 agent 调用 spawn_subagent，子任务完成后自动汇总一次，模型输入包含最新用户约束和独立的内部通知来源。
- HTTP 事件按 seq 补读，TUI 不重复展示同一进度或汇总。
- YAML 多行描述、参考资源、路径越界、逃逸符号链接、二进制、超大文件和缺失文件均覆盖。
- 压缩保留发现入口、任务控制和未完成摘要。旧预算夹具已为保留的工具 schema 分配空间；不再靠删除这些能力达到预算。

## 真实模型评测限制

已扩展 `cmd/eval/skill`：增加显式触发、多 skill 组合和参考文件读取，检查实际加载的 skill 名；记录成功用例、工具次数、模型次数与输入消息字节，输出 Markdown 和 JSON，支持 `-baseline` 对比同名用例。

实际执行 `go run ./cmd/eval/skill -out /tmp/easygo-framework-live-eval.md` 在配置加载阶段返回：

```text
skill-eval: resolve model apikey: environment variable MODEL_API_KEY is required
```

当前没有 `MODEL_API_KEY`、`SUBMODEL_API_KEY` 或仓库 `.env`。因此没有真实模型成功率、加载正确率或前后成本数据；确定性测试不作为这些指标的替代。提供凭证后可用 `configs/eval/skill.yaml` 运行并保存真实报告。
