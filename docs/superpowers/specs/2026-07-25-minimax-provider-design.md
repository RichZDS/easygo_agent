# MiniMax Provider 最小接入设计

日期：2026-07-25  
状态：已批准（待实现）

## 目标

让用户能把 MiniMax 配置为聊天 Provider，并走现有 OpenAI 兼容流式链路，不新增独立 Streamer。

## 背景

生产路径只有一个 `HTTPProviderStreamer`（`POST {BaseURL}/chat/completions` + SSE）。  
`provider_name` 仅用于凭证绑定、模型配置白名单和（部分）默认 BaseURL。  
MiniMax 官方提供 OpenAI 兼容接口，因此最小接入只需扩展白名单与前端选项。

## 决策

| 项 | 选择 |
|----|------|
| 接入深度 | 最小接入：白名单 + Settings UI，复用现有 Streamer |
| 默认 BaseURL | **不设**；用户在 Settings 自行填写 |
| Create 时强制 base_url | **否**；与 deepseek/openai 一样允许空 |
| 空 base_url 运行时行为 | 沿用现有逻辑：回退到 `https://api.openai.com/v1` |
| thinking / tools 等专有参数 | **不做** |

## 行为

1. `provider_name = "minimax"` 可通过 `PUT /credentials` 与 `POST /model-configs`
2. 用户填写 API Key、模型名（如 `MiniMax-M3`）、可选 `base_url`（如 `https://api.minimax.io/v1`）
3. Turn 执行时解密同名凭证，调用现有 `HTTPProviderStreamer`
4. 不新增路由、表结构或加密逻辑

## 改动清单

### 后端

- `internal/service/modelconfig/model_config.go`：白名单增加 `"minimax"`
- `resolveProviderBaseURL`：**不改**
- Streamer / 凭证 / 路由：**不改**

### 前端

- `frontend/src/pages/SettingsPage.tsx`：
  - Provider 下拉增加 `minimax`
  - 模型名 placeholder：选中时为 `MiniMax-M3`
  - Base URL placeholder：可提示 `https://api.minimax.io/v1`（仍为可选）

### 测试 / 文档

- 若已有白名单相关测试则补 `minimax` 可创建用例；否则不新增测试文件
- README 不做大改

## 明确不做

- MiniMax 默认 BaseURL
- 国内站 / 国际站切换 UI
- 独立 `MiniMaxProvider` / 新 Streamer
- thinking、tools、reasoning_split 等专有参数
- 修改 `internal/agent/**` 遗留 DeepSeek 封装

## 成功标准

1. Settings 可选 MiniMax 并保存凭证与模型配置  
2. 填写有效 key + base_url + 模型名后，现有聊天流可收到回复  
3. 未选 MiniMax 时 deepseek / openai 行为不变  

## 风险

空 `base_url` 时请求会打到 OpenAI 默认地址，可能造成迷惑；本设计接受该行为以保持与现有供应商一致。后续若需可再加运行时明确报错。
