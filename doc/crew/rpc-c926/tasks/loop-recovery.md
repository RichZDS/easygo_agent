Foreman status: done (2026-09-28); see ../closure.md. Docker runtime validation remains explicitly unexecuted.

# TS Loop integration correction: ordinary tool errors vs uncertain writes
assignee: loop
status: done
## 1. Evidence
Current src/loop.ts catches every executeTool error, persists is_error, then throws. This regresses the previous native loop and the reference agents' behavior: a division-by-zero or invalid read argument aborts the run instead of giving the model the error to explain/correct.
## 2. Required behavior
Recover ordinary local argument/unknown-tool/calculator errors: append the is_error tool result durably, append to in-memory messages, continue the bounded loop (ordinary tool error is not a persistence failure). Cancellation, deadline, ownership or storage failure MUST still stop immediately. Failed result commit MUST prevent any next tool/model.
Distinguish uncertain mutating RPC operations. workshop_submit/resume/cancel transport failure or remote internal/execution error may occur after acceptance; do not feed those to automatic model retry. Persist failed/uncertain outcome and stop further side effects. Explicit remote rejections before acceptance (invalid params/not found/forbidden/conflict/capacity) may be modeled as tool errors. Keep no automatic HTTP retries. Document classifier and preserve sanitized upstream code; never leak arbitrary error bodies.
Do not relax namespace injection, argument bounds or submit key run_id:call_id. No new RPC wire/config fields needed.
## 3. Scope
Only services/agent-loop source/tests. No Docker/docs outside its test comments; foreman updates deployment docs. New commit, preserve 7f7855f.
## 4. Tests
Actual public RPC run: model requests bad calculator -> committed error result -> next model gives final answer -> completed. Same for known invalid read args. Unknown outcome after workshop.submit accepted/disconnected stops run and NEVER sends a second submit or later tool; cancellation and commit-failure tests stay green. Update prior tests only if they deliberately expected the old abort-on-any-error behavior; keep zero forbidden side effects assertions.
## 5. Delivery
Single follow-up commit + typecheck/test exits and worklog. No push or peer contact.
