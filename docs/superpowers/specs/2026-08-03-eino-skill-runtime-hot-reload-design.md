# Eino Skill Runtime 与下一 Turn 热更新设计

日期：2026-08-03  
状态：已确认，待实施计划  
相关：`2026-07-28-eino-skill-workspace-design.md`、Eino v0.9.13 Skill middleware、现有 `internal/skill/workspace`

## 1. 目标

在现有每用户 Skill workspace 和只读 Eino filesystem Backend 之上，完成一条可实际使用的最小链路：

1. 应用启动时将版本库内的内置 skill 原子同步到运行态目录。
2. 每次对话 Turn 按认证用户构建隔离的 Eino Skill middleware。
3. 模型可自主发现并按需加载内置或当前用户的 skill，并可读取 skill 引用文件。
4. 用户可通过最小 HTTP API 上传、查看和删除私有 skill。
5. 用户在同一聊天 Session 中变更 skill 后，下一 Turn 必须看到新目录状态，无需重启服务。

第一个内置 demo skill 为 `easygo-agent-skill`。用户询问 EasyGo Agent 使用的框架、技术栈或项目架构时，模型可加载该 skill 并读取项目 README 副本后作答。

## 2. 非目标

- Skill 管理前端页面。
- 同一个正在执行的 Turn 内动态切换 skill 视图。
- 覆盖上传或原子替换已有同名用户 skill；更新流程为先删除再上传。
- 每日定时同步内置 skill。
- Skill 市场、分享、跨用户授权或多实例共享存储。
- 强制模型在 system instruction 中调用某个业务 skill。
- Fork / fork-with-context skill、AgentHub 或 ModelHub。

## 3. 核心决策

### 3.1 每 Turn 构建，不建立目录缓存

`RuntimeFactory.Build` 接收 `userID`，每次调用都通过 `workspace.Manager.NewBackend` 获取当前用户 Backend，并重新创建 Eino Skill Backend 与 handlers。

进程内不缓存 skill 列表、frontmatter 或用户 Backend。因此一次成功上传或删除完成后，下一 Turn 的 Runner 构建会重新扫描 workspace，天然获得新目录状态。

### 3.2 只读文件工具显式装配

不设置 `deep.TypedConfig.Backend`。该字段会让 DeepAgent 自动注册包括 `write_file` 和 `edit_file` 在内的完整文件工具集，与模型侧只读约束不符。

运行时显式创建 Eino filesystem handler，只启用：

- `ls`
- `read_file`
- `glob`
- `grep`

明确禁用 `write_file`、`edit_file` 和大工具结果落盘。Skill handler 与只读 filesystem handler 一起传入 `deep.TypedConfig.Handlers`。

### 3.3 原子发布用户目录

上传内容先在 `skills.root_dir` 下、用户 workspace 之外的 staging 目录完成解压和全部校验。校验成功后，以同一文件系统内的 `os.Rename` 原子发布到 `workspaces/{user_id}/{skill_id}`。

删除时先将目标目录原子移出用户 workspace，再清理 staging 中的旧目录。这样新的 Runner 构建只会看到变更前或变更后的完整目录，不会看到半解压或半删除状态。

正在执行的旧 Turn 不承诺观察上传或删除。下一 Turn 生效是本阶段唯一热更新保证。

## 4. 目录布局

```text
skills/
  builtin-src/
    easygo-agent-skill/
      SKILL.md
      references/
        README.md
  builtin/                         # 运行态原子镜像，不纳入版本库
    easygo-agent-skill/
      ...
  staging/                         # 上传、删除和同步的临时目录
  workspaces/
    {user_id}/
      easygo-agent-skill -> ../../builtin/easygo-agent-skill
      {user-skill-id}/
        SKILL.md
        references/
          ...
```

`workspace.Manager` 继续负责创建和校验 `builtin-src`、`builtin`、`staging` 与 `workspaces`，以及维护每用户的内置 skill 相对 symlink。

## 5. 配置

本阶段配置如下：

```yaml
skills:
  root_dir: skills
  readme_src: README.md
  max_zip_bytes: 5242880
  max_extracted_bytes: 20971520
  max_files: 200
```

约束：

- `root_dir`、`readme_src` 必须为非空字符串。
- `max_zip_bytes`、`max_extracted_bytes`、`max_files` 必须大于零。
- 默认值分别为 5 MiB、20 MiB、200 个文件。
- 删除当前未使用的 `sync_cron`；未来真正实现定时同步时再加入。
- YAML 继续使用严格字段校验，已经移除或拼错的字段必须导致启动配置解析失败。

## 6. 组件设计

### 6.1 `internal/skill/sync`

`Syncer.Sync(ctx)` 执行两阶段启动同步：

1. 将 `skills.readme_src` 原子复制到 `builtin-src/easygo-agent-skill/references/README.md`。
2. 将完整 `builtin-src` 复制到 sibling 临时目录，校验只包含普通文件和目录，再原子切换为 `builtin`。

