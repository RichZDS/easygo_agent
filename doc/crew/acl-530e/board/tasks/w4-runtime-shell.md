# W-4 · Docker 模式下给 Claude/Pi/OpenClaw 开放 shell 工具

assignee: workshop · 状态: done（be7dac0；工头已签 51dadc9）

## 现场 / 锚点
- workshop 席在 `/tmp/crew/acl-530e/workshop/runtime-crew-tool-question.md` 发现：`runner.go` 的 `engineArgs` 给 Claude 的 `--tools` 是 `Read,Glob,Grep`（写策略再加 `Edit,Write`），给 Pi 的是 `read,grep,find,ls`（写策略再加 `edit,write`）；`runtime_config.go` 里 OpenClaw 的 `tools.allow` 只有 `read`（写策略再加 `write,edit`）。三个 CLI 都没有 shell 工具，也就调不了 `easygo-crew`，没法真正跑测试。
- 当初排除 shell 的理由见 `doc/runtime-provider-compatibility.md`：host 模式下"工具白名单不是 OS 隔离"。
- 已有先例：Docker 路径把"隔离"交给外层容器。`docker.go` 会把 Codex 的 `sandbox_mode` 替换成 `danger-full-access`，注释写着 "Only this initialized Docker path delegates isolation to the mandatory outer container"。
- 设计稿 §5.3 的前提是"四种 CLI 都能执行 shell 命令"。这一单就是让这个前提在 Docker 模式下成立。

## 修法
1. **只在 Docker 路径**给三个 CLI 加上它们各自的 shell 工具，做法同 Codex 的 sandbox 替换：由 DockerRunner 改写参数或配置，host 的 `CommandRunner` 行为完全不变。
   - Claude：`Bash`。
   - Pi：`bash`。
   - OpenClaw：`exec`，或者该版本实际的 shell 工具名。
2. **工具名和相关参数必须对着镜像里固定的版本实测**，不要照文档猜：
   - 版本是 `easygo-task-runtime:platform` 里的 claude 2.1.281、pi 0.87.1、openclaw 2026.9.6。
   - 用专用 daemon 执行 `docker run --rm --network none --entrypoint <cli> easygo-task-runtime:platform --help`（或对应的配置校验子命令）取证。不调用模型，不需要网络。
   - 要特别确认 Claude 的 `--restricted` / `--bare` 会不会禁用 Bash。如果会，Docker 模式下去掉 `--restricted`，并在注释里写明理由：外层容器才是边界。
3. **read-only 策略的工作流**（比如以后的终审）也开放 shell。工作区只读由 OS 挂载保证，shell 不会破坏这一点。
4. **文档**：更新 `doc/runtime-provider-compatibility.md` 和 `doc/workshop.md`，说明两种模式的区别——Docker 模式由容器负责隔离、开放 shell；host 模式不开放 shell，只有 Codex 能用 easygo-crew。

## 明令不做
- 不改 host 模式的工具集。
- 不开 MCP，也不开网络类工具。
- 不重建完整 runtime 镜像，只用已有的 `easygo-task-runtime:platform` 读 help。
- 不调用付费模型。真实模型验证由工头来做。

## 测试
- 单测覆盖 Docker 与 host 两种模式、两种策略、三个 CLI 的参数和配置，外加 resume 参数。
- 证据：三个 CLI 的 help 或配置校验输出里，确实存在你使用的工具名和参数。
- `cd services/workshop && go test -race ./... && go vet ./...`。

## 交付
- 单独一笔提交。
- 报审里写清三个 CLI 各自实测到的工具名和证据路径，并列出你认为只有真实模型运行才能证明的点，由工头去跑。
