# 托管平台：部署、对账与运维

托管模式在三个核心服务之外只多一样东西：由工坊控制器拉起的一次性任务容器。对外只暴露 Web（由 TS agent-loop 进程提供）；网关、工坊和 agent-loop 之间仍是带证书权限的 mTLS RPC，不对宿主外开放。

| 组件 | 镜像 / Dockerfile | 持有的秘密 | 数据 |
|---|---|---|---|
| agent-loop + Web | `services/agent-loop/Dockerfile` | 服务私钥、bootstrap 管理员密码（仅首次） | `agent.sqlite`（会话/运行）、`platform.sqlite`（账户/钱包/账本）、`knowledge.sqlite` |
| ai-gateway | `services/ai-gateway/Dockerfile` | 服务私钥、模型供应商 key | `meter.db`（bbolt：在途预留与结算 outbox） |
| workshop 控制器 | `services/workshop/Dockerfile` | 服务私钥、专用 Docker socket | 任务库 + 每任务工作区/HOME |
| 任务容器（每次执行一个） | `deploy/runtime/Dockerfile` | 无 | 只挂本任务工作区和本次执行的模型 UDS |

## 1. 前置条件

- **专用 Docker daemon**。工坊控制器拿到 socket 就等于拿到宿主 root 级能力，不要复用跑着其他业务的 daemon。控制器只会清理带自己 owner 标签的容器。
  验收时用的是 `--bridge=none --iptables=false --ip6tables=false --ip-masq=false` 的测试 daemon。它证明了 Compose 内部网络和 `127.0.0.1` 端口可用，但**没有证明核心容器能出网访问模型供应商**，不能直接当生产模板；生产 daemon 需要正常的出网能力（任务容器本身始终是 `--network none`）。
- 构建镜像（仓库根目录为 build context）：

```bash
export DOCKER_HOST=unix:///path/to/dedicated/docker.sock
docker build -f services/ai-gateway/Dockerfile -t easygo-ai-gateway:platform .
docker build -f services/agent-loop/Dockerfile -t easygo-agent-loop:platform .
docker build -f services/workshop/Dockerfile   -t easygo-workshop:platform .
docker build -f deploy/runtime/Dockerfile      -t easygo-task-runtime:platform .
```

任务镜像的标签在控制器启动时解析成不可变镜像 ID，之后任务一律用该 ID，不自动拉取。换运行时镜像要重启工坊。

## 2. 首次部署

1. 生成配置与开发 PKI（目录必须是新的，文件以 `wx` 创建，不会覆盖已有 state）：

```bash
node scripts/configure-platform.mjs --state /srv/easygo/state \
  --origin https://agent.example.com --admin-email ops@example.com
```

它写出 `gateway.json`、`loop.json`、`workshop.json` 和 `pki/`，不读也不写任何密码或 key。`--origin` 以 `https:` 开头时自动打开 Secure cookie。state 路径不能含符号链接，而且**必须就是 Docker daemon 看到的宿主路径**：工坊按 `host_root` 把任务工作区 bind 给任务容器，启动时会实际探测一次映射，对不上就拒绝启动。`scripts/dev-pki.sh` 生成的是开发 CA，生产请换成自己的 CA 签发，文件名保持一致。

2. 模型路由：生成器把 `services/ai-gateway/config.deepseek.example.json` 的模型表写进 `gateway.json`，供应商 key 只从环境变量 `DEEPSEEK_API_KEY` 读。改用其他供应商时编辑 `gateway.json` 的 `models`。

3. 启动：

```bash
export EASYGO_PLATFORM_STATE=/srv/easygo/state
export EASYGO_PLATFORM_PORT=8090                 # 只绑 127.0.0.1
export EASYGO_DOCKER_SOCKET=/path/to/dedicated/docker.sock
export EASYGO_DOCKER_GID=$(stat -c %g "$EASYGO_DOCKER_SOCKET")
export EASYGO_ADMIN_PASSWORD=...                 # 仅首次创建管理员时需要，>=12 字符
export DEEPSEEK_API_KEY=...
docker compose -f compose.platform.yaml up -d
```

4. **管理员初始化**：agent-loop 首次启动时用 `bootstrap_admin.email` + `EASYGO_ADMIN_PASSWORD` 创建管理员。管理员已存在后，该变量不再被读取，可以从环境里删掉；它也不能把已有普通用户静默提升为管理员。公开注册的账户一律是普通用户、余额 0。

5. 冒烟：浏览器打开 origin，用管理员登录，注册一个测试用户，给它发少量积分，发一条消息，确认钱包页出现一条已结算的用量。仓库里的 `scripts/test-platform-compose.mjs` 和 `services/agent-loop/test/platform-live-browser.mjs` 只适用于**夹具模型**部署（`configure-platform.mjs --fixture-url`），不要对真实供应商部署运行。

## 3. HTTPS

Web 进程只说 HTTP，TLS 在反向代理终止：

