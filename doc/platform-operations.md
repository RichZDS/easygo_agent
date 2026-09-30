# 托管平台：部署、对账与运维

托管模式在三个核心服务之外只多一样东西：由工坊控制器拉起的一次性任务容器。对外只暴露 Web（由 TS agent-loop 进程提供）；网关、工坊和 agent-loop 之间仍是带证书权限的 mTLS RPC，不对宿主外开放。

| 组件 | 镜像 / Dockerfile | 持有的秘密 | 数据 |
|---|---|---|---|
| agent-loop + Web | `services/agent-loop/Dockerfile` | 服务私钥、bootstrap 管理员密码（仅首次） | `agent.sqlite`（会话/运行）、`platform.sqlite`（账户/钱包/账本）、`knowledge.sqlite` |
| ai-gateway | `services/ai-gateway/Dockerfile` | 服务私钥、模型供应商 key | `meter.db`（bbolt：在途预留与结算 outbox） |
| workshop 控制器 | `services/workshop/Dockerfile` | 服务私钥、专用 Docker socket | 任务库 + 每任务工作区/HOME |
| 任务容器（每次执行一个） | `deploy/runtime/Dockerfile` | 无 | 只挂本任务工作区和本次执行的模型 UDS |

## 1. 前置条件

- **Linux 上的 Docker Engine（rootful）和 Docker Compose v2**（建议 2.20 以上）。工坊控制器按宿主路径把任务工作区 bind 给任务容器，rootless Docker 和 Docker Desktop 没有验证过。
- **最好给平台一个专用 Docker daemon**（`.env` 里的 `EASYGO_DOCKER_SOCKET`）。工坊控制器拿到 socket 就等于拿到宿主 root 级能力，不要复用跑着其他业务的 daemon。控制器只会清理带自己 owner 和数据目录标签的容器。
  验收时用的是 `--bridge=none --iptables=false --ip6tables=false --ip-masq=false` 的测试 daemon。它证明了 Compose 内部网络和 `127.0.0.1` 端口可用，但**没有证明核心容器能出网访问模型供应商**，不能直接当生产模板；生产 daemon 需要正常的出网能力（任务容器本身始终是 `--network none`）。
- `docker compose up -d --build` 会从仓库根目录构建五个镜像：

| 镜像 | Dockerfile | 说明 |
|---|---|---|
| `easygo-init:platform` | `deploy/init/Dockerfile` | 一次性初始化，每次 `up` 先运行 |
| `easygo-ai-gateway:platform` | `services/ai-gateway/Dockerfile` | |
| `easygo-agent-loop:platform` | `services/agent-loop/Dockerfile` | 同时提供 Web |
| `easygo-workshop:platform` | `services/workshop/Dockerfile` | |
| `easygo-task-runtime:platform` | `deploy/runtime/Dockerfile` | 任务镜像，约 2 GB，内含四个 CLI |

构建要下载 Go 模块、npm 包和 CLI。daemon 若以 `--bridge=none` 运行（如验收用的测试 daemon），先用 `docker build --network host -f <Dockerfile> -t <镜像> .` 按上表手动构建，再执行不带 `--build` 的 `docker compose up -d`：镜像已存在时 Compose 不会再构建。`--network host` 只用于受信的镜像构建，任务容器始终是 `--network none`。

任务镜像的标签在工坊启动时解析成不可变镜像 ID，之后任务一律用该 ID，不自动拉取。任务镜像重新构建后，`docker compose up -d` 会连带重启工坊。

## 2. 首次部署

1. **填 `.env`**：`cp .env.example .env`，至少填 `DEEPSEEK_API_KEY` 和 `EASYGO_ADMIN_PASSWORD`（至少 12 个字符）。其他变量都有默认值，说明见 `.env.example`：