同步失败时保留上一份完整的 `builtin`。首次启动没有可用镜像且同步失败时，应用启动失败。

本阶段只在应用启动时执行，不注册 cron job。

### 6.2 `internal/skill/store`

`Store` 依赖 `config.Skills` 和 `workspace.Manager`，提供：

- `Upload(ctx, userID, skillID, archive) (SkillInfo, error)`
- `List(ctx, userID) ([]SkillInfo, error)`
- `Delete(ctx, userID, skillID) error`

`SkillInfo` 最少包含：

- `skill_id`
- `source`：`builtin` 或 `user`
- `readonly`
- `description`

Frontmatter 解析由独立的小型 `internal/skill/manifest` 组件完成，供启动同步、上传校验和列表复用，避免三处实现不同的解析规则。`name` 和 `description` 必须为非空字符串；用户上传的 `name` 还必须与请求 `skill_id` 完全一致。

上传规则：

- `skill_id` 必须匹配 `^[a-z0-9-]{2,64}$`。
- ZIP 可以在根直接包含 `SKILL.md`，或使用唯一顶层目录；最终目录名始终由请求中的 `skill_id` 决定。
- 必须存在且只能解析出一个目标 `SKILL.md`。
- `SKILL.md` 必须包含合法 YAML frontmatter；其中 `name` 必须与请求 `skill_id` 完全一致。
- 本阶段只允许 inline skill。`context`、`agent` 或 `model` 非空时拒绝上传，避免配置看似生效但运行时缺少 AgentHub 或 ModelHub。
- 禁止绝对路径、`..`、路径逃逸、symlink、设备文件和其他特殊文件。
- 同时限制压缩包字节数、解压后总字节数和普通文件数量。
- 与内置 skill 同名或与已有用户 skill 同名均返回冲突，不覆盖现有内容。

同一进程内的发布和删除临界区串行执行：解压与内容校验可并行，但最终的重名复查、rename 发布和 rename 删除必须在 Store 的 mutation lock 内完成。这样并发上传同一 `skill_id` 时只有一个成功，也不会由 rename 意外覆盖已有目录。单机存储是本阶段前提，因此不增加分布式锁。

删除规则：

- 只能删除当前用户 workspace 中的普通用户 skill 目录。
- 内置 skill 返回禁止操作。
- 不存在的用户 skill 返回资源不存在。
- 不跟随或删除非 manager 管理的 symlink。

### 6.3 `internal/agent`

`RuntimeFactory` 新增 `workspace.Manager` 依赖：

```text
Build(ctx, userID, modelSpec)
  -> registry.Build(ctx, modelSpec)
  -> workspaces.NewBackend(ctx, userID)
  -> skill.NewBackendFromFilesystem(BaseDir: "/")
  -> skill.NewTyped[*schema.AgenticMessage]
  -> filesystem.NewTyped[*schema.AgenticMessage]
  -> deep.NewTyped(Handlers: [filesystem, skill])
  -> adk.NewTypedRunner
```

`RunService.Prepare` 已经持有认证后的 `userID`，直接将其传入 `RuntimeFactory.Build`。Runner 仍在数据库事务外构建，workspace 或 middleware 错误不会留下 running run 记录。

### 6.4 HTTP、依赖注入与启动

新增登录态路由：

| 方法 | 路径 | 行为 |
|---|---|---|
| `POST` | `/api/v1/skills` | multipart 上传：`skill_id` + `file` |
| `GET` | `/api/v1/skills` | 列出当前用户可见 skill |
| `DELETE` | `/api/v1/skills/:skill_id` | 删除当前用户私有 skill |

Controller 只负责认证用户解析、multipart/路径参数处理和业务错误到 HTTP 响应的投影。ZIP 校验、权限和文件操作全部位于 Store。

应用启动顺序：

1. 加载并校验配置。
2. 初始化日志与基础设施。
3. 创建 workspace manager。
4. 执行内置 skill 启动同步。
5. 创建 Skill Store，并清理仅匹配 Store 自有前缀的历史 staging 残留。
6. 创建带 workspace manager 的 RuntimeFactory。
7. 通过 Wire 装配 SkillController，并注册路由。
8. 启动 HTTP 服务。

## 7. 数据流

### 7.1 内置 skill 自动加载

1. 启动同步发布 `builtin/easygo-agent-skill`。
2. 第一次为用户构建 Runner 时，`EnsureWorkspace` 建立内置 symlink。
3. Skill Backend 扫描 `/*/SKILL.md` 并获得 demo frontmatter。
4. Skill middleware 将可用 skill 描述暴露给模型。
5. 模型自主调用 `skill` 后，skill 内容指示模型使用 `read_file` 读取 `/easygo-agent-skill/references/README.md`。

### 7.2 下一 Turn 热更新

```text
Turn N Runner 已构建
  -> 用户上传或删除 skill
  -> Store 原子修改 workspace 目录入口
  -> Turn N+1 调用 RuntimeFactory.Build(userID)
  -> 新 Skill Backend 重新扫描 workspace
  -> Turn N+1 看到变更后的完整 catalog
```

