# 平台 Web、账户与额度

`services/agent-loop/src/platform/server.ts` 导出 `createPlatform(config, callbacks)`。它由 TS agent-loop 进程挂载；不增加第四个部署服务。核心 RPC、网关钱包 mTLS 方法的注册、Docker 镜像复制和真实端到端验证由集成方负责。

## 接入

```ts
const platform = await createPlatform({
  listen: '127.0.0.1:8080',
  database: '/data/platform.sqlite',
  public_origin: 'http://localhost:8080',
  secure_cookies: false, // HTTPS 站点必须为 true
  registration: true,
  bootstrap_admin: { email: 'admin@example.test', password_env: 'PLATFORM_ADMIN_PASSWORD' }
}, {
  rpc: async (method, params) => dispatch(method, params)
});
// 返回 {server, address, wallet, close(): Promise<void>}
// await platform.wallet.reserve(params)
// await platform.wallet.settle(params)
```

`public_origin` 必须是无路径、无尾斜杠的完整 HTTP(S) origin。所有写请求必须带严格相等的 `Origin`，远程 TUI 也需要发送该头。HTTPS 要求 Secure cookie；TLS 可以在可信反向代理终止。请求限速只采用真实 socket 地址，不信任 `X-Forwarded-For`。不要把多个服务经任意代理全部映射到同一个无限流公网出口。

bootstrap 密码通过指定环境变量读取，仅在第一次创建管理员时需要。最短 12 个字符、最多 1024 UTF-8 字节；已有普通用户不能被 bootstrap 静默提升。注册账户绑定随机独立 namespace、初始余额 0、固定 user 角色。密码为随机盐 scrypt，cookie 随机 256 bit，数据库只留 cookie SHA-256；HttpOnly、SameSite=Strict、7 天有效。登录轮换当前 cookie，注销立即撤销，禁用账户立即失去认证和预留权限；禁用不阻止已有实际用量入账。

构建不需要新增包：Node 22 内建 SQLite、crypto、HTTP。运行目录必须保留 `web/` 与 `dist/` 同级：静态文件相对 `dist/platform/server.js` 查找，和当前工作目录无关。部署镜像需复制 `services/agent-loop/web/`。

## HTTP 路由

成功为 JSON；错误为 `{error:{code,rpc_code}}`，只返回安全机器码，不返回异常消息、密码或上游响应正文。

| 方法 / 路径 | 参数与结果 |
|---|---|
| POST `/api/register` | `{email,password}` → `{user}`，201，并登录 |
| POST `/api/login` | `{email,password}` → `{user}` |
| POST `/api/logout` | `{}` → `{ok:true}`，撤销当前会话 |
| GET `/api/me` | `{user,registration}` |
| POST `/api/rpc` | `{method,params}` → `{result}`；只有契约 allowlist |
| GET `/api/wallet?after=0` | `{balance_micros,held_micros,available_micros,ledger}`；流水按 seq 升序，最多 100 条 |
| GET `/api/usage?offset=0` | `{receipts}`；最近在前，最多 100 条，保留原始 settlement 与独立 resolution |
| GET `/api/admin/users?offset=0` | `{users}`；最多 100 条，不含密码和会话 token |
| POST `/api/admin/credits` | `{user_id,amount_micros,reason,idempotency_key}` → `{ledger_seq,duplicate}` |
| GET / PUT `/api/admin/tariff` | PUT `{input_micros,output_micros}`；返回版本化费率行，单位为每 token 的 microcredits |
| GET `/api/admin/usage/pending?offset=0` | `{receipts}`，最多 100 个 reserved/pending 请求 |
| POST `/api/admin/usage/resolve` | `{namespace,request_id,decision:'release'\|'settle',usage?,reason,idempotency_key}` → `{reservation_id,status,charged_micros,duplicate}` |

`params.namespace` 由认证层注入，浏览器显式传此字段会被拒绝。allowlist 只包括 platform-v1 的 agent session/run/catalog、workshop 和 knowledge 方法；钱包、服务配置和任意 RPC 代理不公开。回调仍必须对 session/task/run ID 进行注入 namespace 下的所有权检查。

请求 JSON 上限 256 KiB，重复 JSON 字段、非对象和顶层未知参数被拒绝。具体 RPC params 的字段由既有 core/knowledge/workshop validator 校验。每 socket 300 次/分钟，每认证用户 240 次/分钟，认证端点每 socket 20 次/分钟、每邮箱 10 次/分钟，最多 4 个并行密码 KDF；限速键表上限 8192。页面采用严格 CSP，不内嵌脚本、不渲染模型 HTML。

