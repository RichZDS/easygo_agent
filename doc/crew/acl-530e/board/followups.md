# P1 后续项（工头记录，不阻塞 P1）

- **F-1**（来自 L-2 终审 P3）：`workshop_reply` 和 `workshop_submit` 的幂等 key 是 `${run.id}:${call.id}`。call id 超过 91 字节时，key 会超过契约规定的 128 字节，工坊按契约回 -32602，loop 把它当可恢复错误交还模型，但模型改不了 call id。
  - 常见 provider 的 call id 大约 30 字节，不受影响。
  - 修法：call id 过长时，对它取 sha256 再拼进 key。
- **F-2**（来自 L-1 终审 P3）：knowledge 工具参数超过 64 KiB 时，reason 从 `invalid_string` 变成 `tool_arguments_too_large`。仍是可恢复的 -32602，接受，不修。
- **F-3**（来自 R1 对 W-1 39863f1 的复审 P3）：`runNative` 先在 `diagnosticLimit + maxSecretLength` 大小的缓冲里整段替换 secret，再截断到 `diagnosticLimit`。
  - 精心构造的 stderr 可以在截断处留下 token 的 53 字符前缀（`runner.go:177-185,237-244`）。
  - 只有子进程自己能构造这种输出，而子进程本来就持有完整 token；53 字符也过不了 `ConstantTimeCompare`。风险很低，但违反"token 不进任何输出"这条原则。
  - 转为 W-7，派给 workshop2，排在 W-6 之后。
- **F-4**（工头集成时发现）：生产配置 `configure-platform.mjs:28` 给 agent-loop 授权了 `workshop.message` 和 `workshop.evidence`。但端到端脚本 `test-platform.mjs`、`test-platform-faults.mjs`、`test-platform-project.mjs`、`test-services.mjs` 的授权名单还是 P1 之前的样子。
  - 现有夹具流程用不到这两个方法，回归不受影响。
  - 缺口在于：Web → loop → workshop 这条路径上，这两个方法只有 `platform.test.mjs` 的单测覆盖，没有端到端覆盖。
  - 放到 P2（Web 班子看板）里一起补，并同步各脚本的授权名单。
- **F-5**（已知限制）：生产配置 `pids_limit` 128、内存 1 GiB，还没用真实模型样本验证过；工头的样本用的是 256 / 2 GiB。夹具场景用 128 能通过。
- **F-6**（W-7 终审 P3，接受不修）：截断处如果有两段相邻的 secret 前缀，只会剥掉最后一段；终审构造的探针留下了 8 个字符。
  - 接受理由：子进程本来就持有完整 token，想泄露随时可以在输出的任何位置打印任意片段，脱敏防的是意外泄露，不是恶意泄露。
  - 意外情况下，截断只会在末尾造成一段残缺，已经处理掉；相邻两段前缀只可能是刻意构造出来的。