| 变量 | 默认值 | 作用 |
|---|---|---|
| `EASYGO_ADMIN_EMAIL` | `admin@example.com` | 首次启动创建的管理员 |
| `EASYGO_PORT` / `EASYGO_BIND` | `8090` / `127.0.0.1` | Web 端口和绑定地址 |
| `EASYGO_PUBLIC_ORIGIN` | `http://localhost:<EASYGO_PORT>` | 浏览器实际访问的 origin，写请求严格校验 |
| `EASYGO_REGISTRATION` | `false` | 是否开放自助注册 |
| `EASYGO_DATA_DIR` | `./state/platform` | 数据目录，见第 7 节 |
| `EASYGO_DOCKER_SOCKET` | `/var/run/docker.sock` | 工坊使用的 daemon |
| `EASYGO_TRUSTED_PROXIES` | 空 | 反向代理地址，见第 3 节 |
| `EASYGO_TASK_MEMORY_MIB` / `_CPUS` / `_PIDS` | `2048` / `1` / `256` | 每个任务容器的上限 |
| `EASYGO_MAX_OUTPUT_TOKENS` | `32768` | 每次模型调用的输出上限，见第 4 节 |

2. **启动**：`docker compose up -d --build`。每次 `up` 都先运行一次性的 `init` 容器（root，无网络），它会：
   - 检查首次启动的管理员密码；
   - 在数据目录的 `pki/` 生成内部 CA 和四个服务身份（有效期 10 年；证书缺失或 30 天内到期时，下一次 `up` 会整套换新，旧的改名保留）。这是只在 Compose 网络内部使用的私有 CA，要换成自己的 CA 就按同样的文件名替换；
   - 通过 Docker API 查出数据目录在 daemon 上的真实路径，写进工坊的 `host_root`。工坊启动时会实际探测一次映射，对不上就拒绝启动。socket 不在本机时用 `EASYGO_HOST_DATA_DIR` 直接指定；
   - 按 `.env` **重写** `gateway.json`、`loop.json`、`workshop.json`，所以不要手改这三个文件。`.env` 改动会让 init 重建，Compose 随后重启三个服务，新配置生效；
   - 把数据目录交给容器用户 UID 1000。数据目录根、`pki/` 和 CA 私钥只有 root 能读。

   init 不读取也不写入模型 key，管理员密码只检查长度。工坊容器以 root 启动，只为读出 Docker socket 的属组，随即用 `setpriv` 降到 UID 1000 并丢掉全部 capability；三个服务都带 `no-new-privileges`。

3. **模型路由**：`gateway.json` 的模型表来自 `services/ai-gateway/config.deepseek.example.json`，供应商 key 只从 `DEEPSEEK_API_KEY` 读，只注入 ai-gateway 容器。改用其他供应商时编辑这个模板的 `models`，再 `docker compose up -d --build`（init 镜像带的是构建时的模板）。

4. **管理员初始化**：agent-loop 首次启动时用 `EASYGO_ADMIN_EMAIL` + `EASYGO_ADMIN_PASSWORD` 创建管理员。管理员已存在后，密码变量不再被读取，可以从 `.env` 删掉；改它也不会改掉已有密码。它不能把已有普通用户静默提升为管理员。公开注册的账户一律是普通用户、余额 0。

5. **冒烟**：浏览器打开 origin，用管理员登录，在「管理」页给自己发少量额度，发一条消息，确认「额度与用量」页出现一条已结算的用量。仓库里的 `scripts/test-compose.mjs`、`scripts/test-platform-compose.mjs` 和 `services/agent-loop/test/platform-live-browser.mjs` 只适用于**夹具模型**部署，不要对真实供应商部署运行。

**不用 Compose 部署时**（比如三个服务分在不同机器上），用 `scripts/configure-platform.mjs` 生成同样的三份配置和开发 PKI，挂载方式参照 `compose.yaml`：

```bash
node scripts/configure-platform.mjs --state /srv/easygo/state \
  --origin https://agent.example.com --admin-email ops@example.com --registration false
```

