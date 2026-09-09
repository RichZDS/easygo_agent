# 用户级长期记忆：一手资料调研与落地建议

日期：2026-09-09。范围：为 EasyGo 的 CLI（TUI）和 HTTP Gateway 设计同一套用户级长期记忆；本文件是设计调研，不修改产品代码。

## 结论

应把“会话历史”“会话压缩摘要”“用户长期记忆”“不可绕过的产品策略”明确分成四层。

1. 现有 `agent_sessions.context_messages` 和 `agent_turns.messages` 继续是会话事实来源；压缩只能改变本轮模型上下文，不能删除原始审计历史。
2. 新建**独立的记忆数据库**和 `MemoryStore`，以已完成的会话为只读输入，写入可追溯、可过期、可删除的用户记忆。Gateway 和 TUI 都只能通过同一个 `MemoryRuntime` 检索、注入和记账，不能各自读写一份文件或表。
3. 记忆是“用户上下文”，不是系统规则。注入时必须标明“仅在不与系统、开发者、安全和当前用户请求冲突时采用”；权限、数据隔离、敏感操作限制仍须由代码/网关强制执行。
4. 定时归档应异步、只处理已稳定完成的对话、按上下文上限分批，并以来源会话/轮次 ID 做幂等。每天 03:00 是合适的调度时间，但不应假设一次任务一定完整覆盖所有用户。

这与 Codex 的后台异步抽取及“提取模型 + 整合模型”分工、Claude Code 的短索引 + 按需主题文件、以及 OpenCode “压缩上下文但保留持久历史”的做法一致。见后文的逐项证据。

## 外部产品的可复用事实

### Codex

