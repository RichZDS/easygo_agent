# 迷宫 Skill / Tool 评测报告

- 时间：2026-09-12T01:08:14Z
- 模型：`deepseek-flash`
- 用例：4 / 4 通过

## 做了什么

新增文字迷宫：每个字是一个实体。`墙` 不可走，`路` 可走，`起` 起点，`终` 终点，跑的时候用 `人` 标当前位置。

存储在 `data/maze/`，一迷宫一个 JSON，例如 `data/maze/sample-xiaojing.json`：

```json
{
  "id": "sample-xiaojing",
  "name": "小径",
  "describe": "从起到终只需下、右两步",
  "grid": [
    ["墙", "墙", "墙", "墙"],
    ["墙", "起", "路", "墙"],
    ["墙", "路", "终", "墙"],
    ["墙", "墙", "墙", "墙"]
  ]
}
```

指令：`1` 上、`2` 下、`3` 左、`4` 右。小径的通关路线是 `2 4`（下、右）。

四个迷宫工具：

| 工具 | 作用 |
| --- | --- |
| `listmaze` | 列出 id / name / describe |
| `detailmaze` | 迷宫详情和二维数组 |
| `makemaze` | 用二维数组新建迷宫 |
| `runmaze` | 输入指令序列，看 `人` 能不能到 `终` |

Skill：`skills/playing-maze/SKILL.md`。目录启发式会列出它的 Use when，正文不进启动上下文。

生产路径（`internal/app/bootstrap.go`）采用**启发式**：原生只挂 `calculator`、`load_skill`、`call_tool`。迷宫四个工具藏在 `call_tool` 后面。

## 对照实验

| 模式 | 启动时模型能看见的 tool | 系统提示词 |
| --- | --- | --- |
| 启发式 | `calculator`, `load_skill`, `call_tool` | 只有 skill 目录，没有迷宫 schema |
| 启动绑定 | `calculator`, `listmaze`, `detailmaze`, `runmaze`, `makemaze` | 普通助手提示，没有迷宫 skill |

同一份用户问题跑两边。

## 结果

| 模式 | 用例 | 结果 | 实际调用 | 耗时 |
| --- | --- | --- | --- | --- |
| 启发式 | 列出迷宫 | 通过 | `load_skill` → `call_tool` | 3.654s |
| 启发式 | 用 2 4 跑小径 | 通过 | `load_skill` → `call_tool` | 3.835s |
| 启动绑定 | 列出迷宫 | 通过 | `listmaze` | 2.382s |
| 启动绑定 | 用 2 4 跑小径 | 通过 | `runmaze` | 2.594s |

### 启发式 · 列出迷宫

原生列表里没有 `listmaze`。模型先 `load_skill(playing-maze)`，再 `call_tool`，答出了 `sample-xiaojing` / 小径。

### 启发式 · 跑小径

同样先加载 skill。结论：能到终点，轨迹 起 → 路 → 终，`reached = true`。

### 启动绑定 · 列出迷宫

没有 skill、没有 `load_skill`。模型直接调 `listmaze`。说明 **只要把工具绑在启动列表里，模型从 schema 就知道这个工具存在**，不必启发式。

### 启动绑定 · 跑小径

直接 `runmaze`，走到终，网格里 `人` 停在终点。

## 结论

你的判断成立：

1. **启动就绑定**：agent 会知道这些 tool。这次绑定组从未调用 `load_skill`，却准确用了 `listmaze` / `runmaze`。工具一多，每一份 JSON schema 都会进每一轮上下文。
2. **启发式**：agent 启动时只知道目录和 `call_tool`。迷宫四个工具的参数细节在 `playing-maze` 加载之后才进入本轮。代价是多一次 `load_skill`，大约多 1 秒。
3. **后续很多 tool 时**，应继续用现在的生产路径：目录 skill + `load_skill` + `call_tool`，不要把业务工具一次性挂到 Deep Agent 上。

复跑：

```powershell
go run ./cmd/eval/maze -config configs/eval/memory.yaml -out doc/eval/maze.md
```
