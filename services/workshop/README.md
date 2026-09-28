框架与模型选择的新入口见 [运行配置说明](../../doc/workshop-runtimes.md)。网关复用模式无需把供应商密钥传入 CLI；原独立 Codex 示例仍保留。

# CLI 工坊服务

Go 编写，默认 HTTPS RPC 端口 8443。持久化工作流快照、任务、原生 CLI 会话、事件和产物，支持取消、续跑及结果分页。调用方法见 [RPC 契约](../../contracts/rpc-v1.md)。

```bash
cd services/workshop
go test ./...
go run ./cmd/server --config /absolute/path/to/config.json
```

配置包括 `listen`、`tls`、`authorization` 和 `workshop`。本机启动时使用本机可访问的证书与数据路径；Docker 示例使用 `/data/workshop`，整个目录都在工坊专属数据卷中。

```bash
# 仓库根目录
docker build -f services/workshop/Dockerfile .
```

镜像安装 Node 26、Python、git，以及固定版本 Codex 0.157.1、Claude Code 2.1.281、Pi 0.87.1 和 OpenClaw 2026.9.6。对应构建参数是 `CODEX_VERSION`、`CLAUDE_CODE_VERSION`、`PI_VERSION`、`OPENCLAW_VERSION`；更改版本后需验证 CLI 参数和事件兼容性。进程以 UID 1000 运行，不挂 Docker socket。

示例 Codex 引擎只接收 allowlist 中的 `CODEX_API_KEY`。CLI 默认家目录位于每个任务工作区的 `.workshop-home`，随数据卷持久化；不使用宿主登录目录。进程退出码为 0 仍不足以认定成功，必须收到引擎终态并核验配置的产物。

部署、权限、证书和验证边界见 [服务说明](../README.md)。
