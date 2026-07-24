# Agent 对话系统架构优化设计

**日期：** 2026-07-24  
**状态：** 已评审通过（阶段 A 详细设计）  
**范围：** 按 A → B → C 分阶段优化；本文详细定义阶段 A，并预告 B/C。

## 1. 背景与问题

当前仓库存在两条互不联通的对话执行路径：

1. **生产路径：** `TurnService` + Outbox + `StreamWorker` + `ExecutionService`（裸 `net/http` 调 OpenAI 兼容 `/chat/completions`），经 SSE 推事件。
2. **未完工路径：** `AgentChatService`（TODO，无路由）与 `internal/agent`（eino ChatModel / DeepAgent / agentic_model），几乎未被主流程调用。

另有分层问题（CRUD API 与 Turn 编排混挂、Controller 直连 Redis Stream、伪流式、无工具调用），按优先级放到后续阶段。

## 2. 目标与阶段划分

| 阶段 | 目标 | 本 spec |
|------|------|---------|
| **A** | 统一执行路径：唯一模型调用内核 + 删除双实现/死代码 | **详细设计** |
| **B** | 理清分层边界：CRUD vs Turn、Service 拆分、SSE 下沉 | 预告 |
| **C** | 补齐 Agent 能力：真流式、工具调用、DeepAgent 循环 | 预告 |

### 已确认决策（阶段 A）

- 保留 Turn / Outbox / StreamWorker / SSE 编排骨架，只替换 `ExecutionService` 的模型调用内核。
- 只接入 **ChatModel**（单轮非流式 completion）；不启用 DeepAgent / 工具循环。
- 抽象 **OpenAI 兼容 ChatModel** 适配层，覆盖全部已配置供应商，**彻底删除**裸 HTTP `complete()`。
- 删除 `AgentChatService` 及 Wire/Controller 悬挂引用；保留 `deep_agent` / `agentic_model` / DeepSeek `chat_model`，标明未接入，供阶段 C 使用。
- **对外行为冻结**：Turn API、SSE 事件名与 payload、失败码与文案保持不变。

## 3. 阶段 A：包结构与职责

```
internal/agent/
├── chatmodel/                 # 唯一执行内核（新增）
│   ├── completer.go           # ChatCompleter、Message、Usage、CompatConfig
│   ├── openai_compat.go       # eino-ext openai 实现
│   └── factory.go             # Factory.NewCompleter
├── chat_model/deepseek.go     # 保留，标明未接入
├── agentic_model/             # 保留，标明未接入
├── deep_agent/                # 保留，标明未接入
└── config.go                  # DeepSeek 专用配置；保留给未接入组件

internal/service/chat/
├── execution.go               # 骨架不变；complete → ChatCompleter
├── turn.go                    # 不动
└── agent_chat.go              # 删除

internal/controller/init.go    # 去掉 AgentChat
internal/wire/                 # 去掉 NewAgentChatService；注入 Factory
```

| 单元 | 职责 | 依赖 |
|------|------|------|
| `chatmodel.Factory` | 按凭证+模型参数创建 Completer | `eino-ext/components/model/openai` |
| `ChatCompleter` | 单次非流式补全，返回 content / usage / finishReason | 无 DB / 业务知识 |
| `ExecutionService` | Turn：上下文 → Complete → 落库 → 发事件 | Factory、DB、cache、cipher |
| `TurnService` / `StreamWorker` / SSE | 编排与投递 | 不变 |

**刻意不做（B/C）：** 拆 TurnService、SSE 出 Controller、真流式、工具调用、DeepAgent。

## 4. 阶段 A：接口、数据流、错误映射

### 4.1 接口

```go
type Message struct {
    Role    string // system | user | assistant | tool
    Content string
}

type Usage struct {
    PromptTokens     uint32
    CompletionTokens uint32
    TotalTokens      uint32
}

type ChatCompleter interface {
    Complete(ctx context.Context, messages []Message) (content string, usage Usage, finishReason string, err error)
}

type CompatConfig struct {
    APIKey    string
    BaseURL   string // 已解析的最终 URL
    Model     string
    MaxTokens int
    Timeout   time.Duration // 默认 90s
}

type Factory interface {
    NewCompleter(ctx context.Context, cfg CompatConfig) (ChatCompleter, error)
}
```

### 4.2 BaseURL 解析（对齐现网，在 ExecutionService 侧完成）

| 条件 | 结果 |
|------|------|
| `user_model_config.base_url` 非空 | 使用该值（trim 尾 `/`） |
| 空且 `provider_name == "deepseek"` | `https://api.deepseek.com/v1` |
| 其它空 | `https://api.openai.com/v1` |

