# W-5 · 容器回收在高负载下不再让整个工坊停摆

assignee: workshop2（`acl-workshop2-530e`）· 状态: done（1e1e78c + 返工 fd480ab；工头初核三处返工已落实，破坏验证 e/ps、f 判红后恢复；工头已签 51dadc9）
基线：`feat/acl-workshop` 的 `be7dac0`（已含 W-1…W-4）。在 worktree `/home/ubuntu/Projects/easygo-acl-w5` 的分支 `feat/acl-workshop-w5` 上施工。

## 现场 / 锚点

### 工头真实模型样本里的现象（09-29 12:50）
- 机器负载 17.9（4 核，负载主要来自别的会话）。
- Pi 任务的检查容器在 1.2 s 内正常退出，退出码 0。
- 随后 `DockerRunner.cleanup` 的 20 s 预算内，`ps` → `inspect` → `rm --force` 这一串 docker 命令没做完。
- 结果有三条：
  1. `cleanupFailure` 被永久置位。
  2. 这次验收记成 `error`。证据里退出码是 0，`duration_ms` 却是 25681，这个时长把回收也算进去了。
  3. 下一个 OpenClaw 任务直接被拒，报 `Docker cleanup previously failed; operator recovery required`。
- 负载降下来以后重跑，OpenClaw 和 Pi 都通过。
- 证据：`/tmp/crew/acl-530e/foreman/live/others.log`、`/tmp/crew/acl-530e/foreman/live/reports/r-mY4dwU.json`（失败）和 `reports/r-zV8VxK.json`（重跑通过）。

### 代码（行号以 be7dac0 为准）
- `docker.go:284` `remove`：先 `ps -aq --filter name=^/NAME$`，再 `inspect`，校验 owner 标签，最后 `rm --force <ID>`。注释写明："不存在"只能由一次成功的过滤列表确立，不能从传输错误推断。这条保持不变。
- `docker.go:312` `cleanup`：单次 20 s，失败一次就置位 `cleanupFailure`，之后永不清除。
- `cleanupFailure` 置位后拒绝新工作的地方有两处：
  - `docker.go:406-415` 的 `Run`；
  - `acceptance_docker.go:32-39` 的 `runCheck`。
- `docker.go:500-505`：`Run` 延迟回收失败时，追加错误 `task container cleanup failed; admission disabled`。
- `acceptance_docker.go:47-51`：`runCheck` 延迟回收失败时，**覆盖** `err`。命名返回值 `exitCode` 此时已经是 0，于是调用方拿到"退出码 0 + 错误"，状态被判成 `error`。
- `acceptance.go:170-173`：`duration_ms` 是围着整个 `runCheck` 量的，包含回收时间。
- `docker.go:326` `Initialize`：重启时回收本 owner 的全部容器，这是现有的最终兜底。它的探针容器也走 `r.cleanup`。
- DockerRunner 没有 Close，`Runner` 接口只有 `Run`，所以**不要新开常驻 goroutine**，见修法第 4 条。

### 原来为什么失败即关闭
怕任务或检查结束后容器还活着，继续占资源、写工作区。一旦**确认容器已经不在运行**，它就不再危险，只是占一点磁盘（rootfs 只读、日志驱动为 none）。

## 修法

1. **回收先有界重试。** `cleanup` 内部最多尝试 3 次 `remove`：
   - 每次用独立的 20 s 上下文，单次预算的含义不变；
   - 两次之间退避 1 s、2 s。
   - 任意一次成功就返回成功。前一次的 `rm` 可能在 daemon 侧其实已经完成，下一次 `ps` 为空时同样算成功。

2. **重试仍失败时，确认一次状态。** 用一个独立的 20 s 上下文：
   - 先 `ps -aq --filter name=^/NAME$`：
     - 命令成功且结果为空：容器已不存在，按成功处理。
   - 否则 `inspect` 那个 ID，只能解析出恰好一个对象，而且 owner 三个标签必须全部匹配。
     - 满足 `State.Running=false`、`Paused=false`、`Restarting=false`，并且 `Status ∈ {created, exited, dead, removing}`：判为**"已停止、待回收"**。
       - 不置位 `cleanupFailure`。
       - 把名字记进 DockerRunner 的待回收集合，受 `mu` 保护。
       - `cleanup` 返回一个可以区分的哨兵错误，比如 `errCleanupDeferred`，调用方用 `errors.Is` 判断。
     - 其它任何情况，包括仍在运行、`ps` 或 `inspect` 失败、解析失败、标签不符：保持**失败即关闭**，置位 `cleanupFailure`，返回原有类别的错误。

