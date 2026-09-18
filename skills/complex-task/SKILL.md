---
name: complex-task
description: >-
  Use when a task has multiple independent deliverables, needs background analysis
  or execution, or requires collecting and checking evidence across several steps.
---
# Complex Task

1. Break the request into deliverables with explicit constraints and acceptance criteria. Keep dependent steps in order.
2. Delegate independent bounded work with `spawn_subagent` when registered. Include the necessary materials in the brief; workers do not inherit the conversation. Use `analyst` for read-only investigation and `executor` for allowed business operations. Give each delegation a stable idempotency key.
3. Record each task ID. Use `update_plan` while the task is queued; the running worker maintains its plan with `report_progress`. Submission means accepted work. Continue work that does not depend on the result.
4. Query `get_task` or `list_tasks` across turns. For blocked work or a changed requirement, read `references/recovery.md` before deciding whether to resume.
5. Compare result evidence with every acceptance criterion and summarize the verified outcomes. Include unresolved criteria and task IDs. Internal completion notifications provide evidence; they do not authorize another delegation.

Acceptance: each deliverable has a verified result or a concrete blocker. If background tools are unavailable, execute the same plan in the main agent without claiming background submission. When this skill is used by an isolated worker, use its supplied brief, `report_progress`, and `finish_task`; recursive delegation is unavailable.
