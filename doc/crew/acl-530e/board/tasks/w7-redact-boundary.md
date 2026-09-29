# W-7 · 诊断截断处不留 secret 前缀

assignee: workshop2（`acl-workshop2-530e`）· 状态: done（021326e，R1 终审通过；工头已签 51dadc9）
基线：W-6 之后的 `feat/acl-workshop-w5`，同一分支继续提交。

## 现场 / 锚点
- 出处：R1 对 W-1 的复审 P3，报告 `board/review-w1-39863f1.md`，探针 `/tmp/crew/acl-530e/review/39863f1/probe_test.go` 的 `probeTokenAndEnv`。
- `runNative` 的处理顺序是：
  1. 把 stderr 收进 `diagnosticLimit + maxSecretLength` 大小的缓冲（`runner.go:22`）；
  2. 整段做 secret 替换（`runner.go:177-185`）；
  3. 截断到 `diagnosticLimit`（`runner.go:237-244`）。
- 构造方法：缓冲开头放一份完整 secret（64 位 hex），末尾放同一个 secret 的前 63 个字符。开头那份被替换成 `[REDACTED]`，缓冲缩短 54 字节；截断再丢掉末尾 10 字节，第二份于是剩下 53 个字符，进入诊断事件。
- 同样的"先替换、后截断"顺序，如果别处也有（stdout 结果文本、`agent_message`、结果 text 的截断），一并检查。

## 修法
- 截断以后，检查结果的**末尾**是否是任一 secret 的前缀，只看长度 ≥ 4 的前缀。如果是，把这段前缀去掉，或者替换成 `[REDACTED]`，再满足长度上限。
- 所有"先替换、后截断"的地方都统一用同一个 helper。
- 不改诊断的上限，不改 `[REDACTED]` 这个标记，不改错误改写文案。

## 明令不做
- 不动 relay 鉴权和 token 的生成方式。
- 不改 W-5、W-6 的逻辑。

## 测试
1. 把终审的构造写成正式测试：诊断事件里不能出现该 secret 任何长度 ≥ 4 的前缀，同时要保留尽可能多的正常文本。
2. 边界：secret 恰好跨在截断点上、有多个 secret、没有 secret、缓冲刚好等于上限、上限减一。
3. 破坏验证：去掉新 helper，测试 1 必须判红。
4. 全部经闸门运行，按 `board/resource-rules.md`。报审前跑一次 `cd services/workshop && go test -race ./... && go vet ./...`。

## 交付
在 `feat/acl-workshop-w5` 上单独提交一笔，报审格式同前。

## 追加：W-5 终审的 P3，只改测试
- `TestDockerCleanupFailsClosedWhenStillRunning`（测试 d）只把 `Running` 设为 true，`Status` 仍是空字符串。把 `docker.go` 的 `st.Running ||` 拿掉以后，这条测试照样是绿的。
- 改成三个子用例：`Status: "exited"` 分别配 `Running`、`Paused`、`Restarting` 为 true，各自单独锁住对应的判断。
- 破坏验证：依次拿掉这三个判断，对应的子用例必须判红。
- 这部分并进 W-7 同一笔提交。
