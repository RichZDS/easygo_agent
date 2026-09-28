# Native runtime verification — final evidence review

## 1. Task
ACK ae36c921-dd93-4f1d-a414-cc9855c3b805. Reviewed foreman real-run evidence and current source e34e8b0; only updated doc and handoff. No implementation or tests modified this turn. Worker branch remains on its prior implementation ancestors; e34e8b0 was read through git show, not cherry-picked. No push, installs, paid calls, credential reads or UI messages.

## 2. Facts / independent checks
- /tmp/crew/runtime-a928/native-real-root-3.log is now complete: Codex, Claude, Pi AND OpenClaw all first+resume PASS; OpenClaw 4 negative parser cases PASS; Pi settled test PASS; whole suite reports ok 22.122s.
- Independently parsed every first/resume pair: exact expected text and stable UUID; Python assertions exit 0. Log SHA256 8c98e065488e9195c0829ca3b5be98cc0b561a0791660229f84acf21a81523ca.
- Independent isolated --version probes exit 0: Node26.10.0, Pi0.87.1, OpenClaw2026.9.6 (eb377ac). Log confirms Codex0.157.1 and Claude2.1.281. No network fixture rerun claimed in this restricted worker.
- Package metadata identities/repositories match official Pi/OpenClaw upstream. Lockfile exact versions/integrities recorded; resolved URLs are a mirror. No independent publisher-signature authentication claimed.
- Source e34e8b0 confirms Codex argv scope fix; Pi --session-dir/--session-id, telemetry off, role-aware raw content parse and stop+settled success; OpenClaw task HOME and meta.error/isError rejection. Foreman adjusted fixture for Claude HEAD /api/hello liveness request.
- Dockerfile now Node26 family with Codex0.157.1/Claude2.1.281/Pi0.87.1/OpenClaw2026.9.6 pins, matching tested CLIs. Node image is major-tagged, not exact26.10.0; no Docker-run proof in this log.
- Codex resume usage is 6 input/4 output vs first3/2; all other runtime turns3/2. Synthetic numbers and positive-usage test do not establish a uniform per-run billing contract. Avoid summing cumulative native usage as deltas.
- OpenClaw SQLite maintenance diagnostics report rejected background worker work but both CLI turns and suite PASS; do not equate maintenance exitCode1 to CLI task failure.

## 3. Files / commit
Cherry-pick ONLY new doc commit 2d019a0c03d8534b2077c7f34cf23d7ba229f6a2.
- doc/runtime-provider-compatibility.md: leading evidence update, initial-attempt history label and final verified runner supplement.
- /tmp/crew/runtime-a928/native-cli/foreman-version-confirmation.json
- /tmp/crew/runtime-a928/native-cli/foreman-log-confirmation.json
- /tmp/crew/runtime-a928/board/handoff-native-cli.md and handoff-research.md now contain this final evidence review.
- git diff/staged diff whitespace checks exit0; doc-only commit, clean worktree.

## 4. Remaining / scope
Local text fixture and native session continuity are proven by foreman's actual runner run and independently reviewed here. The earlier parser defect is fixed, tests passing. Remaining outside this proof: live DeepSeek, actual tool execution, write policy/OS sandbox enforcement, Docker execution and normalized per-run token accounting. Foreman retains publication ownership.

## 5. Pitfalls
Use final doc supplement as current conclusion; initial blocked/red logs are historical. Don't describe background SQLite diagnostics as failed task. Don't claim independent live execution by researcher; actual socket-enabled run was foreman's. Don't treat all protocol/runtime combinations as tested: Codex Responses, Claude Messages, Pi/OpenClaw Chat Completions were exercised. Don't combine old worker implementation commits with foreman's integrated branch; cherry-pick only new documentation commit.