它默认不覆盖已有文件（加 `--force` 才重写配置，PKI 永不覆盖），不读也不写任何密码或 key。`--origin` 以 `https:` 开头时自动打开 Secure cookie。state 路径不能含符号链接；工坊的 `host_root` 默认是 `<state>/workshop`，控制器看到的路径和 daemon 看到的不同时用 `--host-root` 指定。`scripts/dev-pki.sh` 生成的开发证书默认 7 天过期，用 `EASYGO_PKI_DAYS` 指定有效期。

## 3. HTTPS

Web 进程只说 HTTP，TLS 在反向代理终止：

- `public_origin` 必须是浏览器实际访问的 `https://` origin，所有写请求都校验 `Origin` 严格相等；
- `secure_cookies` 必须为 true（init 按 origin 自动设置）；
- Compose 默认只把 Web 绑到 `127.0.0.1`，代理在同机转发；不要把 8441/8442/8443 暴露给代理或公网。

限速默认按真实 socket 地址计算，不信任 `X-Forwarded-For`。放在代理后面时，要把代理地址写进 platform 配置的 `trusted_proxies`（`.env` 的 `EASYGO_TRUSTED_PROXIES`，逗号分隔；不用 Compose 时是 `configure-platform.mjs --trusted-proxy IP`），否则所有用户共享同一个地址的额度（登录每分钟 20 次、全部请求每分钟 300 次）。语义与 Caddy/nginx 示例见 [platform-web.md](platform-web.md)。

**Compose 部署要写网络网关地址，不是 127.0.0.1**：发布端口经 docker-proxy 转发，Web 容器看到的对端永远是 Compose `core` 网络的网关。`compose.yaml` 把该网络固定为 `EASYGO_PLATFORM_SUBNET`（默认 `172.31.250.0/24`）、网关 `EASYGO_PLATFORM_GATEWAY`（默认 `172.31.250.1`），与宿主已有网段冲突时两者一起改。于是同机反向代理时 `.env` 写：

```dotenv
EASYGO_PUBLIC_ORIGIN=https://agent.example.com
EASYGO_TRUSTED_PROXIES=172.31.250.1
```

再执行 `docker compose up -d`。

这样做的前提是 Web 端口只绑 `127.0.0.1`（Compose 默认如此），只有宿主本机进程能从该网关地址连进来；不要把端口改绑到公网地址后还保留这个白名单。

## 4. 积分与费率

- 默认费率：输入 + 输出每 1000 token = 1 积分；1 积分 = 1,000,000 microcredits，全部整数运算。缓存 token 是输入 token 的子集，不重复计费。
- 管理员 `GET/PUT /api/admin/tariff` 查看/修改费率。费率有版本；预留时快照当时的版本，之后按该版本结算，改价不影响已在途的请求。
- 发积分 `POST /api/admin/credits {user_id, amount_micros, reason, idempotency_key}`，同一幂等键重放不重复入账。没有接支付，也没有自动充值。
- 网关在调用供应商**之前**预留：请求序列化字节数 + 1024 作为输入估计，加上最大输出。余额不足直接拒绝，不产生供应商调用。预留是保守估计，不是 tokenizer 报价。
- 最大输出由 `gateway.json` 的 `meter.max_output_tokens` 决定，网关会把每个请求的输出上限压到这个值。代码生成类 CLI 常在一次响应里写出整个文件：真实测试中上限 4096 时每个写文件的响应都被截断、任务失败，改为 32768 后一次响应输出约 2.1 万 token 并顺利完成。因此默认写 32768（`.env` 的 `EASYGO_MAX_OUTPUT_TOKENS`）。代价是每次调用要先冻结约 33 积分，余额低于此值的用户发不出请求；只做短对话的部署可以调低。

## 5. 对账

管理员「待核对」列表（`GET /api/admin/usage/pending`）列出仍冻结着积分的请求，状态有两种：

| 状态 | 怎么产生 | 含义 |
|---|---|---|
| `pending` | 网关拿不到可信用量：供应商连接中断、网关崩溃后重启、客户端断开导致流被取消 | 供应商可能已经算了钱，平台不知道算了多少 |
| `reserved` | 正常在途请求（几秒内会自己结算）；或网关在钱包预留成功、本地记为「已激活」之前崩溃（极窄窗口） | 长时间停在这里的，同上。网关本地只留着一条 `authorizing` 记录，重启时不会为它补发回执，只能由管理员处理 |

