# EasyGo Eino Agent Template

这是一个用于复制或 fork 的 Go Agent 模板，不是完整 Agent 平台。

模板只演示以下基础接法：

- Bubble Tea 终端交互；
- transport-neutral Gateway；
- Eino 原生 classic ReAct Agent；
- 单个 OpenAI-compatible 模型；
- Eino 原生 Calculator Tool；
- 可选 OpenTelemetry stdout tracing。

## 非目标

本项目不提供 HTTP/RPC API、登录鉴权、模型权限、额度、数据库、持久化记忆、MCP、Skill 系统、任务调度、多模型路由或通用插件内核。衍生项目应根据自己的真实需求添加这些能力。

## 架构

```text
TUI adapter ---> Gateway ---> Eino ReAct
                   |             |-- OpenAI-compatible model
future adapter ----+             `-- Calculator Tool
                   |
                   `-----------> OpenTelemetry callbacks
```

Gateway 隐藏 Eino message、stream 和 callback 细节。TUI 只依赖稳定的请求与事件；未来项目可以在同一个 Gateway 外增加 HTTP、gRPC 或消息队列 adapter。

## 环境要求

- Go 1.25 或更高版本；
- 支持 Tool Calling 的 OpenAI-compatible 模型；
- 对应模型服务的 API Key。

## 配置

程序只读取 `configs/config.yaml`：

```yaml
agent:
  system_prompt: You are a helpful assistant.
  max_steps: 8

model:
  name: gpt-4.1-mini
  base_url: ""
  timeout: 120s
  apikey: "{EASYGO_AGENT_API_KEY}"

tracing:
  enabled: false
  exporter: stdout
```

API Key 在 YAML 中通过 `{ENV}` 引用环境变量；环境变量可从项目根目录 `.env` 加载：

```bash
cp .env.example .env
```

```bash
EASYGO_AGENT_API_KEY='replace-with-a-disposable-key'
```

程序只校验运行必需字段，不执行用户归属、provider 状态、模型白名单、权限或额度判断。

## 启动

```bash
go run main.go
```

按键：

- `Enter`：提交输入；
- 生成中 `Ctrl+C`：取消本次运行；
- 空闲时 `Ctrl+C`：退出。

对话历史只存在当前进程。退出程序后历史丢失；失败或取消时的 partial output 会保留在屏幕上，但不会进入下一轮模型上下文。

## Calculator Tool

模板固定注入一个 Eino 原生 Tool：

```text
calculator(operation, a, b)
operation = add | subtract | multiply | divide
```

它不访问网络、文件系统或数据库，用于展示 Tool schema、ReAct 调用、流式事件和确定性测试。

## Tracing

将 `configs/config.yaml` 改为：

```yaml
tracing:
  enabled: true
  exporter: stdout
```

程序会输出 `agent.run`、模型和 Tool spans。默认不记录 API Key、prompt、response、Tool 参数或 Tool 输出。衍生项目可以在应用装配层把 stdout exporter 换成 OTLP。

## 验证

```bash
go test ./...
go test -race ./...
go vet ./...
```

自动测试使用 fake model，不访问真实模型或外部网络。真实模型 smoke test 需要显式提供一次性 API Key。

## 旧凭证安全提醒

旧版本曾在 tracked `configs/config.yaml` 中提交远程 MySQL 地址和明文口令。本次删除文件不会清除 Git 历史；凭证所有者必须在数据库侧轮换该口令。本重构不改写 Git 历史。
