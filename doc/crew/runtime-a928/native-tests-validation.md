# Native gateway/relay 独立测试交付

## 1. 任务与精确基线

- 工单：`board/tasks/native-tests.md`；初始指令 `c2bfd507-91de-4499-83f9-b494447dcb5f`，续行指令 `fa843a57-7cb2-4fb9-938e-abe965b09d6d`。
- 指定 foreman 提交：`f05376ce1d70d4063e34a75d5e7d7873eabe564f`，Add operator runtime profiles and scoped native model relay。
- 本分支 cherry-pick 后精确 parent/HEAD：`0324635b8f13041eaf079e518597732d3a86be75`，分支 `test/runtime-adapters`。
- 已知 foreman 表示 `c8d53ef` 修复语法截断与错误 SSE。本 worker 没有把它 cherry-pick 进来，也没有对其给出通过结论；保留原 parent 的 red 证据供复验。

## 2. 文件与覆盖

仅新增以下三个 Go 测试文件，没有实现修改：

| 文件（相对 /home/ubuntu/Projects/easygo-runtime-test） | 覆盖 |
|---|---|
| `services/ai-gateway/gateway/native_boundary_test.go` | 三协议 Native 实际 HTTP 请求强制 operator model、参数、端点、认证 header；子请求 URL/headers 不变成传输配置；原生工具保留；未知 model/协议/非法输入拒绝；已知 usage/cost 与未知计量；SSE 完成前不返回且不报告 first delta；取消关闭 provider；response 超限/HTTP 截断/错误与 content-type；三个故意保留的错误响应 red case |
| `services/ai-gateway/server/native_boundary_test.go` | 真实 mTLS RPC → 本机 provider；method/namespace/certificate 授权与服务证书 pin；外层 URL/header 注入与重复字段拒绝；强制 upstream model/dummy key；HTTP provider 错误消毒 |
| `services/workshop/workshop/relay_boundary_test.go` | 真实 loopback HTTP → mTLS RPC fixture；三协议固定路径/token/namespace/model route；错误 token/path/query/重复 key 不达 upstream；不同任务 token 隔离；task/caller 取消关闭 RPC；decoded/envelope body 上限；错误消毒；SSE 原始字节；错误服务 pin；不兼容 direct/gateway profile 拒绝 |

验证边界说明：gateway RPC 测试的真实终点是 dummy provider；relay 测试的真实终点是 mTLS RPC fixture。没有跨 Go module 增加依赖，也未把两者伪称为单一完整 relay → real gateway → provider E2E。此轮没有启动真实 agent CLI，因此“child 获得 ephemeral token”在 relay 返回能力凭证层验证，未验证未来 adapter 如何注入 child 环境。

## 3. 实际运行与 red 证据

### 权限切换前

1. 在 `services/ai-gateway` 执行：

```text
/home/ubuntu/sdk/go/bin/go test ./gateway -run TestNativeBoundary -count=1 -timeout=45s
```

真实退出 **1**。仅 `TestNativeBoundaryRejectsInvalidProviderSuccess` 失败：

```text
--- FAIL: TestNativeBoundaryRejectsInvalidProviderSuccess/malformed-json
    invalid provider response accepted as successful NativeResponse (malformed-json)
--- FAIL: TestNativeBoundaryRejectsInvalidProviderSuccess/truncated-sse
    invalid provider response accepted as successful NativeResponse (truncated-sse)
--- FAIL: TestNativeBoundaryRejectsInvalidProviderSuccess/error-sse
    invalid provider response accepted as successful NativeResponse (error-sse)
    raw provider error detail exposed to caller
```

复现输入分别是 `{"status":"completed"`、只有 delta 没有终态的 responses SSE、含 `dummy-provider-private-detail` 的显式 `type:error` SSE。原实现 `native.go` 忽略 decodeResponse/decodeStream 错误，只不记录 usage，然后仍然返回原始 Body 与 nil error。无真实秘密被使用。

其余同次 Native 选择/计量/限制/取消/缓冲测试通过；这是原始运行事实，不等于全部最终测试在修复版本通过。

