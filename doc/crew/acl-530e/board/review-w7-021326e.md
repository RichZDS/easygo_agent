# 审查 · W-7 · 021326eee7688fbaa03ad33113c3d21ac4ea4d65

结论：**通过**。W-1 复审里的 53 字符前缀已经去掉。另有一条 P3。

提交在 `feat/acl-workshop-w5`，父提交 `c97378be105c36a3429ae18a2fbf83fff8f380b8`。本笔 diff 只有 `runner.go`、`runner_redact_test.go`、`docker_test.go`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review` 的 detached HEAD `021326e`。没有改施工者分支，也没有提交。探针和破坏性补丁都已删掉，`git diff` 为空。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 相邻的两段 secret 前缀只剥掉最后一段

`secretPrefixSuffixLength` 只返回当前末尾最长的一段匹配（`runner.go:254-270`）。`redactAndBound` 只按这个长度切一次（`runner.go:291-293`）。切完以后新的末尾如果是另一条 secret 的前缀，就留在结果里。

复现：两条 64 字符 secret，输入是第二条的前 8 字符紧挨第一条的前 10 字符，长度刚好等于上限。结果是 `fedcba98`，`truncated=false`。这 8 个字符是第二条 secret 的前缀。日志在 `green.log` 的 `W7_STACKED text="fedcba98" trunc=false leaked=true`。探针原文是 `/tmp/crew/acl-530e/review/021326e/probe_test.go`。

W-1 的单条 secret、末尾只有一段前缀的构造不受这个影响：同一条 secret 的较短前缀已经被更长的那段包含。

## 已验证

1. **W-1 的构造不再留下前缀，正常文本没有多删。** secret 是 64 个十六进制字符，和 relay 的 `hex.EncodeToString` 32 字节一致（`relay.go:45-50`）。缓冲上限 `diagnosticLimit + maxSecretLength` = 16384+64。开头一份完整 secret，末尾同一 secret 的前 63 字符，中间 16321 个 `B`。诊断正文是 `[REDACTED]` 加这 16321 个 `B`，共 16331 字节，再加原来的 ` [diagnostics truncated]`，共 16355 字节。事件文本里没有该 secret 任何长度 ≥ 4 的前缀。3 字符后缀会保留。不含 secret 的普通文本按上限切开，一个字节不多删。
2. **上限刚好和上限减一。** 长度等于上限、末尾是 8 字符前缀时，只去掉这 8 个字符，`truncated` 仍是 false。长度比上限少 1、末尾没有前缀时，原文不动。正式表里的「没有 secret、刚好等于上限、上限减一」三行也通过。跨在截断点上的用例和两条 secret 各有一段末尾前缀的用例通过；后者切完只剩 `[REDACTED]`。
3. **先替换再截断的地方只有诊断这一处，已经走 `redactAndBound`。** `runNative` 在 `runner.go:181` 调用它。stdout 超过 `maxOutput` 时直接失败，不截断正文（`runner.go:355-356`）。`agent_message`、result 和发出去的事件是整段替换（`runner.go:391-392`、`445`、`457`），没有第二处截断。
4. **去掉末尾剥离后，测试 1 判红。** `TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary` 失败。同一条命令里，跨截断点和多 secret 两行也失败；没有 secret、刚好等于上限、上限减一仍然通过。去掉剥离后再量同一种 W-1 构造，诊断里最长的 secret 前缀是 53（`sab-helper-len.log` 的 `W7_SAB_LONGEST 53`）。`runner.go` 已复原。
5. **测试 d 拆成三个子用例，各自锁住一个判断。** `TestDockerCleanupFailsClosedWhenStillActive` 的 Running、Paused、Restarting 都先把 `Status` 设成 `exited`（`docker_test.go:654-661`）。`exited` 属于已停止（`docker.go:327-332`），所以失败关闭只能来自 `docker.go:373` 的三个布尔值。三个子用例都通过。拿掉 `st.Running` 时只有 Running 失败，另外两个通过；Paused、Restarting 同样只让自己的子用例失败。失败形态都是 `container confirmed stopped; removal deferred`。`docker.go` 已复原。本笔没有改 W-5 的判断本身。
6. **全量只跑了一次。** `cd services/workshop && go test -race ./... -count=1` 退出码 0（workshop 包 73.9 秒）。`go vet ./...` 退出码 0。没有设置 `EASYGO_DOCKER_TEST_*`，没有新建镜像。

## 没覆盖到

- 没有起真实容器。回收三个子用例用的是假 Docker。
- 没有把短于 `[REDACTED]`（10 字符）的 secret 放进替换。生产 relay token 是 64 字符，替换会变短。
- 没有重审 W-5、W-6 的产品逻辑。本笔 diff 没有改它们。
- 正式测试「缓冲刚好等于上限」用的是一串 `x`，末尾没有 secret。末尾带前缀的情况是审查探针补的。

## 证据

目录：`/tmp/crew/acl-530e/review/021326e/`

| 文件 | 含义 | 退出码 |
|---|---|---|
| `green.log` | 正式两支测试、三个回收子用例，以及审查探针 | 0 |
| `probe_test.go` | 探针原文。精确长度、3 字符保留、普通切开、相邻前缀 | |
| `sab-helper.log` | 去掉末尾剥离后的正式测试 | 1 |
| `sab-helper-len.log` | 同一种构造下最长残留前缀 53 | 0 |
| `sab-Running.log` / `sab-Paused.log` / `sab-Restarting.log` | 各拿掉一个判断 | 都是 1，且只有对应子用例失败 |
| `race.log` | `go test -race ./... -count=1` | 0 |
| `vet.log` | `go vet ./...` | 0 |

命令在 `services/workshop` 下执行，Go 是 `/home/ubuntu/sdk/go/bin/go`。重命令都经 `CREW_SEAT=acl-review-530e /tmp/crew/acl-530e/bin/heavy`。
