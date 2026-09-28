## ACK 2026-09-25T07:08:44.994007+00:00
Message ID: 4e1fb01e-a7de-4c22-a09e-076b8e67e54b
Read smux and loop-json.md receipt addendum. Keeping strict parser commit intact; adding bounded TaskSummary/list/result receipt checks before exposure and targeted one-write proofs. No wire/config additions, push or peer/UI contact.

## COMPLETE / STRICT JSON HANDOFF 2026-09-25T07:03:52.656531+00:00
Referenced message ID: 52ba64c6-d915-4450-87ad-8faf3442348a
Blueprint: /tmp/crew/rpc-c926/board/tasks/loop-json.md
Review: /tmp/crew/rpc-c926/board/review-go.md
New fix commit: 7b87843ddcb9c45637297d5f70f5daee50baf445
Parent preserved: adaf939f45d9ee19a3a95a667f466fbfdaf86a74 (and original 7f7855f unchanged).
Branch: feat/rpc-ts-loop-c926; clean working tree. No push, peer/UI contact, root client, Go, Docker or other features changed.

### Shared source API for operator client
`services/agent-loop/src/strict-json.mjs` is standalone dependency-free JavaScript ESM; named export `parseJSON(text: string): unknown`, documented with JSDoc. Foreman can import this exact SOURCE helper directly without TS build. TS imports `./strict-json.mjs`; allowJs copies it to dist and emits strict-json.d.mts with the same API.
Callers must decode network bytes using fatal UTF8 validation before parseJSON, e.g. `parseJSON(new TextDecoder('utf-8', {fatal:true,ignoreBOM:true}).decode(rawBytes))`. The helper validates token structure, decoded-key uniqueness recursively (including escaped equivalence), and maximum 64 nested containers. It rejects malformed/trailing JSON; final JSON.parse only runs after structural validation. Every parser failure is SyntaxError('Invalid JSON'), without source/key/position data.

### Integration details
TS public ingress validates full raw bytes with fatal TextDecoder before parsing. RpcClient unary/SSE uses fatal streaming TextDecoder with EOF flush, correctly accepting multi-byte characters split across network chunks. Both unary bodies and SSE data frames use the same helper before envelope/error classification. SSE strips only its single optional ASCII space after data:, preserving invalid JSON whitespace for rejection.
SSE terminal errors are held until stream end, after UTF8 flush/framing/terminal checks; a malformed or disconnected tail cannot turn a potentially accepted write into a safe rejection. Valid clean SSE rejections and existing ordinary tool-error recovery remain supported. Mutating protocol errors still become uncertain_tool_outcome with no next model/tool request.
Files: src/strict-json.mjs, src/rpc.ts, src/server.ts, tsconfig.json, test/strict-json.test.mjs only.

### Actual worker commands/results (foreman independently signs off)
- `npm run typecheck --prefix services/agent-loop`: exit 0 after final changes.
- `npm test --prefix services/agent-loop > /tmp/crew/rpc-c926/loop-json-test.log 2>&1`: exit 0; 58 passed, 0 failed/canceled/skipped; includes TS build and existing safe-error/cancellation/storage/restart/SSE tests.
- `node /tmp/crew/rpc-c926/loop-json-repro-green.mjs > /tmp/crew/rpc-c926/loop-json-repro-green.log 2>&1`: exit 0.
- `git diff --cached --check`: exit 0; commit exit 0; git status clean.

Original reviewer reproducer is unchanged. The separate green script uses its EXACT duplicate-request and duplicate-code mutation/model fixture behavior; only the helper import target, expected assertions, and result labels changed. Green evidence:
- Duplicate ingress: HTTP 400, RPC -32700, sessionCount 0.
- Accepted workshop mutation then duplicate code response: status failed; asserted error code uncertain_tool_outcome; accepted 1; gatewayCalls 1; only trusted attempt-1 key present; persisted tool error uncertain_tool_outcome.

