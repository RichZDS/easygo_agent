## COMPLETE / REVIEW HANDOFF 2026-09-25T06:50:49.975651+00:00
Read-only review of exact integrated HEAD 1c89eb8410cd0a91c648154194a957e0fe5474a0 complete. References c67f4f77-0e3f-4f7e-838d-7b866821fbb0 and d1c0441f-2de4-4d37-bbc3-6d3d111c9825.
Report: /tmp/crew/rpc-c926/board/review-go.md.
One P2 reproduced finding: permissive duplicate-key parsing lets an ambiguous mutating upstream error be treated as recoverable, enabling a second accepted submission under a new tool idempotency key. Same decoder also accepts duplicate method/namespace inbound requests. No auth bypass claimed. Exact source lines, public mTLS reproducer, actual outcomes and minimal fix boundary recorded in report.
Snapshot build exit 0; two targeted test groups exit 0 (8 + 5 passing tests); custom repro exit 0 confirms undesirable behavior (2 accepted mutations / 3 gateway calls / completed run). Evidence logs under /tmp/crew/rpc-c926/review/go. No other actionable finding in reviewed auth/state/recovery paths; limitations stated. No implementation edit, new commit, push, peer/subagent or UI send. Root owns fix decisions and final matrix.

## REVIEW STATUS 2026-09-25T06:49:54.896402+00:00
Target 1c89eb8, independent snapshot only. Reproduced one actionable parser/recovery issue: TS JSON.parse accepts duplicate keys in client requests and upstream error envelopes. A real TLS fixture accepted workshop.submit then returned ambiguous error code -32603 followed by duplicate -32602; loop classified it as safe rejection, made a second model call and executed a second submission under a new idempotency key (2 accepted effects, 3 model calls, run completed). Also duplicate method/namespace session-create request executes instead of fail-closed rejection. Repro script/log: /tmp/crew/rpc-c926/review/go/agent-loop/repro-boundaries.mjs and /tmp/crew/rpc-c926/review/go/repro-boundaries.log; exit 0 confirms assertions. Existing 8 selected auth/fencing/SSE/recovery tests pass; additional selected cancellation/restart/atomic-terminal tests running. No auth bypass claimed; no implementation changes.

## ACK 2026-09-25T06:48:26.543350+00:00
Received d1c0441f-2de4-4d37-bbc3-6d3d111c9825. Review snapshot fixed at 1c89eb8410cd0a91c648154194a957e0fe5474a0; all copied TS src files byte-compared with that Git source. No implementation edits. Initial moving-HEAD reads are excluded from findings.

## ACK 2026-09-25T06:47:39.148238+00:00
Received c67f4f77-0e3f-4f7e-838d-7b866821fbb0. Loaded smux and review blueprint; beginning read-only integrated TS/RPC auth and state/recovery review. No edits, subagents, peers or UI sends.

## COMPLETE / FOLLOW-UP HANDOFF 2026-09-25T06:32:30.600715+00:00
ACK / reference: 311fff72-f2d0-4e74-8ad3-9fe1879d94e7. Exact go-followup.md scope completed. FILE delivery only; no UI send.

Commit: 05eb40a5664d060aef1bb3f075f4caf66eff528b
Title: Fix RPC null results and restore production metadata observations
Branch: feat/rpc-go-services-c926
Parent: ba3221bccedf0fc70ffaf486c052a85dabcdcc82 (preserved). Foreman should cherry-pick ONLY this new follow-up commit onto its collected branch.
Working tree verified clean; no push or merge.

Changes:
1. services/workshop/config.example.json workshop.root = /data/workshop, matching the writable /data deployment volume. No Dockerfile changes; no container runtime claim.
2. rpc.Envelope.MarshalJSON emits result on every success, including result:null; errors never emit result, even if the caller supplies a nonnil Result. Standard Envelope unmarshal remains usable. The same envelope encoder serves unary and SSE terminal responses.
3. Optional rpc.ServerConfig.Audit hook plus rpc.JSONLogger with a shared mutex. Both production Go command entrypoints enable stderr JSON audit. Gateway server.Config.Observer is passed into gateway.New, and its production command enables model observations using the SAME JSONLogger instance as RPC audit. Model request_id remains exactly the RPC ID.

Log schema:
- kind=rpc: request_id, principal_id (from authorized actual TLS leaf), method, namespace, duration (time.Duration JSON nanoseconds), status (HTTP), error_code (domain code; omitted on success). Domain/SSE failure may correctly have status=200 plus error_code. Invalid unauthenticated/unparsed metadata remains empty; no body fallback.
- kind=model: existing gateway.Observation fields unchanged: request_id, model, protocol, http_status, latency, first_delta, has_first_delta, usage, cost, error_code when present. No prompt, response text, upstream error body or credential values.
- TLS handshake rejections remain handled by TLS before any HTTP audit hook; HTTP health/denial/business/SSE outcomes are audited. No forwarded identity/header trust added.

