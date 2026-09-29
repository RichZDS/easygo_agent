# 审查 · W-4 · be7dac06a3ab24551e1d749632f1951c3de917a3

结论：**通过**。

队列 #8。提交 `be7dac0`，父提交是 `3adec82`。审查检出是 `/home/ubuntu/Projects/easygo-acl-review2` 的 detached HEAD `be7dac0`。没有改施工者分支，也没有提交。红测改过的 `docker_runtime.go` 已复原。`git diff` 为空。

P0–P3 无。三个 CLI 的工具名和关键参数对着镜像 `easygo-task-runtime:platform`（`sha256:8cc696a4b2b8b5ab1097a556a63a8f032c03657c47acfad66a1d49ebb96e9ff8`，没有重建）离线核对过。真实模型有没有真的把 shell 用起来，留给工头。

## 问题

### P0 · 无

### P1 · 无

### P2 · 无

### P3 · 无

## 已验证

1. **只在已初始化的 DockerRunner 上改写。** `dockerRuntimeArgs` 只从 `DockerRunner.Run` 调用（`docker.go:425`），而且在 `initialized` 检查之后（`docker.go:411-412`）。`dockerRuntimeConfig` 只包在 Docker 路径的配置写入回调里（`docker.go:488-492`）。host 的 `CommandRunner.configureRuntime` 仍把 `writeRuntimeJSON` 直接交给 `configureRuntimeEndpoint`（`runtime_config.go:69`），不会追加 `exec`。本提交没有改 `runner.go`。host 工具串仍是 Claude `Read,Glob,Grep`（写策略再加 `Edit,Write`）、Pi `read,grep,find,ls`（写策略再加 `edit,write`）、OpenClaw `read`（写策略再加 `write,edit`）（`runner.go:88-106`，`runtime_config.go:122-129`）。
2. **三个名字和镜像一致。** 专用 daemon、`--network none`、不调用模型。
   - Claude Code 2.1.281。`--tools` 的例子里有 `Bash`。`--restricted` 会拿掉 Bash、PowerShell、REPL 和其他会跑命令的工具，以及 WebFetch，**除非 `--tools` 点了名**。`--bare` 写明内建功能不受影响。因此保留 `--restricted` / `--bare`，并在 `--tools` 末尾加 `Bash`、再加 `--allowedTools Bash`，和 help 一致（`docker_runtime.go:15-36`）。没有点名 WebFetch，也没有去掉 `--strict-mcp-config`。
   - Pi 0.87.1。内建工具表有 `bash`（Execute bash commands）。`--tools` 是逗号分隔的允许名单。Docker 路径只把 `bash` 追加进这份名单（`docker_runtime.go:17-31`）。`--no-extensions` 仍在。help 里 `--no-approve` 的意思是忽略项目本地文件，不是批准命令。
   - OpenClaw 2026.9.6（`eb377ac`）。`agent --help` 写明 `--local` 是在本地跑嵌入式 agent。schema 里 `tools.exec.host` 的枚举是 `auto|sandbox|gateway|node`，`mode` 的枚举含 `full`（trusted local operation）。代码写入 `allow` 追加 `exec`，`tools.exec={"host":"gateway","mode":"full"}`（`docker_runtime.go:56-59`）。用与生产相同形状的配置做 `config validate --json`：`valid:true`。`exec-policy show --json`：请求的 host 是 gateway，有效 mode/security 都是 `full`，ask 是 `off`，审批库不存在时也如此。`plugins.enabled` 仍是 false。