3. **待回收集合有上限**：超过 32 个时，按失败即关闭处理，也就是置位 `cleanupFailure`。这是给 daemon 长期坏掉准备的保险。

4. **清扫不开常驻 goroutine。** 在 `Run` 和 `runCheck` 的准入处，也就是通过 `initialized` 和 `cleanupFailure` 检查之后，顺手做一次尽力清扫：
   - 总预算 20 s，对集合里的每个名字调用 `remove`，它会校验 owner 标签；成功就从集合移除，失败就留在集合里。
   - 同一时刻只允许一个清扫，用 `mu` 下的标志位控制，其余调用直接跳过。
   - 清扫失败**不阻止**准入。
   - 重启以后照旧由 `Initialize` 兜底。

5. **调用方语义。**
   - `runCheck`：
     - 延迟回收返回 `errCleanupDeferred` 时，保留已经得到的结果，包括退出码、`timedOut` 和 `err`，不覆盖；
     - 只有失败即关闭时，才像现在一样把 `err` 改成 `check container cleanup failed`。
     - 超时的检查同样适用：`rm --force` 失败了，但状态确认显示容器已停止，结果就还是超时（failed），而不是 error。
   - `Run`：`errCleanupDeferred` 不追加 `admission disabled` 错误，其它行为不变。
   - `Initialize` 的探针容器：`errCleanupDeferred` 视为成功。
     - 注意它的 `defer` 用 `errors.Join(err, r.cleanup(name))`，要单独处理，别把哨兵错误 join 进初始化错误。
   - 孤儿循环里直接调用的 `r.remove` 不改。

6. **证据时长只算检查本身。** 让 `runCheck` 把"创建到 `start --attach` 返回"这段时长交给调用方，比如多返回一个 `time.Duration`，或者由 `acceptance.go` 在回收之外计时，实现方式由你定。`duration_ms` 不再包含回收时间。改动只限于 `acceptance.go:170-173` 附近这几行。

## 明令不做

- 不放宽"状态不明或仍在运行就失败即关闭"。
- 不删除别的 owner 的容器，删除一律经过 `remove` 的标签校验。
- 不改单次 20 s 预算的含义，也不改 `Initialize` 孤儿回收的失败语义。
- 不开常驻 goroutine，不给 `Runner` 接口加方法。
- 不改 W-1 到 W-4 的其它行为。遇到别的问题，写进报审的"偏差"一节，不要顺手修。
- 不 push、不 merge、不调用付费模型。

## 测试

1. **假 docker 单测**，用 `DockerRunner.command` 注入，参照 `docker_test.go` 的现有写法：
   - a. `rm` 前两次失败、第三次成功：不置位，`cleanup` 返回 nil。
   - b. 前一次 `rm` 实际已生效，下一次 `ps` 为空：成功。
   - c. `rm` 一直失败，`inspect` 显示已停止（`exited`）：
     - 不置位，进入待回收集合，`cleanup` 返回 `errCleanupDeferred`；
     - 下一次 `Run` 或 `runCheck` 准入时清扫成功，集合变空。
   - d. `rm` 一直失败，`inspect` 显示 `Running=true`：置位；下一次 `Run` 和 `runCheck` 被拒。
   - e. 确认阶段的 `ps` 失败，或者 `inspect` 失败：置位。
   - f. `inspect` 标签不符：置位，而且不发 `rm`。
   - g. 集合达到上限后再来一个待回收：置位。
   - h. `runCheck` 拿到退出码 0，之后回收进入待回收：返回 `(0, false, nil)`。
   - i. `runCheck` 超时，`rm` 失败，但确认已停止：返回 `(-1, true, nil)`。
   - j. `runCheck` 拿到退出码 0，之后回收失败即关闭：仍然返回错误。这条要和 h 对照着写。
   - k. 两个 goroutine 同时准入：只有一个在清扫；在 `-race` 下跑。
