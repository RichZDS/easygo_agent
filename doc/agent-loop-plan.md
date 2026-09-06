# Agent loop 实施计划

按现有依赖，将需求中的 ANNO / PG Circle 分别解释为 Eino / PostgreSQL。

## 目标与顺序

- [x] 原生消息存储：`schema.AgenticMessage` JSONB，原始轮次记录与压缩后的上下文分别保存。
- [x] PostgreSQL 会话模块：用户名隔离、会话列表、历史分页、同会话互斥、原子提交；内存实现用于开发与测试。
- [x] Eino loop：加载上下文 → 输入 → 每次模型调用前检查/压缩 → 模型 → 工具 → 再检查 → 最终回答。
- [x] 独立摘要 Agent：复用 Eino summarization 的触发、提示词和 finalize；摘要 Agent 无工具、不递归压缩。
- [x] 共用 runtime：流式 UI 事件与原生历史分离；失败和取消留存轮次记录，不将不完整工具链放入后续上下文。
- [x] CLI 用户/会话恢复、单次命令；HTTP 会话接口与 SSE 运行接口，无前端。
- [x] fake model 回归测试、PostgreSQL 集成测试入口、配置和运行文档。

## 决策

保持现有 Deep Agent 构造入口，关闭模板不需要的内置 todo 和通用 task 工具；业务工具由 Eino 自动循环执行，以无工具调用的最终回复结束，max_steps 防止无限循环。

不创建自定义聊天消息类型。自定义结构仅承载会话身份、轮次状态和面向 CLI/HTTP 的展示事件。保存框架最终 state 的 Messages，工具调用、结果、推理块和 usage 保留原生字段。

PostgreSQL 为默认持久化后端。使用按会话 advisory lock（持有专用连接，无长事务），轮次结束时短事务写入记录与上下文。内存后端显式选择，作为可运行的开发模式；先不加 Redis，避免多实例缓存失效和会话一致性复杂度。

HTTP 默认仅监听 loopback；用户名是模板级会话命名空间，不是认证身份。公开部署需在可信网关完成身份认证和用户名绑定。

## 验证重点

工具调用后再次调用模型、流式 chunk 原生合并、摘要后跨轮恢复、取消/错误不污染上下文、并发会话隔离、同会话冲突、存储失败不得报告完成、HTTP 断开取消、历史分页。

## 验证记录（2026-09-06）

`go test ./...` 与 `go vet ./...` 通过。已用临时 PostgreSQL 16.9 实例执行 `TestPostgresContract`，真实 JSONB 读写与 advisory lock 验证通过，实例测试后关闭。

`go test -race ./...` 在当前环境无法运行：Go 的 `CGO_ENABLED=0`，且未找到 C 编译器。未调用真实模型服务；CLI 的 OpenAI-compatible 接法通过本地 HTTP 测试服务器验证。
