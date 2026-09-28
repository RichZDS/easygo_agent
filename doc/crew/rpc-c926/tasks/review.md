Foreman status: done (2026-09-28); see ../closure.md. Docker runtime validation remains explicitly unexecuted.

# Independent integrated security/correctness review
assignee: go
status: done
## 1. Site
Root integrated worktree /home/ubuntu/Projects/easygo-rpc-services. Go pieces and TS initial + safe tool recovery are integrated. Root scripts/test-services.mjs already passed real three-process mTLS flow, cancellation, SIGKILL restart and owner exclusion before last tool-recovery follow-up. Root will rerun final tests. Review exact source, not worker claims.
## 2. Scope
Read-only review TS service + Go/TS RPC interoperability and authorization. Prioritize concrete actionable bugs: certificate/permission bypass, namespace isolation, cancellation/terminal races, ownership fencing/restart, write-side effect replay and tool-error classifier, stream terminal handling. Check current Docker/config wiring for practical launch blockers if obvious. Use targeted reproductions through public APIs; may copy isolated snapshot to /tmp/crew/rpc-c926/review. No paid models/production. No other workers or subagents.
## 3. Do not
No implementation edits, no broad stylistic requests, no speculative blockers. Separate reproduced failures from unproven risks. Do not duplicate root full test matrix. Root publishing alone.
## 4. Delivery
Write /tmp/crew/rpc-c926/board/review-go.md with source HEAD/time, concrete findings + file/line + repro/actual exits, or no findings plus coverage limits. Then worklog completion. No UI send needed.