Proof and actual exits (Go /home/ubuntu/sdk/go/bin/go):
- packages/rpc-go: go test ./... = 0; final go test -race ./... = 0; go vet ./... = 0.
- services/ai-gateway: go test ./... = 0; final go test -race ./... = 0; go vet ./... = 0.
- services/workshop: go test ./... = 0; final go test -race ./... = 0; go vet ./... = 0.
- git diff --cached --check = 0; commit = 0; clean post-commit status verified.
- Logs: /tmp/crew/rpc-c926/board/go-followup-{rpc,gateway,workshop}-race.log and go-followup-{rpc,gateway,workshop}-vet.log (vet success files empty).

Regression evidence:
- TestNilSuccessAndErrorEnvelopes uses real mTLS unary and SSE responses to assert literal result:null on success, no error field, and no result field on error; Envelope decoding still succeeds.
- TestAuditVerifiedIdentityAndFailureMetadata exercises success, business conflict, namespace denial and SSE failure; records verified principal despite forged identity/auth headers, actual status plus correct domain error, and excludes prompts/keys/deltas.
- TestJSONLoggerConcurrent verifies complete parseable records under concurrent calls with race detector.
- Gateway TestCommandProcess captures actual child process stderr. It runs success and upstream HTTP502 with distinct RPC IDs, requires exactly one correlated model and RPC record for each, asserts model/protocol/status/latency/first-delta/usage/cost fields, checks error propagation, and proves absence of prompt, response, API key and upstream error-body fixtures.
- Workshop TestCommandProcess captures actual child stderr for list success and missing-task failure, verifies principal/method/namespace/duration/status/error, and excludes the private task parameter.
- Existing TLS/auth/negative, gateway core and workshop core suites remain green.

No remaining worker blocker. Root three-process proof and independent acceptance remain foreman-owned as the follow-up blueprint specifies. No TS, Dockerfiles, root scripts/README, contract, production or reference-project edits. One transient test-fixture JSON typo during implementation was corrected before final passing checks; no implementation/test weakening.

## STATUS 2026-09-25T06:31:58.679688+00:00
Follow-up 311fff72-f2d0-4e74-8ad3-9fe1879d94e7: all three fixes implemented; shared RPC, gateway and workshop go test ./... passed. Gateway child-process test captures model and RPC JSON records for two distinct success/failure IDs, verifies matching correlation, metadata, status/error, and absence of prompts/provider response/API key/upstream error body. Workshop child-process test captures success/failure audit and excludes task parameter. Nil unary and SSE result tests verify explicit result:null and absence of error; error omits result. Concurrent JSON logger test and real-TLS verified-principal/SSE-failure audit tests added. Final race+vet running for all affected modules.
Public wiring: optional ServerConfig.Audit func(rpc.Audit) and gateway server.Config.Observer gateway.Observer, both json:"-". Both production commands enable Audit via rpc.JSONLogger(os.Stderr); gateway shares that mutex-protected logger with model observations. JSON kind is rpc or model. RPC fields: request_id, principal_id, method, namespace, duration (nanoseconds), status (actual HTTP), error_code (domain, absent on success). Model metadata retains the existing gateway.Observation fields, request_id identical to RPC id. No contracts/Dockerfiles/TS/root docs changed.

## ACK 2026-09-25T06:29:26.027416+00:00
Received follow-up message 311fff72-f2d0-4e74-8ad3-9fe1879d94e7. Loaded smux; read all go-followup.md. Will correct /data/workshop example root, null success envelopes, restore production model observations and RPC metadata audit. Preserve ba3221b and create one new scoped commit. FILE reports only; no UI send.

## COMPLETE / HANDOFF 2026-09-25T06:26:39.350270+00:00
Receipt: ACK b58c78be-82fd-453c-bcf3-f34f7a3f8651 from rpc-foreman-c926. Blueprint complete in worker scope; ready for foreman independent review/cherry-pick. This is a FILE delivery, not a claim of UI delivery. Last observed foreman UI state was blocked; no message was sent into it.

- Worktree: /home/ubuntu/Projects/easygo-rpc-go
- Branch: feat/rpc-go-services-c926
- Single implementation commit: ba3221bccedf0fc70ffaf486c052a85dabcdcc82
- Commit title: Extract Go gateway and workshop services with pinned mTLS RPC
- Working tree clean after commit. No push, no main merge, no production/reference-worktree changes.

