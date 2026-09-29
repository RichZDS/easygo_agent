# 工头真实模型样本 · P1 harness · 09-29

模型：deepseek-flash（经 ai-gateway，key 只在网关进程环境里）。镜像 `easygo-task-runtime:acl-live`，是在 `:platform` 上叠加 easygo-crew 构建的。快照 `int/acl-p1-s3`（1d7df73）。脚本 `scripts/test-harness-live.mjs`（工头专用，未提交）。

每个 CLI 的任务都是：在工作区实现一个 Node ESM 的 slugify 项目，并写 `node --test` 测试。平台的验收检查是 `/pack/checks/npm-test.sh`，在独立检查容器里运行：无网络、工作区只读、没有 relay。

| CLI | 报告 | outcome | 验收 | claims.tests | false_green | harness | 输入/缓存/输出 tokens | 检查耗时 |
|---|---|---|---|---|---|---|---|---|
| codex | r-wd7ynQ | submitted | passed | pass | false | 通过 | 80066 / 77824 / 1216 | 601 ms |
| claude | r-mY4dwU | submitted | passed | pass | false | 通过 | 3636 / 21248 / 1837 | 1961 ms |
| openclaw | r-zV8VxK | submitted | passed | pass | false | 通过 | 51789 / 44672 / 1501 | 937 ms |
| pi | r-zV8VxK | submitted | passed | pass | false | 通过 | 20114 / 18944 / 1097 | 1804 ms |

- 三份报告的 key_leaks 都是 0（脚本对整个 state 目录做流式扫描）。
- 第一次跑 pi 和 openclaw 时（r-mY4dwU）没有通过：
  - pi 的检查退出码为 0，但检查容器 `rm` 在负载 17.9 下超过 20 s，`cleanupFailure` 被置位，验收记为 `error`；
  - 随后 openclaw 被拒，报 "Docker cleanup previously failed"。
  - 工头删除了残留容器（我们自己的 owner），负载降下来后重跑，两者都通过。
  - 这个问题立为 W-5。
- 已证明的：
  - 四种 CLI 在 Docker 模式下都能用各自的 shell 执行 `easygo-crew report/submit`（W-4 生效）；
  - 平台自己运行检查并留下证据；
  - outcome 由平台判定。
- 没有证明的：
  - 这四次都是诚实的通过，没有出现"自报通过、检查失败"的真实假绿；
  - ask/blocked 路径和 resume 追加未读消息，这两项只由夹具场景证明（L-3）。