处理：`POST /api/admin/usage/resolve {namespace, request_id, decision, usage?, reason, idempotency_key}`。

- `decision:"release"`：确认供应商没计费，解冻、不扣费；
- `decision:"settle"`：必须带 `usage:{known:true,input_tokens,output_tokens,...}`，按预留时的费率快照扣费；
- decision 只接受字符串字面量，其他类型（数组、对象等）一律 400，不改动账本；
- 每次处理都写入不可变审计记录。处理之后再到达的回执：如果这笔请求此前还没有任何原始回执，就只保存原始用量作为证据，不会二次扣费或再次解冻；如果已经有过一条回执（例如用量未知的那条），与之不同的新回执会被拒绝（`settlement_conflict`），原记录保持不变。

**没有超时自动退款**：待核对的积分会一直冻结，直到管理员处理。建议每天看一次待核对列表，并对照供应商控制台的用量。已知的超额使用（实际用量超过预留）照实扣费，允许余额为负，之后不再放行该账户的新请求。

## 6. 故障恢复（实测行为）

以下由 `scripts/test-platform-faults.mjs` 在真实进程和真实容器上用 SIGKILL 注入验证：

| 故障 | 恢复方式 | 账务结果 |
|---|---|---|
| agent-loop（钱包）在 CLI 原生调用途中崩溃 | 网关把供应商回执写进 bbolt outbox，每秒重试结算，钱包回来后送达 | 恰好扣一次 |
| agent-loop 在主链流式调用途中崩溃 | 网关看到客户端断开。若取消前已拿到供应商报告的用量，按已知用量结算；否则按「用量未知」记为 pending。该 run 重启后标记为 `interrupted`，不重放 | 已知用量：恰好扣一次；未知：积分冻结，待管理员核对后恰好一次（本次实测走的是未知分支） |
| ai-gateway 在向供应商发出请求后崩溃 | 重启时把在途记录转成 `uncertain` 回执送给钱包 | 积分冻结，出现在待核对列表 |
| workshop 在任务容器运行中崩溃 | 重启时回收自己标签的孤儿容器，任务标记 `interrupted`，需用户显式 resume | 不重放；崩溃前已发生的模型调用照常按网关回执计费 |
| 三个服务正常重启 | — | 钱包、用量记录、历史完全一致 |

**agent-loop 崩溃后约 30 秒才能重新启动**：数据库持有者租约为 30 秒，崩溃留下的租约到期前，新进程会以 `database_owned` 退出。这是有意的 fail-closed 设计，防止两个进程同时写库。Compose 的 `restart: unless-stopped` 会自动重试；等待时间取决于最后一次心跳，最长约 30 秒加上重启间隔，实测样本为 24–30 秒，不是 SLA。正常 `docker compose stop` 会释放租约，没有这个等待。

## 7. 备份与恢复

数据目录（`EASYGO_DATA_DIR`，默认 `./state/platform`）里的数据库**互相引用，必须作为一个整体备份和恢复**：

- 网关 `meter.db` 的待结算回执 ↔ `platform.sqlite` 的预留记录；
- `agent.sqlite` 的记忆 outbox ↔ `knowledge.sqlite` 的已处理回合；
- 工坊任务库 ↔ `workshop/workspaces/` 下的工作区。

只回滚其中一个会造成错配，例如网关对已不存在的预留反复重试结算，或者丢掉已完成回合的记忆提取。推荐冷备份：

```bash
docker compose stop                                  # 网关退出前会尽量冲刷 outbox
sudo tar -C state -czf easygo-state-$(date +%F).tgz platform
docker compose start
```