2. **真容器**，用专用 daemon：
   - 包一层真的 `command`，让检查容器的 `rm` 前 3 次都返回失败；
   - 跑一个很快退出 0 的检查；
   - 断言：
     - 验收是 `passed`，`duration_ms` 小于 10000；
     - 下一个任务能被准入；
     - 准入时的清扫把那个已停止的容器删掉了，用 `ps -a --filter label=<owner>` 取证。
3. **全量**：`cd services/workshop && /home/ubuntu/sdk/go/bin/go test -race ./... && /home/ubuntu/sdk/go/bin/go vet ./...`。
   - 跑之前用 `ps -eo args | grep -c '[g]o test -race'` 确认没有别人在跑全量，有就等。
4. **回退验证**：把第 5 条的 `runCheck` 改动临时回退，确认 h、i 两条会判红。在报审里写明，然后恢复。

## 交付

- 单独一笔提交，放在 `feat/acl-workshop-w5` 上。
- `doc/workshop.md` 加一小段，说明回收重试、"已停止、待回收"，以及失败即关闭的边界。
- 报审里要写清：
  - 重试和清扫的参数；
  - "状态不明时失败即关闭"由哪几条测试证明；
  - 证据路径（放在 `/tmp/crew/acl-530e/workshop2/`）。

---

## W-5 返工（工头初核 1e1e78c，09-29）

整体结构对得上施工图：重试、确认、待回收、准入清扫、三处调用方语义、计时，都做到了。真容器测试的写法也不错。下面三处要改。

1. **确认阶段的 `ps` 失败要直接失败即关闭。**
   - 现在 `confirmCleanup` 在 `ps` 出错时继续往下走 `inspect`（`docker.go` 里 `if err == nil && raw == ""` 那行）。容器如果确实已停止，就会被判成待回收。
   - 这和施工图第 2 条（"`ps` 或 `inspect` 失败 → 失败即关闭"）、你自己写进 `doc/workshop.md` 的"确认阶段 `ps`/`inspect` 失败……保持原有的失败即关闭"、以及测试名 `FailsClosedWhenConfirmationTransportFails/ps` 都不一致。
   - 改成：`ps` 出错就 `return false, err`；`ps` 成功且为空，返回 gone；`ps` 成功且非空，再 `inspect`。

2. **测试 e（两个子用例）和 f 现在证明不了它们声称的东西。**
   - `registerFakeContainer` 的 State 是零值，Status 为 `""`，本来就不在允许集合里，所以不管有没有 `ps` 失败或标签不符，都会失败即关闭。把第 1 条的 bug 留着，e/ps 照样是绿的，这就是证据。
   - 改法：e 的两个子用例和 f 都把假容器设成真正停止的状态：`Status: "exited"`，`Running`、`Paused`、`Restarting` 都为 false。这样失败即关闭只可能来自传输失败或标签不符。
   - **破坏验证**，要写进证据：
     - 临时让 `ps` 失败继续往下走 `inspect`（也就是现状），e/ps 必须判红；
     - 临时删掉 `confirmCleanup` 里的标签循环，f 必须判红；
     - 两处都恢复后判绿。
   - 顺手检查 d、g 有没有同样的问题：d 应该只因 `Running=true` 判红；g 的假容器已经是 `exited`，没问题。

3. **`doc/workshop.md` 把 W-4 的小节标题删了。**
   - diff 里 `-## Docker 与 host 的 shell 工具` 这一行被删掉，W-4 那段正文于是挂到了你新加的"容器回收"小节下面。
   - 把这个标题恢复原样，多出来的空行也清掉。

另外说明两点：
- 你改了 `docker_test.go` 和 `acceptance_integration_test.go`，它们不在归属表里。它们是被改代码的测试文件，工头认可，已补进归属表。
- 32 这个上限保持硬编码常量，不做成配置。

交付：在 `feat/acl-workshop-w5` 上**另起一笔提交**，不要 amend，方便终审对照。先按用例跑 `-race`，再跑一次全量 `go test -race ./... && go vet ./...`，跑之前照旧确认没有别人在跑全量。然后报审。
