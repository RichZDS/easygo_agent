Foreman status: done (2026-09-28); see ../closure.md. Docker runtime validation remains explicitly unexecuted.

# Fix independently reproduced ambiguous-JSON retry issue
assignee: loop
status: done
## 1. Evidence
Read /tmp/crew/rpc-c926/board/review-go.md. Exact integrated target 1c89eb8. Reviewer reproduced a workshop mutation accepted then malformed duplicate error.code (-32603 then -32602); permissive JSON.parse makes it look safely rejected and allows another mutation.
## 2. Required fix
Reject duplicate keys recursively (including escaped spelling equivalents), excess nesting (64), invalid UTF8 and malformed/trailing JSON on TS RPC ingress and upstream JSON/SSE frames BEFORE error classification. Preserve ordinary valid rejected-tool recovery. Ambiguous/malformed mutating RPC response MUST remain uncertain_tool_outcome and stop side effects.
Add a self-contained JavaScript ESM helper src/strict-json.mjs with JSDoc parseJSON(text:string):unknown, usable without TS build. TS build may enable allowJs and copy it into dist (no runtime dependencies). Foreman operator scripts will import this SAME helper source to avoid separate parser semantics. Parser must understand JSON string escapes/decoded keys, not regex duplicate detection. Constant sanitized errors, no source text/key content in error messages. Final JSON.parse after validating token structure is fine. Validate UTF8 using fatal TextDecoder or isUtf8 on complete raw bytes; don't reject valid characters split across network chunks.
Use helper in server readBody and RpcClient unary/SSE parse. TypeScript consumer imports ./strict-json.mjs. No handwritten crypto, no other feature work.
## 3. Scope
Only services/agent-loop source/tsconfig/tests; no root client edits, no Go/Docker. Preserve delivered commits, new fix commit only. Do not alter original red reviewer snapshot/logs.
## 4. Proof
Original reviewer mutation reproducer must now show only one accepted mutation, failed/uncertain run and no second model call. Ingress duplicate envelope/namespace creates no session/run. Cover nested duplicate and escaped equivalent names, malformed UTF8 across chunks, valid split UTF8, valid SSE/ordinary recoverable tool error. Typecheck and npm test. Report actual commands/exits.
## 5. Deliver
New commit + helper API + original repro green evidence in worklog. No push/peer/UI contact.

## Same uncertainty boundary: validate mutation receipts
A syntactically valid RPC success such as result:null is still not a valid workshop.submit receipt. Before returning workshop task results to the model, validate the TaskSummary minimum contract (nonempty id, namespace equals trusted run namespace, known status); get/cancel/resume must match requested task_id. A malformed/mismatched mutation receipt is an uncertain remote/protocol outcome, never a successful null tool result that invites resubmission. Scoped list entries must also match trusted namespace before exposing data. Read result pages must match task_id (and requested run_id if supplied). Keep errors sanitized; don't return the rejected payload. Cover accepted-submit followed by result:null or wrong namespace: one mutation only, no second model/tool dispatch. This is part of the same malformed-response uncertainty fix; no new wire/config fields.
