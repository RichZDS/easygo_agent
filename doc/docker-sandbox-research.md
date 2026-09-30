> 历史文档：这里描述的代码已于 2026-09-30 从主分支移除，完整实现保留在 git tag `legacy-go-app-final`。

# Docker 本地代码沙箱调研

调研日期：2026-09-11

## 结论

普通 Docker 容器可以实现本地、运行时禁网、可写代码、可编译执行、主动销毁和定时销毁的代码沙箱，但租约、运行中容器 TTL、容量控制和崩溃恢复必须由 EasyGo 自己实现。

最终方案采用“一 Agent 会话一逻辑申请”的租约模型：申请 ID 绑定一个具名 volume，运行容器只是可休眠、可重建的计算载体。始终设置两层上限：默认最多 3 个运行中容器、最多 2 个同时进行的 create/start。容器会复用一段时间以摊薄 create/start/stop 开销，但整个申请从创建开始绝对不得超过 5 小时。

实施前本机 Docker daemon 处于关闭状态。验证阶段按约定临时启动 Docker Desktop，完成真实容器测试和 benchmark 后再次关闭；本文只报告这台机器本次测得的数据，不把它外推成其他机器的固定成本。

## Docker 能力与边界

### 生命周期和 TTL

- `docker create` 只创建容器和可写层，不会启动容器；之后需单独 `start`。[Docker container create](https://docs.docker.com/reference/cli/docker/container/create/)
- `docker exec` 只能在容器 PID 1 正在运行时执行，容器重启后原 exec 不会自动恢复。[Docker container exec](https://docs.docker.com/reference/cli/docker/container/exec/)
- `docker stop` 先发送 `SIGTERM`，宽限期后发送 `SIGKILL`；强制 `docker rm -f` 会用 `SIGKILL` 终止运行中容器再删除。[Docker container stop](https://docs.docker.com/reference/cli/docker/container/stop/)、[Docker container rm](https://docs.docker.com/reference/cli/docker/container/rm/)
- `--rm` 只在容器主进程退出后自动删除容器；container prune 的对象是已停止容器。[Docker run clean-up](https://docs.docker.com/reference/cli/docker/container/run/#clean-up---rm)、[Docker container prune](https://docs.docker.com/reference/cli/docker/container/prune/)

由上述官方生命周期原语可得：普通 Docker 没有可直接配置的“运行中容器到期后销毁”租约。EasyGo 必须自行维护 `expires_at`，到期时 stop/remove，且在进程重启后做一次全量对账。

Docker 容器 label 在对象整个生命周期内是静态的，修改 label 必须重建容器。因此可变的 `expires_at` 和调用次数不能只放 label，应存入 Controller 自有的 bbolt 状态卷；label 仅用于标识 EasyGo 沙箱、owner、创建时间和固定的 hard deadline。[Docker object labels](https://docs.docker.com/engine/manage-resources/labels/)

建议租约算法：

```text
base_idle_ttl = 1m
hard_deadline = created_at + 5h
idle_ttl = min(base_idle_ttl * 2^distinct_run_index, 5h) # first run index is 0
expires_at = min(now + idle_ttl, hard_deadline)
```

首次申请只建立逻辑申请和 volume；显式 create 或第一次文件/执行操作才启动容器。同一 run 的成功操作刷新空闲截止时间，但只在该 run 首次有效使用时提升一个指数档位；状态查询和失败请求不续租。Agent 可主动休眠或销毁。达到 hard deadline 时，即使有新调用也不再续租；新的工作应申请新的 ID。

### 工作区和数据销毁

Docker volume 会在使用它的容器被删除后继续保留；`docker rm -v` 和 `--rm` 只自动清理由 Docker 匿名创建的 volume，具名 volume 不会被自动删除。[Docker storage](https://docs.docker.com/engine/storage/)、[Docker container rm](https://docs.docker.com/reference/cli/docker/container/rm/#remove-a-container-and-its-volumes--v---volumes)

最终方案利用这个性质，将每个申请的工作区放在独立具名 volume 中：

- 只读 root filesystem；编译器和运行时全部预装在镜像中。
- `/workspace` 挂载该申请专属的具名 volume，容器停止或重建后内容仍保留。
- `/tmp` 使用有明确 size 的 tmpfs；临时文件不会进入持久工作区。
- 不把 volume 共享给其他申请，也不把宿主路径暴露给代码沙箱。

标准 local volume 没有跨 Docker Desktop/Linux 都可靠的单 volume 硬配额。因此实现使用每申请 1 GiB 软水位、20 GiB 总水位、30 个申请上限和 10 GiB 宿主剩余空间保护。Controller 在写入前检查，并在执行期间立即检查一次、此后每秒检查；越线会取消命令并停止容器。它仍是软限制，最坏可超出一个检测周期内的写入量，不能承诺对抗恶意高速写满。需要硬保证时，应改用具备硬配额的 volume driver 或受限 tmpfs 加快照。`/tmp` 的 tmpfs 计入容器 memory cgroup，且页面可能进入 swap，因此 `memory-swap` 应等于 `memory`。[Docker tmpfs mounts](https://docs.docker.com/engine/storage/tmpfs/)

不使用可写 bind mount。Docker 官方明确指出，可写 bind mount 内的进程可以创建、修改或删除宿主文件。[Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/#considerations-and-constraints)

### 运行时禁网

容器创建时使用 `NetworkMode=none`，不连接 bridge、不发布端口。Docker 的 `none` network driver 只在容器内创建 loopback 设备，并将容器与宿主及其他容器隔离。[Docker none network driver](https://docs.docker.com/engine/network/drivers/none/)

禁网只针对容器运行时。沙箱镜像需在部署阶段构建完成，外部依赖必须预装、vendor 化，或由受信任的管理层作为文件上传。运行时不允许容器自行 pull、`pip install` 或 `go get` 网络依赖。

### 资源和宿主安全

Docker 容器默认没有 CPU 或内存限制，可使用宿主调度器允许的全部资源。Docker 官方同时指出，宿主 OOM 可能影响 daemon 和其他系统进程。[Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/)

建议初始限制如下，随后依据本机基准调整：

- 每容器 1 CPU。
- 每容器 2 GiB memory，`memory-swap` 与 memory 相同。
- 每容器最多 256 PIDs。
- `/workspace` 使用具名 volume，软水位 1 GiB；`/tmp` 256 MiB tmpfs，`/dev/shm` 64 MiB。
- 全局最多 3 个运行/启动中的容器，最多 30 个未销毁申请。
- 同时进行 create/start 的沙箱最多 2 个。
- 每个 Agent 会话最多 1 个沙箱，同一沙箱内命令串行执行。

其余容器设置：非 root 用户、`ReadonlyRootfs=true`、drop all Linux capabilities、`no-new-privileges`、默认 seccomp、private PID/IPC namespace、无 device、无 privileged、无宿主 socket、无宿主环境变量或密钥。

Docker Engine API/socket 只能由可信管理层访问，绝不能挂进代码沙箱。Docker 官方说明，控制 daemon 的用户能够创建宿主目录挂载等高权限容器，Docker group 也授予 root 级权限。Linux 生产部署优先使用 Rootless Docker；若管理层以容器部署并挂载 rootful socket，必须把管理 API 做成固定模板和严格白名单，不能把 image、mount、capabilities 等原始 Docker 参数转交给 Agent。[Docker Engine security](https://docs.docker.com/engine/security/)、[Rootless mode](https://docs.docker.com/engine/security/rootless/)、[Linux post-install warning](https://docs.docker.com/engine/install/linux-postinstall/#manage-docker-as-a-non-root-user)

### 并发和恢复

Docker 官方的 daemon 配置公开了 image pull/push 的并发限制，但没有提供应用级的“最多运行 N 个容器” admission control。因此 EasyGo 必须在创建前原子占用容量，并把 `allocating`、`running`、`destroying` 都计入上限。[dockerd options](https://docs.docker.com/reference/cli/dockerd/)

推荐流程：

1. Controller 在 bbolt 事务中申请容量和 owner lease；同一 owner 的重复申请幂等返回现有申请 ID。
2. 通过 Docker Engine API create/start，固定使用受信任镜像和服务端资源模板。
3. create 或 start 失败时释放容量，并尽力强制删除残留容器。
4. 容量满时先停止最久未使用的空闲容器并保留其 volume；若全部 busy，则 FIFO 等待最多 30 秒，再返回结构化 `capacity_exhausted` 和建议重试时间。
5. 每 10 秒扫描空闲和过期 lease；空闲容器只 stop，hard deadline 才删除容器和 volume。
6. 管理进程启动时按 EasyGo label 枚举全部容器和 volume，与 bbolt 对账；遗留执行先停止，孤儿和已过 hard deadline 的资源删除。

Docker events 可以报告 `create`、`start`、`die`、`oom`、`stop` 和 `destroy` 等事件，也能按 label 过滤，但官方说明最多只回放最近 256 条事件。因此事件流用于快速响应，不能代替启动时和周期性 reconciliation。[Docker system events](https://docs.docker.com/reference/cli/docker/system/events/)

运行资源数据可通过 `/containers/{id}/stats` 或 `docker stats` 读取，包括 CPU、memory、PIDs 和 block I/O。[Docker container stats](https://docs.docker.com/reference/cli/docker/container/stats/)

## 编译镜像建议

仓库当前声明 Go 1.25，因此基础镜像应与 Go 1.25 对齐，并在构建阶段安装：

- Go 1.25 工具链。
- Python 3、pip、venv。
- Node.js 22 和 npm。
- GCC、G++、make 和常见 libc headers。
- git、bash、coreutils、findutils、tar、unzip、jq、ripgrep。

镜像构建完成后以固定 tag/digest 运行，容器创建时禁止隐式 pull。`HOME`、Go build cache 和 Python virtualenv 都指向 `/workspace`，避免向只读 root filesystem 写入。

官方镜像层为只读、不可变并可由多个容器共享；每个容器增加自己的可写层。采用只读 root filesystem 和独立 volume 后，多 Agent 不会各复制一整份编译镜像，但每个编译任务仍会独立消耗 CPU、memory、PIDs 和临时空间。[Docker storage overview](https://docs.docker.com/engine/storage/)

## 当前机器事实

以下是 2026-09-11 在测试前后的检查结果：

- 系统：macOS/Darwin 25.6.0，arm64。
- 宿主：12 个逻辑 CPU，25,769,803,776 bytes RAM，即 24 GiB。
- Docker client：29.7.2，API 1.55，darwin/arm64，context 为 `desktop-linux`。
- Docker Engine：29.7.2，Docker Desktop Linux VM 分配 12 CPU、8,319,238,144 bytes RAM（约 7.75 GiB），overlayfs、cgroup v2、内置 seccomp 和 cgroup namespace。
- 普通 `docker compose` 插件路径未被 CLI 找到；测试脚本回退到 Docker Desktop 内置 Compose v5.5.0 并成功完成部署。
- Runtime 镜像配置大小为 347,803,357 bytes，本机解包后的 `docker image ls` 显示约 1.45 GB；镜像用户为 `1001:1001`，并带有 Controller 强制校验的 runtime label。
- Docker Desktop 从 daemon 关闭到 `docker info` 可用，本轮单次观察约 5 秒。该值包含 VM/daemon 唤醒，不属于容器 create/start 成本，也不是稳定 benchmark。

Docker Desktop 的 VM 资源配额与宿主资源并不相同，容量计算必须使用 daemon 的 `docker info`/Engine Info 数据，而不是直接使用 macOS 的 12 CPU 和 24 GiB。官方说明 Resource Saver 唤醒 Linux VM 通常需要 3–10 秒，这属于 VM 冷启动时间，必须与热态容器生命周期分开测量。[Docker Desktop Resource Saver](https://docs.docker.com/desktop/use-desktop/resource-saver/)

## 本机实测结果

### 正确性和压力测试

- 真实 Docker 契约测试通过 Python、Go、Node.js、C++ 编译/执行、`network=none`、非 root、只读根文件系统、cgroup 参数、路径穿越与符号链接逃逸防护、后台进程清理、stop/start 和容器重建后的 volume 持久化，以及 destroy/硬过期后的容器与 volume 删除。
- 100 个并发申请压力测试通过；运行容器观测值始终不超过配置的 3，容量 waiter 取消后无泄漏，测试结束时专用 managed label 下的容器和 volume 均为零。
- 第一次高频 benchmark 暴露了 Docker Desktop 在 volume 创建/删除并发期间偶发的全局 `DiskUsage` 快照失败：5 个 Controller 请求失败，并被展开记录为 7 个 phase failure。实现随后加入 4 次短暂、指数退避且可取消的重试；持久失败仍 fail-closed。相同 100 次矩阵复跑为 0 failure。

### 100 次 no-op 生命周期矩阵

以下 p50/p95/p99 均为毫秒；daemon 已热、镜像已缓存，预先丢弃 3 次 warmup。并发度 1、3、6 各执行 100 个完整申请，共 6,000 个 phase 观测，0 failure，总测量时间 103.467 秒。该轮使用 schema 3，已经记录 Controller 内部的真实容量排队时间。

- 并发 1：active reuse exec 为 39.1/42.6/51.7；cold create 上界为 27.2/32.2/45.4，create→start 为 35.5/41.0/68.7，start→ready 为 21.5/24.2/33.0；stop 为 16.8/22.1/22.4，remove 为 6.1/7.2/8.9，完整 destroy HTTP 为 32.7/40.7/44.5。三条激活路径的 queue p50 均约 0.45。
- 并发 3：active reuse exec 为 52.4/66.3/161.8；cold create 上界为 47.2/78.0/134.0，create→start 为 42.5/60.9/66.7；stop/start 路径的 start 上界为 49.8/77.2/98.3。三条路径的 queue p50 为 0.68–0.75、p95 为 1.40–2.14。
- 并发 6：active reuse exec 为 317.8/352.1/373.9；cold create 上界为 184.2/220.9/248.1；stop/start 路径的 start 上界为 180.2/231.3/241.6。三条路径的 queue p50 为 112.9–122.6、p95 为 149.2–174.0。Controller 只允许 3 个运行槽，HTTP 上界和 exec 竞争因此不能解释成纯 Docker Engine 成本。
- no-op exec 的容器 CPU time p50 在并发 1/3/6 下分别约 16.6/18.6/20.6 ms；peak memory p95 分别约 11.66/10.95/11.16 MiB；peak PIDs p95 分别为 3/9/9。

轻量工作负载下 3 个运行槽稳定、6 个调用会明确排队，因此保留方案要求的默认 `max_running: 3`。但 2 GiB 是限制而非预留；若三个任务同时逼近上限，总计可达 6 GiB，只给本机约 7.75 GiB 的 Desktop VM 留约 1.75 GiB。按下文 2 GiB 保守 reserve 公式，这台机器的生产建议是把 `max_running` 调成 2，或先提高 Docker Desktop VM 内存；默认值 3 的压力测试通过不等于三个内存饱和编译任务也有充足余量。

### 编译负载小样本

为确认工具链并获得量级数据，另在并发 1/3/6 下各跑 5 个申请、每个申请覆盖 Python/Go/Node.js/C++ 和三条复用路径，共 435 个 phase 观测，0 failure。样本很小，只用于量级判断，不用于精确容量调参。

- 并发 1 的首次 active exec p50：Python 108 ms、Node.js 57 ms、C++ 166 ms、Go 4.44 s。Go 首次构建的 p50 CPU time 为 4.42 s、peak memory p95 约 285 MiB、peak PIDs p95 为 20。
- 同一工作区缓存保留后，Go 在 stop/start 与容器重建路径的 exec p50 都约 65 ms，证明将 volume 与容器生命周期解耦能保留编译缓存。
- 并发 6 时首次 Go 构建 p50/p95 为 4.66/9.02 s；Python、Node.js、C++ 的 active exec p50 分别约 366/323/557 ms。这里同时包含 3 槽位排队和 CPU 竞争，进一步说明不应无限制地让多 Agent 各启一个容器。

镜像首次联网构建耗时受 registry 和 apt 网络影响很大，本轮第一次下载/安装约 13 分 48 秒后因传输 EOF 失败；加入 apt retry 后利用已缓存层的成功重试约 72 秒。这是部署阶段网络与镜像构建成本，不应混入运行期的 create/start 数据。

## 本机基准协议

### 前置条件

1. 由操作者启动 Docker daemon；基准程序本身不得启动 Docker Desktop。
2. 记录 `docker version`、`docker info`、Docker Desktop VM CPU/memory/swap、storage driver、文件系统和宿主负载。
3. 提前构建并固定沙箱镜像 digest，确认镜像已在本机；测量阶段不得 pull 或联网。
4. 关闭无关构建任务。每轮开始前确认没有 EasyGo label 的残留容器。
5. 冷态测试与热态测试分开：冷态只测 Docker Desktop/daemon 唤醒；容器数据全部在 daemon 已热且镜像已缓存的条件下测。

### 测试负载

准备五个完全离线的固定负载：

- `noop`：启动空闲 PID 1，ready probe 为一次 `exec true`。
- `python`：上传固定 Python 文件，执行 `python3 -m py_compile`，再运行它并校验固定输出。
- `go`：写入只依赖标准库的固定 Go module，执行 `go build` 和产物，校验固定输出。
- `node`：执行固定 Node.js 脚本并校验输出。
- `cpp`：编译并运行只依赖标准库的 C++ 程序。

另做复用对照：在同一个已启动容器内连续运行 `exec true`、Python 和 Go 负载，不创建新容器。

### 样本矩阵

`cmd/sandbox-bench` 的默认固定矩阵是：先丢弃 10 次完整预热；随后在并发度 1、3、6 下各运行 100 个独立申请。每个申请都执行全部五种负载，并分别走 active reuse、stop/start、以及主动移除容器后使用原 volume 重建三条路径。每个申请最后通过 Controller destroy；任一步失败也使用独立 30 秒 cleanup context 再次 destroy。测试脚本退出时还会按专用 namespace label 兜底检查和清理。

容器丢失测试属于可信 operator 行为：benchmark 直接连接本地 Unix Docker socket，按 managed、namespace、application 和 resource 四个 label 精确定位唯一容器，只移除已由 Controller release 的容器，不删除 volume。程序拒绝远端 Docker host，也不会启动 Docker Desktop。

### 计时点和资源采样

benchmark 主要从 Controller 外部观测 Docker events/stats；容量排队时长由 Controller 随 create 响应显式返回。每项都携带 `observation_method` 或 `missing_reason`，边界定义如下：

- `queue_ms`：Controller 从进入容量队列到取得运行槽的单调时钟差，包含为释放槽位而执行的 LRU stop；不含前置空间检查、等待同申请操作锁、`max_starting` 信号量等待或 Engine create/start。
- cold/recreate `create_ms`：Controller HTTP 请求开始到宿主收到带 application label 的 Docker `create` event；这是包含 admission/queue 和 event 传输的上界，不是纯 Engine ContainerCreate 时延。
- `start_ms`：cold/recreate 使用 Docker `create` 到 `start` 两个 event 的 `timeNano` 差；stop/start 使用请求开始到 `start` event 的上界。
- `ready_ms`：Docker `start` event 到 Controller create HTTP 响应，包含 ready probe、状态持久化和响应开销。若宿主与 Docker Desktop VM 的事件时钟不一致导致负值，该样本输出缺失而不是钳成零。
- `exec_ms`：Agent 实际负载的 Controller HTTP 往返时间；Controller 仅在内部 ExecInspect 确认结束后返回。
- `stop_ms`：release 请求开始到宿主收到 Docker `stop`/`die` event；若 event 不可用，显式标为 Controller release HTTP 往返 fallback。
- `remove_ms`：可信 benchmark 发起 Engine ContainerRemove 到宿主收到 Docker `destroy` event；若 event 不可用，显式标为 ContainerRemove API 往返 fallback。

执行负载期间先后读取一次 stats，并以 25 ms 周期采样 `/containers/{id}/stats`，结束后再读一次。CPU time 是容器 cgroup 累计 CPU 的前后差，包含该窗口内极少量 supervisor/清理开销；peak memory 和 peak PIDs 是采样最大值，可能漏掉短于采样周期的尖峰；block I/O 是累计 read/write bytes 差。任何 stats 失败或样本不足都会写入 `stats_missing_reason`，不会补零或伪造。

报告 schema v4 还在同一 25 ms 周期内只读采样宿主进程表，并为 apply/create/start/ready/exec/stop/remove/destroy 等 phase 输出 `host_cpu_time_ms`、`host_peak_rss_bytes`、`host_peak_pids`、`host_stats_samples`、`host_observation_method` 和 `host_missing_reason`。macOS 匹配 Docker Desktop 的 `com.docker.*` backend，并把约 2 GiB RSS 的 `com.apple.Virtualization.VirtualMachine` 纳入启发式统计；后者无法仅凭进程名证明归属 Docker，因此同时运行其他 Apple Virtualization 工作负载时可能被包含。Linux 尽量匹配 `dockerd`、`containerd`、`containerd-shim*` 和 `docker-proxy`。若 benchmark 位于看不到这些进程的 PID namespace、`ps` 失败、样本不足或累计 CPU 计数不可靠，相应数值保持缺失并解释原因，不写成 0。

phase 的宿主资源窗口使用最靠近边界、从两侧包围该边界的进程样本。小于 25 ms 的连续 create/start/ready phase 可能共用或重叠同一采样窗口，所以这些 host CPU delta 不能相加；并发测试中它们反映整机 Docker backend 同期活动，也不能归因于某一个 application。该 profiler 不执行任何 Docker Desktop/daemon 启停命令，VM 唤醒成本仍须作为独立冷态测试记录。

### 报告和判定

JSON 报告保留每个原始样本以及按 concurrency/path/workload/phase 分组的汇总；汇总包含样本数、时延缺失数、宿主指标缺失数、错误率、mean、p50、p95、p99、最大值，以及 exec 的容器 CPU time、peak memory/PIDs、block I/O 分布和各 phase 的宿主 CPU/RSS/PID 分布。不得把 Docker Desktop VM 冷启动的 3–10 秒计入普通容器 create/start 指标。

初始配置始终保留 3 个运行容器和 2 个并发创建的保护上限。基准只用于向下收紧或在有资源余量时显式上调：

- 选择 p95 相对并发度 1 不超过 1.5 倍、错误率为 0、且不发生 OOM 的最高创建并发度。
- active cap 不得超过 `floor((DockerMemTotal - 2 GiB reserve) / 2 GiB)`，并同时受配置值 3 限制；不足一个时拒绝启用 sandbox。
- 任一压力轮出现 OOM、daemon 无响应、残留容器或 p95 超过基线 2 倍，都降低创建并发度或 active cap 后重测。
- 指数保温默认保持启用。若 active exec 相比 stop/start 没有显著收益，仍保留 1 分钟初始保温以支持同一 run 的多步写代码和编译流程；不建立跨申请的预热池。

## 官方资料

- [Docker Engine API](https://docs.docker.com/reference/api/engine/)
- [Docker container create](https://docs.docker.com/reference/cli/docker/container/create/)
- [Docker container exec](https://docs.docker.com/reference/cli/docker/container/exec/)
- [Docker container prune](https://docs.docker.com/reference/cli/docker/container/prune/)
- [Docker none network driver](https://docs.docker.com/engine/network/drivers/none/)
- [Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/)
- [Docker storage](https://docs.docker.com/engine/storage/)
- [Docker tmpfs mounts](https://docs.docker.com/engine/storage/tmpfs/)
- [Docker Engine security](https://docs.docker.com/engine/security/)
- [Docker object labels](https://docs.docker.com/engine/manage-resources/labels/)
- [Docker system events](https://docs.docker.com/reference/cli/docker/system/events/)
- [Docker container stats](https://docs.docker.com/reference/cli/docker/container/stats/)
- [Docker Desktop settings](https://docs.docker.com/desktop/settings-and-maintenance/settings/)