Additional tests cover nested duplicates, escaped-equivalent names (including surrogate-pair key spellings), 64 accepted / 65 rejected containers, malformed/trailing JSON, duplicate envelope/namespace/run input creating no session/run, malformed and truncated UTF8 across chunks, valid split UTF8 unary/SSE and ingress, nested/escaped/deep/invalid-UTF8 mutation responses, SSE duplicate delta, malformed/disconnected SSE error tails, and valid recoverable SSE rejection.

### Original red evidence preserved byte-for-byte
- /tmp/crew/rpc-c926/review/go/agent-loop/repro-boundaries.mjs SHA256 c282a0b14c3cfa7f13251a54e5e31648c5efe01bfeb7eb56e2c05ac613e04722
- /tmp/crew/rpc-c926/review/go/repro-boundaries.log SHA256 64ee3a45380dcf9dacc9f450bf839015eb76a196c01d6261a254a79148ce718e
Both hashes captured before implementation and reconfirmed after verification. Original snapshot/logs untouched.
Green report paths: /tmp/crew/rpc-c926/loop-json-repro-green.mjs, /tmp/crew/rpc-c926/loop-json-repro-green.log, /tmp/crew/rpc-c926/loop-json-test.log.

Foreman remaining work: import this helper in operator scripts with fatal byte decoding, independently review/integrate and run its chosen final matrix. No worker blocker; no Docker/production/paid-provider validation claimed.

## ACK 2026-09-25T06:59:15.272586+00:00
Message ID: 52ba64c6-d915-4450-87ad-8faf3442348a
Read smux and loop-json.md. Reading board/review-go.md (the blueprint points to board/, not board/tasks/). Implementing shared source ESM strict JSON parser and fatal UTF8 validation for TS ingress/upstream unary/SSE only. Preserve original red evidence and previous commits; no root/Go edits or push/peer/UI contact.

## COMPLETE / RECOVERY HANDOFF 2026-09-25T06:44:42.975771+00:00
Referenced message ID: 473bcbdf-3753-4153-b223-8fe11952653c
Blueprint: /tmp/crew/rpc-c926/board/tasks/loop-recovery.md
New follow-up commit: adaf939f45d9ee19a3a95a667f466fbfdaf86a74
Parent preserved: 7f7855f405c52b6c0470f8682ce5982141c6f729
Branch: feat/rpc-ts-loop-c926; worktree clean. No push or peer contact. Only services/agent-loop source/tests changed; no wire/config additions.

### Correction / classifier
- Local argument validation, unknown tool, calculator and invalid read argument errors produce a durably committed is_error tool result, append that exact result to in-memory messages, and let the bounded loop continue. Invalid argument shape is now handled at the tool boundary rather than rejecting the whole model response.
- Workshop RPC failures have a distinct internal wrapper; malformed remote responses cannot masquerade as local invalid-argument errors.
- Mutating workshop.submit/resume/cancel errors recover only for explicit pre-acceptance numeric RPC rejections: -32602 (params), -32004 (missing), -32003 (forbidden), -32009 (conflict), -32029 (capacity). All other transport/protocol/internal/execution outcomes record uncertain_tool_outcome and stop before another tool/model call. No automatic HTTP retries.
- Read-only RPC failures can be returned as tool errors. Abort/deadline errors bypass recovery; ownership and storage failures still throw. Error-result commit failure halts before any later effect/model. Namespace injection, argument bounds, trusted run_id:call_id submit key, step cap and 30-second lease unchanged.
- RPC errors retain only numeric error code and optional data.code matching /^[a-z][a-z0-9_]{0,63}$/. Arbitrary upstream messages and other data are discarded before reaching model history, terminal error or event records. Code comments document the classifier.

