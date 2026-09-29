# L-1 · 工具注册表与角色说明

assignee: loop · 状态: done（78d893f；工头已签 51dadc9）

## 现场 / 锚点
- **工具**：`services/agent-loop/src/tools.ts` 里有写死的 `TOOLS`（calculator + 7 个 workshop 工具）和 `executeTool` 的 switch。
- **调用处**：`src/loop.ts` 的 `request()` 把 `TOOLS` 和 `knowledge.tools()` 拼起来；`execute()` 里按名字判断走 knowledge 还是 executeTool。
- **可恢复错误**：`recoverableToolError` 和 `callWorkshop` 的"不确定结果"规则。这套安全语义必须原样保留。
- **配置**：`src/types.ts` 的 `Config.system_prompt`；`src/server.ts` 负责读取配置。
- **测试**：`test/service.test.mjs` 第 50、212、435、462 行用到 calculator；`scripts/test-services.mjs` 第 119 行也用到。

## 修法
1. **注册表**：新建 `src/tools/`，定义统一的工具条目：定义、适用角色、执行函数、是否会改状态（mutating）、错误分类。
   - 把现有 7 个 workshop 工具和 knowledge 工具都注册进去；
   - `loop.ts` 只通过注册表取工具定义、执行工具；
   - 工具名重复在启动时就报错。
2. **角色**：本期只有 `assistant` 一个角色，所有会话都用它；接口要为 P2 的 `foreman` 留好位置。
3. **删掉 calculator**。原来用它测"本地参数错误可恢复"的测试，改用别的确定性本地校验错误，比如 `workshop_get` 缺 `task_id`。`contracts/rpc-v1.md` 里提到 calculator 的那句不要改，这个文件归 workshop 席。在报审里告诉工头，由工头改。
4. **角色说明**：
   - loop 配置增加可选的 `pack_dir`。设置后读取 `<pack_dir>/roles/assistant.md`（最大 16 KiB）作为系统提示词。
   - `pack_dir` 和 `system_prompt` 同时设置时，启动报错。
   - 都不设时，行为和现在一样。
   - 用英文写 `packs/base/roles/assistant.md`，内容要讲清：
     - 工坊任务被接受不等于完成；
     - 要看 `outcome` 和验收结果；
     - 不采信施工者的自报；
     - 施工者提问（asked）时，用回复加续跑的方式回应。
5. **部署接线**：
   - `scripts/configure-platform.mjs` 给 loop 和 workshop 的配置都写上 `pack_dir: "/opt/easygo/packs/base"`。
   - `services/agent-loop/Dockerfile` 把 `packs/base` 复制到 `/opt/easygo/packs/base`。
   - workshop 的 Dockerfile 归 workshop 席，在报审里提醒工头协调。

## 明令不做
- 不加新的业务工具。L-2 的工具和班子工具都不在这一单。
- 不改工具的错误语义和幂等 key 的生成规则。
- 不碰平台钱包相关代码。

## 测试
- `cd services/agent-loop && npm run typecheck && npm test` 全绿。
- 新增的测试覆盖：
  - 注册表的重名检测；
  - 按角色过滤工具；
  - 注册表路径下的 mutating 工具在不确定结果时仍然让 run 失败，而不是交还给模型重试；
  - `pack_dir` 的读取、超限、与 `system_prompt` 冲突、文件缺失；
  - calculator 已经不存在（模型调用它会得到 `unknown_tool`）。
- `node scripts/test-services.mjs` 如果能在本机跑（它需要什么看脚本头部），要跑通；跑不了就说明原因。

## 交付
- 在 `feat/acl-loop` 上单独提交一笔。
- worklog 报审写清：提交号、文件清单、测试命令和退出码、偏差说明。
- 然后 herdr-msg 通知工头。
