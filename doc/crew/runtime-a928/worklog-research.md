ACK: ae36c921-dd93-4f1d-a414-cc9855c3b805 — 收到领班实跑结果；核对日志、版本和修复源码，仅更新文档及 handoff。

ACK: 210cae94-dce5-4642-97f9-b5d04215ad22 — native-cli 施工图已完整读取；按 f05376c + c8d53ef 验证实际 CommandRunner，仅加测试/报告，不改实现。

ACK: 2b2abe83-323b-4774-bc0e-47826c351adb — runtime-research-a928 已完整读取 onboard/research.md、board/brief.md、board/tasks/research.md；按指定 worktree 开始兼容性分析。


## Progress
- Worktree: /home/ubuntu/Projects/easygo-runtime-research
- Branch: research/runtime-providers
- Scope: doc only; no push, paid calls, real credentials, peer messages or subagents.

## Compatibility finding — primary documentation (pending foreman second check)
- DeepSeek NOW explicitly documents native Responses API for Codex: https://api-docs.deepseek.com/quick_start/agent_integrations/codex/ . Do NOT hard-reject Codex + DeepSeek as chat-only. Downloaded page says native Responses and supplies custom provider + model catalog. No live API verified.
- Installed Codex 0.157.1 differs from Docker pin 0.156.0. Claude 2.1.281 matches Docker pin. Help probes all exit 0 in clean temp HOME; logs research-evidence/.
- Claude --bare help states ANTHROPIC_API_KEY or explicit apiKeyHelper only, while DeepSeek Claude guide uses ANTHROPIC_AUTH_TOKEN. Testing both against local fixture before recommendation.
- Pi upstream relocated to earendil-works/pi and current package is @earendil-works/pi-coding-agent. Current docs have agent_settled beyond agent_end; pin parser version.

## Native fixture results — pending foreman second check
All processes used allowlisted env + fresh temp HOME, only loopback HTTP fixture and dummy keys. Evidence: /tmp/crew/runtime-a928/research-evidence/probe.py and named JSON/stdout/stderr logs.
- codex-file-ignored exit 1, no HTTP: provider only in config.toml is absent with --ignore-user-config.
- codex-cli-provider exit 0, POST /v1/responses, thread.started + item.completed + turn.completed: -c model_provider and -c model_providers.* DO survive --ignore-user-config.
- codex-chat-rejected exit 1, no HTTP: wire_api=chat rejected by installed 0.157.1.
- CORRECTION to earlier Claude concern: despite --bare help wording, BOTH ANTHROPIC_AUTH_TOKEN and ANTHROPIC_API_KEY worked on actual Claude 2.1.281, exit 0, result success; token sends Authorization, key sends x-api-key. Retain DeepSeek official token recipe, do not add a speculative blocker.
- These fixtures prove local CLI protocol/config and terminal parsing only, not real DeepSeek integration or tool/sandbox safety.

## FINAL — research complete, pending foreman review
- Commit `1676853c51ec75f18b547d6a841cbaad8303f9a2` on `research/runtime-providers`; worktree clean; no push.
- Doc: `/home/ubuntu/Projects/easygo-runtime-research/doc/runtime-provider-compatibility.md`
- Full five-section handoff: `/tmp/crew/runtime-a928/board/handoff-research.md`
- Evidence `/tmp/crew/runtime-a928/research-evidence/`, including reproducible native fixtures and source snapshots/hash manifest.
- Local assertions confirmed fixture exits, successful native session UUIDs and terminal events; git whitespace check passed. No app tests for doc-only change.
- Important conclusion: DeepSeek native Responses is documented now; token-only Claude bare mode works in actual fixture despite help text wording.
- Remaining: foreman second check, pinned Docker/Pi/OpenClaw native validation, live provider/real tool/resume tests. All proof limits explicit in doc.
- No foreman UI message attempted per onboarding; file report is the authorized channel, no delivery claim.

## Native CLI prerequisite blocker (immediate report)
- New managed sandbox denies external DNS: urllib GET registry.npmjs.org for both Pi and OpenClaw failed with URLError [Errno -3] Temporary failure in name resolution. No install performed, no global state modified. Approval policy never; cannot escalate.
- Loopback listener prerequisite: PermissionError(1, 'Operation not permitted')

## REPRODUCED DEFECT — OpenClaw failure parser (report before fixes)
- Baseline cherry-picked as c604557 + 156f89e, corresponding to f05376c + c8d53ef.
- Actual streamParser.parseOpenClaw in runtime_events.go ignores meta.error and payloads[].isError. Both failure shapes become success=true and emit result events when the envelope includes a UUID and payloads array. Top-level error and meta.aborted are correctly rejected.
- Repro: /home/ubuntu/sdk/go/bin/go test ./workshop -run 'TestNative(OpenClawRejectsFailureOutcomes|PiRequiresSettled)$' -count=1 -v from services/workshop, exit 1. meta_error + error_payload FAIL; top_level_error + aborted + Pi settled contract PASS. New test file services/workshop/workshop/native_runtime_test.go lines ~169-190.
- Evidence scope: real parser fed protocol failure fixtures, not actual OpenClaw process (not installed). Current CLI documentation says CLI error exits nonzero, which runner.Run later checks; nevertheless failure JSON is misclassified and success/result emitted before that check, and zero-exit failure envelopes would incorrectly succeed.
- No implementation edits. Full native Run + first/resume harness written using optional absolute WORKSHOP_NATIVE_* binaries; listener remains denied by sandbox, npm DNS unavailable.