数据目录的根只有 root 能读，所以要 `sudo`；以 root 解包时 tar 会保留原属主。停止后先确认待核对列表的状态，恢复到旧快照时要预期：快照之后发生的扣费、发放和对账会丢失，需要按供应商用量和审计记录人工补录。`pki/` 里的私钥要单独、加密保管；不要把数据目录打包进仓库或工单附件。`docker compose down` 只删容器和网络，不动数据目录。

## 8. 隔离边界与已知限制

任务容器：`--network none`、只读根文件系统、UID 1000、去掉全部 capabilities、`no-new-privileges`、PID/内存/CPU/tmpfs 上限；只挂本任务工作区和本次执行的模型 UDS，看不到 Docker socket、服务私钥和供应商 key。只读工作流的工作区挂载本身就是只读的。

仍然存在的限制，部署前要清楚：

- **不是 VM 隔离**。任务容器与宿主共享内核，内核漏洞可以越界。处理不可信用户的生产环境应把工坊 daemon 放进独立 VM，或换成 gVisor/Kata 类运行时。
- **持久工作区的磁盘上限是控制器侧配额**：`sandbox.disk_quota_bytes`（默认 2 GiB）、`disk_quota_files`（默认 200,000 个条目）、`disk_poll_ms`（默认 2000）。单文件有硬上限（容器 `--ulimit fsize`，超出即 EFBIG）；总量靠轮询统计（按实际占用块计，不跟随符号链接，硬链接只算一次），超出时终止任务、标记 `disk_quota_exceeded`、不登记产物，已超额的任务不能 resume。两次轮询之间可以短暂超额：16 MiB 配额、200 ms 轮询时实测超出约 6–8 MiB。需要硬性总量上限时，把工坊数据目录放在支持 project quota 的 XFS 上，或放进独立 VM/磁盘。
- **没有租户级或全局的磁盘预算，也不会自动清理已结束任务的工作区**。配额只限制单个任务，同一用户可以提交多个任务把盘占满。开放给不可信用户之前，需要加上按用户的总量预算和工作区保留期限，并给数据盘单独设容量告警。
- **容量没有测过**。验收的并发与 1 小时 soak 用的是单机、8 个客户端、夹具模型，而且单 IP 每分钟 300 次请求的限速本身就会限制单机压测。那些数字只说明这台机器在该负载下没有泄漏、错账或崩溃，不代表生产吞吐。
- 平台自己不做供应商侧限流和熔断；上游 429/5xx 按次失败，会产生待核对或释放记录。

## 9. 从旧版迁移

- **记忆与技能**：旧 Go 版导出的八类 Markdown profile 和 `<name>/SKILL.md` 技能目录，可以通过受控的操作员导入（先预览、再提交）写入新 Knowledge 库，见 [platform-knowledge.md](platform-knowledge.md)。**不会**迁移旧 PostgreSQL/Eino 的会话历史。
- **终端界面**：`cmd/easygo-remote` 复用旧 Bubble Tea 界面，登录托管平台的公开 API，身份由服务端绑定：

```bash
go run ./cmd/easygo-remote --url https://agent.example.com --email you@example.com
# 其他：--session ID 续接会话；--runtime NAME 指定工坊运行时；--sessions / --runtimes 列表后退出
```

- 原 `cmd/easygo-agent` 本地应用已从主分支移除（代码在 tag `legacy-go-app-final`），不参与托管部署。

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