Codex 的本地记忆默认关闭；开启后会从合格的既往聊天异步生成记忆，跳过仍在进行或过短的会话，并从生成字段中排除机密信息。它等待聊天闲置后再处理，接近速率限制时也可能跳过后台生成。[官方记忆文档](https://learn.chatgpt.com/zh-Hans/docs/customization/memories)

它将摘要、持久化条目、近期输入和既往聊天的佐证放在 `~/.codex/memories/`，并明确把它们定位为“生成的状态数据”，而非人工维护的规则文件。该文档同时提供了独立的“是否可生成记忆”和“是否使用记忆”开关，以及外部上下文（MCP/网页/工具搜索）参与过的聊天不用于生成记忆的配置项。[官方记忆文档](https://learn.chatgpt.com/zh-Hans/docs/customization/memories)

**可用设计要点：**采用异步抽取、候选会话资格、敏感信息过滤、抽取/整合两个模型配置和生成/使用两个独立开关；把来源证据与最终记忆分开存。不要把生成的长期记忆作为唯一的强制规则来源；Codex 也建议把必须遵守的团队规则放进 `AGENTS.md` 或受版本控制的文档。[官方记忆文档](https://learn.chatgpt.com/zh-Hans/docs/customization/memories)

Codex 的 `AGENTS.md` 是另一层项目指令：启动时从全局到项目路径收集，离工作目录更近的文件在合并提示中排在后面；其全局位置默认是 `~/.codex`，并且有总字节上限。[官方 AGENTS.md 文档](https://learn.chatgpt.com/es-419/docs/agent-configuration/agents-md)

### Claude Code

Claude Code 区分人工写的 `CLAUDE.md` 与自动记忆：前者存指令/规则，后者存从纠正和偏好中得到的学习；两者都只是上下文，并非硬执行机制。自动记忆列出的分类包括 `user`（角色、专业和偏好）与 `feedback`（纠正和确认过的方法），正好对应本需求中的用户偏好和“不要这样做”的经验。[官方 memory 文档](https://code.claude.com/docs/en/memory)

每个项目的自动记忆有独立目录，包含一个每次启动加载的 `MEMORY.md` 索引和按主题按需读取的文件；启动只加载该索引的前 200 行或 25 KB。主题文件不会在启动时全部灌入上下文，写入带 YAML frontmatter 的文件时会记录 ISO 8601 的 `modified` 时间。记忆文件不随会话转录清理而删除，直到用户或 Agent 编辑/删除它们。[官方 memory 文档](https://code.claude.com/docs/en/memory)

其自定义子 Agent 还明确支持 `user`、`project`、`local` 三种持久记忆作用域；其中 `user` 目录跨项目，而 `project` 是默认建议。子 Agent 的启动提示只预装 `MEMORY.md` 的前 200 行/25 KB，并被要求在过大时整理索引。[官方 subagent memory 文档](https://code.claude.com/docs/en/sub-agents)

**可用设计要点：**用一个很小的“索引/用户画像”承载总览，详情按类型拆分，按需检索而非把整库塞给模型；每条记录都要有更新时间、类别、来源和人工可审计/删除的能力。Claude 也明确说明指令文本不是强制层，必须强制的事情应使用真正的客户端/服务端控制。[官方 memory 文档](https://code.claude.com/docs/en/memory)

### OpenCode

OpenCode V2 把持久指令、技能、MCP 和会话上下文分开组合，并把指令源值作为持久化 delta 保存；`AGENTS.md` 用于长期项目指引。它在每次模型请求前组合这些来源，嵌套指令首次发现后只注入一次并记录在持久会话历史中。[官方 Instructions 文档](https://opencode.ai/v2/docs/instructions)

它的 compaction 会用“结构化摘要 + 最近上下文尾部”替代**活动模型上下文**，但明确不删除较早的持久会话消息；摘要记录目标、需求、决定、已完成/进行中工作、阻塞点、下一步、文件和额外上下文。[官方 Compaction 文档](https://opencode.ai/v2/docs/compaction)

OpenCode 的官方规格还把“持久事件/投影消息”和流式文本、推理、工具输入 delta 区分：后者是短暂的，不应为了重放而膨胀持久存储；已接收的输入、消息 ID、时间和投影可幂等重放。[官方 V2 schema changelog](https://github.com/anomalyco/opencode/blob/dev/specs/v2/schema-changelog.md)

**可用设计要点：**EasyGo 当前“原始 `agent_turns` + 可能压缩后的 `context_messages`”的分层应保留。记忆作业应消费完成的持久轮次，绝不消费 SSE 文本片段或未完成的工具链；压缩摘要不能被误当成用户长期记忆。

### OpenCloud 的边界

本次核验到的 OpenCloud 官方资料将它定义为应用基础设施和面向 ChatGPT/Claude/Codex 的状态化 MCP/CLI 接口，不定义编码 Agent 的用户长期记忆格式或召回策略。其 MCP 会话只保存有限的连接/入职状态，OAuth token 仍是请求级，不应借它推断长期用户记忆设计。[官方 MCP 文档](https://docs.opencloud.ai/getting-started/mcp)

因此 OpenCloud 可以承载独立数据库和 cron，但不能作为本功能的记忆语义参考；真正可借鉴的同类实现是上面的 Codex、Claude Code 和 OpenCode。

## 推荐的领域划分

| 层 | 唯一事实来源 | 生命周期 | 送入模型的方式 |
| --- | --- | --- | --- |
| 会话原文 | 现有会话数据库的 `agent_turns.messages` | 会话保留策略 | 当前会话需要时，或被归档任务分批读取 |
| 会话压缩 | `agent_sessions.context_messages` | 会话级、可重建 | 仅用于继续该会话 |
| 用户长期记忆 | 新的专用 memory database | 独立的保留、过期、删除策略 | 检索后最多注入预算内的 Top 5 |
| 五槽用户画像 | memory database 为源，Storage 中的 Markdown 为物化副本 | 每次 consolidation 生成新版本 | 总览；必要时在详情前注入 |
| 系统/安全策略 | Gateway、鉴权、工具权限和系统/开发者提示 | 部署配置 | 不依赖任何用户记忆 |

这里的“用户”必须是认证后的 canonical principal ID，而不是当前 Gateway URL 中可由请求者随意填写的用户名。现有 README 已说明该 URL 用户名只是模板级命名空间、公开部署需由可信网关认证绑定身份；长期记忆读写必须沿用这个可信绑定，不能让任意调用方指定别人的 `user_id`。

## 独立数据库和字段建议

使用单独的 `memory_dsn`/独立数据库，而不是在会话库中追加表。记忆服务只拥有该数据库的写权限；归档 Worker 对会话库仅有只读权限。这符合“专门一个数据库存记忆”的隔离要求，也让删除、保留和备份可独立管理。

建议以以下表作为最小模型（名称可按 Go 包约定调整）。所有时间均使用 `timestamptz`，由数据库 `now()` 生成。

### `user_memories`

一行是一个可检索的、原子化的长期事实，而不是一整段对话摘要。

| 字段 | 用途 |
| --- | --- |
| `id uuid`、`user_id text` | 记忆和隔离主体；`(user_id, fingerprint)` 唯一 |
| `kind` | `preference`、`style`、`prompt_pattern`、`agent_instruction`、`habit`、`correction`、`error`、`reference` 等受限枚举 |
| `title`、`content`、`normalized_content`、`fingerprint`、`tags text[]` | 给人看、给模型看、去重及检索；`normalized_content` 不直接注入 |
| `confidence`、`importance`、`user_confirmed` | 仅作入库阈值和冲突处理，不偷偷改变题设的 50/50 召回权重 |
| `status` | `candidate`、`active`、`stale`、`suppressed`、`deleted`；检索只取 `active` |
| `source_first_seen_at`、`source_last_seen_at` | 该事实第一次/最后一次在会话中被观察到的时间；时效评分用后者而非建行时间 |
| `created_at`、`updated_at`、`expires_at`、`superseded_by` | 审计、自然失效和新旧偏好替换 |
| `retrieval_count`、`last_retrieved_at` | 只在内容真正进入本次 Agent 提示后原子更新；不要把“候选查询”计作调用 |
| `redaction_state`、`sensitivity`、`delete_requested_at` | 秘密/敏感内容过滤、用户删除和审计 |

索引至少为 `(user_id, status, source_last_seen_at DESC)` 和 `(user_id, status, retrieval_count DESC)`；如采用全文/向量只用于候选预筛选，最终排序仍必须是下文定义的 50/50。

### 证据、召回与作业表

`memory_evidence` 保存 `memory_id`、`source_session_id`、`source_turn_id`、`message_ordinal`、经过脱敏的简短摘录/哈希、`observed_at`、`extractor_version`；这样每条画像和记忆都可回到会话原文解释“为什么有这一条”。

`memory_retrievals` 保存 `id`、`memory_id`、`user_id`、`session_id`、`run_id`、`rank`、`score`、`injected_at`、`created_at`。它既是调用次数审计，也是避免重试重复加计数的幂等键（例如 `(run_id, memory_id)` 唯一）。

`memory_consolidation_runs` 保存 `id`、`user_id`、`scheduled_for`、`source_high_water_turn_id`、`source_window_start/end`、`status`、`attempt`、`model_id`、`prompt_version`、`input_digest`、`started_at`、`finished_at`、`error`、`created_at`。`memory_consolidation_cursor` 保存每用户已成功消费的最后来源位置和 `updated_at`。这两个表使每天的任务可重试、可解释，而不是用“今天是否跑过”的布尔值。

`user_memory_settings` 保存 `user_id`、`generation_enabled`、`use_enabled`、`exclude_external_context`、`retention_days`、`updated_at`、`deleted_at`。生成与使用必须可分别关闭；借鉴 Codex 的外部上下文排除开关，默认应谨慎地不把含外部工具返回、密钥或第三方私密数据的会话拿去抽取。

### 仅五行的画像表和 Storage 副本

`user_memory_profiles` 使用 `(user_id, slot)` 唯一约束，并把 `slot` 限为五个值，因而每个用户最多五行：

1. `agent`：长期的协作方式、输出格式和明确要求；
2. `memory`：稳定的偏好、兴趣、背景与常提主题；
3. `experiment`：使用习惯、有效的交互模式和已验证做法；
4. `error`：用户纠正、反感、禁止重复的错误及其正确替代；
5. `prompt`：常用提示词模板、常用任务结构与风格偏好。

每行至少有 `id uuid`、`user_id`、`slot`、`version bigint`、`content_markdown`、`source_memory_ids uuid[]`、`generated_at`、`updated_at`、`last_retrieved_at`、`retrieval_count`、`status`、`created_at`。`Error` 中应记录“错误行为 + 用户期望的替代行为”，不应原样保存辱骂或无关攻击文本。

数据库是 source of truth。成功事务后，将对应版本物化到：

```text
Storage/Long-term Memory/User Profiles/<profile-id>/
  Agent.md
  Memory.md
  Experiment.md
  Error.md
  Prompt.md
```

每个文件应有不可注入的元数据头（`profile_id`、`slot`、`version`、`generated_at`、来源记忆 ID），并由数据库版本控制。Storage 写失败不应让数据库回滚到旧画像；应把该物化标为待修复，由重试任务按版本重建。运行时以数据库为准，不能因某个 Markdown 文件被手动编辑而越权改变记忆。

## 召回、排序与提示注入

对每次 TUI 输入和 Gateway `runs`，在加载会话上下文后、调用主 Agent 前执行同一个 `MemoryRuntime.Retrieve(userID, sessionID, input)`：

1. 校验 canonical user ID，检查 `use_enabled`，过滤 `active`、未删除、未过期且已通过敏感性策略的记忆；
2. 可用当前输入做候选预筛选（关键词/向量/类别），但不能改变最终权重定义；
3. 用数据库当前时间计算并只返回 Top 5，受一个固定 token/字符总预算限制；
4. 创建 `memory_retrievals` 后才增加 `retrieval_count` 和 `last_retrieved_at`；同一 `run_id` 重试不能重复加；
5. 把选中内容连同 `id`、`kind`、`source_last_seen_at` 组装为结构化的“用户长期上下文”，再追加到模型请求。不得把它拼入系统策略，也不得允许记忆覆盖当前用户输入。

题设要求时间和调用次数各 50%。定义为：

```text
time_score      = exp(- age_days(source_last_seen_at) / half_life_days)
frequency_score = ln(1 + min(retrieval_count, frequency_cap))
                  / ln(1 + frequency_cap)
score           = 0.5 * time_score + 0.5 * frequency_score
```

`half_life_days` 与 `frequency_cap` 是版本化配置（建议初始 90 天和 20 次），两个子分都落在 `[0, 1]`。排序为 `score DESC, source_last_seen_at DESC, id ASC`，以取得稳定结果；`confidence`、`user_confirmed` 和敏感性只决定是否有资格被召回，不额外偷加第三个排名权重。对于有明确过期日的临时偏好，时间到期即不参与排名。

这会产生“常用记忆更常被选中”的正反馈，正是调用次数参与权重的结果。因此界面/日志必须展示选中 ID、两个子分、总分和计数；用户能禁用、删除或确认某条记忆。可以把已确认的强偏好设为不失效，但不应暗中改写上述 50/50 公式。

画像文件适合做紧凑索引，而不是额外再把全部五个文件全文灌进每次请求。推荐先按任务只取关联 slot 的短摘要，再取上述 Top 5 原子记忆；需要细节时由 Agent 经受控的 `get_memory(id)` 读取。Claude Code 的“短索引启动加载、主题文件按需加载”正是为了避免长期上下文吞噬会话预算。[官方 memory 文档](https://code.claude.com/docs/en/memory)

## 每日 03:00 consolidation Agent

调度器以配置时区（生产环境明确设为 `Asia/Shanghai`）每天 03:00 创建 job。Job 本身不是一个无限上下文的 Agent；它是下面的可恢复管道，模型仅负责受控抽取和整合：

1. 在记忆数据库为用户领取 job（每用户互斥锁/唯一运行记录）。从会话库读取在启动时确定的 `source_high_water_turn_id` 之前、状态为 `completed`、已闲置超过稳定窗口的 `agent_turns`；运行中、失败、取消或后写入的轮次不处理。
2. 按 `session_id, turn_id` 固定排序，依消息和令牌预算切块。块保存起止来源 ID、内容哈希和字数；绝不按 SSE token 片段切割。超过预算时继续下一块，直到高水位，不丢弃后段。
3. 用无工具的“抽取 Agent”逐块输出严格 JSON 候选：`kind`、原子 `claim`、证据指针、置信度、是否长期有效、是否包含敏感数据、与既有记忆的关系（新增/增强/冲突/取代）。模型不能直接写数据库或 Storage。
4. 规则层先拒绝密钥、凭据、第三方隐私、一次性任务和无证据候选；随后“整合 Agent”将所有合格候选与该用户现有 active 记忆对照，合并同义项、标记冲突/过期项，并按明确的候选重要性取本轮最多 Top 5 入库。这里的 Top 5 是**当天新增/更新的长期记忆候选**，不等于运行时按 50/50 召回的 Top 5。
5. 在一个记忆数据库事务中 upsert `user_memories`/`memory_evidence`，生成或更新五个 profile slot，推进 cursor，并完成 run。之后异步物化五个 Markdown；物化失败可重试且不会重复抽取。

会话库和独立记忆库不能做隐式跨库事务，所以采用“至少一次读取 + 基于来源证据唯一键的幂等写入”。崩溃后从未推进的 cursor 重新读是安全的；不能仅靠“已读标记”防重复。OpenCode 的持久输入先入库、稳定消息 ID 和幂等重试的分离，是这一做法的有用参照。[官方 V2 schema changelog](https://github.com/anomalyco/opencode/blob/dev/specs/v2/schema-changelog.md)

## 归档、删除与隐私边界

* 会话原文的归档/删除与长期记忆分别执行。原文删除后，关联 `memory_evidence` 可以保留不可还原的哈希和最小元数据，或按用户设置级联清除可读摘录。
* “忘掉 X”应立即把匹配 `user_memories` 设为 `deleted`、从画像下一版本移除、停止召回，并排入 Storage 物化清理；不要等待凌晨任务。
* 新偏好与旧偏好冲突时保留 `superseded_by` 链路，而非静默覆盖。没有新证据的老条目可先转 `stale`，而不是凭模型猜测删除。
* 记忆抽取输入、候选 JSON、模型版本、提示版本和敏感信息扫描结果应保留在 job 审计中；生产日志不记录完整用户文本。
* 外部工具返回、网页内容和可疑 prompt-injection 文本默认不成为长期记忆；这也是 Codex 提供 `disable_on_external_context` 的实际价值。[官方记忆文档](https://learn.chatgpt.com/zh-Hans/docs/customization/memories)
* 压缩/回滚不等于删除。OpenCode 的文档明确区分“活动投影被移除”和“持久会话历史仍存在”；EasyGo 的删除接口也应对两个存储层给出明确语义。[官方 Snapshots 文档](https://opencode.ai/v2/docs/snapshots)

## “真实 Agent 达到 95%”的可验收定义

“让真实 Agent 跑一次感觉正确”不能证明 95%。实现完成后，固定模型版本、提示版本、温度和排名参数，用真实抽取/整合模型跑一套**预先人工标注**的保密评测集；模型不得自己给自己打分。

建议至少 200 个样本，覆盖：稳定偏好、风格、常用提示、纠正与错误、同一偏好的重复证据、相互冲突的新旧偏好、短期信息、密钥/第三方隐私、带外部工具上下文、跨用户隔离、过期、删除、TUI 与 Gateway 同时调用、重试和长对话多块归档。

一个样本仅在以下条件全部满足时计为成功：

* 应提取的长期事实被正确分类并可追溯到正确的会话/轮次；不应存的短期、敏感或第三方内容未入库；
* 对给定新输入，要求的记忆在 Top 5 内，排序分数严格等于 50/50 公式，且没有其他用户、被删除、已过期或被取代的内容；
* 用户画像恰好不超过五个 slot、Storage 版本对应数据库版本，Gateway 与 TUI 获得同一结果；
* 用真实主 Agent 生成的回复遵从正确偏好/纠正，但不把记忆当作高于系统和当前用户请求的指令。

通过门槛设为端到端成功率 `>= 95.0%`（至少 190/200），同时要求跨用户泄露、秘密入库、删除后仍注入、以及重复 job 造成计数双增这四类严重错误为 0。把失败样本按“抽取、合并、排序、注入、模型遵从”拆分报告；否则一个 95% 总数无法定位该修哪里。

## 对本仓库的下一步实现顺序

1. 先定义 memory domain/Store 接口和独立数据库迁移，再把 Gateway/TUI 都接到同一检索入口；不先做 Storage 文件直读。
2. 为现有 `agent_turns` 增加只读、按用户/完成状态/稳定时间分页的归档查询接口；保持其原生 JSONB 审计语义不变。
3. 实现幂等的检索记账、50/50 SQL/Go 排名和删除/禁用开关，并为 TUI/Gateway 写同一组契约测试。
4. 再实现 03:00 Worker、分块抽取、整合和五槽画像物化，最后用上述真实模型评测集执行 95% 门槛。

