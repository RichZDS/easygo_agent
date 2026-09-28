# Platform knowledge and remote terminal

The managed runtime is TypeScript. `cmd/easygo-remote` reuses the existing Bubble Tea transcript and QueueManager interface, but all execution, identity and durable state belong to the authenticated public platform. No Go agent is constructed on this path.

## Integration contract

```ts
import { Knowledge } from './knowledge/index.js';
const knowledge = new Knowledge({ database: '/private/knowledge.db' }, {
  generate: (namespace, messages) => meteredGatewayGenerate(namespace, {
    messages, tool_choice: 'none', max_output_tokens: 4096,
  }),
});
```

The gateway wrapper supplies its configured model, output cap and request timeout. It must use the normal metered gateway and disable tools. Knowledge validates text-only, `finish_reason: stop`, bounded JSON, allowed fields, scores and source IDs. It does not make provider calls itself.

| Hook | Caller obligation |
|---|---|
| `prompt(namespace): Message[]` | Prepend retrieved reference data at each model context construction. |
| `tools(): Tool[]` | Add `list_skills` and `load_skill` to allowed tools. |
| `execute(call: Block, namespace, signal)` | Pass trusted run namespace and cancellation signal. |
| `dispatch(method, params)` | Only authenticated allowlisted memory/skill RPC; platform injects namespace. |
| `recordCompleted(namespace, run_id, messages)` | Drain only a successful core Store transaction's completion outbox, with this run's messages, then acknowledge. Never pass full session history or failed/uncommitted runs. |
| `start()` / `close()` | Start catch-up/scheduler once; await close before process teardown. |

`recordCompleted` synchronously persists a namespace/run ID/digest receipt. Identical retries are no-ops; conflicting replay fails. It retains at most 16,000 text characters per run for extraction, excludes system/tool/attachment content and obvious credential patterns, and clears raw text after a committed consolidation. Receipts remain for deduplication. It is a trusted internal method, not public RPC.

## Memory and skills

Eight kinds are preserved: `agent`, `memory`, `experiment`, `error`, `preference`, `style`, `prompt`, `constraint`. Default profile size is five, configurable from one to twenty. Content is at most 4,000 UTF-8 bytes. Ranking uses importance × confidence, then update time and ID. Profiles contain server IDs, provenance, source run IDs, version and timestamp. Changes and deletion tombstones are recorded in the `revisions` table. Existing entries require `expected_version` for edits/deletes; missing or stale versions conflict.

- `agent.memory.list {}` returns `{memories, checkpoint}`.
- `agent.memory.upsert {kind, content, importance?, confidence?, id?, expected_version?, source_run_ids?}` edits or creates a record. Source IDs must belong to this namespace's completion receipts.
- `agent.memory.delete {id, expected_version}` creates a tombstone.
- `agent.memory.consolidate {}` processes at most twenty pending runs, with extraction and reconciliation model calls.
- `agent.skills.list {}` returns catalog metadata only; SQL projects name/description/version, without loading bodies into JS.
- `agent.skills.get {name}` returns one body, bounded to 64 KiB. Names are alphanumeric/underscore/hyphen, never paths.
- `agent.skills.upsert {name, description, content, expected_version?}` and `.delete {name, expected_version}` maintain versioned entries. At most 100 active skills, descriptions at most 512 bytes.
- `agent.memory.import` and `agent.skills.import` take `{entries, provenance?, dry_run?}`. Default is a full validation preview that rolls back; `dry_run:false` atomically commits all entries. Imports are at most 1 MiB; the Web transport may impose a smaller body cap, so split operator batches accordingly.

All examples omit namespace because the public API supplies it from the login session. Unknown fields are rejected. The two model-facing tools expose only read access to the current user's skills. Bodies enter context only through `load_skill`.

Consolidation persists batch/revision and extracted candidates before reconciliation. Model outputs cannot choose IDs, namespaces or filesystem paths. Every extracted source must be from the actual batch; reconciliation can also refer to existing profile sources. Profile writes, checkpoint advance and job deletion commit together. User edits made while a model call runs cause a snapshot conflict and retry instead of an overwrite. User/import records are retained; background consolidation replaces only model records. Model output cannot resurrect deleted records with the same kind/content.

The scheduler runs catch-up at startup and then at `consolidate_interval_ms` (default 24 hours, minimum one second). Each tick visits at most 100 namespaces sequentially, with a rotating cursor; overlapping ticks are suppressed. Failure persists a constant reason, attempt counter and exponential retry time (2 seconds to one hour). Retry happens at the next eligible tick or explicit consolidation call. Only one active model job per namespace is permitted per Knowledge instance; deploy a single instance per knowledge DB. SQLite transaction/revision checks prevent stale commits across instances, but do not deduplicate their model generation charges. Gateway timeout/output bounds remain necessary.