- `public_origin` 必须是浏览器实际访问的 `https://` origin，所有写请求都校验 `Origin` 严格相等；
- `secure_cookies` 必须为 true（生成器按 origin 自动设置）；
- Compose 只把 Web 绑到 `127.0.0.1`，代理在同机转发；不要把 8441/8442/8443 暴露给代理或公网。

限速默认按真实 socket 地址计算，不信任 `X-Forwarded-For`。放在代理后面时，要把代理地址写进 platform 配置的 `trusted_proxies`（`configure-platform.mjs --trusted-proxy IP`），否则所有用户共享同一个地址的额度（登录每分钟 20 次、全部请求每分钟 300 次）。语义与 Caddy/nginx 示例见 [platform-web.md](platform-web.md)。

**Compose 部署要写网络网关地址，不是 127.0.0.1**：发布端口经 docker-proxy 转发，Web 容器看到的对端永远是 Compose `core` 网络的网关。`compose.platform.yaml` 把该网络固定为 `EASYGO_PLATFORM_SUBNET`（默认 `172.31.250.0/24`）、网关 `EASYGO_PLATFORM_GATEWAY`（默认 `172.31.250.1`），与宿主已有网段冲突时两者一起改。于是同机反向代理的配置是：

```bash
node scripts/configure-platform.mjs --state /srv/easygo/state \
  --origin https://agent.example.com --trusted-proxy 172.31.250.1
```

这样做的前提是 Web 端口只绑 `127.0.0.1`（Compose 默认如此），只有宿主本机进程能从该网关地址连进来；不要把端口改绑到公网地址后还保留这个白名单。

## 4. 积分与费率

- 默认费率：输入 + 输出每 1000 token = 1 积分；1 积分 = 1,000,000 microcredits，全部整数运算。缓存 token 是输入 token 的子集，不重复计费。
- 管理员 `GET/PUT /api/admin/tariff` 查看/修改费率。费率有版本；预留时快照当时的版本，之后按该版本结算，改价不影响已在途的请求。
- 发积分 `POST /api/admin/credits {user_id, amount_micros, reason, idempotency_key}`，同一幂等键重放不重复入账。没有接支付，也没有自动充值。
- 网关在调用供应商**之前**预留：请求序列化字节数 + 1024 作为输入估计，加上最大输出（默认 4096）。余额不足直接拒绝，不产生供应商调用。预留是保守估计，不是 tokenizer 报价。

## 5. 对账

管理员「待核对」列表（`GET /api/admin/usage/pending`）列出仍冻结着积分的请求，状态有两种：

| 状态 | 怎么产生 | 含义 |
|---|---|---|
| `pending` | 网关拿不到可信用量：供应商连接中断、网关崩溃后重启、客户端断开导致流被取消 | 供应商可能已经算了钱，平台不知道算了多少 |
| `reserved` | 正常在途请求（几秒内会自己结算）；或网关在预留成功、记下「已激活」之前崩溃（极窄窗口） | 长时间停在这里的，同上，且网关侧没有记录 |

处理：`POST /api/admin/usage/resolve {namespace, request_id, decision, usage?, reason, idempotency_key}`。

- `decision:"release"`：确认供应商没计费，解冻、不扣费；
- `decision:"settle"`：必须带 `usage:{known:true,input_tokens,output_tokens,...}`，按预留时的费率快照扣费；
- decision 只接受字符串字面量，其他类型（数组、对象等）一律 400，不改动账本；
- 每次处理都写入不可变审计记录。处理之后再到达的供应商回执只保存原始用量作为证据，不会二次扣费或再次解冻。

**没有超时自动退款**：待核对的积分会一直冻结，直到管理员处理。建议每天看一次待核对列表，并对照供应商控制台的用量。已知的超额使用（实际用量超过预留）照实扣费，允许余额为负，之后不再放行该账户的新请求。

## 6. 故障恢复（实测行为）

以下由 `scripts/test-platform-faults.mjs` 在真实进程和真实容器上用 SIGKILL 注入验证：

| 故障 | 恢复方式 | 账务结果 |
|---|---|---|
| agent-loop（钱包）在 CLI 原生调用途中崩溃 | 网关把供应商回执写进 bbolt outbox，每秒重试结算，钱包回来后送达 | 恰好扣一次 |
| agent-loop 在主链流式调用途中崩溃 | 网关看到客户端断开，按「用量未知」记为 pending；该 run 重启后标记为 `interrupted`，不重放 | 积分冻结，待管理员核对，结算后恰好一次 |
| ai-gateway 在向供应商发出请求后崩溃 | 重启时把在途记录转成 `uncertain` 回执送给钱包 | 积分冻结，出现在待核对列表 |
| workshop 在任务容器运行中崩溃 | 重启时回收自己标签的孤儿容器，任务标记 `interrupted`，需用户显式 resume | 不重放、不计费 |
| 三个服务正常重启 | — | 钱包、用量记录、历史完全一致 |

