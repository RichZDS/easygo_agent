# Independent Go-worker review of integrated TS/RPC

Reviewed at 2026-09-25T06:50:49.975651+00:00.
Exact source HEAD: `1c89eb8410cd0a91c648154194a957e0fe5474a0` in `/home/ubuntu/Projects/easygo-rpc-services`.
References: foreman review message `c67f4f77-0e3f-4f7e-838d-7b866821fbb0`, exact-target clarification `d1c0441f-2de4-4d37-bbc3-6d3d111c9825`.
TS source copied to `/tmp/crew/rpc-c926/review/go/agent-loop`, byte-compared with `git show 1c89eb8:services/agent-loop/src/*`, and built there. Integrated TS source and HEAD still matched at review completion. Initial reads before the target stabilized are excluded from this review.

## Finding: [P2] Reject duplicate JSON keys before classifying mutating RPC failures

Status: **reproduced through real mTLS/public APIs**, one finding with two related entry points.

Primary location: `services/agent-loop/src/rpc.ts:129` (unary `JSON.parse(buffer)`); SSE uses the same permissive parsing at `rpc.ts:84`. The parsed error becomes a trusted numeric rejection at `rpc.ts:65–71`; `tools.ts:78–85` labels `-32602` recoverable and `loop.ts:135–139` proceeds to another model/tool turn.
Related ingress location: `services/agent-loop/src/server.ts:44` (`JSON.parse` of the client body). In contrast, shared Go RPC rejects duplicate keys before dispatch in `packages/rpc-go/json.go:43–45`.

The TS JSON parser silently keeps the last duplicate key. This loses evidence that a response is ambiguous before the new safe-tool-recovery classifier decides whether another mutation is allowed. A workshop peer that has accepted `workshop.submit` and then returns the malformed response below is interpreted as a safe pre-acceptance rejection:

```json
{"jsonrpc":"2.0","id":"<matching RPC id>","error":{"code":-32603,"code":-32602,"message":"ambiguous rejection"}}
```

I reproduced the complete consequence with the exported test harness/public RPC API and a local real-TLS workshop fixture:

1. Gateway asks for workshop_submit with tool ID `attempt-1`.
2. Workshop fixture records the accepted side effect, then sends the duplicate-code response above.
3. Loop commits an ordinary tool error with `upstream.code=-32602` and calls the model again.
4. Model issues the same intended submission using tool ID `attempt-2`; its different trusted idempotency key means this executes another side effect.
5. The run ends **completed**, with **2 accepted workshop mutations and 3 gateway calls**, rather than stopping as an uncertain outcome after the first mutation.

This does not claim the current Go encoder produces duplicate fields. It proves a malformed upstream response can bypass the newly specified fail-closed outcome classifier. The regression fixtures already test accepted/disconnected and malformed responses; duplicate-key ambiguity is the missing boundary.

The second part of the same repro sent a client request containing duplicate method and namespace keys:

```json
{"jsonrpc":"2.0","id":"duplicate-create","method":"agent.run.cancel","method":"agent.session.create","params":{"namespace":"unauthorized","namespace":"demo"}}
```

The TS service returned HTTP 200 and created a session in demo. The resulting session count was 1. This demonstrates inconsistent strict-envelope handling with Go. **No certificate or namespace authorization bypass is claimed**: permission checks still apply to the final parsed value.

Suggested fix boundary: reject duplicate keys recursively while decoding client request JSON and upstream unary/SSE JSON, before any authorization/dispatch/error-recovery classification. Treat ambiguous mutating upstream responses as protocol errors so `callWorkshop` yields uncertain_tool_outcome. Add regressions asserting no client mutation and no second model/tool call for these inputs. A JSON.parse reviver alone cannot detect overwritten duplicate keys.

Reproducer (all files outside the implementation worktrees):

```bash
cd /tmp/crew/rpc-c926/review/go/agent-loop
node repro-boundaries.mjs
```

Actual exit **0**, meaning the assertions confirming the undesirable behavior passed. Evidence:
- `/tmp/crew/rpc-c926/review/go/agent-loop/repro-boundaries.mjs`
- `/tmp/crew/rpc-c926/review/go/repro-boundaries.log`

Output includes:
- `REPRO duplicate request accepted; mutated sessions: ... status:200 ... sessionCount:1`
- `REPRO malformed mutating error allowed next model and duplicate side effect: ... status:completed, accepted:2, gatewayCalls:3 ...`
- Persisted first tool error contains `{"code":"upstream_error","upstream":{"code":-32602}}` and the two actual keys end in `:attempt-1` and `:attempt-2`.

## Other reviewed behavior and evidence

Source review found no additional actionable issue in the requested areas:
- TS Authorizer validates socket authorization and matches the actual leaf DER fingerprint before exact method/namespace grants. Caller-controlled headers are not used for identity. Outbound clients validate chain, hostname and pin and disable cached TLS sessions/keepalive. Go layer independently enforces chain/leaf authorization, exact permissions and TLS1.3.
- Store operations are namespace-scoped; ownership is checked inside immediate transactions; startup owner acquisition and running-to-interrupted changes are atomic. Queued runs can restart; previously running side effects are not replayed.
- Loop persists complete assistant messages before tools and persists each result/error before the next tool/model call. Final context and terminal record commit in one transaction. Canceled/finished terminal states cannot be overwritten by later completion.
- Ownership is rechecked after a gateway response and before tool dispatch. Selected ownership-fencing test confirms no later workshop call.
- Uncertain mutating transport/internal/protocol failures generally stop the loop; explicit rejection codes and local argument errors can recover after durable error-result commit. The duplicate-key issue above is the reproduced exception.
- Streaming requires a matching terminal envelope; duplicate terminal frames and malformed deltas are checked. Provisional events are persisted distinctly and do not commit assistant/tool output on stream failure.
- Static Docker/config checks showed the reviewed workshop root at `/data/workshop` with a `/data` volume and UID1000 directory setup; distinct identity-only private-key mounts and public trust mounts. No new obvious launch blocker identified in the checked wiring. The example model names are operator placeholders.

## Targeted execution, actual exits

All commands ran in the isolated TS snapshot, using installed dependency files through a read-only node_modules symlink; no integrated source/build artifacts were written.

1. `npm run build` — exit **0**.
2. `node --test --test-name-pattern='public mTLS|ownership fencing|accepted then disconnected|explicit mutating pre-acceptance|SSE duplicate|failed error-result commit' test/service.test.mjs` — exit **0**, 8 tests passed, 0 failed. Log `/tmp/crew/rpc-c926/review/go/targeted-tests.log`.
3. `node --test --test-name-pattern='cancel aborts gateway|cancel during workshop|active database owner|process death|terminal transaction' test/service.test.mjs` — exit **0**, 5 tests passed, 0 failed. Log `/tmp/crew/rpc-c926/review/go/state-tests.log`.
4. `node repro-boundaries.mjs` — exit **0**, reproduced the finding described above.

These are selected boundary tests, not a duplicate of root's final matrix. Worker results are review evidence for foreman independent verification/signoff.

## Coverage limits

No implementation changes, commits, pushes, UI sends, peer communication or subagents. Only requested reports, snapshot build artifacts and repro files under `/tmp/crew/rpc-c926` were created. No production, paid model calls or operator credentials used; test PKI/databases were temporary. No Docker execution or full deployment verification claimed. No new Go/TS three-process run was made here because the foreman owns that final matrix. No claim of exhaustive state-space exploration, event-loop-pause/clock-jump fault testing, or elimination of every race. Unproven possibilities were not promoted to findings.
