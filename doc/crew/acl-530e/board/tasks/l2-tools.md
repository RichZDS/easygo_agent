# L-2 · loop 侧读消息、回复、看证据

assignee: loop · 状态: done（76530b2；工头已签 51dadc9）· 契约：`board/contract-p1.md` §2–§4

## 现场 / 锚点
- **工具注册表**：L-1 的成果。
- **校验与幂等**：`validateReceipt` 负责可信身份校验；`workshop_submit` 的幂等 key 用 `${run.id}:${call.id}`。
- **Web 侧**：`src/loop.ts` 的 `workshopCall` 白名单；平台 Web 经 `/api/rpc` 调用这些方法。
- **联调对象**：工坊这边的实现由 workshop 席在 W-1、W-2 里完成。你先对着契约写，测试用假工坊。

## 修法
1. **新工具**（角色 `assistant`）：
   - **`workshop_messages {task_id, after?}`**：调 `workshop.events`，只保留 `crew.message`、`crew.read`、`acceptance` 三种事件和状态变化，返回有界列表和 `next` 游标。单次最多返回约 32 KiB，超出就截断，并返回游标让模型继续读。
   - **`workshop_reply {task_id, text}`**：调 `workshop.message`。
     - 幂等 key 是可信的 `${run.id}:${call.id}`；
     - 属于 mutating：不确定结果按现有规则让 run 失败，只有明确的拒绝才可以交还给模型。
   - **`workshop_evidence {task_id, run_id?, evidence_id?, offset?, limit?}`**：调 `workshop.evidence`，结果有界。
2. **回执校验**：
   - 新方法的回执都要校验：namespace、task_id 必须和请求一致，类型和枚举要合法。
   - `workshop_get`、`workshop_list` 的回执要接受并校验新增的 run 字段：`outcome`、`acceptance_state`、`false_green`、`evidence_count`。
3. **Web 白名单**：`workshopCall` 加上 `workshop.message` 和 `workshop.evidence`。Web 界面本身留到 P2，这一单不改。
4. **说明**：`assistant.md` 补上这三个工具的用法。

## 明令不做
- 不做班子、任务板、工头工具，这些是 P2 的事。
- 不改 Web 界面。
- 不自己轮询、不自动唤醒，P1 里只有模型在调用工具时才去读。

## 测试
- 用假工坊做单测：
  - 三个工具的参数边界；
  - 回执伪造：namespace 不一致、task_id 不一致、枚举非法，都会被拒；
  - `workshop_reply` 结果不确定时 run 失败；
  - `workshop_messages` 的过滤、游标和截断；
  - Web 白名单。
- `npm run typecheck && npm test` 全绿。

## 交付
- 在 `feat/acl-loop` 上单独提交一笔。
- worklog 报审。
- 然后 herdr-msg 通知工头。
