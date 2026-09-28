# TypeScript Agent Loop 服务

独立 Node 22.23+ 服务，默认 HTTPS RPC 端口 8442。拥有自己的 SQLite 会话、FIFO 队列、原始消息与事件；通过 mTLS RPC 调用模型网关和工坊，不嵌入 Go Loop，也不持有模型密钥。

```bash
cd services/agent-loop
npm ci
npm run typecheck
npm test
npm run build
node dist/server.js --config /absolute/path/to/config.json
```

配置格式见 `config.example.json` 和 [RPC 契约](../../contracts/rpc-v1.md)。本机启动时将证书路径、数据库路径、下游 URL 改为本机实际位置。Docker 默认数据库为 `/data/agent.sqlite`，位于独立数据卷。

```bash
# 仓库根目录
docker build -f services/agent-loop/Dockerfile .
```

构建和运行使用 Node 22 镜像，并检查最低版本。生产安装无第三方运行时 npm 依赖；编译输出为 `dist/`。模型和工具完整消息在推进前持久化，流式增量保持 provisional；进程中断后的执行不会自动重放。

部署、权限、证书和验证边界见 [服务说明](../README.md)。
