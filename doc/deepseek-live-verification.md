# DeepSeek 真实接口验收

2026-09-28，使用用户授权的 DeepSeek API key，对业务代码 `de4459516ab62777fbadbd2285d473569548269a` 执行真实网络测试。此次未修改生产逻辑；新增显式启用的 live 测试脚本。密钥不进入仓库、配置文件或测试报告。

模型列表鉴权返回 HTTP 200，可用模型为 `deepseek-flash`、`deepseek-v4-pro`。本次全部生成使用 `deepseek-flash`，限制输出 token；Chat/Anthropic 禁用 thinking，Responses 使用 low reasoning。

| 路径 | 结果 |
|---|---|
| 网关 Chat Completions → DeepSeek | 流式生成成功、用量可读 |
| 网关 Responses → DeepSeek | 流式生成成功、用量可读 |
| 网关 Anthropic Messages → DeepSeek | 流式生成成功、用量可读 |
| TS Agent Loop → 网关 → DeepSeek | 持久化运行 completed，返回预期标记 |
| 用户 API 查询工坊框架目录 | 返回四项允许的运行配置 |

| 真实框架 | 首次调用 | 原生会话续跑 | 实际工具生成文件 |
|---|---|---|---|
| Codex 0.157.1 | 通过 | 同 session、保留上一轮标记 | 通过 |
| Claude Code 2.1.281 | 通过 | 同 session、保留上一轮标记 | 通过 |
| Pi 0.87.1 | 通过 | 同 session、保留上一轮标记 | 通过 |
| OpenClaw 2026.9.6 | 通过 | 同 session、保留上一轮标记 | 通过 |

框架均通过真实工坊进程创建任务，再经任务专属代理、mTLS `gateway.native` 和真实网关调用 DeepSeek。真实供应商密钥只供网关服务使用；工坊子进程使用临时 capability。文件测试启用现有 workspace-write 策略，四个任务的 `proof.txt` 均校验为精确内容 `LIVE_FILE_OK`，并由工坊记录产物。没有用假的模型响应替代这些结果。

第一组记录 12 次模型请求，32,485 输入 token、195 输出 token；文件组记录 9 次请求，41,247 输入 token、653 输出 token。合计 **21 次、73,732 输入 token、848 输出 token**，21 次均有已知用量，无网关模型错误。输入计数包含协议报告的缓存部分；不要再把框架侧可能累计的 usage 加到这里。网关未配置本次价格表，不把 unknown cost 当成零费用，也不估算实际账单金额。

两组测试脚本均退出 0，测试服务退出后关闭。原始非秘密报告保存在本机 `/home/ubuntu/reports/easygo-deepseek-live-20260928/live-report.json` 和 `tools/live-report.json`。文本组使用 `/tmp` 隔离目录，文件组使用仓库外的报告目录，避免 Codex 对 `/tmp` helper 目录的限制。

## 复跑

需要已安装四个 CLI，OpenClaw 使用匹配的 Node 版本。先在 `services/agent-loop` 执行 `npm ci`。从安全的凭证环境提供 `DEEPSEEK_API_KEY`，以下命令不包含真实值：

```bash
export EASYGO_GO_BIN=/path/to/go
export WORKSHOP_NATIVE_CODEX=/absolute/path/to/codex
export WORKSHOP_NATIVE_CLAUDE=/absolute/path/to/claude
export WORKSHOP_NATIVE_PI=/absolute/path/to/pi
export WORKSHOP_NATIVE_OPENCLAW=/absolute/path/to/openclaw
node scripts/test-deepseek-live.mjs
# 第二组仅执行真实文件工具测试；状态父目录需要已存在。
EASYGO_LIVE_STATE_ROOT=/absolute/non-temp/state-parent \
EASYGO_LIVE_TOOLS_ONLY=1 node scripts/test-deepseek-live.mjs
```

这是会产生 API 费用的显式测试，不加入默认测试命令。可用 `EASYGO_LIVE_REPORT_DIR` 指定报告位置，用 `EASYGO_LIVE_ENGINES=pi,claude` 限定框架。

本次证明文本、流式、原生续跑和简单文件工具路径可用；没有测试复杂项目编程、长期运行、并发负载、每任务 OS 隔离或 Docker 容器部署。前一轮文档中的“真实 DeepSeek 未测试”历史限制已由本记录更新，Docker/完整隔离等限制仍保留。
