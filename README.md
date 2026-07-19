# EasyGo Agent 后端

基于 Gin 的可扩展后端骨架，已经接入 MySQL、Redis、统一响应协议、业务错误码、结构化日志、请求链路 ID、健康检查和优雅停机。

## 快速启动

配置统一存放在 `configs/config.yaml`。建议复制一份本地配置，避免提交真实密码：

```powershell
cd backend
Copy-Item configs/config.yaml configs/config.local.yaml
docker compose up -d
go run ./cmd/server -config configs/config.local.yaml
```

如果直接使用仓库中的开发配置：

```powershell
go run ./cmd/server
```

验证服务：

```powershell
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl -Method Post http://localhost:8080/api/v1/users `
  -ContentType "application/json" `
  -Body '{"name":"Ada","email":"ada@example.com"}'
curl http://localhost:8080/api/v1/users/1
```

## YAML 配置

MySQL 和 Redis 均由 YAML 控制：

```yaml
mysql:
  dsn: "root:root@tcp(127.0.0.1:3306)/easygo_agent?charset=utf8mb4&parseTime=True&loc=Local"
  max_open_conns: 25
  max_idle_conns: 10
  conn_max_lifetime: 30m
  auto_migrate: true

redis:
  address: "127.0.0.1:6379"
  password: ""
  db: 0
  dial_timeout: 5s
  read_timeout: 3s
  write_timeout: 3s
```

启动参数 `-config` 可以选择不同环境的文件：

```powershell
go run ./cmd/server -config configs/config.production.yaml
```

解析器会拒绝未知字段、非法时间格式、空连接地址和错误的连接池配置，防止拼写错误被静默忽略。生产配置文件已加入 `.gitignore`，不应把真实密码提交到仓库。

## 目录职责

```text
backend/
├── cmd/server/                 # 程序入口和 -config 参数
├── configs/                    # YAML 配置文件
├── internal/
│   ├── app/                    # 依赖装配、生命周期管理
│   ├── config/                 # YAML 加载和校验
│   ├── contextx/               # 请求上下文公共值
│   ├── errorcode/              # 前后端稳定业务错误码
│   ├── handler/                # HTTP 参数解析和响应
│   ├── middleware/             # Request ID、访问日志、恢复、CORS
│   ├── model/                  # GORM 数据库模型
│   ├── platform/               # Logger、MySQL、Redis 基础设施
│   ├── repository/             # 数据持久化接口和实现
│   ├── response/               # 全局 HTTP 响应体
│   ├── server/                 # Gin 路由和 HTTP Server
│   └── service/                # 业务逻辑
└── compose.yaml               # 本地 MySQL 与 Redis
```

依赖方向是 `handler → service → repository → model`。Handler 不直接访问数据库，GORM Model 也不承担 HTTP 参数绑定职责。

## 统一响应协议

```json
{
  "code": 0,
  "message": "success",
  "data": {},
  "request_id": "c43b4b09c68f1bf819dd573d32819edf"
}
```

| code | 含义 | 默认 HTTP 状态 |
| ---: | --- | ---: |
| 0 | 成功 | 200 / 201 |
| 10001 | 参数错误 | 400 |
| 10002 | 未登录或登录失效 | 401 |
| 10003 | 无权限 | 403 |
| 10004 | 资源不存在 | 404 |
| 10009 | 资源冲突 | 409 |
| 20001 | 数据库错误 | 500 |
| 20002 | 缓存错误 | 500 |
| 50000 | 未分类内部错误 | 500 |

## 日志与迁移

- 控制台输出便于本地调试，文件日志使用 JSON 并按大小滚动。
- 请求自动生成或透传 `X-Request-ID`，响应、访问日志和 GORM 日志使用同一 ID。
- Panic 会记录堆栈并返回统一的 `50000` 响应。
- 开发环境可设置 `mysql.auto_migrate: true`；生产环境建议关闭并使用版本化迁移工具。

运行检查：

```powershell
go test ./...
go vet ./...
```
