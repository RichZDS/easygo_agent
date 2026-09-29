# 终审队列（工头维护，按顺序审）

每审完一笔：把报告写到指定文件，herdr-msg 通知工头，然后回到这里取下一笔。"状态"列由工头更新。

**分工（09-29 增设第二终审后）**：
- R1 `acl-review-530e`（loop 侧）：#2、#3、#7、#6、#11 全部通过，队列已空，待命。
- R2 `acl-review2-530e`（workshop 侧）：#4 W-1（通过）→ #5 W-2（通过）→ #8 W-4（通过）→ #9 W-5（通过）→ #10 W-6（通过）。R2 队列已空，待命；W-7 归 R1。#6 已改派给 R1，R2 跳过。
- 各审各的，不互相联系。跑全量 `go test -race` 之前先用 `ps -eo args | grep -c '[g]o test -race'` 确认没有别人在跑全量，有就等。

| # | 交付 | 提交 | 分支 | 施工图 | 报告文件 | 重点 | 状态 |
|---|---|---|---|---|---|---|---|
| 1 | W-3 取消与退出竞态 | eb46347605a4096c0907683f35153baea6cd18b4 | feat/acl-workshop | tasks/w3-race.md | review-w3-eb46347.md | 超额一律 failed；非 succeeded 不登记产物；30 s 有界扫描；把修复回退后测试要判红 | 通过（工头已签） |
| 2 | L-1 工具注册表 | 78d893f7558d70c039a9ea4e9c0f76a9f64a87ba | feat/acl-loop | tasks/l1-registry.md | review-l1-78d893f.md | mutating 不确定结果仍终止 run；角色过滤同时管展示和执行；pack_dir 边界；knowledge 错误分类不变 | 通过（P3 一条：>64KiB 的 knowledge 参数 reason 变为 tool_arguments_too_large，仍是可恢复 -32602，接受）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 3 | L-2 读消息/回复/证据 | 76530b2a0989a6fe5f5bb481650b94ea94ff041a | feat/acl-loop | tasks/l2-tools.md | review-l2-76530b2.md | 先 run_ids 再整批严格校验 events，截断不能掩盖尾部非法事件；reply 不确定不回模型；evidence 身份与分页；Web 两层白名单的 namespace 不能伪造 | 通过（P3 一条：call id 较长时 reply/submit 的幂等 key 可能超过契约的 128 字节，是明确拒绝且可恢复；记为后续项）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 4 | W-1 施工者通道 + W-1b 说明更正 + W-1c 夹具补丁 | 39863f11ae348d1d8e1fb569b3c1a14938e13e14、a2e0b505fe5dd8432f8ae1d24a0ae0268cba4d39、570ff08d9f71a70bd48a2e1c4d160f0473398f7f | feat/acl-workshop | tasks/w1-channel.md（含末尾 W-1b、W-1c 节） | review-w1-570ff08.md | /crew 路由鉴权，旧 run 的 token 能否写新 run；client_id 幂等与 200 条上限的事务性；resume 拼接未读消息与 crew.read 的原子性；run_ids 顺序与 256 上限；token 不进任何输出、错误或事件；Docker 与 host 两种模式的 env 注入；easygo-crew 重试不重复。worker.md 的 ask/blocked 矛盾由 W-1b 修正，看最终版 | 通过（P3 一条：doc/workshop.md 的夹具 op 表缺 echo_input，由工头在集成时补）；工头已签（集成回归全绿 51dadc9，09-29 15:15）；R1 另对 39863f1 复审通过，P3 一条转为 W-7 |
| 5 | W-2 验收检查与证据 | aa41e657c65807072d66766b0d27cbde9b5bb7fa | feat/acl-workshop | tasks/w2-acceptance.md | review-w2-aa41e65.md | 检查容器能否写工作区/pack、能否联网、能否拿到 relay 或凭证（重点核对 `,dst=/workspace,` 字符串匹配加 readonly 这类实现在参数变化时是否仍然成立）；pack 快照拒绝符号链接、权限与内容哈希；施工者能否在任务内影响检查结果；超时/取消后检查容器必被回收；false_green 真值表；树哈希排除 .workshop-home 且对内容/路径/类型敏感；evidence 身份、跨 namespace、UTF-8 分页与 1 MiB 截断；取消与重启时 acceptance 状态 | 通过（P3 两条：只读挂载靠 `,dst=/workspace,` 子串匹配；重启改写为 interrupted 时留下旧的 false_green。两条都转为 W-6）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 6 | W-3b 终审 P2 修正（小） | 3adec82aad036aee686284d093bc8666cd64c9e0 | feat/acl-workshop | tasks/w3-race.md 末尾 W-3b 节 | review-w3b-3adec82.md | 只在最终扫描自身预算到期时包装为 ErrDiskQuotaScanFailed；调用方取消不能触发；取消/不取消都 failed 且无产物；这是你自己提的 P2，确认是否关闭即可 | 通过（W-3 的 P2 已关闭；-race 下四支各 200 次；去掉包装后判红；P0–P3 无）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 7 | L-3 harness 场景库（两段） | efacf6de7412782bdc0662ad680cd48beed76ecb 与 24fe1ac2afa09f0910cfa0bf4643bbbc2e1713da | feat/acl-loop-l3 | tasks/l3-scenarios.md | review-l3-24fe1ac.md | 调用链是否真的走完模型夹具→loop 工具→工坊→容器→easygo-crew→事件→loop 读回，有没有旁路写操作；每个断言是否测到它声称的东西（挑两三个故意做坏的变体验证会判红）；S4 独立树哈希算法与平台一致且排除 .workshop-home；S5 受信 checker 防改证据；S9 真在检查运行中取消；清理是否彻底 | 通过（P0–P3 无；九场景跑一遍；S3/S4/S9 改坏断言后退出码均为 1）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 8 | W-4 Docker 模式开放 shell 工具 | be7dac06a3ab24551e1d749632f1951c3de917a3 | feat/acl-workshop | tasks/w4-runtime-shell.md | review-w4-be7dac0.md | 只在 Docker 路径放开 shell，host 模式工具集完全不变；三个 CLI 的工具名与固定版本的 help/schema 实测一致；Claude 保留 --restricted/--bare 时显式 Bash 确实生效；read-only 策略下开 shell 不破坏工作区只读；resume 参数一致；文档说明两种模式差别 | 通过（P0–P3 无）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 9 | W-5 容器回收韧性 | 1e1e78ca5afe4a94a44f016dc5211ccc69539f6a 与返工 fd480ab（基线 be7dac0，审 be7dac0..fd480ab） | feat/acl-workshop-w5 | tasks/w5-cleanup-resilience.md | review-w5-fd480ab.md | 状态不明、仍在运行、标签不符、超过上限时必须失败即关闭（逐条对照测试 d/e/f/g）；只有确认已停止才转入待回收；runCheck 在"待回收"时保留结果、在失败即关闭时仍报错（h/i/j 对照）；清扫不阻塞准入、同一时刻只有一个清扫、-race 干净；remove 的 owner 标签校验没有被绕过；duration_ms 不含回收；Initialize 孤儿回收语义不变 | 通过（P3 一条：测试 d 没有单独锁住 Running，Paused/Restarting 也没有用例，并入 W-7）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 10 | W-6 W-2 终审两条 P3 | c97378be105c36a3429ae18a2fbf83fff8f380b8（审 fd480ab..c97378b） | feat/acl-workshop-w5 | tasks/w6-w2-p3.md | review-w6-c97378b.md | 只读由构造决定、不再依赖字段顺序（挪动字段测试仍绿、去掉只读测试判红）；检查容器和 read-only 策略下工作区写入被拒；finishCrewOutcome 重算 false_green 后，真值表在所有 finish 路径成立；Initialize 探针和 workspace-write 行为不变；另核：service.go 两处 interrupted 事件字面量补了 FalseGreen（施工图要求，归属表已补）；提交说明里"又加强了两支 W-5 测试"那句其实是 fd480ab 做的，这笔没有改，核对即可，不算问题 | 通过（P3 一条：取消子用例没有单独锁住"清掉已为真的 false_green"，interrupted/failed 子用例已锁住同一函数，接受不修）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |
| 11 | W-7 诊断截断不留 secret 前缀 | 021326eee7688fbaa03ad33113c3d21ac4ea4d65（审 c97378b..021326e） | feat/acl-workshop-w5 | tasks/w7-redact-boundary.md | review-w7-021326e.md | 截断以后，末尾不残留任何 secret 的前缀（≥4 字符）；所有先替换后截断的地方都走同一个 helper；正常文本不被多删；去掉 helper 后测试判红；另含 W-5 测试 d 拆成 Running/Paused/Restarting 三个子用例（Status 都是 exited）。工头初核：runner.go 里先替换后截断的只有诊断这一处，stream parser 超限直接判失败、不截断 | 通过（P3 一条：相邻两段前缀只剥掉最后一段；接受不修，理由见 followups F-6）；工头已签（集成回归全绿 51dadc9，09-29 15:15） |

**资源：09-29 14:15 起按 `board/resource-rules.md` 执行，所有重负载命令经 `/tmp/crew/acl-530e/bin/heavy` 闸门运行。**

资源提示：
- 你的上下文窗口有 256K，多笔审查会逐渐占满。每笔审完，把关键结论都写进报告文件，不要依赖记忆。
- 如果快满了，先告诉工头，由工头给你换一个新会话接着审，交接就靠这些报告文件。
- 真实容器和 `-race` 大循环很吃资源。workshop 席同时也在跑 W-2 的测试，能串行就串行。