# 一键 Compose：compose.yaml + compose.fixture.yaml，临时项目名、临时数据目录，跑完全部删除
EASYGO_DOCKER_SOCKET=/path/to/dedicated/docker.sock node scripts/test-compose.mjs
```

`test-compose.mjs` 检查 init 生成的配置（daemon 路径、属主和权限、配置里没有密码和 key），在这套部署上跑 `test-platform-compose.mjs`（注册、发额度、对话扣费、隔离任务和产物下载），再改一个 `.env` 值重新 `up`，确认新配置生效、PKI 和账户保留。镜像缺失时才构建，`EASYGO_COMPOSE_BUILD=1` 强制重建。

各次实测结果与证据位置见 [平台验收记录](platform-verification.md)。

## 11. `EASYGO_*` 环境变量总表

仓库代码（`doc/` 以外）读取或设置的 `EASYGO_*` 变量共 68 个，按由谁设置分四组。**第 4 组是测试专用，生产部署不要设置。** 模型供应商 key（如 `DEEPSEEK_API_KEY`）不带这个前缀，不在表内。

### 11.1 托管部署：`.env`，经 `compose.yaml` 和 init 生效

| 变量 | 默认值 | 作用 |
|---|---|---|
| `EASYGO_ADMIN_PASSWORD` | 无，首次启动必填 | 首次启动创建的管理员密码，至少 12 个字符。init 只检查长度，agent-loop 按 `loop.json` 的 `password_env` 读取；管理员已存在后不再读取 |
| `EASYGO_ADMIN_EMAIL` | `admin@example.com` | 首次启动创建的管理员邮箱 |
| `EASYGO_PORT` / `EASYGO_BIND` | `8090` / `127.0.0.1` | Web 在宿主上的端口和绑定地址 |
| `EASYGO_PUBLIC_ORIGIN` | `http://localhost:<EASYGO_PORT>` | 浏览器实际访问的 origin；以 `https:` 开头时打开 Secure cookie |
| `EASYGO_REGISTRATION` | `false` | 是否开放自助注册，只接受 `true` / `false` |
| `EASYGO_DATA_DIR` | `./state/platform` | 数据目录（配置、PKI、数据库、任务工作区），见第 7 节 |
| `EASYGO_DOCKER_SOCKET` | `/var/run/docker.sock` | 挂给 init 和工坊的 Docker socket，应指向专用 daemon |
| `EASYGO_HOST_DATA_DIR` | 空，init 通过 Docker API 查询 | daemon 看到的数据目录路径，写进 `workshop.json` 的 `host_root`；socket 不在本机时指定 |
| `EASYGO_TRUSTED_PROXIES` | 空 | 反向代理地址，逗号或空白分隔，见第 3 节 |
| `EASYGO_TASK_MEMORY_MIB` / `EASYGO_TASK_CPUS` / `EASYGO_TASK_PIDS` | `2048` / `1` / `256` | 每个任务容器的上限，允许范围 64–65536 / 0.1–32 / 16–4096 |
| `EASYGO_MAX_OUTPUT_TOKENS` | `32768` | 每次模型调用的输出上限（1–131072），写进 `gateway.json` 的 `meter`，见第 4 节 |
| `EASYGO_RUNTIME_IMAGE` | `easygo-task-runtime:platform` | 任务容器镜像：Compose 按这个名字构建，工坊按它启动任务 |
| `EASYGO_PLATFORM_SUBNET` / `EASYGO_PLATFORM_GATEWAY` | `172.31.250.0/24` / `172.31.250.1` | Compose 内部网段；和宿主网段冲突时两项一起改，`EASYGO_TRUSTED_PROXIES` 也跟着改 |
| `EASYGO_PKI_DAYS` | 见右 | `scripts/dev-pki.sh` 的证书有效期（天，1–99999），不设时 CA 30 天、服务证书 7 天。Compose 部署固定为 3650：init 在变量为空时用 3650，而 `compose.yaml` 不向 init 传这个变量。单独运行 `configure-platform.mjs` 或 `dev-pki.sh` 时才需要设 |

### 11.2 三服务独立部署：`compose.services.yaml`

只用于 `docker compose -f compose.services.yaml`，一键托管平台的 `compose.yaml` 不读这组变量。

| 变量 | 默认值 | 作用 |
|---|---|---|
| `EASYGO_GATEWAY_PORT` / `EASYGO_AGENT_PORT` / `EASYGO_WORKSHOP_PORT` | `8441` / `8442` / `8443` | 三个 RPC 端口映射到宿主 `127.0.0.1` 上的端口 |
| `EASYGO_GATEWAY_CONFIG` / `EASYGO_AGENT_CONFIG` / `EASYGO_WORKSHOP_CONFIG` | 各服务目录下的 `config.example.json` | 挂到容器内 `/run/easygo/config.json` 的配置文件 |
| `EASYGO_PKI_DIR` | `./state/pki` | 各服务身份目录和 `public/` 证书目录所在的目录 |

