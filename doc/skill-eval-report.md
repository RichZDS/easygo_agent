# Skill 目录加载评测

- 时间：2026-09-12T00:46:57Z
- 通过：3 / 3

Agent 启动时只注册启发式目录。专门 skill 必须通过 `load_skill` 进入上下文。

| 用例 | 结果 | 工具 | 耗时 |
| --- | --- | --- | --- |
| 算术走目录再加载 | 通过 | load_skill, calculator | 2.317s |
| 口令走目录再加载 | 通过 | load_skill | 1.546s |
| 无关问题不加载 | 通过 |  | 930ms |

## 算术走目录再加载

- 结果：通过
- load_skill：true

```text
CALC:578 — 17 乘以 34 的结果是 578。
```

## 口令走目录再加载

- 结果：通过
- load_skill：true

```text
[[ALPHA-ROSE-9]]
```

## 无关问题不加载

- 结果：通过
- load_skill：false

```text
杭州西湖位于浙江省杭州市区，以“一山二塔三岛三堤五湖”的湖光山色和众多名胜古迹闻名于世，是中国著名的风景旅游胜地，并被列入世界文化遗产名录。
```

## 注册的目录启发式

```markdown
---
name: skill-catalog
description: Use when deciding whether a specialized skill applies before answering, using tools, or changing procedure.
---

# Skill Catalog

This is the only skill registered at startup. Specialized procedures stay on disk.

## Required sequence

1. Read Directory below.
2. If a row's "Use when" matches the current user request, call `load_skill` with that exact Name before any other tool and before answering.
3. Follow the loaded skill. If no row matches, answer normally and do not call `load_skill`.
4. Load at most one skill per user request unless a loaded skill explicitly requires another.

## When writing a new skill

Add `SKILL/skill/<name>/SKILL.md` with YAML `name` and a "Use when..." `description`. The Directory is rebuilt from those descriptions at process start. Put the triggering condition in the description; put the procedure in the skill body.

## Directory

| Name | Use when |
| --- | --- |
| `bracket-token-reply` | Use when the user asks to repeat, echo, quote, or return a passphrase, token, or口令. |
| `strict-arithmetic` | Use when the user asks for a numeric calculation, arithmetic, or an exact product, sum, difference, or quotient. |
| `tool-error-first` | Use when a tool call has just failed, returned an error, or the user reports a tool failure. |

```