工厂只接收已解析的 `CompatConfig.BaseURL`，避免 `agent` 包依赖 GORM 模型。

### 4.3 单轮数据流（对外事件不变）

```
ProcessTurn
  → 查 turn / config / session / credential，解密 API Key
  → contextMessages（逻辑不变）
  → append 当前 user input
  → Factory.NewCompleter(CompatConfig)
  → Completer.Complete
       成功 → emit delta{content} → persistCompletion → emit completed
       失败 → fail(PROVIDER, "model request failed")
  → 配置/凭证失败仍用 MODEL_CONFIG / SESSION / CREDENTIAL
```

- 每个 Turn 创建 Completer，用完即弃；不做跨 Turn / 跨用户缓存。
- `persistCompletion` 行为不变：user + assistant 消息、更新 session、turn=completed、双写 Redis。
- Completer 使用 eino openai 的 **Generate**（非 Stream）；不解析 `tool_calls`。

### 4.4 错误映射

| 内部情况 | 对外 Turn |
|----------|-----------|
| Factory / Completer 创建失败 | `PROVIDER` + `"model request failed"` |
| Complete 网络 / 非 2xx / 空 content | 同上 |
| 解密等 | 保持现有 CREDENTIAL 等码与文案 |
| 落库失败 | `return err`（不 ack，可 reclaim） |

阶段 A 不新增对外 `error_code`；返回错误前用 zap 记录底层 `err` 与安全相关参数。

## 5. 阶段 A：删除清单与 Wire

| 动作 | 目标 |
|------|------|
| 删除 | `internal/service/chat/agent_chat.go` |
| 删除字段 | `controller.AllControllers.AgentChat` |
| Wire | 去掉 `chat.NewAgentChatService`；重新生成 `wire_gen.go` |
| 注释 | `chat_message.go` 中指向 AgentChatService 的注释改为实际调用方 |
| 删除 | `execution.go` 裸 HTTP：`complete`、request/response 结构体、`http.Client` 字段 |
| 保留并标明未接入 | `chat_model/deepseek.go`、`agentic_model/`、`deep_agent/`、`agent/config.go` |
| 不动 | Turn API、Outbox、StreamWorker、SSE 协议、CRUD 路由 |

装配：

```
NewOpenAICompatFactory() → chatmodel.Factory
NewExecutionService(db, cipher, cache, factory)
```

## 6. 阶段 A：测试与验收

### 测试

- **单元：** BaseURL 解析表驱动；用 fake `ChatCompleter` / mock Factory 测 `ProcessTurn` 成功与 PROVIDER 失败路径。
- **适配层：** 消息转换与空 content 错误（httptest 或 seam，不依赖真网）。
- **回归：** Turn 创建 → Worker → SSE：事件顺序仍为 `status(running)` → `delta` → `completed`（或 `failed`）。

### 验收标准

1. 唯一模型调用路径：`ExecutionService` → `chatmodel.ChatCompleter`；无裸 `chat/completions` 客户端代码。
2. `AgentChatService` 及悬挂引用清除，工程可编译。
3. 任意 OpenAI 兼容供应商（含 DeepSeek）经同一 Factory 完成一轮 Turn。
4. 对外 Turn/SSE 事件与失败文案与改前一致。
5. `deep_agent` / `agentic_model` / DeepSeek chat_model 仍在仓库，带「未接入」标注，无生产调用点。

## 7. 阶段 B / C 预告（不在本实现范围）

### 阶段 B — 分层边界

- 收敛或鉴权约束会话/消息 CRUD，避免绕过 Turn 状态机。
- SSE / Redis Stream 细节从 Controller 下沉到 Service（或专用 gateway）。
- 拆分 `TurnService`：生命周期、Outbox 发布、摘要压缩职责分离。
- 清理示例性 `TimeTask` 与真实 `StreamWorker` 的调度关系。

### 阶段 C — Agent 能力

- 真流式：Completer/Agent 流式输出 → 多次 `delta` 事件。
- 工具调用与 `tool_call` / `tool_result` 消息落库。
- 接入 `deep_agent`（ResumableAgent）多步循环；Completer 可演进为 Agent Runner。

## 8. 非目标（全阶段共同）

- 不更换 MySQL / Redis / Gin / Wire / zap 技术栈。
- 不在阶段 A 修改对外 HTTP 路由契约。
- 不引入第二套任务队列替代现有 Outbox + Redis Stream。
