# CLI：个人客户端

`cmd/easygo-remote` 是 EasyGo 的命令行客户端。它复用旧 Bubble Tea 界面，登录托管平台的公开 API 使用，本地不运行任何 Agent。

## 定位

- **Web 是唯一的主控制台。** 注册、钱包、管理员功能都在 Web 上；以后的班子看板、消息、证据和放行按钮也只做在 Web 上。多人使用都走 Web。
- **CLI 只给个人用。** 登录者本人在终端里使用自己的会话、任务、记忆和技能。CLI 不做管理、不管钱包、不管注册，也不做班子的看板、消息、证据和放行。

依据见[定位与设计草案](agent-cluster-design.md) §9。

## 能做什么

| 用法 | 调用的公开方法 |
|---|---|
| 默认：新建会话，进入交互终端（发消息、看运行队列、Ctrl+C 取消） | `agent.session.create`、`agent.session.history`、`agent.run.start/get/events/cancel` |
| `--session ID`：续接自己的会话，恢复历史 | `agent.session.history` 和上面的 run 方法 |
| `--sessions`：列出最近会话后退出 | `agent.session.list` |
| `--runtimes`：列出工坊运行时后退出 | `agent.workshop.catalog` |
| `--rpc 方法名`：从 stdin 读一个 JSON 参数对象，调用记忆或技能方法 | 见下 |

`--rpc` 只接受这 10 个方法，其他方法名直接拒绝：`agent.memory.list/upsert/delete/consolidate/import`、`agent.skills.list/get/upsert/delete/import`。参数见 [platform-knowledge.md](platform-knowledge.md)。

CLI 只请求 `/api/login`、`/api/logout`、`/api/rpc` 三个路径，不访问注册、钱包、用量和 `/api/admin/*`。namespace 由服务端按登录会话绑定，客户端自带 namespace 会被拒绝。

密码默认用安全提示输入；`--password-env NAME` 从指定环境变量读取后立即清除，没有命令行密码参数。除本机回环地址外必须用 HTTPS，重定向一律拒绝。

## 与旧应用的关系

CLI 只由三个包组成：`internal/tui`（界面）、`internal/remotetui`（平台适配）、`internal/clientapi`（运行队列接口、run 记录、运行事件等共用声明）。旧应用的 `agentruntime` 和 `conversation` 用类型别名引用 `clientapi` 里的声明，CLI 不依赖旧应用的任何包；旧 Go 本地应用已于 2026-09-30 从主分支移除（代码在 tag `legacy-go-app-final`），CLI 不受影响。

检查方法：

```bash
go list -deps ./cmd/easygo-remote | grep '^easygo-agent'
```

输出只能是这三个包和 `cmd/easygo-remote` 本身。`cmd/easygo-remote` 的测试 `TestCLIDependsOnlyOnClientPackages` 做同样的检查，也覆盖这三个包的测试依赖。

## 验证

```bash
go test -race ./cmd/easygo-remote ./internal/tui ./internal/remotetui
```

真实终端验收见 [remote-tui-proof.md](remote-tui-proof.md)。
