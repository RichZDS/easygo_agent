# ai-gateway

Go 服务，默认端口 8441。它持有主 Agent Loop 使用的模型供应商密钥。工坊 CLI 使用独立配置的执行凭证。调用方拿不到这些密钥；agent-loop 只通过 mTLS RPC 调用 `gateway.models` 和 `gateway.generate`。

镜像在仓库根目录构建：

```sh
docker build -f services/ai-gateway/Dockerfile .
```

构建阶段使用 `golang:1.25-bookworm`，把 `packages/rpc-go` 放到 `/src/packages/rpc-go`，把本服务放到 `/src/services/ai-gateway`，再执行 `go build -trimpath -o /out/server ./cmd/server`。运行阶段是 `debian:bookworm-slim`，装了 `ca-certificates`，进程是 UID 1000。入口是 `/usr/local/bin/server --config /run/easygo/config.json`。Go 的 flag 同时接受 `-config`。

系统 CA 只用于访问模型供应商的 HTTPS。和其他两个服务之间的连接仍是配置里的 mTLS，不另开明文端口。

Compose 挂载：

- `$EASYGO_PKI_DIR/ai-gateway` → `/run/easygo/identity:ro`
- `$EASYGO_PKI_DIR/public` → `/run/easygo/trust:ro`
- `services/ai-gateway/config.example.json` → `/run/easygo/config.json:ro`

传入容器的供应商变量是 `OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、`CUSTOM_API_KEY`。真正读哪一个，看配置里 `models.*.api_key_env`。这个目录没有数据卷。

启用计费时，配置里的 `meter` 块有两个可选字段，控制本地计费库的清理：

- `retention_seconds`：已结算的请求保留多久，默认 604800（7 天）。保留期内同一个 `request_id` 仍按重复请求拒绝；回执还没被钱包确认的请求不会被清。
- `authorizing_timeout_seconds`：钱包授权没走完的请求过多久清掉，默认 3600（1 小时）。

两个字段缺省或为 0 时用默认值，负数启动报错。回执积压数、刷新错误有变化或有记录被清掉时，stderr 会多一行 `{"kind":"meter",...}`，只含计数、错误文案和时间。

共享的启动、证书和备份说明在 [`../README.md`](../README.md)。本机没有 Docker，这个镜像没有构建。