2. RPC 测试首次退出 **1**：duplicate JSON 的预期写成 -32602，而公共 envelope 层实际拒绝为 -32600。测试已修正，其他 case 当次未报失败。修正后未完成可监听环境复跑。
3. Relay 测试首次退出 **1**：fixture 调用 rpc.NewServer 时没有设置 Listen，报 `invalid listen address`。已补 `127.0.0.1:0`；这是测试夹具问题，不是产品缺陷。未弱化产品边界断言。

### 权限切换后最终文件

以下三个编译命令均真实退出 **0**，使用可写 /tmp 编译缓存；没有联网安装依赖：

```text
# services/ai-gateway
GOCACHE=/tmp/crew/runtime-a928/validation-go-cache /home/ubuntu/sdk/go/bin/go test -c -o /tmp/crew/runtime-a928/native-gateway.test ./gateway
GOCACHE=/tmp/crew/runtime-a928/validation-go-cache /home/ubuntu/sdk/go/bin/go test -c -o /tmp/crew/runtime-a928/native-server.test ./server
# services/workshop
GOCACHE=/tmp/crew/runtime-a928/validation-go-cache /home/ubuntu/sdk/go/bin/go test -c -o /tmp/crew/runtime-a928/native-relay.test ./workshop
```

无 socket 测试：

```text
/tmp/crew/runtime-a928/native-relay.test -test.run '^TestNativeRelayBoundaryProfileProtocolValidation$' -test.v
```

真实退出 **0**，PASS。

监听测试：

```text
/tmp/crew/runtime-a928/native-server.test -test.run '^TestNativeRPCBoundaryAuthorizationAndRoute$' -test.timeout=15s
```

真实退出 **2**，原错误：

```text
panic: httptest: failed to listen on a port: listen tcp6 [::1]:0: socket: operation not permitted
```

按续行指令停止监听尝试，没有换 bind 地址、网络命名空间、远程机器或其他绕过手段。此失败是 sandbox 权限，不能作为实现错误或测试通过。

## 4. 交付方式与复跑

`git add` 三个测试文件真实退出 **128**：

```text
fatal: Unable to create '/home/ubuntu/Projects/easygo_agent/.git/worktrees/easygo-runtime-test/index.lock': Read-only file system
```

因此无法创建要求的测试 commit。没有修改 Git 存储路径/权限，也没有绕过。交付工作区内的三个文件，以及纯新增文件补丁：

`/tmp/crew/runtime-a928/board/native-tests-validation.patch`

Foreman 可在修复后的分支应用此补丁或直接复制三个文件，然后运行：

```text
# services/ai-gateway
/home/ubuntu/sdk/go/bin/go test ./gateway ./server -run 'TestNative(Boundary|RPCBoundary)' -count=1 -timeout=90s
# services/workshop
/home/ubuntu/sdk/go/bin/go test ./workshop -run TestNativeRelayBoundary -count=1 -timeout=90s
```

请首先检查原三个 red case 在 c8d53ef 上是否消失，再运行全部上述测试。若修复改变对 framework-specific 成功响应的支持语义，需独立核对 `TestNativeBoundaryUnknownUsageIsNotFree`，不能只为绿灯去掉未知计量断言。

最终文件 SHA-256：

```text
f55617c5ca98c74f12b5dbde868d21508f05836ca4a7c30a5bddd5240ea4e03c  services/ai-gateway/gateway/native_boundary_test.go
2d9a35edd35644b11c0ce79cb67aa99cc36a3831172407145e06e6b720416b3c  services/ai-gateway/server/native_boundary_test.go
e85afbb12fbed3c39e9cd6859a5aa7de3685efceee7d1be276fc8ee234c30ad9  services/workshop/workshop/relay_boundary_test.go
```

## 5. 剩余与注意

- 三文件编译通过，不宣称完整 runtime suite 通过。修复版本的实际监听测试由 foreman 运行。
- 未覆盖单链路 relay → real gateway → provider、真实 CLI 环境注入、HTTP raw envelope 截断/伪造 ID 的全部 relay 分支；这不是这些分支已正确的证明。
- 未改实现、未支付调用、未读真实凭证、未联系 peer、未推送。所有 keys 与测试 provider 文本都是 dummy。
- 工作流按 onboarding 仅在共享文件回执，不向可能 blocked 的 foreman UI 发消息；没有虚构 delivery_id。