3. **只读策略也有 shell，工作区挂载仍只读。** 单测对三种引擎、两种策略、是否 resume 共 12 支都看了 Docker 参数：只读策略的工作区挂载以 `,readonly` 结尾，写策略不是；`--network none`、`--user 1000:1000` 还在（`docker_runtime_test.go:67-77`）。挂载生成式没有改：工作区是 `dst=/workspace,bind-propagation=rprivate`，只读策略再在末尾加 `,readonly`（`docker.go:260`、`273-274`），`.workshop-home` 另挂一份可写（`docker.go:277`）。我用同一形状在 runtime 镜像里跑了 `/bin/sh`：`touch /workspace/nope` 得到 Read-only file system（退出 1），`touch` home 和 `/tmp` 成功。这是挂载本身，不是三个 CLI 的 shell 工具。
4. **resume 和未初始化。** Claude 的 `--resume`、Pi/OpenClaw 的 `--session-id` 在单测里仍等于原 session id。`initialized=false` 时第二次 `Run` 不再调用容器（`docker_runtime_test.go:118-122`）。Codex 不走 shell 追加，`dockerRuntimeArgs` 对其他引擎原样返回；原有的 `sandbox_mode=danger-full-access` 替换还在 `docker.go:512-522`。
5. **测试会判红。** 把三个名字改成 `NotBash`、`notbash`、`not-exec` 后，12 支全部失败：Claude 在 `docker_runtime_test.go:82`，Pi 在 `:89`，OpenClaw 在 `:107`。日志 `red-shell.log`，退出码 1。随后 `git checkout -- services/workshop/workshop/docker_runtime.go`。工作区干净，HEAD 仍是 `be7dac0`。
6. **全量。** 单测 `TestDockerAndHostShellTools` 先单独通过（`unit.log`，退出 0）。然后 `cd services/workshop && go test -race -count=1 -timeout 300s ./...` 退出 0（workshop 包 57.591s）。`go vet ./...` 退出 0。没有设置 `EASYGO_DOCKER_TEST_*`，验收容器测试在全量里 Skip。跑之前没有别的 `go test`。

## 没覆盖到

这些只有真实模型跑一轮才能证明，按施工图留给工头：

- Claude 在 `--restricted --bare --permission-prompts none` 下，`--allowedTools Bash` 是否真的不弹许可。只读策略的 permission mode 是 `dontAsk`，写策略是 `acceptEdits`。help 只说明「会弹的权限在 prompts=none 时直接拒绝」，没有写明裸的 `Bash` 是否预先允许全部命令。`--allowedTools` 的例子是 `Bash(git *)`。
- Pi 的 `bash` 在 `--print` 里会不会等人批准。`--no-approve` 不是这个开关。
- OpenClaw 的 `host=gateway` 在 `agent --local`、网络 none、容器里没有独立 gateway 进程时，命令是否落在本容器里，以及 `exec` 能不能调用 `easygo-crew`。schema 和 `exec-policy show` 只证明配置有效、ask 为 off。
- 三个 CLI 自己去写只读工作区，我只证明了同等挂载下 `/bin/sh` 写不进去。
- 没有重跑验收检查的真实容器，没有调用模型，没有重建镜像。

## 证据

| 项 | 路径 |
|---|---|
| 单测 12 支 | `/tmp/crew/acl-530e/review2/be7dac0/unit.log`（EXIT:0） |
| 改名判红 | `red-shell.log`（EXIT:1），`red-mutation.txt` |
| 全量 -race / vet | `race.log`（TEST_EXIT:0），`vet.log`（VET_EXIT:0） |
| Claude 版本与 help | `claude-version.log`，`claude-help.log` |
| Pi 版本与 help | `pi-version.log`，`pi-help.log` |
| OpenClaw 版本、help、agent/config/exec-policy help | `openclaw-version.log`，`openclaw-help.log`，`oc-agent-help.log`，`oc-config-help.log`，`oc-exec-policy-help.log`，`oc-exec-show-help.log`，`oc-validate-help.log` |
| schema | `oc-schema.json`（完整 JSON，2418889 字节）。客户端在容器退出清理时被 90 秒 timeout 杀掉，meta 里是 EXIT:124；文档本身能解析 |
| 配置校验与有效策略 | `oc-sample.json`，`oc-validate.log`（VALIDATE_EXIT:0，POLICY_EXIT:0） |
| 只读挂载探针 | `readonly-touch.log`（ws:1，home:0，tmp:0，DOCKER_EXIT:0） |
