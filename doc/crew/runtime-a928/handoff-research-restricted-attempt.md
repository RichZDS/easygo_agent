# Native CLI verification handoff

## 1. Task
ACK `210cae94-dce5-4642-97f9-b5d04215ad22`. Verified target f05376c + c8d53ef through cherry-picks c604557 + 156f89e in research/runtime-providers. Scope only new tests and doc; no implementation, Go module, deployment or global install changes. No push, peer contact or real auth/provider calls. **Native CLI verification remains incomplete due sandbox prerequisites.**

## 2. Facts / reproduced findings
- Actual OpenClaw parser at runtime_events.go:49-88 accepts meta.error and payloads[].isError, sets success=true and emits result. TestNativeOpenClawRejectsFailureOutcomes/meta_error and /error_payload fail, go test exit 1. Fixtures match official EmbeddedPiRunResult type, https://raw.githubusercontent.com/openclaw/openclaw/main/src/agents/pi-embedded-runner/types.ts . Top-level error and meta.aborted cases pass. Current CLI nonzero failure exit protects final task status when honored, but result events are already emitted before that check. No native OpenClaw execution claimed.
- TestNativePiRequiresSettled passes: agent_end alone not success, agent_settled succeeds. Parser fixture, not native Pi.
- TestNativeRuntimeDirectAndResume added using real CommandRunner.Run, direct RuntimeSpec, loopback protocol fixtures, dummy credential, task-scoped state, first turn + resume transcript/UUID checks. It compiles; cannot exercise turns here.
- Full selected-binary attempt exits 1: Codex 0.157.1 / Claude 2.1.281 isolated version probes pass, then loopback socket creation fails operation not permitted. Pi/OpenClaw /tmp candidate binaries absent. npm registry shell DNS fails Errno -3. No installs attempted after unmet network prerequisite.
- Source-only deployment incompatibility: official OpenClaw package main reports 2026.9.6, engines.node >=24.16.0 <25 || >=26.1.0. Host Node 22.23.2 / node:22 Docker do not qualify. Pi source 0.87.1 needs Node >=22.19.0. These are SOURCE candidates, not npm-publisher-verified or native-tested pins.
- git staged diff --check passed (exit 0); test suite is intentionally red for reproduced parser bug. No unrelated full suite run.

## 3. Files / exact reproduction
Cherry-pick ONLY `678495965ef06857aaff0573c20226cf5753e5cc` — not c604557/156f89e, which are foreman's ancestors already.
- services/workshop/workshop/native_runtime_test.go (201 lines).
- doc/runtime-provider-compatibility.md follow-up section.
- /tmp/crew/runtime-a928/native-cli/commands.json records actual args, binary paths, exits. Logs: parser-contracts.log, all-four-attempt.log, native-runner-attempt.log.

From /home/ubuntu/Projects/easygo-runtime-research/services/workshop:
```bash
WORKSHOP_NATIVE_CODEX=/home/ubuntu/.npm-global/bin/codex \
WORKSHOP_NATIVE_CLAUDE=/home/ubuntu/.npm-global/bin/claude \
WORKSHOP_NATIVE_PI=/tmp/crew/runtime-a928/native-cli/pi/node_modules/.bin/pi \
WORKSHOP_NATIVE_OPENCLAW=/tmp/crew/runtime-a928/native-cli/openclaw/node_modules/.bin/openclaw \
/home/ubuntu/sdk/go/bin/go test ./workshop -run '^TestNativeRuntimeDirectAndResume$' -count=1 -v
/home/ubuntu/sdk/go/bin/go test ./workshop -run 'TestNative(OpenClawRejectsFailureOutcomes|PiRequiresSettled)$' -count=1 -v
```
Both actual commands exit 1 for distinct reasons recorded above. The /tmp Pi/OpenClaw paths are not installed. Default unset native variables skip only explicit binary prerequisites; explicitly selected missing binaries/listener denial fail.

## 4. Remaining / owner
Foreman needs to fix parser (tests deliberately not weakened), run native tests in a network/listener-capable environment, inspect npm publisher/repository/dist integrity, choose pinned Pi/OpenClaw and matching Node. Native first/resume, provider schema/config, plugin behavior, tools and filesystem policy still need real execution. This worker cannot complete native proof under current sandbox/never-approval restrictions. Source review found no proven Pi flag/schema failure; do not treat absence of a reproduced defect as native compatibility.

## 5. Pitfalls
Earlier standalone Codex/Claude fixture success is NOT success of new CommandRunner. Parser fixtures are NOT native CLI runs. Do not reuse session across runtime/provider changes. OpenClaw current requirements exceed Node22; don't claim latest supported in existing image. No OS sandbox or paid provider compatibility claim. Worktree clean after commit, no push. Findings first reported in worklog before committing. No UI notification attempted per onboarding (foreman reads file; no delivery claim).