### 11.3 工坊为任务进程设置：不要手工设置

| 变量 | 设置方 → 读取方 | 作用 |
|---|---|---|
| `EASYGO_RELAY_SOCKET` | 工坊 → `task-shim` | 本次执行的模型 relay UDS，固定为 `/run/easygo-relay/model.sock` |
| `EASYGO_RUNTIME_API_KEY` | 工坊 → 原生 CLI 的生成配置 | 只在本次执行的 relay 上有效的能力 key，不是供应商 key |
| `EASYGO_CREW_URL` / `EASYGO_CREW_TOKEN` | 工坊 → `easygo-crew` | 任务内 crew 接口的地址和令牌 |

工坊配置里引擎的 `env_allowlist` 不能包含以 `EASYGO_RUNTIME_` 或 `EASYGO_CREW_` 开头的名字（启动时报配置无效），所以不能从宿主转发或覆盖这组变量。验收检查容器（`fixture-check`）要求环境里没有任何 `EASYGO_*`。

### 11.4 测试专用：生产部署不要设置

**离线**（不需要 Docker，不花钱）：

| 变量 | 读取方 | 作用 |
|---|---|---|
| `EASYGO_GO_BIN` | `Makefile`（`make test-e2e`）、`scripts/lib/procs.mjs`、`services/agent-loop/test/knowledge-platform.test.mjs` | go 可执行文件。端到端脚本默认 `go`；knowledge-platform 测试没设时用 PATH 上的 go，找不到就跳过 |
| `EASYGO_GO` | 已移除 | 已并入 `EASYGO_GO_BIN`，knowledge-platform 测试不再读它 |
| `EASYGO_KNOWLEDGE_PLATFORM_DIST` | `services/agent-loop/test/knowledge-platform.test.mjs` | 被测的 agent-loop 编译产物目录，默认 `services/agent-loop/dist` |
| `EASYGO_REMOTE_TEST_URL` / `EASYGO_REMOTE_TEST_EMAIL` / `EASYGO_REMOTE_TEST_PASSWORD` | `internal/remotetui/integration_test.go`，由上一个测试设置 | 远程 TUI 集成测试连接的平台地址和夹具账户；不设 URL 时跳过 |
| `EASYGO_RPC_TEST_PROVIDER_KEY` | `scripts/test-services.mjs` | 夹具供应商的假 key，由脚本自己设置；测试断言它不会进入任务进程 |
| `EASYGO_FIXTURE_HELPER` | `services/workshop/cmd/fixture-cli/crew_test.go` | 测试以子进程方式再次启动自己时的标记 |
| `EASYGO_TUI_TEST_PASSWORD` | `scripts/test-remote-tui.py` | `--password-env` 的默认变量名，存放测试账户密码 |

**专用 Docker daemon**（夹具模型，不花钱；`make test-e2e-docker` 缺前三个变量时直接报错）：