## 钱包与持久化

`src/platform/store.ts` 导出 `PlatformStore`、`Wallet`（内部代码与测试使用）；生产只将 `platform.wallet.reserve/settle` 交给网关 mTLS 身份。

`reserve` / `settle` 参数同 platform-v1。内部事务是同步 `BEGIN IMMEDIATE`，公开集成可以 `await` 返回值。预留为 `(namespace,request_id)` 唯一；相同 fingerprint 重放返回同一 receipt 和 `duplicate:true`，不同 fingerprint 返回冲突。safe integer 参数与 BigInt 中间算术防止舍入和溢出。费率版本在预留时固定。

初始费率每输入/输出 token 1000 microcredits，即 1000 总 token = 1 credit。1 credit = 1,000,000 microcredits。缓存读写是输入 token 的子集，缓存读写之和不得大于输入，不重复加价。已知实际用量无论 complete 或 uncertain 都只扣一次并释放预留；未知 complete/uncertain 保持 pending 和冻结，不自动超时退款。rejected 必须没有实际非零用量，释放预留。超预留实际用量可使余额为负，后续预留拒绝。

管理员对账只处理 reserved/pending。settle 要求 `usage.known=true`；release 不接受 usage。按预留快照计费，在同一事务调整余额和冻结、追加流水与审计、保存独立 resolutions，不修改原始 settlement。相同幂等键和相同负载可重试；换负载或重复解决同一预留冲突。已存在的完全相同原始 settlement 重放仍为无副作用重复；人工处理尚无 receipt 的 reserved 请求后再来的 settlement 返回 `reservation_resolved`。操作前必须核实网关已停止重放/请求已终止，避免人工释放正在运行的请求。

数据库开启 WAL、FULL 同步、外键、5 秒 busy timeout。表：`platform_migrations`、`accounts`、`auth_sessions`、`tariffs`、`reservations`、`ledger`、`grants`、`audit`、`resolutions`。v1 建账户和账务；v2 增加 reconciliation。ledger/audit/tariff/resolution 有禁止修改和删除的触发器；已留存 settlement 有不可变触发器。后台没有自动清理账本和 pending。

## 页面和知识库参数

中文响应式控制台包含注册/登录、总览、会话与运行事件、工坊运行时选择和取消/续跑/结果、记忆和技能编辑/导入、额度/原始用量/流水、管理员发放/费率/人工对账。UI 金额用 BigInt 十进制转换，失败重试保留幂等键。列表初始只展示 API 的有界页；完整翻页接口存在，当前 UI 尚不提供翻页导航。浏览器轮询周期 4 秒；hidden 页面不轮询。

Knowledge 使用已确认参数：memory `{id?,kind,content,importance?,confidence?,source_run_ids?,expected_version?}`，skill `{name,description,content,expected_version?}`。编辑/删除携带 version；记忆编辑保留既有来源和评分。8 个 memory kind 使用小写 agent/memory/experiment/error/preference/style/prompt/constraint。导入参数为 `{entries,dry_run?,provenance?}`，默认预览不落库，页面明确显示结果；使用者显式 `dry_run:false` 后才真正导入。

## 验证与边界

```bash
cd services/agent-loop
npm ci
npm run typecheck
npm test
```

`test/platform.test.mjs` 覆盖认证/CSRF/角色/namespace、撤销、零余额、并发额度耗尽、费率快照、overflow/cache、重复与冲突、已知中断/未知待核对/拒绝释放、负余额、持久化和管理员对账。并发案例启动 4 个独立 worker/SQLite 连接，各申请 20 次，预算只能准入 10 次。

浏览器证明使用已有 Playwright 和 Chromium，脚本本身不会下载安装工具：

```bash
PLATFORM_PLAYWRIGHT_MODULE=/path/to/playwright-core/index.mjs \
PLATFORM_CHROMIUM=/path/to/chrome \
PLATFORM_BROWSER_EVIDENCE=/tmp/platform-web-browser-evidence \
node test/platform-browser.mjs
```

脚本使用本地 dummy 账户与 fixture RPC，覆盖注册零余额、管理员发放和人工对账、计量对话、恶意 HTML 作为纯文字、工坊取消/续跑/结果、带版本记忆和技能修改、账本与 1440px/390px 布局。它不证明真实 Docker/provider/core 集成；这些属于 foreman 的最终整链验收。SQLite 会打印 Node experimental warning，保留该警告。
