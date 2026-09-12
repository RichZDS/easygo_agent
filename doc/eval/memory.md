# 记忆系统评测结果

- 模型：`deepseek-flash`
- 时间：2026-09-12T02:11:27Z
- 通过：3 / 3

| 档位 | 用例 | 结果 | 压缩 | 耗时 |
| --- | --- | --- | --- | --- |
| 短期记忆（同会话上下文） | 近期事实仍在会话窗口内 | 通过 | 否 | 920ms |
| 单轮长期记忆（新会话召回） | 一轮稳定偏好写入五槽档案后，空上下文仍能遵守 | 通过 | 否 | 3.327s |
| 超长轮长期记忆（压缩后归档再新开会话） | 早期约束在长对话压缩后仍能进入档案并被新会话使用 | 通过 | 否 | 2.027s |

## 短期记忆（同会话上下文）

近期事实仍在会话窗口内。

- 结果：通过
- 命中：夜莺、3月18、王敏
- 探针前召回：空

探针回答：

```text
1. 项目代号：夜莺 2. 截止日期：3月18日 3. 对接人：王敏
```

## 单轮长期记忆（新会话召回）

一轮稳定偏好写入五槽档案后，空上下文仍能遵守。

- 结果：通过
- 命中：白羽豆荚、素食、简体
- 探针前召回：
  - constraint User has a severe allergy to 「白羽豆荚」; never include it in recommendations, menus, or recipes, and alert if an ingredient may be confused with it.
  - constraint External-facing documents must be written in Simplified Chinese.
  - preference User is vegetarian; all dietary recommendations should be vegetarian.
- 长期档案：
  - constraint User has a severe allergy to 「白羽豆荚」; never include it in recommendations, menus, or recipes, and alert if an ingredient may be confused with it.
  - constraint External-facing documents must be written in Simplified Chinese.
  - preference User is vegetarian; all dietary recommendations should be vegetarian.

探针回答：

```text
根据我的长期记忆，回答如下：  **1) 过敏食材** 白羽豆荚。 ⚠️ 提醒：该食材在名称/外形上容易与其他「豆荚类」食材（如甜豌豆荚、四季豆、荷兰豆等荚果类蔬菜）混淆，选购或外食时请留意配料表，避免误食。  **2) 三种符合你饮食习惯（素食）的加餐** - 全麦饼干配鹰嘴豆泥 - 混合坚果（杏仁、核桃、腰果）搭配葡萄干等果干 - 植物基原味酸奶配新鲜水果（如蓝莓、香蕉）  以上均为素食，且不含白羽豆荚及其易混淆的豆荚类食材。  **3) 对外文档语言** 必须使用**简体中文**。  如需，我可以把这三项加餐整理成一份可直接对外使用的简体中文加餐清单。
```

## 超长轮长期记忆（压缩后归档再新开会话）

早期约束在长对话压缩后仍能进入档案并被新会话使用。

- 结果：通过
- 命中：玄枢台账、EG-7741、周五
- 探针前召回：
  - constraint 周五 00:00 至周六 08:00 生产窗口锁死：禁止在周五把变更推到生产；热修也须先把变更单状态改为「冻结」，等周一早会解冻；有人在周五提交生产流水线要直接拒绝并引用冻结令，不商量例外。
  - constraint 所有变更单「申请人」栏必须填工号 EG-7741，不得填写中文姓名。
  - constraint 工号承载列为 ledger_entry.staff_id（申请人）与 audit_event.actor_staff_id（操作人），写入时须校验格式，避免再用昵称做审计。
  - constraint observer_json 必须预置 error_rate、p99_ms、login_ok、ledger_write_ms 四个键，缺一不可；校验要落在网关/写入路径上，而不是只写在文档里。
  - constraint 内部唯一变更台账是「玄枢台账」，不使用 Jira，也不使用 Excel。
- 长期档案：
  - constraint 周五 00:00 至周六 08:00 生产窗口锁死：禁止在周五把变更推到生产；热修也须先把变更单状态改为「冻结」，等周一早会解冻；有人在周五提交生产流水线要直接拒绝并引用冻结令，不商量例外。
  - constraint 所有变更单「申请人」栏必须填工号 EG-7741，不得填写中文姓名。
  - constraint observer_json 必须预置 error_rate、p99_ms、login_ok、ledger_write_ms 四个键，缺一不可；校验要落在网关/写入路径上，而不是只写在文档里。
  - constraint 内部唯一变更台账是「玄枢台账」，不使用 Jira，也不使用 Excel。
  - constraint 工号承载列为 ledger_entry.staff_id（申请人）与 audit_event.actor_staff_id（操作人），写入时须校验格式，避免再用昵称做审计。
- 备注：同会话探针：通过
- 备注：本轮未观察到 compressing 事件；可能已靠启发式 skill/tool 收缩装进预算

探针回答：

```text
根据长期记忆：  1. **你的工号**：**EG-7741**（所有变更单的「申请人」栏必须填这个工号，不得填写中文姓名；审计写入时工号需落在 `ledger_entry.staff_id`（申请人）与 `audit_event.actor_staff_id`（操作人）字段并校验格式）。  2. **内部工具名称**：**玄枢台账**（这是内部唯一的变更台账，不使用 Jira，也不使用 Excel）。  3. **周五能否把变更推到生产**：**不能**。周五 00:00 至周六 08:00 是生产窗口锁死时段，禁止在周五把变更推到生产；即使是热修，也必须先把变更单状态改为「冻结」，等周一早会解冻后才能进行。若有人在周五提交生产流水线，应直接拒绝并引用冻结令，不接受任何例外。  如果你确实有紧急变更，建议现在就把变更单状态置为「冻结」并等周一早会解冻，而不是在周五动生产。
```

## 结论

短期记忆：同会话窗口能保留刚说的工作事实，会话上下文路径可用。

单轮长期记忆：一轮对话经 consolidation 后，新会话仍能遵守档案约束。

超长轮长期记忆：长对话之后关键约束仍能进入档案并在新会话生效。

三档均通过，当前记忆系统在本次 deepseek-flash 抽样下可以把会话上下文和五槽长期档案接到同一条回答路径上。