| 变量 | 读取方 | 作用 |
|---|---|---|
| `EASYGO_DOCKER_TEST_BINARY` / `EASYGO_DOCKER_TEST_ENDPOINT` / `EASYGO_DOCKER_TEST_IMAGE` | `Makefile`、`test-harness.mjs`、`test-platform.mjs`、`test-platform-faults.mjs`、工坊的 Docker 集成测试；前两个付费脚本也读 | docker 可执行文件、专用 daemon 地址、夹具任务镜像（`deploy/runtime/Dockerfile.fixture`）。Go 集成测试缺地址或镜像时跳过 |
| `EASYGO_DOCKER_TEST_ROOT` | `docker_integration_test.go` | 临时根目录的上级目录；checkout 路径太长、UDS 路径超过 AF_UNIX 的 108 字节时指定一个短路径 |
| `EASYGO_DOCKER_NATIVE_IMAGE` | `docker_native_test.go` | 装有四个真实 CLI 的任务镜像，和 `EASYGO_DOCKER_TEST_ENDPOINT` 一起设 |
| `EASYGO_DOCKER_QUOTA_IMAGE` | `disk_quota_integration_test.go` | 磁盘配额测试的夹具镜像，和 `EASYGO_DOCKER_TEST_ENDPOINT` 一起设 |
| `EASYGO_PLATFORM_SOAK_SECONDS` | `test-platform.mjs` | soak 时长（秒），默认 0 不跑 |
| `EASYGO_PLATFORM_KEEP` | `test-platform.mjs` | 为 `1` 时通过后不退出，保留平台直到收到 SIGTERM/SIGINT |
| `EASYGO_DOCKER_BINARY` | `test-compose.mjs` | docker 可执行文件，默认 `docker` |
| `EASYGO_COMPOSE_BUILD` | `test-compose.mjs` | 为 `1` 时强制重建镜像 |
| `EASYGO_PLATFORM_ORIGIN` / `EASYGO_PLATFORM_EVIDENCE` / `EASYGO_REQUIRE_ARTIFACT` | `test-platform-compose.mjs`，由 `test-compose.mjs` 设置 | 被测 origin、证据目录（`test-compose.mjs` 自己也读）、为 `1` 时要求产物下载成功 |
| `EASYGO_FIXTURE_URL` | `compose.fixture.yaml` 设置，init 读取 | 把网关的模型地址换成夹具模型（`configure-platform.mjs --fixture-url`） |

**付费**（调用真实供应商；只由 `test-harness-live.mjs`、`test-platform-project.mjs`、`test-deepseek-live.mjs` 读取）：

| 变量 | 读取方 | 作用 |
|---|---|---|
| `EASYGO_LIVE_KEY_FILE` | harness-live、platform-project | 供应商 key 文件路径，必填 |
| `EASYGO_LIVE_MODEL` | harness-live、platform-project | 模型别名，默认 `deepseek-flash` |
| `EASYGO_LIVE_STATE_ROOT` | 三个脚本 | 状态目录的上级目录，各脚本默认值不同 |
| `EASYGO_LIVE_TASK_SECONDS` | harness-live、platform-project | 单个任务时限（秒），默认 900 / 1500 |
| `EASYGO_LIVE_BASE_IMAGE` | harness-live | 基础任务镜像，默认 `easygo-task-runtime:platform` |
| `EASYGO_LIVE_RUNTIMES` | harness-live | 要跑的运行时，默认 `codex,claude,pi,openclaw` |
| `EASYGO_LIVE_KEEP` | harness-live | 为 `1` 时保留任务工作区、二进制、镜像 overlay 和临时 PKI |
| `EASYGO_LIVE_RUNTIME` / `EASYGO_LIVE_RUNTIME_IMAGE` | platform-project | 运行时（默认 `codex`）和任务镜像（默认 `easygo-task-runtime:platform`） |
| `EASYGO_LIVE_MAX_OUTPUT` / `EASYGO_LIVE_REPAIR_ROUNDS` / `EASYGO_LIVE_CREDITS` | platform-project | 输出上限（默认 32768）、修复轮数（默认 1）、积分预算（默认 3000） |
| `EASYGO_LIVE_ENGINES` | deepseek-live | 要跑的引擎，逗号分隔，默认全部 |
| `EASYGO_LIVE_TOOLS_ONLY` | deepseek-live | 为 `1` 时跳过协议检查，只跑工具调用部分 |
| `EASYGO_LIVE_REPORT_DIR` / `EASYGO_LIVE_COMMIT` | deepseek-live | 报告目录（默认状态目录）和报告里记录的源码提交（默认 `working-tree`） |

`doc/` 下的历史文档还提到 `EASYGO_TEST_DATABASE_URL` 和 `EASYGO_SANDBOX_INTEGRATION_URL`，它们属于已移除的旧 Go 应用，现行代码不读取。
