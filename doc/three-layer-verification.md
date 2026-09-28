> 此文记录 `eba47cf` 的 Go 本地应用阶段。当前三服务 RPC 部署以 [services/README.md](../services/README.md) 和 [RPC 契约](../contracts/rpc-v1.md) 为准；旧服务命令和 Bearer 接线不适用于新端口。

# 三层改造验收记录 · 2026-09-25

在保留基线 `95131f1` 历史的任务分支上完成实现、独立审查、修复和集成验证。

| 验证 | 结果 |
|---|---|
| `go test ./...` | 通过 |
| `go test -race ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go mod tidy -diff` | 无差异 |
| 修改的 Go 文件格式及 `git diff --check` | 通过 |
| 独立临时 PostgreSQL 数据库中的会话与后台任务测试 | 通过 |
| 公共包依赖检查 | `pkg/ai`、`gateway`、`agentloop`、`workshop` 不依赖 Eino 或 `internal` 应用包 |

## 运行链路证据

- `internal/app/three_layer_integration_test.go`：实际应用组装、原生 Loop、HTTP 模型协议、工坊目录查询、会话归属、任务提交与查询、真实子进程生成文件、产物哈希、最终回答及历史提交。
- `internal/agent/chatmodel/remote_runtime_test.go`：应用连接独立网关；自定义 JSON 别名使用显式非流式模式，验证一次完整响应及工具循环。
- `pkg/gateway` 测试：Chat Completions、Responses、Anthropic 与自定义映射；流式工具参数、推理续轮状态、协议错误、断流、取消、缓存用量和配置计价。
- `internal/agent/chatmodel/gateway_test.go`：带签名的推理流经过消息合并、JSON 存储和读取后，下一次真实本机 HTTP 请求保留签名和工具 ID。
- `internal/agent/deepagent/gateway_integration_test.go`：取消实际运行会关闭阻塞中的上游 HTTP 请求，终态仍为取消。
- `pkg/workshop` 测试：原生 CLI 参数与标准输入、终态判定、取消进程组、超时、原生会话续跑、重启后的中断状态、产物路径限制、所有权、幂等与并发。
- `internal/tools/workshop_results_integration_test.go`：真实子进程产生大于工具传输上限的正文，状态与摘要仍可读取；分页可重建完整正文及 Unicode 内容，并可读取历史 attempt。
- `internal/usermemory/model_agent_test.go`：长期记忆提取与整合各只调用一次禁用工具的模型请求；模型错误、非法 JSON、意外工具调用及取消不被当作成功。

## 独立审查闭环

审查发现并复现了两个跨层问题，均保留回归覆盖并由集成人重新执行：

1. Anthropic 的工具块若只有初始 `{}` 而没有参数增量，完整响应与流式事件不一致。修复在工具块完成时补发未发送的完整参数；同时覆盖 Responses 的同类路径，不重复发送已有增量。
2. 工坊允许的正文可能超过对话工具的响应上限。修复为有明确截断标记的摘要、分页任务列表和按 UTF-8 边界读取完整正文；保持传输上限，不静默丢失内容。

## 验证边界

协议服务与 CLI 输出均使用确定性的本机测试夹具；子进程、HTTP、文件和 PostgreSQL 操作实际执行。未调用真实付费模型，未运行 Docker 沙箱集成测试，未部署生产服务。

PostgreSQL 初次复验误用了已运行基线测试的临时数据库：遗留队列记录被下一次 claim 选中，导致现有测试清理等待。保留了该失败记录，并改为会话、后台任务各用一个全新可丢弃数据库后通过。测试实例已停止；本次没有修改持久化表结构。

原始日志和 crew 施工图、审查、交接记录归档在执行机器的 `/home/ubuntu/reports/easygo-three-layer-20260925/crew.tar.gz`。功能与限制见 [架构说明](three-layer-architecture.md)、[网关](ai-gateway.md)、[Loop](agent-loop-native.md) 和 [工坊](workshop.md)。
