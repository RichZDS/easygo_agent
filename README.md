# EasyGo Agent 后端

基于 Gin 的精简后端骨架，包含 MySQL、Redis、统一响应体、错误码、结构化日志、健康检查和优雅停机。

## 启动

默认读取 `configs/config.yaml`：

```powershell
cd backend
go run ./cmd/server
```

也可以指定其他 YAML 文件：

```powershell
go run ./cmd/server -config configs/config.local.yaml
```

服务地址为 `http://localhost:8080`：

```powershell
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

- `/healthz`：检查 HTTP 服务是否存活。
- `/readyz`：检查 MySQL 和 Redis 是否均可用。
- MySQL 或 Redis 启动连接失败时，应用直接退出，不会假启动。

## YAML 配置

配置文件只保留 MySQL 和 Redis：

```yaml
mysql:
  host: 127.0.0.1
  port: 3306
  user: root
  password: "password"
  database: wiki
  charset: utf8mb4
  max_idle_conns: 10
  max_open_conns: 100
  conn_max_lifetime: 3600

redis:
  host: 127.0.0.1
  port: 6379
  password: "password"
  db: 0
```

`conn_max_lifetime` 的单位是秒。MySQL DSN 由驱动生成，特殊字符密码不需要手动转义。

配置加载器会拒绝未知字段、非法端口、空数据库名和错误的连接池参数。生产密码建议放进已忽略的 `configs/config.production.yaml`，不要提交到 Git。

## 目录

```text
backend/
├── cmd/server/                 # 程序入口
├── configs/                    # MySQL、Redis YAML 配置
├── internal/
│   ├── app/                    # 依赖装配与生命周期
│   ├── config/                 # YAML 加载与校验
│   ├── errorcode/              # 前后端业务错误码
│   ├── handler/                # HTTP Handler
│   ├── middleware/             # 日志、Recovery、CORS、Request ID
│   ├── model/                  # GORM Model 存放位置
│   ├── platform/               # Logger、MySQL、Redis
│   ├── repository/             # 数据持久层
│   ├── response/               # 全局响应体
│   └── server/                 # Gin 路由和 HTTP Server
└── compose.yaml
```

User Service 和 User API 已移除。`model` 与 `repository` 保留作为后续业务模型和持久层的存放位置。

## 统一响应体

```json
{
  "code": 0,
  "message": "success",
  "data": {},
  "request_id": "c43b4b09c68f1bf819dd573d32819edf"
}
```

运行检查：

```powershell
go test ./...
go vet ./...
```
