# EasyGo Agent 后端

EasyGo 的对话执行层基于 Eino ADK。`ChatModelAgent`、`Runner`、`AgentEvent`、模型重试和上下文总结均由 Eino 提供；EasyGo 只负责 HTTP 投影、业务生命周期和 MySQL 持久化。

当前固定使用 Eino `v0.9.13`，仅允许注册过的 Eino model adapter：

- DeepSeek：`eino-ext/components/model/deepseek`
- OpenAI：`eino-ext/components/model/openai`
- MiniMax：通过 Eino OpenAI-compatible adapter，必须显式配置 `base_url`

不存在自研 Provider HTTP fallback。

## 对话执行模型

一次 Turn 在单个 `POST /api/v1/sessions/:id/turns:stream` 请求内完成：

1. MySQL 事务锁定 Session，创建 running Turn 和已完成的 user Message。
2. 为本 Turn 创建 Eino Model、summarization middleware、`ChatModelAgent` 和 `Runner`。
3. `Runner.Run` 产生的 `AgentEvent` 直接投影为 SSE。
4. Eino 输出合并为 `schema.Message`，随后在 MySQL 事务中写入 assistant/tool Message 和 Turn 终态。

客户端断开连接会取消请求上下文并将 Turn 标记为 cancelled。不提供远程取消、后台 worker、Redis 事件流、重连或事件回放。一个 Session 同时只允许一个 Active Turn；不同 Session 可以并行。

失败或取消前产生的部分 assistant 输出会持久化并对用户可见，但不会进入后续模型上下文。对话历史只从 MySQL 读取；达到输入预算 80% 时，由 Eino 原生 summarization middleware 在当次运行内压缩上下文，摘要不单独持久化。

## 启动

默认读取 `configs/config.yaml`，并从 `backend/.env` 加载密钥：

```powershell
cd backend
Copy-Item .env.example .env
docker compose up -d
go run .
```

`.env` 必填项：

| 变量 | 说明 |
|---|---|
| `EASYGO_JWT_HS256_SECRET` | JWT 签名密钥，至少 32 字符 |
| `EASYGO_CREDENTIAL_KEK_V1` | 凭证加密密钥，Base64 编码的 32 字节随机值 |

新安装按顺序应用 `002`、`003`；如果环境已应用过 `002`，只执行 `003`：

```powershell
mysql -u root -p easygo_agent < sql/002_user_model_async_chat.sql
mysql -u root -p easygo_agent < sql/003_eino_native_turns.sql
```

`003` 保留旧的 `chat_outbox` 和 `chat_summary` 物理表一个版本，但新代码不再读写它们。

服务默认监听 `http://localhost:8080`：

- `/healthz`：进程存活检查
- `/readyz`：MySQL 就绪检查

## 配置

```yaml
mysql:
  host: 127.0.0.1
  port: 3306
  user: root
  password: "password"
  database: easygo_agent
  charset: utf8mb4
  max_idle_conns: 10
  max_open_conns: 100
  conn_max_lifetime: 3600

skills:
  root_dir: skills
  readme_src: README.md
  max_zip_bytes: 5242880
  max_extracted_bytes: 20971520
  max_files: 200
```

配置使用严格 YAML 字段校验。Skill 上传默认限制为 ZIP 5 MiB、解压后 20 MiB、最多 200 个普通文件。

## Skills

启动时，服务会将 `skills/builtin-src/` 原子同步到运行态 `skills/builtin/`。内置 `easygo-agent-skill` 引用当前项目 README，供模型在回答框架、技术栈和架构问题时自主加载。

每个 Turn 都按认证用户重新构建 Skill catalog，并且只向模型开放 `ls`、`read_file`、`glob`、`grep` 四个文件工具。模型不能通过这些工具写入或编辑 workspace。

登录后可管理当前用户的私有 Skill：

```bash
curl -X POST http://localhost:8080/api/v1/skills \
  -H "Authorization: Bearer $TOKEN" \
  -F "skill_id=my-skill" \
  -F "file=@my-skill.zip"

curl http://localhost:8080/api/v1/skills \
  -H "Authorization: Bearer $TOKEN"

curl -X DELETE http://localhost:8080/api/v1/skills/my-skill \
  -H "Authorization: Bearer $TOKEN"
```

ZIP 根目录或唯一顶层目录内必须包含 `SKILL.md`。Frontmatter 的 `name` 必须与 `skill_id` 一致，`description` 不能为空；当前仅支持 inline skill，不接受 `context`、`agent` 或 `model` 覆盖。内置和已有同名 Skill 不会被覆盖。

上传或删除成功后，同一 Session 的下一 Turn 会立即使用新的 catalog，无需重启服务。已经开始执行的 Turn 不保证动态切换 Skill 视图。

## SSE 协议

执行端点返回 Eino 风格事件：

- `message`
- `stream_chunk`
- `tool_calls`
- `tool_result`
- `tool_result_chunk`
- `action`
- `error`

EasyGo 额外发送 `turn` 事件表示 `running`、`completed`、`failed` 或 `cancelled`。协议不发送 SSE `id`，也不接受 `Last-Event-ID`。

相同 Session 下重复提交 `request_id` 返回 HTTP 409，并携带已有 `turn_id` 和状态，不回放历史输出。

## 代码结构

```text
internal/
├── agent/          # Eino adapter registry 与每 Turn runtime factory
├── controller/     # HTTP/SSE 投影
├── model/          # Eino-aligned Message 与 EasyGo 持久化 envelope
├── service/chat/   # Turn 事务边界与 AgentEvent 消费
├── skill/          # manifest、workspace、原子同步与用户 Skill Store
└── platform/       # MySQL、日志等基础设施
```

架构术语见 [CONTEXT.md](CONTEXT.md)，关键决策见 [ADR 0001](docs/adr/0001-use-eino-runner-for-synchronous-turn-execution.md)。

## 验证

```powershell
go test ./...
go vet ./...
```