### What changed
- Moved pkg/ai and pkg/gateway into services/ai-gateway (module easygo-agent/services/ai-gateway); moved pkg/workshop into services/workshop (module easygo-agent/services/workshop). Root imports and require/replace directives rewired.
- All moved core source/tests checked byte-for-byte against the baseline after only import path substitutions: match. Existing provider, streaming, reasoning, bounded workshop views, durability, native subprocess, and regression tests preserved.
- Deleted root cmd/ai-gateway and cmd/workshop production entrypoints; replaced with services/*/cmd/server. Replaced legacy command-specific bearer/listen/observer tests with actual mTLS CLI process tests, env credential resolution proof and graceful SIGTERM. Legacy library HTTP handlers/tests retained only as transitional local app test harness; new RPC listeners expose no /v1 routes.
- Added stdlib-only easygo-agent/rpc module packages/rpc-go. TLS >=1.3, verified chains plus SHA-256 actual leaf DER allowlist, outbound chain/hostname/leaf-pin helper, exact method and namespace permissions with explicit '*' wildcard only, health authorization, no forwarded identity trust.
- JSON-RPC strict profile: string ID bounds, params object, namespace grammar, duplicate keys rejected recursively (including opaque provider state), unknown/case-incorrect fields rejected, no batch/notification execution, bounded request reads and network writes, sanitized error envelopes with mutually exclusive result/error.
- Gateway models/generate mapped; complete ProviderState retained; provisional SSE gateway.delta notifications and exactly one terminal result or error; cancellation reaches upstream HTTP.
- Workshop all eight methods mapped to current scoped APIs; required submit idempotency key; Summary/ListPage/Result return bounded views, no private host workspace/config fields; sanitized domain errors.
- Config examples: services/ai-gateway/config.example.json and services/workshop/config.example.json. Own public cert gets only health; agent-loop gets health plus service business methods, namespace '*'. No end-client business grants by default.

### Build/start and public APIs
- From services/ai-gateway: /home/ubuntu/sdk/go/bin/go run ./cmd/server --config FILE; default listen :8441.
- From services/workshop: /home/ubuntu/sdk/go/bin/go run ./cmd/server --config FILE; default listen :8443.
- CLI only accepts --config; listen is a JSON field. TLS/authorization shape is exactly contracts/rpc-v1.md. Gateway FileConfig is embedded at top level; workshop config is under workshop. Nonempty legacy bearer_token_env is rejected.
- rpc.NewServer(ServerConfig, map[string]Method, maxRequestBytes) (*http.Server,error); rpc.Serve(context.Context,*http.Server) error; rpc.ClientTLS(TLSConfig,peerCertificateFile) (*tls.Config,error).
- ai-gateway/server.LoadConfig(io.Reader) (Config,error), New(Config) (*http.Server,error), Methods(*gateway.Gateway).
- workshop/server.LoadConfig(io.Reader) (Config,error), New(Config) (*http.Server,*workshop.Service,error), Methods(*workshop.Service). Caller closes owned workshop service after stopping HTTP; production command does this.
- Test support only: easygo-agent/rpc/rpctest generates temporary P-256 test PKI and starts real TLS HTTP servers. No committed private keys/cert fixtures.

### Executed checks and actual exits
Go executable /home/ubuntu/sdk/go/bin/go; version go1.25.14 linux/amd64.
- Root: go test ./... = 0; go test -race ./... = 0; go vet ./... = 0.
- packages/rpc-go: go test ./... = 0; final go test -race ./... = 0; final go vet ./... = 0.
- services/ai-gateway: go test ./... = 0; final go test -race ./... = 0; final go vet ./... = 0.
- services/workshop: go test ./... = 0; final go test -race ./... = 0; final go vet ./... = 0.
- git diff --cached --check = 0; commit command = 0; clean git status verified after commit.
Evidence logs: /tmp/crew/rpc-c926/board/go-{root,rpc,gateway,workshop}-race.log and go-{root,rpc,gateway,workshop}-vet.log. Successful vet logs are intentionally empty. Core-move equivalence verification printed match and exited 0.

Meaningful integration evidence:
- Shared rpc TestRealTLSIdentityAndAuthorization: authorized roundtrip; rejects no client cert, foreign CA client, unknown pinned leaf with same CN, expired leaf, TLS1.2, wrong server pin and wrong hostname; method/namespace and health grants; forged headers ignored; /v1 absent.
- Shared strict profile tests: invalid envelopes, duplicate/unknown/case variants, batch/notification/invalid ID/params, body bounds, namespace grammar, null/fraction scalars, depth/UTF8/opaque duplicates; invalid requests execute no method.
- Gateway server tests: real TLS generate/models, ProviderState, malformed nested input, missing/null request, exact SSE envelope IDs, success/truncation/upstream error terminal, cancellation reaches upstream. No upstream error text leakage.
- Workshop TestAllWorkshopMethodsOverTLS: each method through actual TLS to real native-protocol local subprocess; artifact created, idempotent repeat/conflict, cancellation, resume, result/list/events, cross-namespace reads and mutations denied, bounds/null/fractions/unknown params denied.
- Both TestCommandProcess tests start actual child command processes, reach mTLS health, perform business RPC, and shut down with SIGTERM. No request/response/credential log leakage.

### Remaining ownership / limitations / pitfalls
- Foreman must independently rerun proof and own final signoff, full client->TS->gateway/workshop three-process integration, PKI scripts, root deployment/docs, merge/push. Worker test results above are evidence, not foreman signoff.
- Docker is not installed here; no container execution claimed. No paid/live model or native provider CLI calls; HTTP provider and native CLI protocol fixtures are local.
- Run checks in every module: root go test ./... does not traverse nested modules. Module replace paths are relative to repository structure; no go.work required.
- Unknown/unpinned client leaf is rejected during TLS handshake; known pinned identities with wrong method/namespace get HTTP403 JSON-RPC -32003. Health-only cert can reach health but not business RPC.
- workshop.events retains the existing Event array API from the contract; Summary/ListPage/Result bounds are inherited unchanged.
- Actual gateway/workshop core implementations remain unchanged apart from moved imports. Root old local app remains transitional and continues to use its existing legacy library HTTP harness; new deployments mount only RPC.
- No pending blocker or design deviation. Earlier transient local test issues (wrong command cwd and an overbroad test assertion matching workspace-write) were corrected; final checks above pass.

## STATUS 2026-09-25T06:25:24.256492+00:00
All four modules (root, packages/rpc-go, services/ai-gateway, services/workshop) completed go test -race ./... and go vet ./... with exit 0. All workshop RPC methods exercised through TLS, real local native-protocol shell subprocess creates artifact, idempotency/conflict/scope checks and cancel/resume pass. Both replacement CLI process tests prove mTLS health, business roundtrip and graceful SIGTERM without logged request/response/key. Added final small hardening: bounded JSON write deadlines, explicit missing/null gateway request rejection and wrong server hostname test; rerunning affected module checks now. Foreman status checked: blocked, so no UI messages sent. Full report will stay in this FILE channel.

## STATUS 2026-09-25T06:22:03.421397+00:00
Extraction and initial RPC handlers implemented. Root go test ./... exit 0; gateway core tests exit 0; workshop preserved core tests exit 0; shared RPC real TLS/profile tests exit 0. Added generated ephemeral PKI helper package easygo-agent/rpc/rpctest for integration tests. Config examples now present at services/ai-gateway/config.example.json and services/workshop/config.example.json; exact launch `go run ./cmd/server --config FILE` from each module (only --config, listen in JSON). No service Dockerfiles/readmes touched.
APIs final: rpc.NewServer(ServerConfig, map[string]Method, maxRequestBytes) (*http.Server,error), rpc.Serve(ctx,*http.Server), rpc.ClientTLS(TLSConfig,peerCertificateFile). gateway server.New(Config) (*http.Server,error); workshop server.New(Config) (*http.Server,*workshop.Service,error), caller closes workshop after HTTP stops. Strict Decode rejects duplicate keys recursively, unknown and incorrectly cased fields. TLS unknown pinned clients rejected during handshake. Service integration/command tests and race/vet in progress. One initial command used wrong cwd while writing files; corrected immediately within own worktree, later tests above run in correct locations.

## STATUS 2026-09-25T06:18:08.356826+00:00
Public interface plan (contract unchanged): packages/rpc-go module easygo-agent/rpc exports TLSConfig, Authorization, ServerConfig (listen/tls/authorization), NewServer(config, methods), Serve(ctx, server), ClientTLS(identity, peerCertificateFile), strict Decode, Method/Stream and sanitized RPC Error. Gateway package services/ai-gateway/server and workshop package services/workshop/server will export Config, LoadConfig(io.Reader), New(config) returning *http.Server (workshop additionally owns service lifecycle). Commands in each module: go run ./cmd/server --config FILE; listen configured in JSON, defaults :8441/:8443. Example config locations as blueprint. Existing legacy HTTP library tests remain; legacy root commands replaced by mTLS command tests. No peer messages; no push.

## ACK 2026-09-25T06:17:41.482654+00:00
Received message b58c78be-82fd-453c-bcf3-f34f7a3f8651 from rpc-foreman-c926. Read smux and all onboarding go.md. Beginning blueprint and repository inspection; file reports are the acknowledgment channel.

# Worklog go