### Worker evidence; foreman independently verifies/signs
- `npm run typecheck --prefix services/agent-loop`: exit 0.
- `npm test --prefix services/agent-loop > /tmp/crew/rpc-c926/loop-recovery-test.log 2>&1`: exit 0; 38 tests passed, 0 failed/canceled/skipped. Build included.
- `git diff --check`: exit 0.
- Commit command exit 0; git status shows clean assigned branch and log confirms the original commit remains the parent.
Evidence: /tmp/crew/rpc-c926/loop-recovery-test.log.
New actual public-mTLS RPC proofs: bad calculator, invalid read args and null argument shape -> committed error visible in SQLite before second model request -> completed final answer; error-result storage trigger failure -> no subsequent tool/model; submit/resume/cancel accepted then disconnected -> failed uncertain outcome with exactly one upstream request and no later tool/model; internal/execution/malformed write responses -> fail closed; all five explicit rejection codes -> safe recovery; arbitrary body/key sentinel absent from history/events/run errors. Prior bounds assertions now inspect committed invalid_integer errors while retaining zero forbidden workshop effects. Existing cancellation, successful-result commit failure, ownership, restart, step cap and SSE suites remain green.
No paid model/container/production validation claimed. Foreman previously reported the original three-process integration green; this correction still requires foreman independent integration review. No worker blocker remains.

## ACK 2026-09-25T06:41:47.411657+00:00
Message ID: 473bcbdf-3753-4153-b223-8fe11952653c
Read smux and all loop-recovery.md. Implementing bounded safe-tool-error recovery, retaining fail-closed handling for cancellation/deadline/storage/ownership and uncertain mutating RPC outcomes. New follow-up commit only; preserving 7f7855f. Reports via this file; no push/peer contact.

## COMPLETE / HANDOFF 2026-09-25T06:29:02.563282+00:00
Referenced message IDs: cc7ed4d1-e2af-4937-acbc-fb3fae09a708; c8dc60f9-1025-4d59-b74a-df6b452428ac
Task: independent durable TypeScript Agent Loop service.
Branch: feat/rpc-ts-loop-c926
Final single task commit: 7f7855f405c52b6c0470f8682ce5982141c6f729
Previous intermediate 84fd3fb was amended; use final commit above. Working tree clean. No push, no peer/subagent communications, no other worktree changes.

### Integration correction completed
workshop_result schema/runtime limit is 4..32768, default 8192; workshop_list schema/runtime limit is 1..100, default 20. Public RPC tests cover zero, below-min, above-max, fractional, exact endpoints, omitted/default limits and the schemas actually sent to gateway. Rejected values produce failed runs with zero workshop requests. Lease remains 30 seconds.

### Deliverable / exports
All 14 files are under services/agent-loop: .gitignore, package.json/package-lock.json, tsconfig.json, config.example.json; src/types.ts,validation.ts,rpc.ts,store.ts,tools.ts,loop.ts,server.ts; test/helpers.mjs,service.test.mjs. Dockerfile and README untouched.
- `import { startServer, parseConfig } from './dist/server.js'`; `const service=await startServer(config)` yields `server`, `address` (Node AddressInfo), and async idempotent `close()`.
- `import { RpcClient } from './dist/rpc.js'`; `new RpcClient(tls, endpoint, timeoutMs=120000)`; `call(method, params, signal?, onDelta?)`, `close()`.
- `node dist/server.js --config FILE`; npm scripts build/typecheck/test/start. SIGTERM/SIGINT gracefully abort outstanding gateway/workshop requests and release ownership.
- Config exact contract keys; example public certificate paths and exact agent methods in config.example.json. No private keys checked in. Node 22.23+; no runtime npm dependencies.

### Implemented behavior
Independent Node HTTPS loop; TLS 1.3 verified CA/hostname/actual-leaf fingerprints, exact cert/method/namespace authorization including health; strict JSON-RPC profile/unknown params checks. Bounded incremental SSE decoder requires one terminal result; deltas remain provisional. Remote-only gateway/workshop, explicit nonstream option, opaque provider_state and usage/cost retained.
SQLite FULL-sync WAL lease ownership/fencing, startup exclusion, durable queued FIFO by session, bounded cross-session concurrency, scoped idempotency conflict handling, raw messages/events and successful context. Assistant committed before first tool; each tool result committed before next effect/model. Terminal assistant/context/status/event committed in one transaction; failed/canceled/interrupted turns cannot poison next run's context. No replay of previously running work on restart; queued work restored.
Calculator + all 7 workshop tools; trusted namespace and submit key run_id:tool_call_id. Step limit plus final tool-disabled turn; serialized UTF8 request budget and independent tool-disabled compaction with explicit oversize failure. Logs contain only operation metadata.

