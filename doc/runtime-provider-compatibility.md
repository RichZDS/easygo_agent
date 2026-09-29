# Runtime / provider compatibility

本文保留分阶段研究记录。最终已实现的接口、四框架实测结果及部署方式以 [运行配置说明](workshop-runtimes.md) 为准；文末补充记录完成后的复核，前文的“未实现/受限”描述是当时状态。

Research date: 2026-09-28. Repository baseline: `dc63aa3`. This is adapter guidance, not a claim that all four runtimes have shipped or passed integration tests.

**Evidence update:** the final [verified runner supplement](#verified-runner-supplement-after-foreman-integration) supersedes the earlier “documented only,” missing binary, blocked fixture and unfixed parser status below. Earlier sections retain the baseline research and initial reproduction history.

## Decision

Keep runtime choice separate from a resolved model route: **protocol + endpoint + provider model ID + credential environment reference + supported parameters**. Reuse the main model route only when the selected runtime understands its protocol. Preserve the three deployment services; native CLIs can execute inside Workshop without adding another public gateway or a generic protocol proxy.

**DeepSeek now documents native Responses support and a Codex integration.** A blanket “DeepSeek cannot run under Codex” rule is incorrect against the current [DeepSeek Codex guide](https://api-docs.deepseek.com/quick_start/agent_integrations/codex/). This is documented support, not a paid DeepSeek test. An arbitrary Chat Completions route still cannot be fed directly to Codex.

## Evidence and compatibility matrix

Evidence levels: **native fixture** = the installed real CLI called an in-process loopback HTTP fixture using dummy credentials; **documented** = primary documentation/source; **unknown** = not tested here. No production, paid provider calls, real auth/config reads, or global installations were used.

| Runtime | Model route / direct DeepSeek | Noninteractive interface | Evidence and release constraint |
| --- | --- | --- | --- |
| Codex | Custom providers require Responses. DeepSeek publishes `https://api.deepseek.com/` with `wire_api="responses"`. | `codex exec --json … -`; resume with `codex exec resume … SESSION_ID -`. | Native fixture passed on **0.157.1**; Workshop Docker pins **0.156.0**, so that image still needs its own proof. Real DeepSeek remains documented only. |
| Claude Code | Anthropic Messages; DeepSeek publishes `https://api.deepseek.com/anthropic`. An OpenAI Chat/Responses endpoint is not interchangeable. | `claude --print --output-format stream-json --verbose …`; prompt on stdin; `--resume SESSION_ID`. | Native fixture passed on **2.1.281**, matching Workshop's Docker pin. Real DeepSeek remains documented only. |
| Original Pi | Custom `models.json` routes include `openai-completions` and Anthropic-compatible APIs; a DeepSeek Chat route is a reasonable candidate. | `pi --mode json …`; RPC exists but is unnecessary for one subprocess per turn. | **Documented/source only**; binary absent. Current upstream package is `@earendil-works/pi-coding-agent`, source package version **0.87.1**, requiring Node **>=22.19.0**. No claim about older `@mariozechner` releases. |
| OpenClaw | Custom provider config supports OpenAI/Anthropic-compatible routes; keep its own embedded runtime explicitly selected. | `openclaw agent --local --json …`; newer docs also offer `agent exec`. | **Documented only**; binary absent and no install/version probe. Pin a release and verify flags/config/output before enabling. `--local` removes the need for a fourth Gateway service. |

Sources: [Codex provider configuration](https://learn.chatgpt.com/docs/config-file/config-advanced), [DeepSeek Claude integration](https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code/), [Pi package](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/package.json), [Pi models](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/docs/models.md), [OpenClaw custom providers](https://docs.openclaw.ai/concepts/model-providers/custom-providers).

## Existing Workshop constraints

- `services/workshop/workshop/types.go`: `Workflow` stores engine/model/policy, but no provider route; `EngineConfig` only supplies a trusted binary and environment allowlist.
- `runner.go`: only Codex/Claude are accepted; process cwd is the task workspace; `HOME`, `CODEX_HOME`, `CLAUDE_CONFIG_DIR` are task-scoped. The allowlist is applied **after** those defaults and can overwrite them; protect reserved path/config variables when expanding provider support.
- `runner.go`: Codex skips user config and rules; Claude uses bare/restricted mode with a small file-tool set. Output parsing requires a native UUID and a terminal success event. Pi/OpenClaw need separate parsers, not the current catch-all Codex branch.
- `workshop_test.go` uses the Go test executable as a synthetic CLI. Those tests prove Workshop orchestration, not native CLI compatibility. The native fixtures below are independent additional evidence.
- `services/ai-gateway/config.example.json` already has full endpoints and protocol values `chat_completions`, `responses`, `anthropic`, `custom`. Its RPC method `gateway.generate` is not an OpenAI/Anthropic HTTP endpoint; pointing a native CLI at the RPC server cannot reuse the route automatically.

## Codex: keep `--ignore-user-config`, pass an explicit route

For a launcher-created task HOME and task CODEX_HOME, with only the selected credential exposed as `TASK_MODEL_API_KEY`, the minimal **documented DeepSeek candidate** is:

```bash
codex exec --json --skip-git-repo-check --ignore-user-config --ignore-rules \
  -c 'approval_policy="never"' -c 'sandbox_mode="read-only"' \
  --model deepseek-flash \
  -c 'model_provider="task"' \
  -c 'model_providers.task.name="Task provider"' \
  -c 'model_providers.task.base_url="https://api.deepseek.com/"' \
  -c 'model_providers.task.env_key="TASK_MODEL_API_KEY"' \
  -c 'model_providers.task.wire_api="responses"' \
  -c 'web_search="disabled"' \
  -c 'model_catalog_json="/absolute/task/codex/models.json"' -
```

Generate the catalog from reviewed provider metadata for the pinned CLI. DeepSeek's guide supplies model metadata, reasoning settings and a catalog; merely changing `--model` can leave fallback metadata. The fixture deliberately used `fixture-model` and received a metadata warning before successful completion. Do not copy DeepSeek's inline bearer-key storage into argv; use Codex's `env_key` facility. Do not run the guide's installation script against operator HOME.

The actual 0.157.1 fixture established:

1. Provider only in task `config.toml` + `--ignore-user-config`: exit **1**, `Model provider fixture not found`, no HTTP request.
2. Same provider passed with `-c model_provider` and `-c model_providers.*`: exit **0**, authenticated `POST /v1/responses`, successful JSONL events. CLI overrides survive the ignore flag.
3. `wire_api="chat"`: exit **1**, explicit unsupported-protocol error, no HTTP request.

Resume repeats the explicit route/policy arguments after `exec resume` and supplies the recorded UUID before `-`. The installed resume help accepts these flags but lacks `--sandbox`; keep `-c sandbox_mode`. Preserve the same task CODEX_HOME and route snapshot. Resume execution itself was not fixture-tested here.

Parse `thread.started.thread_id`, completed `agent_message` items and `turn.completed.usage`; treat `turn.failed`, top-level `error`, malformed JSON, missing session and unsuccessful process exit as failures. A completed item of type `error` may be a metadata warning; the fixture demonstrates that it is not equivalent to terminal `turn.failed`.

Source: [official Codex advanced configuration](https://learn.chatgpt.com/docs/config-file/config-advanced), [DeepSeek's Responses integration and catalog](https://api-docs.deepseek.com/quick_start/agent_integrations/codex/), installed exec/resume help and native fixture logs. Sandbox policy enforcement and tool execution were **not** tested by the no-tool fixture.

## Claude: preserve restricted mode and map the Messages route

Set task HOME and `CLAUDE_CONFIG_DIR`, `ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic`, and expose the selected credential as `ANTHROPIC_AUTH_TOKEN` (DeepSeek's documented recipe). Launch from the workspace with prompt stdin:

```bash
claude --print --output-format stream-json --verbose --bare --restricted \
  --strict-mcp-config --permission-prompts none --permission-mode dontAsk \
  --tools Read,Glob,Grep --model deepseek-flash
```

For the existing file-write policy, change tools to `Read,Glob,Grep,Edit,Write` and mode to `acceptEdits`; add `--resume SESSION_ID` for a subsequent turn. Keep arbitrary CLI args, MCP definitions and settings outside user-submitted configuration.

The installed `--bare` help says authentication is strictly `ANTHROPIC_API_KEY` or an explicit helper. **Actual 2.1.281 behavior is broader:** a token-only fixture and an API-key-only fixture both succeeded under the exact flags above. Token used `Authorization`; API key used `x-api-key`; both called `/v1/messages?beta=true`. Do not turn that help-text discrepancy into an unsupported-auth error. Prefer one selected auth source and test against the real provider before promising service compatibility.

Parse `system/init.session_id`, assistant text blocks and `result`; require `subtype="success"`, `is_error=false`, valid session and process exit 0. Both fixtures produced these. Unknown informational `system` events also occurred and should not be mistaken for terminal success. Usage can include cache-read/cache-creation fields.

Installed restricted-mode help says file tools are confined to working directories and user/project/local settings are ignored; managed settings and explicit `--settings` still apply. Bare mode is not “nothing loaded”: built-in features remain. These are CLI controls, not proof of an OS sandbox. Fresh HOME also prevents ordinary account/session reuse.

Sources: [Claude CLI reference](https://code.claude.com/docs/en/cli-reference), [DeepSeek Claude setup](https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code/), installed help and native fixture logs. Live DeepSeek, tools, boundary enforcement and resume are untested.

## Pi: use its JSON mode and explicit session storage

This is the original Pi coding agent, whose [old repository](https://github.com/badlogic/pi-mono) redirects to [earendil-works/pi](https://github.com/earendil-works/pi). The following is based on source commit `6f7551516b84278eb9da1c340c8e7bc66be1a6ba`, not a locally executed release.

Set task HOME, `PI_CODING_AGENT_DIR=/absolute/task/pi`, `PI_OFFLINE=1`, `PI_TELEMETRY=0`; create a private `models.json`:

```json
{"providers":{"task":{"baseUrl":"https://api.deepseek.com","api":"openai-completions","apiKey":"${TASK_MODEL_API_KEY}","models":[{"id":"deepseek-flash"}]}}}
```

From the task workspace, feed the prompt on stdin:

```bash
pi --mode json --provider task --model deepseek-flash \
  --session-dir /absolute/task/pi/sessions --session-id TASK_UUID \
  --no-approve --no-extensions --no-skills --no-prompt-templates \
  --no-themes --no-context-files --offline --tools read,grep,find,ls
```

Reuse that exact session ID and directory for subsequent turns. The current source accepts `--session-id` and `--no-context-files`; older releases must not inherit this command unverified. Add `edit,write` only for the intended write tool surface; leave shell tools excluded. `--no-tools` supports a tool-free first fixture. `PI_OFFLINE` disables automatic network activity such as catalog updates; it is not an egress firewall or a prohibition on the requested model call.

The documented JSONL starts with `type="session"`, `id`; text and usage are in assistant `message_end.message`. Current Pi distinguishes `agent_end` (a low-level run, possibly retrying) from `agent_settled` (no automatic work left). Require settled completion, a successful final assistant stop reason and exit 0; inspect retry/error events. Do not stop at the first `agent_end`. RPC is a longer-lived bidirectional protocol and lacks the JSON session header, so it would need a different adapter.

**No native filesystem sandbox is claimed.** Pi's security guide states tools run with process permissions and cwd does not confine paths. `--no-approve` suppresses trust-gated project resources, not file access; the docs also identify session-directory lookup before trust, making explicit `--session-dir` necessary. A task HOME/session isolates state, not host resources. Use external isolation if Workshop policy promises a filesystem boundary; otherwise fail closed rather than label a tool allowlist “read-only sandbox.”

Sources: [CLI](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/docs/cli.md), [argument source](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/src/cli/args.ts), [environment](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/docs/environment-variables.md), [JSON events](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/docs/json.md), [security](https://github.com/earendil-works/pi/blob/6f7551516b84278eb9da1c340c8e7bc66be1a6ba/packages/coding-agent/docs/security.md).

## OpenClaw: local embedded execution, not another Gateway

The following is a **documented candidate, not fixture-tested syntax for an installed version**. Official docs snapshot: `openclaw/docs` commit `d8e087070a80b9ab264728c5231cacd2c32ee82e`.

Set task HOME, `OPENCLAW_HOME`, `OPENCLAW_STATE_DIR`, and `OPENCLAW_CONFIG_PATH` to launcher-owned paths. Write a task-only config rather than copying operator state. The minimal provider fragment is:

```json
{
  "agents": {"defaults": {
    "workspace": "/absolute/task/workspace",
    "model": {"primary": "task/deepseek-flash"},
    "models": {"task/deepseek-flash": {"agentRuntime": {"id": "openclaw"}}}
  }},
  "models": {"providers": {"task": {
    "baseUrl": "https://api.deepseek.com", "api": "openai-completions",
    "apiKey": "${TASK_MODEL_API_KEY}",
    "models": [{"id": "deepseek-flash", "name": "Task DeepSeek"}]
  }}},
  "tools": {"allow": ["read"]}
}
```

```bash
openclaw agent --local --agent main --session-id TASK_UUID \
  --model task/deepseek-flash --message-file /absolute/task/prompt.txt \
  --json --timeout 120
```

Keep state and UUID for resume; do not use `--deliver`, channel routing or ambient Gateway state. Verify the pinned version's first-turn explicit session creation and agent selection before integration. Current docs require exclusive ownership of the state directory for local runs, so use a separate state directory per task and serialize turns within it. Treat stdout as a bounded **JSON document**, not Codex-style JSONL; check payload text, `meta.agentMeta` session/usage, result errors and process status against actual version fixtures. A returned session identifier must be validated before binding it to a task.

For write workflows, an explicit `read,write,edit` allowlist is a narrower candidate than the broad `coding` profile. Validate the effective tool set, plugins, bootstrap/context, filesystem enforcement and symlink behavior with the pinned version. Tool policy alone is not OS isolation. Current runtime selection uses **model/provider-scoped** `agentRuntime.id`; whole-agent keys are legacy, and `pi` inside OpenClaw is an old alias for `openclaw`, not the standalone Pi CLI.

Newer docs recommend `openclaw agent exec --message-file - --cwd PATH --config FILE --json` for one-shot automation. Its JSON envelope is different (`ok`, `status`, `final`, `sessionId`, optional `usage`), it defaults to temporary state, and timeout exits 2. `--state-dir` retains state but does not itself establish a continuation contract. `--auth-env-only` skips config and cannot be combined with `--config`, so it cannot simply be added to the custom-provider recipe. Do not substitute this interface for durable `agent --local` sessions without a versioned proof.

Sources: [agent CLI](https://docs.openclaw.ai/cli/agent), [environment](https://docs.openclaw.ai/help/environment), [custom routes](https://docs.openclaw.ai/concepts/model-providers/custom-providers), [runtime policy](https://docs.openclaw.ai/gateway/config-agents/runtime-and-cli-backends), [tool policy](https://docs.openclaw.ai/gateway/config-tools/tool-policy). Model/tool quality and direct DeepSeek execution are unknown here.

## Reusing the main model route

Recommended compatibility mapping, derived from the APIs above:

| Main route protocol | Codex | Claude | Pi / OpenClaw |
| --- | --- | --- | --- |
| `responses` | Custom Responses provider | Reject unless an explicit alternative Messages route exists | Native protocol support must be checked for the pinned adapter/version |
| `anthropic` | Reject unless an explicit alternative Responses route exists | Anthropic base URL and selected credential | `anthropic-messages` custom route, then native fixture |
| `chat_completions` | Reject unless an explicit alternative Responses route exists | Reject unless an explicit alternative Messages route exists | `openai-completions` custom route, then native fixture |
| `custom` mapping | Unsupported without a separately implemented, proven adapter | Same | Same; generic compatibility is not established |

Main gateway endpoints are **complete operation URLs**, whereas CLI provider fields normally want **base URLs**. Convert only recognized route suffixes (`/responses`, `/chat/completions`, `/messages`) with validated scheme/path/query semantics; preserve `/v1` or `/anthropic` prefixes as appropriate. Reject ambiguous/custom URLs rather than silently appending the wrong path. DeepSeek's documented Responses route makes a separately explicit Codex route possible; it does not authorize guessing alternate endpoints for every provider.

Keep credentials in the selected child environment, not API responses, argv, artifacts or user-controlled files. Reusing an environment variable name does not make its value present in the Workshop deployment: credential injection is an explicit deployment concern. Snapshot the effective route with the task and retain native session state; changing runtime/provider mid-session needs a new session or a separately proven migration. No transcript format is portable across these four CLIs.

## Reproduction and handoff evidence

On this host, `/tmp/crew/runtime-a928/research-evidence/` contains:

- `*-help.txt`, `*-version.txt`: isolated HOME probes of Codex/Claude; all help/version commands exited 0. Pi/OpenClaw were not on PATH.
- `probe.py`: standard-library loopback SSE fixture and fresh child HOME/environment launcher. Run with `python3 /tmp/crew/runtime-a928/research-evidence/probe.py`; no real provider credentials required. Overall script exit was 0; per-child statuses below are separately recorded.
- `<probe>.json`: exact argv, environment **names**, HTTP path/model/auth header **names**, child exit, timeout flag and temporary HOME. `<probe>.stdout/.stderr` contain native output. These logs are local handoff artifacts, not part of the doc-only commit.

| Probe | Exit | HTTP / terminal evidence |
| --- | --- | --- |
| `codex-file-ignored` | 1 | No request; unknown provider |
| `codex-cli-provider` | 0 | `/v1/responses`; `thread.started`, agent text `fixture-ok`, `turn.completed` |
| `codex-chat-rejected` | 1 | No request; unsupported `wire_api=chat` |
| `claude-auth-token` | 0 | `/v1/messages?beta=true`; Authorization header; successful result/session |
| `claude-api-key` | 0 | `/v1/messages?beta=true`; x-api-key header; successful result/session |

The fixed fixture usage values and Claude's computed cost fields are synthetic, not measurements of paid consumption. No test exercised real tools, escape prevention, live DeepSeek, model quality, native resume, Docker's Codex pin, Pi or OpenClaw. Those gaps remain explicit acceptance work for the implementing team. Foreman must independently verify reported version/status numbers before user-facing publication.

## Follow-up: initial restricted Workshop adapter validation

Target implementation: `f05376c` + `c8d53ef`, cherry-picked onto this research branch as `c604557` + `156f89e`. New test: `services/workshop/workshop/native_runtime_test.go`. No implementation changes were made.

**Validation is incomplete because this follow-up session cannot open network sockets.** Shell requests to `registry.npmjs.org` failed with DNS error `[Errno -3]`; even `127.0.0.1:0` listener creation failed with `operation not permitted`. Thus Pi/OpenClaw were not installed, and this follow-up has no successful real `CommandRunner.Run` execution for any CLI. The earlier standalone Codex/Claude fixture evidence above is not substituted for runner evidence.

### Reproduced defect, reported before fixes

`runtime_events.go:49` decodes neither `meta.error` nor `payloads[].isError`. The actual parser accepts both of these known failure fields, sets `success=true` and emits a `result` event when a valid UUID and payload array are present. The new `TestNativeOpenClawRejectsFailureOutcomes/meta_error` and `/error_payload` fail against the target implementation. A `retry_limit` metadata error and an error text payload are valid shapes in the [official embedded result types](https://raw.githubusercontent.com/openclaw/openclaw/main/src/agents/pi-embedded-runner/types.ts).

The same test confirms top-level `error` and `meta.aborted=true` are rejected. `TestNativePiRequiresSettled` passes: `agent_end` alone does not finish a run; `agent_settled` does. These are **parser contract fixtures, not native OpenClaw/Pi executions**. Current OpenClaw documentation promises nonzero process status for failure, which `runner.Run` checks after parsing; that protects final task status in that case, but does not correct the parser's premature success event or protect against zero-exit failure envelopes.

### Install/runtime constraint

The [official OpenClaw package source](https://raw.githubusercontent.com/openclaw/openclaw/main/package.json) currently identifies `openclaw` version **2026.9.6** and requires Node **`>=24.16.0 <25 || >=26.1.0`**. Host Node **22.23.2** and the existing `node:22-bookworm-slim` Workshop image do not meet that requirement. This is a source-level deployment incompatibility, not a locally reproduced OpenClaw launch. Pi source **0.87.1** requires Node **>=22.19.0**. These are upstream source candidates, **not npm-publisher-verified or locally tested package pins**; restricted DNS prevented that step. Do not call either version validated for Docker.

### Native test entry point and actual outcomes

`TestNativeRuntimeDirectAndResume` uses the real production `NewCommandRunner` and `Run` with direct `RuntimeSpec` profiles. The test creates loopback Responses / Messages / Chat Completions SSE fixtures, supplies only a dummy selected credential, and asks each runtime for an initial turn and resume. It checks model/auth routing, returned UUID, final text/usage, stable resumed session and inclusion of the first assistant message in the resumed provider request. Fresh task HOME is created by the actual runner. No shell tools or external message channels are requested.

The binary variables are opt-in external prerequisites; unset variables skip only that native case. A selected missing binary, failed version check, or denied listener **fails** instead of skipping. Version probes themselves run with clean task HOME/config directories. The full attempt used these exact paths (the two `/tmp` binary paths do not yet exist):

```bash
cd /home/ubuntu/Projects/easygo-runtime-research/services/workshop
WORKSHOP_NATIVE_CODEX=/home/ubuntu/.npm-global/bin/codex \
WORKSHOP_NATIVE_CLAUDE=/home/ubuntu/.npm-global/bin/claude \
WORKSHOP_NATIVE_PI=/tmp/crew/runtime-a928/native-cli/pi/node_modules/.bin/pi \
WORKSHOP_NATIVE_OPENCLAW=/tmp/crew/runtime-a928/native-cli/openclaw/node_modules/.bin/openclaw \
  /home/ubuntu/sdk/go/bin/go test ./workshop \
  -run '^TestNativeRuntimeDirectAndResume$' -count=1 -v
```

| Check | Actual result |
| --- | --- |
| Codex isolated version | 0.157.1, exit 0; native fixture blocked at listener creation |
| Claude isolated version | 2.1.281, exit 0; native fixture blocked at listener creation |
| Pi/OpenClaw selected `/tmp` binaries | Missing; failed prerequisite, no native run |
| Full native test command | Exit 1; no success or resume claim |
| Parser contract command | Exit 1: two OpenClaw failure cases reproduced; top-level error, aborted and Pi settled cases passed |

Parser reproduction command:

```bash
/home/ubuntu/sdk/go/bin/go test ./workshop \
  -run 'TestNative(OpenClawRejectsFailureOutcomes|PiRequiresSettled)$' -count=1 -v
```

Exact command/status records and logs: `/tmp/crew/runtime-a928/native-cli/commands.json`, `parser-contracts.log`, `all-four-attempt.log`. The added OpenClaw tests intentionally remain red until the implementation owner fixes the parser. Native positive/resume tests compile but their end-to-end fixture behavior remains unverified here. Required next step: run them in an environment permitting npm installation and loopback listeners, verify npm publisher/integrity and pin package+Node versions together. Tool execution, policy enforcement, provider-live compatibility and OS isolation remain untested.

## Verified runner supplement after foreman integration

The foreman subsequently installed temporary runtimes and ran the **actual Workshop runner**. Independently reviewed evidence is `/tmp/crew/runtime-a928/native-real-root-3.log`; it now ends with the complete suite `PASS` and `ok ... 22.122s`, including OpenClaw. Reviewed implementation: `e34e8b0`. This supersedes the initial environment-blocked attempt; the researcher did not rerun network fixtures in the restricted session or change implementation.

| Runtime | Verified binary/version | Initial turn | Native resume |
| --- | --- | --- | --- |
| Codex | `/home/ubuntu/.npm-global/bin/codex`, 0.157.1 | PASS | PASS |
| Claude | `/home/ubuntu/.npm-global/bin/claude`, 2.1.281 | PASS | PASS |
| Pi | `/tmp/easygo-native-runtimes/pi-cli`, 0.87.1 | PASS | PASS |
| OpenClaw | `/tmp/easygo-native-runtimes/openclaw-cli`, 2026.9.6 (`eb377ac`) | PASS | PASS |

For every runtime, the log records `native-fixture-first`, then `native-fixture-resumed` under the same UUID. The test also checks selected credential/model routing and that the resumed provider request retains the first assistant message. This proves local text-turn configuration, parser completion and session continuity through `CommandRunner.Run` against dummy HTTP providers. It does **not** prove live DeepSeek behavior, tool execution, write-policy enforcement or an OS isolation boundary.

Pi/OpenClaw wrappers explicitly execute `/tmp/easygo-native-runtimes/node_modules/.bin/node`. Independent version probes in a fresh isolated HOME exited 0 and confirmed **Node 26.10.0**, **Pi 0.87.1**, **OpenClaw 2026.9.6**. Installed package metadata matches `@earendil-works/pi-coding-agent` / `earendil-works/pi` and `openclaw` / `openclaw/openclaw`. The temporary lockfile records these exact versions, mirrored tarball URLs and integrity hashes; this review did not independently authenticate npm publisher signatures.

At `e34e8b0`, Dockerfile pins all four CLI versions to those in the table and uses `node:26-bookworm-slim`, resolving the earlier Node 22 incompatibility. The image tag pins a Node major line, **not** the tested 26.10.0 patch, and this host-based run is not a Docker image execution test.

Reviewed adapter corrections in that implementation:

- Codex provider overrides are inserted after `exec` or `exec resume`, preserving the native command scope.
- Pi uses an explicit task session directory and UUID instead of the earlier single session-path argument, and sets `PI_TELEMETRY=0`. Its raw event parser avoids decoding user string content as assistant content blocks. Success requires an assistant `stop` result followed by `agent_settled`.
- OpenClaw explicitly receives task `OPENCLAW_HOME`; the tested generated custom provider config and `plugins.enabled=false` complete both turns. Its parser now rejects `meta.error` and `payloads[].isError`. All four failure-outcome cases and the Pi settled contract test report PASS in the same log.
- The native HTTP fixture permits Claude's separate `HEAD /api/hello` liveness probe; authenticated generation requests retain their model/credential checks.

Two output details limit interpretation of the passing run. OpenClaw emitted SQLite maintenance diagnostics with a rejected background maintenance operation, while both turns and the suite passed; the maintenance worker's `exitCode=1` is not the CLI's task exit. Codex's reported usage is **3 input / 2 output** on the initial turn and **6 / 4** on resume, while the other runtimes report **3 / 2** for each turn. These are synthetic fixture values, and the test only requires positive output usage. The Codex observation is consistent with cumulative native usage; normalization into per-run billing deltas is not established by this test and must not be assumed when summing runs.

Reproduction uses the fixed installed wrapper paths:

```bash
WORKSHOP_NATIVE_CODEX=/home/ubuntu/.npm-global/bin/codex \
WORKSHOP_NATIVE_CLAUDE=/home/ubuntu/.npm-global/bin/claude \
WORKSHOP_NATIVE_PI=/tmp/easygo-native-runtimes/pi-cli \
WORKSHOP_NATIVE_OPENCLAW=/tmp/easygo-native-runtimes/openclaw-cli \
  /home/ubuntu/sdk/go/bin/go test ./workshop -run '^TestNative' -count=1 -v
```

Run from `services/workshop` on the integrated implementation with loopback listeners permitted. The review's independent version/status extraction is preserved in `/tmp/crew/runtime-a928/native-cli/foreman-version-confirmation.json` and `foreman-log-confirmation.json`. The latter records the source log SHA-256 and both session/text/usage records per runtime. The initial failing logs remain historical evidence; the current integrated parser defect status is **fixed and passing**.


## P1 Docker shell tools (2026-09-29)

This section adds the Docker execution distinction to the historical host-runner
notes above. Fixed image `easygo-task-runtime:platform` was queried offline on
2026-09-29 with the dedicated daemon, `--network none`, fresh temporary HOME,
read-only root and no credentials. Observed versions: Claude Code 2.1.281,
Pi 0.87.1, OpenClaw 2026.9.6 (`eb377ac`). No model was called.

| Runtime | Docker-only tool policy | Host CommandRunner |
| --- | --- | --- |
| Claude | Append `Bash` to `--tools`, add `--allowedTools Bash`; retain `--bare`, `--restricted`, `--strict-mcp-config` and noninteractive permission handling | Existing file-only tools and policy unchanged |
| Pi | Append `bash` to `--tools`; retain extension/context discovery restrictions | Existing file-only tools unchanged |
| OpenClaw | Append `exec` to `tools.allow`, set `tools.exec={"host":"gateway","mode":"full"}`; keep plugins disabled and the existing file policy | No exec tool or exec policy injected |

The installed Claude help explicitly says `--restricted` removes command tools
**unless `--tools` names them**. Therefore explicit Bash works with restricted
mode; removing it or using bypassPermissions is unnecessary. Its `--bare` help
states built-in features remain available. The explicit Bash allow rule permits
noninteractive shell calls without changing host settings. Pi help lists `bash`
as a built-in command execution tool.

OpenClaw's installed config schema identifies the `exec` tool and accepts
`host=gateway`, `mode=full`. An offline `config validate --json` returned valid,
and `exec-policy show --json` reported effective mode/security `full` and ask
`off`, including when no approval store exists. Here “gateway” means the embedded
CLI process's local execution target **inside the task container**, not the
Workshop controller or a new service.

Both workspace-write and read-only Docker workflows receive shell tools. The
outer container enforces network isolation, UID/resource limits, a read-only root,
and the workflow's workspace mount policy. A read-only workspace remains read-only
regardless of shell availability; its native HOME submount retains the existing
write permission. No MCP or network tools were added. Host mode continues to
exclude shell from Claude/Pi/OpenClaw; only Codex can invoke easygo-crew there,
and only when a model relay supplies the crew environment.

Evidence: `/tmp/crew/acl-530e/workshop/w4-versions.log`, `w4-claude-help.log`,
`w4-pi-help.log`, `w4-openclaw-schema.json`, `w4-openclaw-exec-schema.json`,
`w4-openclaw-validate.log`. Parameter/config tests cover each CLI × policy ×
initial/resumed run through DockerRunner and the host configuration path. These
checks establish supported flags, effective local policy and adapter wiring;
real model tool selection, successful easygo-crew execution by each native CLI,
and native-session tool behavior still require the foreman's model proof.
