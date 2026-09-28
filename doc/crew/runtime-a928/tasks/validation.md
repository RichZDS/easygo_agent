# Independent runtime feasibility and test plan
assignee: validation; status: done
## 1. Anchors
Read workshop runner, durable task workflow snapshots/resume/idempotency, server RPC, agent-loop tools. User wants runtime/model choices without direct shell injection, and main model reuse.
## 2. Deliverable
Analyze seams needed for runtime adapters and model profiles. Independently inspect current vulnerabilities/constraints that matter for this change, propose concrete tests for selection authorization, provider compatibility, credentials isolation, cancellation/resume and canonical idempotency. Write board/analysis-validation.md. You will later receive an exact implementation commit/blueprint for independent adversarial tests.
## 3. Exclusions
Read-only initial task: no code edits, no new design decisions, no paid providers or credentials, no other worktrees, no push.
## 4. Proof
Use actual source evidence and bounded local fixture probes if useful. Do not invent findings; distinguish current code from proposed additions.
## 5. Delivery
Write ACK referencing bootstrap id and report to board/worklog-validation.md. Ready for later validation task; report uncertainty immediately.
