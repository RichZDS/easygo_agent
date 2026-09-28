# Research handoff

## 1. Task
Compatibility analysis only, completed in `/home/ubuntu/Projects/easygo-runtime-research`, branch `research/runtime-providers`, baseline `dc63aa3`. Bootstrap ACK: `2b2abe83-323b-4774-bc0e-47826c351adb`. Full onboarding and blueprint read. No app changes, push, production, paid calls, real credentials/config inspection, peer contact or subagents.

## 2. Facts and evidence
- DeepSeek official Codex guide now explicitly states native Responses support: https://api-docs.deepseek.com/quick_start/agent_integrations/codex/ . Do not classify DeepSeek globally as incompatible with Codex. Real DeepSeek was not called.
- Installed Codex 0.157.1; Docker pin 0.156.0. Native fixture proves -c model_providers.* survives --ignore-user-config (exit 0 /v1/responses); file-only provider is ignored (exit 1, no HTTP); wire_api=chat rejects (exit 1).
- Installed Claude 2.1.281 matches Docker pin. BOTH token-only and API-key-only --bare/--restricted fixtures exit 0 and produce successful terminal result/session. Contrary to --bare help wording, ANTHROPIC_AUTH_TOKEN is accepted; preserve DeepSeek's official token recipe.
- Pi and OpenClaw absent on PATH, documented/source only. Pi current source 0.87.1 / @earendil-works/pi-coding-agent; current JSON contract has agent_settled after possibly retrying agent_end. OpenClaw --local can avoid fourth Gateway; agent exec has a different output/session contract.
- Tool selection, task HOME and cwd are not an OS sandbox. Pi explicitly lacks cwd confinement. OpenClaw needs pinned-version proof of effective tools/session/output and filesystem policy.
- Primary refs pinned where practical; local help/version probes all exit 0. Native probe driver exits 0 but each child's actual status separately recorded. Parsed successful UUIDs/terminal events and negative cases with Python assertions, exit 0.
- git diff --cached --check and final commit diff check exit 0. An earlier no-index diff check returned 1 because a new file differs; it emitted no whitespace errors. No app test suite run for doc-only change.

## 3. Files and commit
- Commit: `1676853c51ec75f18b547d6a841cbaad8303f9a2` (one doc-only commit).
- Deliverable: `/home/ubuntu/Projects/easygo-runtime-research/doc/runtime-provider-compatibility.md` (177 lines).
- Evidence: `/tmp/crew/runtime-a928/research-evidence/`: probe.py, named JSON/stdout/stderr logs, isolated CLI help/version, primary source snapshots, sha256.json.
- Worklog: `/tmp/crew/runtime-a928/board/worklog-research.md` with requested ACK at top.
- Worktree clean after commit. Nothing pushed.

## 4. Remaining / ownership
Foreman owns implementation, independent second check of version/status numbers before user publication, integration tests and any publish. Re-run native fixture against Docker's pinned Codex; test real resume, tools/isolation, output failure cases and runtime-specific parsers. Pi/OpenClaw require pinned install + native fixtures before enablement. Live DeepSeek provider behavior and model quality are explicitly unknown; paid calls were excluded. No blocker to handing off this analysis.

## 5. Pitfalls
- No hardcoded Codex/DeepSeek ban; distinguish Responses vs chat-only route.
- Main model full operation URL != CLI base URL; preserve path prefixes/query semantics, reject ambiguous conversion. Gateway RPC is not a compatible model API endpoint.
- Current environment allowlist can overwrite task HOME/CONFIG variables; protect reserved variables. Keep secrets out of argv and artifacts.
- --ignore-user-config drops generated provider config.toml too; use CLI provider overrides.
- Preserve native session state and route snapshots; no generic cross-runtime transcript reuse.
- OpenClaw's current model-scoped agentRuntime.id=openclaw avoids accidental nested Codex/Claude harness selection; global agentRuntime is legacy. --auth-env-only rejects --config.
- Pi has no native path sandbox; tool allowlists alone cannot honestly satisfy sandbox policy.
- No UI notification sent: onboarding explicitly says foreman reads worklog and not to send into possibly blocked UI. There is no delivery_id or claimed notification delivery.