## Source-level deployment incompatibility
- Official OpenClaw main package.json reports version 2026.9.6 and engines.node ">=24.16.0 <25 || >=26.1.0" (https://raw.githubusercontent.com/openclaw/openclaw/main/package.json). Host node is 22.23.2 and current Workshop Docker is node:22-bookworm-slim. A current OpenClaw install needs a newer Node runtime; do not pin/install latest under the existing Docker base. Source-level verified, not local package execution.
- Pi reviewed official source 0.87.1 supports Node >=22.19.0. Npm publisher/dist integrity could not be checked through restricted shell DNS; web registry fetch rendered no metadata. These are source candidate versions, NOT npm-installed/tested pins.
- Native test attempt with installed Codex and Claude exited 1 only because loopback socket denied, after isolated version probes passed. Log /tmp/crew/runtime-a928/native-cli/native-runner-attempt.log.

## NATIVE CLI HANDOFF — partial proof, reproducible defects ready
- Only cherry-pick `678495965ef06857aaff0573c20226cf5753e5cc` (new tests/report); own ancestors are foreman's c604557/156f89e cherry-picks. Clean worktree, no push.
- Full five-section report: /tmp/crew/runtime-a928/board/handoff-native-cli.md (also latest handoff-research.md).
- OpenClaw meta.error + payload isError reproduction remains red; parser tests exit 1. Pi settled and supported OpenClaw negative cases pass.
- Full all-four native attempt exit 1: installed Codex/Claude version only passed, listener denied; Pi/OpenClaw binaries uninstalled due network restriction. Exact four paths/logs in report and native-cli/commands.json.
- Native goal not completed: foreman must run on socket/network-enabled environment. No tested npm pins to offer. Source candidates Pi0.87.1 / OpenClaw2026.9.6; latter needs Node>=24.16<25 or >=26.1, incompatible with existing Node22 base.
- Upstream types corroborate both error-field shapes; no implementation changes or silent test weakening. No UI send (file report per onboarding).

## Independent check of foreman native-real-root-3.log
- Log is now complete: all four real CommandRunner first+resume subtests PASS, parser failure cases PASS, Pi settled PASS; suite reports ok 22.122s. Same native UUID retained and expected text across both turns for every runtime. Parsed assertions passed (exit 0); native-cli/foreman-log-confirmation.json records exact fields and log SHA256.
- Independent isolated --version probes exit 0: temporary Node v26.10.0, Pi0.87.1, OpenClaw2026.9.6 (eb377ac). Package names/repository URLs match official upstream; lockfile has pinned versions, mirrored tarball URLs and integrity. No claim of independent publisher signature verification.
- Reviewed e34e8b0 source: Codex provider options now after exec/resume; Pi explicit --session-dir/--session-id + PI_TELEMETRY=0; Pi raw message parser only decodes assistant block content and requires stop+agent_settled; OpenClaw meta.error/isError rejected; OPENCLAW_HOME set. Docker CLI pins match tested versions, Node family26 (not exact26.10.0 image pin).
- OBSERVATION for usage semantics: Codex first output 3 input/2 output, resumed 6/4; the other CLIs return 3/2 on each turn. Fixture reports 3/2 per response. Thus do not describe native usage as uniform per-turn billing or sum Codex resumed usage without defining delta/cumulative semantics. Test currently checks positive usage only. No implementation change made.
- OpenClaw SQLite maintenance diagnostics contain outcome=rejected/exitCode=1 but CLI first+resume and suite PASS. Preserve this distinction, don't label model/runner failed.

## FINAL independent evidence review — native proof accepted at documented scope
- New doc-only commit 2d019a0c03d8534b2077c7f34cf23d7ba229f6a2; only cherry-pick this new commit. Clean worktree; no push.
- Root3 log includes complete OpenClaw first+resume PASS, so all four runtimes pass actual runner fixture+native resume. Parser regressions now pass on e34e8b0.
- Independent version probes all exit0 (Node26.10.0, Pi0.87.1, OpenClaw2026.9.6); exact log fields/UUID checks exit0 with hash recorded.
- Doc final supplement supersedes earlier blocked/unfixed status; historical evidence retained. Handoffs updated, previous restricted-attempt reports preserved.
- Follow-up caveat: Codex resumed usage6/4 vs first3/2 is cumulative-looking; other turns3/2. Don't claim normalized per-run billing from positive usage assertion. SQLite background maintenance warning is separate from task success.
- Proof scope remains dummy-provider text+session, not live DeepSeek/tools/OS isolation/Docker. No UI message sent per onboarding.
