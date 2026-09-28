# Foreman acceptance · 2026-09-28
Baseline dc63aa3. Final branch feat/workshop-agent-runtimes; changes will be squashed into one commit, merged main and published.

Research delivered compatibility doc and native tests. Validation delivered native gateway/relay test files via patch because its Git metadata became read-only; foreman copied exact files then corrected the documented test fixtures. Source review target e34e8b0 is preserved in historical report, not claimed to be final source.

Resolved review findings:
- Native invalid/truncated/provider-error body is no longer returned as success. Independent frame validation preserves native extensions while requiring transport completion.
- Base64 envelope decoded as string then validated bytes, instead of strict []byte shape rejection.
- OpenClaw meta.error and error payloads rejected; null error permitted.
- Successful Responses error:null accepted in JSON and SSE. Original memory-transport repro now green; positive regressions cover both.
- TS explicit-selected run then omitted-choice retry returns original selection; explicit different choice conflicts. Tests include old SQLite schema upgrade and restart persistence.
- Native Codex -c overrides positioned after exec/resume; task CODEX_HOME created. Pi uses exact session-id and directory; user message string content is not parsed as assistant blocks.

Test adjustments approved by independent reviewer: Claude HEAD /api/hello is a separate health probe and excluded from generation assertion; unknown-usage body is valid completed Responses custom tool, not arbitrary malformed object. The large-response relay fixture timeout was raised from4s to20s after race instrumentation plus parallel compilation exhausted4s; all byte-bound/status assertions unchanged.

Foreman execution: four real CLIs first+resume and same-session/history assertions passed with dummy local provider; native failure parsers passed. Native-root3 initially shows Codex/Claude/Pi/OpenClaw versions 0.157.1/2.1.281/0.87.1/2026.9.6; research independently checked log/session/version facts. Node26.10.0 installed only under /tmp, global tools unchanged.
During an earlier failed Codex configuration probe, misplaced -c options caused attempted OpenAI WebSocket connections with dummy authentication, returning401; no real secret or paid call. Corrected final probe routes to local fixture and verifies requests. Preserve failed logs instead of hiding them.

Go root, gateway, workshop race/vet checks pass (see final logs); TS77 tests pass with no skips. Default native CLI tests explicitly skip missing external binaries, but separate all-four execution provided every binary and passed. Operator client and Compose config validation pass. Three real service processes exercise selected Pi-shape child fixture -> loopback capability -> pinned mTLS -> actual model gateway -> local provider, verifies model forced, credential separation, persistent choice, existing auth/idempotency/cancel/crash recovery. Evidence /tmp/easygo-rpc-e2e-K06BQQ.

No Docker Engine, Docker image/container execution, real DeepSeek model-quality/tool validation or per-task OS sandbox proof. OpenClaw maintenance diagnostics occurred while tasks/sessions passed. Native CLI usage may be cumulative (Codex resume); gateway per-request observations are authoritative for billing. Deployment docs make these limits explicit.
