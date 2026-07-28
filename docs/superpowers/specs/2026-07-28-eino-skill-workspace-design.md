# Eino Skill Workspace 设计

日期：2026-07-28  
状态：待实现  
相关：Eino [第九章 Skill Middleware](https://www.cloudwego.io/zh/docs/eino/quick_start/chapter_09_skill_console/)；现有 `deep.NewTyped` 运行时（`internal/agent/runtime.go`）

## 1. 目标

在 EasyGo DeepAgent 上接入 Eino Skill middleware，使模型在对话中**自主判断**是否加载某个 skill，并按需读取 skill 文档。

同时提供：

- **系统内置 skill**（全员只读共享）
- **用户私有 skill**（按用户隔离，不可互访）
- 第一个 demo skill：`easygo-agent-skill`——用户询问「用什么框架」时，加载该 skill 并阅读其 `references/README.md` 作答

## 2. 非目标（本阶段不做）

- Skill 管理前端页面
- 对象存储 / 多机共享磁盘
- 用户修改或覆盖内置 skill
- Skill 市场、分享、授权给其他用户
- 在系统 Instruction 中点名强制调用某个 skill
- 远程取消、与 Skill 无关的对话架构改造

## 3. 概念区分

| 概念 | 含义 |
|------|------|
| Tool | 动作能力（读文件、调外部系统） |
| Skill | 可复用知识/指令包：`SKILL.md` + 可选 `references/*.md` |
| Skill middleware | 发现 skill、把简介注入工具/提示侧，并提供按名加载 skill 的工具 |
| Workspace | 每用户一个目录；Agent 文件系统 Backend 根目录 = 该 workspace |

知识粒度（本项目约定）：

```text
{skill-id}/
  SKILL.md           # 入口：何时用、下一步读什么
  references/        # 按需展开的子文档
    README.md
```

## 4. 目录布局

根路径由配置项指定（例如 `skills.root_dir`，默认相对进程工作目录的 `skills/`）：

```text
skills/
  builtin-src/                          # 内置源（可纳入版本库）
    easygo-agent-skill/
      SKILL.md
      references/
        README.md                       # 由定时任务从仓库 README 刷新
  builtin/                              # 运行态内置（定时从 builtin-src 同步）
    easygo-agent-skill/
      ...
  workspaces/
    {user_id}/                          # 用户私有根；DeepAgent Backend BaseDir
      easygo-agent-skill -> ../../builtin/easygo-agent-skill   # 只读 symlink
      {user-uploaded-skill}/            # 普通目录（可写）
```

约定：

- Agent **只能**访问自己的 `workspaces/{user_id}/`（Backend 根即此目录）
- 内置 skill 在 workspace 内以 **symlink** 指向全局 `builtin/`，始终跟随最新内置内容
- 用户不可写 symlink 目标（全局 builtin）；不可删除内置 symlink 对应的「系统 skill」
- 路径一律相对 workspace（例如 `easygo-agent-skill/references/README.md`）

## 5. 权限与命名

### 5.1 两类 skill

| 类型 | 可见性 | 写 |
|------|--------|-----|
| 内置 | 所有登录用户只读 | 仅运维/定时任务更新 `builtin-src` → `builtin` |
| 用户自建 | 仅所属用户 | 该用户可通过 API 上传/删除 |

### 5.2 skill_id

- 正则：`^[a-z0-9-]{2,64}$`
- 上传时以请求字段 `skill_id` 为准，**不**信任 zip 内顶层目录名
- 与任一内置 skill_id 冲突 → **拒绝上传（HTTP 409）**
- 内置 demo 名称：`easygo-agent-skill`（不用下划线）

### 5.3 发现策略

只依赖 Skill middleware 默认行为（将可用 skill 名/简介注入工具描述或系统侧提示）。**不**在 Instruction 中枚举或点名业务 skill；仅允许极薄的通用原则（若框架已自带则不再追加）。

## 6. 生命周期

### 6.1 用户注册

`POST /api/v1/users` 创建用户成功后，同步执行 `EnsureWorkspace(user_id)`：

1. 创建 `workspaces/{user_id}/`（若不存在）
2. 为 `builtin/` 下每个 skill 目录建立相对 symlink（已存在且指向正确则跳过；损坏则修复）
3. 失败则应使注册失败或明确回滚策略：推荐 **同一请求内失败则返回错误**（避免无 workspace 的半注册用户）；若用户行已写入，需删除用户或标记并提供补偿——实现时优先「workspace 创建失败则回滚用户创建」或「用户创建与 workspace 同事务边界外的补偿删除」。**选定：用户 DB 插入成功后创建 workspace；若 workspace 失败，删除刚创建的用户并返回错误**（与当前无分布式事务现状一致）。

### 6.2 对话 Build Runner

每次构建 DeepAgent / Runner 时：

1. 从认证上下文取 `user_id`
2. `EnsureWorkspace(user_id)`（幂等；防止历史用户无目录）
3. Local filesystem Backend：`BaseDir = workspaces/{user_id}`
4. `skill.NewBackendFromFilesystem` 的 BaseDir 同为该 workspace
5. `skill.NewTyped` middleware 放入 DeepAgent `Handlers`
6. 启用 workspace 内读文件能力（DeepAgent Backend / 等价读文件工具），以便执行 SKILL.md 中的「去读 references/…」

### 6.3 每日同步内置 skill

注册到现有 `cronjob.RegisterBuiltinCronJobs`，建议 cron：`0 3 * * *`（可配置）。

两步：

1. 将 `easygo_agent/README.md`（仓库内路径，相对服务模块根）复制覆盖到  
   `skills/builtin-src/easygo-agent-skill/references/README.md`
2. 将整个 `skills/builtin-src/` 同步到 `skills/builtin/`（目录级镜像；删除 src 中已不存在的 skill 是否同步删除：**是**，使 builtin 与 src 一致）

因用户 workspace 通过 symlink 指向 `builtin/`，同步后全员自动看到更新，无需遍历用户目录。

启动时是否先跑一次同步：**是**（保证首次部署 `builtin/` 存在）；与每日任务共用同一同步函数。

## 7. HTTP API

均需登录（除已有的创建用户接口外）；只能操作当前用户 workspace。

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/skills` | `multipart`：字段 `skill_id` + 文件 `file`（zip） |
| GET | `/api/v1/skills` | 列出可见 skill：内置（`source=builtin`, `readonly=true`）+ 用户自建 |
| DELETE | `/api/v1/skills/{skill_id}` | 仅删除用户自建目录；删内置名 → 403 |

### 7.1 Zip 上传规则

- zip 解压后必须能得到 `SKILL.md`（允许 zip 根直接是 `SKILL.md`，或唯一顶层目录内含 `SKILL.md`；若多种结构歧义则拒绝）
- 最终写入：`workspaces/{user_id}/{skill_id}/`，内容以校验后的文件树为准，目录名强制为请求中的 `skill_id`
- 防护：zip-slip（禁止 `..` 与绝对路径）；默认上限：zip 上传 ≤ 5 MiB，解压后总大小 ≤ 20 MiB，文件数 ≤ 200（均可配置）
- 已存在同名用户 skill：本阶段 **拒绝（409）**（不做覆盖）；若需更新，先 DELETE 再上传

### 7.2 列表响应字段（最小）

- `skill_id` string
- `source`：`builtin` | `user`
- `readonly` bool
- `description`：若可从 `SKILL.md` frontmatter/首段解析则返回，否则空字符串

## 8. 第一个 demo skill

路径：`skills/builtin-src/easygo-agent-skill/`

**SKILL.md** 职责（封面）：

- name / description 明确：用户询问本服务使用什么框架、技术栈、项目说明时使用
- 指示读取：`easygo-agent-skill/references/README.md`（相对 workspace）

**references/README.md**：

- 内容来源于 `easygo_agent/README.md`（由同步任务维护，不在运行期软链到仓库外）

验收对话示例：用户问「你用的什么框架？」→ 日志/SSE 中出现 skill 加载与读文件 → 回答含 README 中的 Eino/技术栈要点。

## 9. 组件划分（实现导向）

| 组件 | 职责 |
|------|------|
| `internal/skill/workspace` | EnsureWorkspace、symlink 修复、路径校验 |
| `internal/skill/store` | 上传解压、列表、删除、与内置名冲突检测 |
| `internal/skill/sync` | builtin-src ↔ README 拷贝 + 目录同步到 builtin |
| `internal/skill/middleware` 或 agent 装配处 | 按 user_id 组装 Backend + Skill middleware |
| `internal/controller/skill.go` | HTTP |
| `internal/cronjob` 注册 | 每日同步任务 |
| `internal/service/user` | CreateUser 成功后 EnsureWorkspace，失败则回滚用户 |

配置：`skills.root_dir`、同步 cron spec、`skills.readme_src`（默认模块内 `README.md`）、zip 大小/文件数上限（默认见 §7.1）。

## 10. 测试与验收

### 10.1 自动化

- EnsureWorkspace：目录与 builtin symlink 正确；幂等
- 上传：合法 zip 成功；缺 SKILL.md 失败；skill_id 非法失败；撞内置名 409；zip-slip 被拒
- 隔离：用户甲写入的文件，用户乙 EnsureWorkspace/List/读路径不可见
- 同步：改源 README 后跑 sync，builtin 与 builtin-src 中附件一致

### 10.2 手工

- 真实模型对话一次「用什么框架」，确认自主加载 skill（不强制 tool 提示词）
- 模型是否调用存在概率性；以日志中出现 skill / 读 README 为主证据

## 11. 风险与约定

- **Symlink 与 Backend**：实现时确认 local filesystem Backend 跟随 symlink 且解析后仍落在允许树内；禁止通过 symlink 逃逸到其他用户 workspace。全局 builtin 允许作为只读共享目标。
- **模型不调用 skill**：属提示/模型行为，不靠 Instruction 点名；若 demo 不稳，可另开任务优化 description，不在本设计改为强制调用。
- **单机目录**：纯本地目录假设；多实例需共享盘或后续改存储，不在本阶段。

## 12. 决策记录（grilling 结论摘要）

1. 知识粒度：`SKILL.md` + `references/`
2. 首目标：打通 Skill middleware + 自主选用；demo 内容为框架/README
3. 发现：仅 middleware 默认注入
4. README：skill 内引用路径 + workspace 内读文件；不直接读仓库
5. 一用户一 workspace；注册时创建；内置以 symlink 挂入
6. 用户 skill：本地目录 + zip 上传 API + 隔离验收
7. 撞名：拒绝；skill_id 请求字段 + `^[a-z0-9-]{2,64}$`
8. 每日两步同步整个 builtin-src → builtin，并先刷新 demo 的 README 副本
9. Backend 根 = 用户 workspace
`)