同一 Session 不保存 skill catalog；Session 历史只保存对话消息，不影响下一个 Runner 的目录发现。

## 8. 失败处理

- 启动同步失败：记录结构化错误并终止启动，不静默禁用 skill。
- Workspace 创建失败：Runner 构建失败，请求在数据库事务开始前终止。
- Skill Backend 或 handler 初始化失败：本次请求失败，不降级为无 skill 的普通 Agent。
- 上传校验失败：清理 staging，不向 workspace 发布任何目录。
- 上传发布失败：保留原 workspace 状态并清理 staging。
- 删除原子移出失败：目标仍保留在 workspace，删除请求失败。
- 删除已移出但同步清理失败：下一 Turn 已不再发现该 skill，本次请求返回内部错误并保留带 Store 专属前缀的 staging 残留；下一次应用启动在 HTTP 服务开始前清理该残留。

所有返回错误都必须在当前返回层使用项目 logger 和强类型 zap 字段记录。可记录 `user_id`、`skill_id`、安全的虚拟路径、大小计数和配置项名；不得记录 ZIP 内容、SKILL.md 内容、密码、令牌或凭证。

## 9. HTTP 错误语义

| 场景 | HTTP |
|---|---:|
| 请求字段、skill ID、ZIP 或 frontmatter 无效 | 400 |
| 未登录 | 401 |
| 删除内置 skill | 403 |
| 删除不存在的用户 skill | 404 |
| 同名内置或用户 skill | 409 |
| 压缩包超过上传限制 | 413 |
| 文件系统、同步或内部装配失败 | 500 |

如果现有 `errorcode` 尚无 413 对应业务码，则为 Skill 上传增加稳定业务码，而不是把超限伪装成普通 400。

## 10. 测试与验收

### 10.1 自动化测试

同步层：

- README 刷新到 demo skill。
- `builtin` 完整镜像 `builtin-src`。
- 源文件删除后运行态镜像同步删除。
- README 读取或镜像构建失败时保留旧 `builtin`。

Store 层：

- 合法 ZIP 上传、列表和删除。
- 根目录与唯一顶层目录两种合法 ZIP 布局。
- 非法 skill ID、缺失 SKILL.md、frontmatter 解析失败、name 不匹配。
- 内置撞名、用户同名、删除内置、删除不存在目标。
- zip-slip、绝对路径、symlink、特殊文件。
- 压缩包大小、解压总量和文件数量边界。
- 不同用户之间上传、列表、读取和删除隔离。
- 任一失败路径不留下可被 Skill Backend 发现的半成品。

Runtime 层：

- Skill Backend 能发现 `easygo-agent-skill` 并解析 frontmatter。
- Runtime handlers 包含 `skill`、`ls`、`read_file`、`glob`、`grep`。
- Runtime handlers 不包含 `write_file`、`edit_file`。
- Workspace、Skill Backend 或 handler 初始化错误会导致 Runner 构建失败。
- 连续模拟三个 Turn：初始 catalog、上传后重新 Build 的 catalog、删除后再次 Build 的 catalog，验证下一 Turn 热更新。

配置和 HTTP 层：

- Skills 默认值与正数校验。
- `sync_cron` 和其他未知 YAML 字段被严格拒绝。
- 三个 Skill 路由均要求登录且只使用认证上下文中的用户 ID。
- 业务错误映射到预期 HTTP 状态和稳定错误码。

验证命令：

```text
go test ./...
go vet ./...
go test -race ./internal/skill/... ./internal/agent
```

### 10.2 手工验收

1. 启动服务后询问“你用的什么框架？”，确认模型自主调用 `skill`、读取 demo README，并基于文档回答。
2. 在同一聊天 Session 中上传测试 skill；下一条消息确认新 skill 已出现在可用 catalog，并可被模型使用。
3. 删除该测试 skill；再发送一条消息，确认新 Runner 的 catalog 不再包含它。

模型是否选择某个工具具有概率性。自动化测试负责确定性验证 catalog、handler 和文件读取链路；真实模型测试负责验证最终用户体验。不得通过 system instruction 点名强制调用 demo skill 来让验收通过。

## 11. Go 实现约束

- 沿用 GORM、项目 logger、Gin、Wire 和 Eino v0.9.13，不引入替代框架。
- 本功能不新增 raw SQL，不在循环中直接或间接执行 SQL。
- 每个返回错误在当前层记录结构化日志。
- 每个新增或修改的命名函数、方法、测试函数及函数表达式都有紧邻的用途注释。
- 文件操作循环必须检查 `ctx.Err()`，避免取消后继续大规模解压、复制或遍历。
- 所有代码改动留在工作树中，由用户审查和提交。

## 12. 验收结论

实现完成的判定标准是：内置 demo skill 可在真实对话中被自主发现和读取；用户私有 skill 可安全上传、列出和删除；同一 Session 的下一 Turn 总能观察到已完成的目录变更；运行时不向模型暴露任何文件写入工具；全部自动化和静态检查通过。
