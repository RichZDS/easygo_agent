框架与模型选择的新入口见 [运行配置说明](../../doc/workshop-runtimes.md)。网关复用模式无需把供应商密钥传入 CLI；原独立 Codex 示例仍保留。

# CLI 工坊服务

Go 编写，默认 HTTPS RPC 端口 8443。持久化工作流快照、任务、原生 CLI 会话、事件和产物，支持取消、续跑及结果分页。调用方法见 [RPC 契约](../../contracts/rpc-v1.md)。

```bash
cd services/workshop
go test ./...
go run ./cmd/server --config /absolute/path/to/config.json
```

配置包括 `listen`、`tls`、`authorization` 和 `workshop`。托管部署显式设置 `workshop.sandbox.mode="docker"`，并把控制器内的 `root` 映射到 Docker daemon 可见的 `sandbox.host_root`；启动时会验证映射。完整配置和离线容器验证命令见 [任务容器说明](../../deploy/runtime/README.md)。

```bash
# 仓库根目录
docker build -f services/workshop/Dockerfile -t easygo-workshop-controller:local .
docker build -f deploy/runtime/Dockerfile -t easygo-task-runtime:local .
```

控制器镜像只包含 Go 服务和 Docker 客户端；Docker socket 与服务证书只挂载给控制器。独立任务镜像安装 Node 26、Python、git，以及固定版本 Codex 0.157.1、Claude Code 2.1.281、Pi 0.87.1 和 OpenClaw 2026.9.6。构建参数为 `CODEX_VERSION`、`CLAUDE_CODE_VERSION`、`PI_VERSION`、`OPENCLAW_VERSION`；更改版本后需验证 CLI 参数和事件兼容性。每次调用创建一个 UID 1000、无外部网络、只读根文件系统且有资源上限的容器，只挂本任务工作区和一次性模型 relay socket。

旧的非托管本机示例通过 allowlist 接收 `CODEX_API_KEY`；Docker 模式禁止该路径，所有 runtime 必须使用 gateway profile。CLI 默认家目录位于每个任务工作区的 `.workshop-home`，随数据卷持久化；不使用宿主登录目录。进程退出码为 0 仍不足以认定成功，必须收到引擎终态并核验配置的产物。

部署、权限、证书和验证边界见 [服务说明](../README.md)。