## Explicit legacy import and export

These operator helpers are never exposed as public filesystem RPC:

```ts
import { previewLegacy } from './knowledge/legacy.js';
import { exportKnowledge } from './knowledge/export.js';
const preview = previewLegacy('/trusted/exported-profile', 'memory');
knowledge.dispatch('agent.memory.import', { namespace, ...preview }); // preview
knowledge.dispatch('agent.memory.import', { namespace, ...preview, dry_run: false });
const snapshot = exportKnowledge(knowledge, namespace);
```

`previewLegacy` reads the old eight Markdown filenames, preserves legacy record IDs and multiline content, and marks imported entries `import:legacy-markdown`. Skill folders use `<name>/SKILL.md` with matching simple one-line `name:` / `description:` frontmatter. Complex YAML/frontmatter or extra skill resources require explicit operator conversion; they are not silently imported. The Linux helper opens directory components through held `/proc/self/fd` handles with `O_NOFOLLOW`, rejects traversal/symlinks and nonregular files, bounds files to 64 KiB and catalog input to 1 MiB. It never writes source data or contacts PostgreSQL.

`exportKnowledge` returns a bounded `easygo-knowledge-v1` snapshot containing full audit metadata plus `import.memory` / `import.skills` preview payloads for a fresh namespace. Exported audit provenance and source references stay in the snapshot. Imported copies receive `import:knowledge-export`; foreign run IDs are deliberately not installed as trusted local completion sources. Existing targets require explicit version-aware conflict resolution. Large skill catalogs can be exported individually via catalog/get calls.

Preserved: eight kinds, bounded ranked profile, reference-data prompt, provenance/revisions, user controls, idempotent completions, extraction/reconciliation checkpoints and lazy skills. Not migrated: live PostgreSQL/Eino session history, old recency/call-count ranking formula, expiry/archive views, legacy daily timezone scheduling, arbitrary skill resources, or all old audit revisions. Current ranking, interval scheduling and explicit export snapshots are documented above.

## Remote terminal

```bash
go run ./cmd/easygo-remote --url http://127.0.0.1:8090 --email user@example.test
# Secure password prompt; no password command-line option.
go run ./cmd/easygo-remote --url http://127.0.0.1:8090 --email user@example.test --sessions
go run ./cmd/easygo-remote --url http://127.0.0.1:8090 --email user@example.test --session SESSION_ID --runtime RUNTIME_NAME
```

`--runtimes` prints the configured remote catalog. `--password-env NAME` reads a caller-supplied environment variable, then unsets it in the process; never put a password value in argv. For noninteractive knowledge access, pass `--rpc agent.memory.list` or another memory/skills method and a JSON params object on stdin (use `--password-env` to keep stdin for JSON). Remote URLs require HTTPS except loopback development; redirects are rejected. The client maintains an in-memory cookie jar, sends Origin on state changes, caps responses to 4 MiB, and refuses caller-provided namespace.

The existing Enter/Tab/queue selection/Delete/Ctrl+C behavior remains. Queued runs restore from `agent.session.history.runs`; the core API caps that to 100, active first, and reports `runs_truncated`. History restores at most ten pages / 2 MiB of text with an explicit truncation notice. `--session` reconnects to the same durable session; polling resumes from event cursors after transient failures, with bounded retry delays. Closing the client/subscription never cancels a durable run. Explicit cancellation uses the server API. Terminal results use committed full text, while stream deltas are provisional. After persistent connection failure or expired authentication, restart/login with the printed session ID. Runtime selection is per client invocation. Memory/skills editing is via the CLI RPC mode rather than new Bubble Tea panels; workshop artifacts/background task panels remain a separate integration surface.

## Verification

```bash
npm run typecheck --prefix services/agent-loop
node --test services/agent-loop/test/knowledge.test.mjs
go test -race ./internal/remotetui ./internal/tui ./cmd/easygo-remote
# After the foreman integrates platform and Store outbox:
node --test services/agent-loop/test/knowledge-platform.test.mjs
```

`knowledge-platform.test.mjs` uses real TS registration/login, namespace injection, core Store/history/outbox, Knowledge and the Go HTTP adapter against temporary local SQLite databases. It uses dummy credentials and no provider calls. In the isolated worker checkout it skips until `dist/platform/server.js` exists; an explicit `EASYGO_KNOWLEDGE_PLATFORM_DIST` may point to a compiled integration snapshot. The Go `TestPlatformIntegration` similarly requires `EASYGO_REMOTE_TEST_URL`, `EASYGO_REMOTE_TEST_EMAIL`, and `EASYGO_REMOTE_TEST_PASSWORD`; otherwise it explicitly skips. This is not evidence of a full metered agent-loop/container execution, production deployment or long-term soak, which remain foreman-owned checks.