### Commands and actual worker results (foreman independent check/signoff still required)
- `npm install --ignore-scripts --prefix services/agent-loop`: exit 0; lock committed.
- `npm run typecheck --prefix services/agent-loop` after final source change: exit 0.
- `npm test --prefix services/agent-loop > /tmp/crew/rpc-c926/loop-test.log 2>&1`: exit 0; 29 Node tests passed, 0 failed/canceled/skipped. This script also builds with tsc.
- `git diff --check`: exit 0.
- `git commit --amend --no-edit`: exit 0; `git status --short --branch` shows only assigned branch, no changes.
Evidence: /tmp/crew/rpc-c926/loop-test.log. Tests generate temporary CA/key/cert fixtures and exercise actual HTTPS public server and TLS upstream fixture sockets, including a SIGKILLed child process.
Coverage: cert rejection/pinning/method/namespace/health/envelopes; SSE and nonstream tool rounds; provider continuation/accounting; FIFO/concurrency/isolation/idempotency/pagination; assistant and tool-result storage failures; atomic terminal rollback; cancellation at gateway and workshop; upstream errors; schema and bounds; max steps/compaction; malformed/missing/duplicate/error/oversized SSE; active owner exclusion and fencing; successful restart context; process death during an already-submitted workshop side effect, interrupted status, queued restoration and no replay; RPC deadline/socket close.

### Limits / remaining foreman work
Runtime bounds are 10-minute run deadline, 120-second individual upstream timeout, 30-second lease heartbeat every 5 seconds; 16 tool calls per response; 1000 queued/running admissions; 32768-byte run input; max 16 MiB upstream bytes and 1 MiB SSE frame. Run deadline implementation exists; tests directly exercise the shorter RPC deadline, not a full 10-minute elapsed run.
Crash test confirms SIGKILL exit, verifies unexpired ownership still excludes startup, then explicitly expires that dead owner's persisted lease to avoid a 30-second test delay. Foreman's real three-process proof should wait actual lease expiry as planned.
Only local HTTPS fixtures/child server process tested here. No paid model calls, Docker execution, production deployment, or combined real Go service integration claimed. Foreman owns three-process integration, review/signoff/merge/push. No worker blocker remains.

## ACK 2026-09-25T06:27:56.615253+00:00
Message ID: c8dc60f9-1025-4d59-b74a-df6b452428ac
Received integration correction. Aligning workshop_result limit to 4..32768 and workshop_list limit to 1..100 in schemas/runtime; adding public-RPC bounds tests. Lease remains 30 seconds. Existing single task commit will be amended after checks; no push.

## STATUS 2026-09-25T06:22:47.411425+00:00
Message ID: cc7ed4d1-e2af-4937-acbc-fb3fae09a708
Implementation in progress; first strict typecheck exit 0. mTLS pinning + bounded SSE, SQLite lease/fencing/FIFO and transactional terminal, loop barriers/tools/compaction, public server implemented. Generated-cert integration tests next.
Exported API: startServer(config) -> {server,address,close}; RpcClient(tls,endpoint,timeoutMs=120000).call(method,params,signal?,onDelta?). CLI dist/server.js --config FILE.
Config is exact contract fields; agent.run.start input is string (32768 UTF8 bytes), key <=256 bytes. Runtime bounds: 10 minute run deadline, 120 second individual upstream deadline, 30 second ownership lease renewed every 5 seconds. No contract additions. Calculator schema matches old Go operation/a/b. Workshop submit key run_id:tool_call_id; model cannot supply namespace or key.
Foreman UI checked blocked; no UI message sent. No external/live provider credentials accessed. Scope only services/agent-loop excluding Dockerfile/README; no push.

## ACK 2026-09-25T06:17:42.022279+00:00
Message ID: cc7ed4d1-e2af-4937-acbc-fb3fae09a708
Read smux and all onboarding loop.md. Executing blueprint in assigned worktree; reports via this file.

# Worklog loop