**agent-loop 崩溃后约 30 秒才能重新启动**：数据库持有者租约为 30 秒，崩溃留下的租约到期前，新进程会以 `database_owned` 退出。这是有意的 fail-closed 设计，防止两个进程同时写库。Compose 的 `restart: unless-stopped` 会自动重试；实测从被杀到恢复服务约 24–28 秒。正常 `docker compose stop` 会释放租约，没有这个等待。

## 7. 备份与恢复

state 目录里的数据库**互相引用，必须作为一个整体备份和恢复**：

- 网关 `meter.db` 的待结算回执 ↔ `platform.sqlite` 的预留记录；
- `agent.sqlite` 的记忆 outbox ↔ `knowledge.sqlite` 的已处理回合；
- 工坊任务库 ↔ `workshop/workspaces/` 下的工作区。

只回滚其中一个会造成错配，例如网关对已不存在的预留反复重试结算，或者丢掉已完成回合的记忆提取。推荐冷备份：

```bash
docker compose -f compose.platform.yaml stop      # 网关退出前会尽量冲刷 outbox
tar -C /srv/easygo -czf easygo-state-$(date +%F).tgz state
docker compose -f compose.platform.yaml start
```

停止后先确认待核对列表的状态，恢复到旧快照时要预期：快照之后发生的扣费、发放和对账会丢失，需要按供应商用量和审计记录人工补录。`pki/` 里的私钥要单独、加密保管；不要把 state 打包进仓库或工单附件。

## 8. 隔离边界与已知限制

任务容器：`--network none`、只读根文件系统、UID 1000、去掉全部 capabilities、`no-new-privileges`、PID/内存/CPU/tmpfs 上限；只挂本任务工作区和本次执行的模型 UDS，看不到 Docker socket、服务私钥和供应商 key。只读工作流的工作区挂载本身就是只读的。

仍然存在的限制，部署前要清楚：

- **不是 VM 隔离**。任务容器与宿主共享内核，内核漏洞可以越界。处理不可信用户的生产环境应把工坊 daemon 放进独立 VM，或换成 gVisor/Kata 类运行时。
- **持久工作区的磁盘上限是控制器侧配额**（`sandbox.disk_quota_bytes` / `disk_quota_files`）：单文件有硬上限（RLIMIT_FSIZE），总量靠轮询统计，超出时终止任务。两次轮询之间可以短暂超额，超额量取决于写入速度 × 轮询间隔。需要硬性总量上限时，把工坊数据目录放在支持 project quota 的 XFS 上，或放进独立 VM/磁盘。
- **容量没有测过**。验收的并发与 1 小时 soak 用的是单机、8 个客户端、夹具模型，而且单 IP 每分钟 300 次请求的限速本身就会限制单机压测。那些数字只说明这台机器在该负载下没有泄漏、错账或崩溃，不代表生产吞吐。
- 平台自己不做供应商侧限流和熔断；上游 429/5xx 按次失败，会产生待核对或释放记录。

## 9. 从旧版迁移

- **记忆与技能**：旧 Go 版导出的八类 Markdown profile 和 `<name>/SKILL.md` 技能目录，可以通过受控的操作员导入（先预览、再提交）写入新 Knowledge 库，见 [platform-knowledge.md](platform-knowledge.md)。**不会**迁移旧 PostgreSQL/Eino 的会话历史。
- **终端界面**：`cmd/easygo-remote` 复用旧 Bubble Tea 界面，登录托管平台的公开 API，身份由服务端绑定：

```bash
go run ./cmd/easygo-remote --url https://agent.example.com --email you@example.com
# 其他：--session ID 续接会话；--runtime NAME 指定工坊运行时；--sessions / --runtimes 列表后退出
```

- 原 `cmd/easygo-agent` 本地应用仍保留用于过渡和回归，不参与托管部署。

## 10. 验证命令

```bash
# 单元与集成
(cd services/agent-loop && npm run typecheck && npm test)
(cd services/ai-gateway && go test -race ./... && go vet ./...)
(cd services/workshop && go test -race ./... && go vet ./...)

# 真实三服务 + 专用 daemon 上的真实任务容器（夹具模型，不花钱）
export EASYGO_GO_BIN=$(command -v go) EASYGO_DOCKER_TEST_BINARY=$(command -v docker) \
  EASYGO_DOCKER_TEST_ENDPOINT=unix:///path/to/dedicated/docker.sock \
  EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:local   # deploy/runtime/Dockerfile.fixture
node scripts/test-platform.mjs                              # 完整链路
EASYGO_PLATFORM_SOAK_SECONDS=3600 node scripts/test-platform.mjs   # 1 小时 soak
node scripts/test-platform-faults.mjs                       # 崩溃/重启故障注入

# 已启动的夹具 Compose 部署
EASYGO_PLATFORM_ORIGIN=http://127.0.0.1:8090 EASYGO_REQUIRE_ARTIFACT=1 \
  EASYGO_ADMIN_PASSWORD=... node scripts/test-platform-compose.mjs
```

各次实测结果与证据位置见 [平台验收记录](platform-verification.md)。
