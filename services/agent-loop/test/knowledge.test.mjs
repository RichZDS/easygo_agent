import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, mkdirSync, writeFileSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { Knowledge, KINDS } from '../dist/knowledge/index.js';
import { previewLegacy } from '../dist/knowledge/legacy.js';
const messages = (s = 'I prefer concise answers') => [
  { role: 'user', content: [{ type: 'text', text: s }] },
  { role: 'assistant', content: [{ type: 'text', text: 'Understood' }] },
];
const entry = (source = 'run-a', content = 'Prefers concise answers') => ({
  kind: 'preference',
  content,
  importance: 0.8,
  confidence: 0.9,
  source_run_ids: [source],
});
const response = (entries) => ({
  model: 'fixture',
  message: { role: 'assistant', content: [{ type: 'text', text: JSON.stringify({ memories: entries }) }] },
  finish_reason: 'stop',
  usage: { known: true, input_tokens: 1, output_tokens: 1 },
  cost: { known: true, amount: 0 },
});
function setup(t, generate = async () => response([entry()]), cfg = {}) {
  const root = mkdtempSync(join(tmpdir(), 'knowledge-'));
  const database = join(root, 'knowledge.db');
  const k = new Knowledge({ database, ...cfg }, { generate });
  t.after(async () => {
    await k.close();
    rmSync(root, { recursive: true, force: true });
  });
  return { k, root, database };
}
const rpc = (k, method, namespace = 'alice', p = {}) => k.dispatch('agent.' + method, { namespace, ...p });
function clearBackoff(database) {
  const db = new DatabaseSync(database);
  db.exec('UPDATE jobs SET next_at=0');
  db.close();
}

test('eight memory kinds, namespace isolation, bounded ranking and versioned user edit/delete', async (t) => {
  const { k } = setup(t, undefined, { profile_limit: 8 });
  assert.equal(KINDS.length, 8);
  for (const kind of KINDS)
    rpc(k, 'memory.upsert', 'alice', { kind, content: 'own ' + kind, importance: kind === 'constraint' ? 1 : 0.1 });
  assert.equal(rpc(k, 'memory.list').memories[0].kind, 'constraint');
  assert.equal(rpc(k, 'memory.list', 'bob').memories.length, 0);
  assert.throws(() => rpc(k, 'memory.upsert', 'alice', { kind: 'memory', content: 'overflow' }), /Request failed/);
  const m = rpc(k, 'memory.list').memories[0];
  assert.throws(
    () => rpc(k, 'memory.upsert', 'alice', { id: m.id, kind: m.kind, content: 'overwrite' }),
    (e) => e.reason === 'expected_version_required'
  );
  const edit = rpc(k, 'memory.upsert', 'alice', {
    id: m.id,
    kind: m.kind,
    content: 'edited',
    expected_version: m.version,
  });
  assert.equal(edit.version, 2);
  assert.throws(
    () => rpc(k, 'memory.delete', 'bob', { id: m.id, expected_version: 2 }),
    (e) => e.reason === 'entry_not_found'
  );
  rpc(k, 'memory.delete', 'alice', { id: m.id, expected_version: 2 });
  assert(!JSON.stringify(k.prompt('alice')).includes('edited'));
  assert.throws(() => rpc(k, 'memory.upsert', 'alice', { kind: 'invalid', content: 'bad' }));
  assert.throws(
    () => rpc(k, 'memory.upsert', 'alice', { kind: 'memory', content: 'password=do-not-store' }),
    (e) => e.reason === 'sensitive_memory'
  );
});

test('skill catalog is lazy and namespaced; paths rejected and imports preview/rollback', async (t) => {
  const { k } = setup(t);
  const imported = rpc(k, 'skills.import', 'alice', {
    entries: [{ name: 'review', description: 'Review a change', content: 'PRIVATE BODY' }],
  });
  assert.equal(imported.dry_run, true);
  assert.equal(rpc(k, 'skills.list').skills.length, 0);
  rpc(k, 'skills.import', 'alice', {
    dry_run: false,
    entries: [{ name: 'review', description: 'Review a change', content: 'PRIVATE BODY' }],
  });
  const p = JSON.stringify(k.prompt('alice'));
  assert(p.includes('Review a change'));
  assert(!p.includes('PRIVATE BODY'));
  assert.equal(k.prompt('bob').length, 0);
  const signal = new AbortController().signal;
  assert.equal(
    (await k.execute({ name: 'load_skill', arguments: { name: 'review' } }, 'alice', signal)).content,
    'PRIVATE BODY'
  );
  await assert.rejects(k.execute({ name: 'load_skill', arguments: { name: 'review' } }, 'bob', signal));
  await assert.rejects(k.execute({ name: 'load_skill', arguments: { name: '../review' } }, 'alice', signal));
  await assert.rejects(
    k.execute({ name: 'load_skill', arguments: { name: 'review', namespace: 'bob' } }, 'alice', signal)
  );
  assert.throws(() =>
    rpc(k, 'skills.import', 'alice', {
      dry_run: false,
      entries: [
        { name: 'good', description: 'ok', content: 'ok' },
        { name: '../bad', description: 'bad', content: 'bad' },
      ],
    })
  );
  assert.equal(rpc(k, 'skills.list').skills.length, 1);
  assert.throws(() =>
    rpc(k, 'skills.upsert', 'alice', { name: 'large', description: 'big', content: 'x'.repeat(65537) })
  );
  rpc(k, 'skills.delete', 'alice', { name: 'review', expected_version: 1 });
  assert.equal(rpc(k, 'skills.list').skills.length, 0);
});

test('completion ingestion is durable/idempotent and extraction is scoped to committed namespace', async (t) => {
  const seen = [];
  const { k, database } = setup(t, async (ns, prompt) => {
    seen.push({ ns, prompt });
    return response([entry()]);
  });
  k.recordCompleted('alice', 'run-a', messages());
  k.recordCompleted('alice', 'run-a', messages());
  k.recordCompleted('bob', 'run-b', messages('Never show Bob history to Alice'));
  assert.throws(
    () => k.recordCompleted('alice', 'run-a', messages('changed')),
    (e) => e.reason === 'completion_conflict'
  );
  const done = await rpc(k, 'memory.consolidate');
  assert.equal(done.processed, 1);
  assert.equal(seen.length, 2);
  assert(seen.every((s) => s.ns === 'alice' && !JSON.stringify(s.prompt).includes('Bob')));
  assert.equal(rpc(k, 'memory.list').memories[0].provenance, 'model');
  assert.equal(rpc(k, 'memory.list', 'bob').memories.length, 0);
  assert.equal((await rpc(k, 'memory.consolidate')).processed, 0);
  const db = new DatabaseSync(database);
  assert.equal(db.prepare('SELECT count(*) n FROM completed WHERE ns=?').get('alice').n, 1);
  assert.equal(db.prepare('SELECT messages FROM completed WHERE ns=?').get('alice').messages, null);
  db.close();
});

test('durable extraction stage survives failed reconciliation and process restart', async (t) => {
  let calls = 0;
  const { k, database } = setup(t, async () => {
    calls++;
    if (calls === 2) throw new Error('fixture failure');
    return response([entry()]);
  });
  k.recordCompleted('alice', 'run-a', messages());
  await assert.rejects(rpc(k, 'memory.consolidate'));
  assert.equal(rpc(k, 'memory.list').checkpoint.through, 0);
  await k.close();
  clearBackoff(database);
  let restartedCalls = 0;
  const restarted = new Knowledge(
    { database },
    {
      generate: async () => {
        restartedCalls++;
        return response([entry()]);
      },
    }
  );
  t.after(() => restarted.close());
  const result = await rpc(restarted, 'memory.consolidate');
  assert.equal(result.processed, 1);
  assert.equal(restartedCalls, 1);
  restarted.recordCompleted('alice', 'run-a', messages());
  assert.equal((await rpc(restarted, 'memory.consolidate')).processed, 0);
  await restarted.close();
});

test('rejects forged sources, authority fields, duplicate JSON keys, unsafe scores, and tool output', async (t) => {
  for (const bad of [
    response([entry('other-users-run')]),
    response([{ ...entry(), namespace: 'bob' }]),
    response([{ ...entry(), importance: 2 }]),
    { ...response([]), message: { role: 'assistant', content: [{ type: 'tool_call', name: 'anything' }] } },
    {
      ...response([]),
      message: { role: 'assistant', content: [{ type: 'text', text: '{"memories":[],"memories":[]}' }] },
    },
  ]) {
    const { k } = setup(t, async () => bad);
    k.recordCompleted('alice', 'run-a', messages());
    await assert.rejects(rpc(k, 'memory.consolidate'));
    assert.equal(rpc(k, 'memory.list').checkpoint.through, 0);
    assert.equal(rpc(k, 'memory.list').memories.length, 0);
  }
});

test('concurrent consolidation shares job; user edit during generation invalidates snapshot', async (t) => {
  let release;
  let entered;
  const ready = new Promise((r) => (entered = r));
  let calls = 0;
  const { k, database } = setup(t, async () => {
    calls++;
    if (calls === 1) {
      entered();
      await new Promise((r) => (release = r));
    }
    return response([entry()]);
  });
  k.recordCompleted('alice', 'run-a', messages());
  const a = rpc(k, 'memory.consolidate');
  const b = rpc(k, 'memory.consolidate');
  assert.equal(a, b);
  await ready;
  rpc(k, 'memory.upsert', 'alice', { kind: 'constraint', content: 'Keep my explicit edit' });
  release();
  await assert.rejects(a, (e) => e.reason === 'profile_changed');
  assert.equal(rpc(k, 'memory.list').checkpoint.through, 0);
  clearBackoff(database);
  await rpc(k, 'memory.consolidate');
  assert(rpc(k, 'memory.list').memories.some((m) => m.content === 'Keep my explicit edit'));
});

test('deleted model memories do not resurrect and source namespace is validated on user edits', async (t) => {
  let source = 'run-a';
  const { k } = setup(t, async () => response([entry(source)]));
  k.recordCompleted('alice', 'run-a', messages());
  await rpc(k, 'memory.consolidate');
  const m = rpc(k, 'memory.list').memories[0];
  rpc(k, 'memory.delete', 'alice', { id: m.id, expected_version: m.version });
  source = 'run-c';
  k.recordCompleted('alice', 'run-c', messages());
  await rpc(k, 'memory.consolidate');
  assert.equal(rpc(k, 'memory.list').memories.length, 0);
  k.recordCompleted('bob', 'bob-run', messages());
  assert.throws(
    () => rpc(k, 'memory.upsert', 'alice', { ...entry('bob-run') }),
    (e) => e.reason === 'invalid_sources'
  );
});

test('scheduled consolidation retries a persisted pending completion', async (t) => {
  const { k } = setup(t, async () => response([entry()]), { consolidate_interval_ms: 1000 });
  k.recordCompleted('alice', 'run-a', messages());
  k.start();
  k.start();
  const deadline = Date.now() + 5000;
  while (!rpc(k, 'memory.list').checkpoint.through && Date.now() < deadline)
    await new Promise((r) => setTimeout(r, 50));
  assert(rpc(k, 'memory.list').checkpoint.through > 0);
});

test('trusted legacy preview rejects traversal and symlinks; imports exported markdown and simple skill frontmatter', async (t) => {
  const { k, root } = setup(t);
  const profiles = join(root, 'profiles');
  mkdirSync(profiles);
  writeFileSync(join(profiles, 'Preferences.md'), '# User long-term memory\n\n- [legacy-id] Prefers short answers\n');
  const preview = previewLegacy(profiles, 'memory');
  rpc(k, 'memory.import', 'alice', preview);
  assert.equal(rpc(k, 'memory.list').memories.length, 0);
  rpc(k, 'memory.import', 'alice', { ...preview, dry_run: false });
  assert.equal(rpc(k, 'memory.list').memories[0].provenance, 'import:legacy-markdown');
  assert.throws(() => previewLegacy(profiles + '/../profiles', 'memory'));
  const outside = join(root, 'outside.md');
  writeFileSync(outside, 'private');
  symlinkSync(outside, join(profiles, 'Agent.md'));
  assert.throws(() => previewLegacy(profiles, 'memory'));
  const skills = join(root, 'skills');
  mkdirSync(join(skills, 'review'), { recursive: true });
  writeFileSync(join(skills, 'review', 'SKILL.md'), '---\nname: review\ndescription: Review changes\n---\nBody');
  assert.equal(previewLegacy(skills, 'skills').entries.length, 1);
  symlinkSync(profiles, join(skills, 'evil'));
  assert.throws(() => previewLegacy(skills, 'skills'));
  symlinkSync(skills, join(root, 'linked'));
  assert.throws(() => previewLegacy(join(root, 'linked'), 'skills'));
});

test('explicit export retains audit provenance and offers bounded preview for fresh namespace', async (t) => {
  const { exportKnowledge } = await import('../dist/knowledge/export.js');
  const { k } = setup(t);
  k.recordCompleted('alice', 'run-a', messages());
  await rpc(k, 'memory.consolidate');
  rpc(k, 'skills.upsert', 'alice', { name: 'review', description: 'Review', content: 'Body' });
  const snapshot = exportKnowledge(k, 'alice');
  assert.equal(snapshot.memories[0].provenance, 'model');
  assert.deepEqual(snapshot.memories[0].source_run_ids, ['run-a']);
  rpc(k, 'memory.import', 'bob', snapshot.import.memory);
  assert.equal(rpc(k, 'memory.list', 'bob').memories.length, 0);
  rpc(k, 'memory.import', 'bob', { ...snapshot.import.memory, dry_run: false });
  rpc(k, 'skills.import', 'bob', { ...snapshot.import.skills, dry_run: false });
  assert.equal(rpc(k, 'memory.list', 'bob').memories[0].provenance, 'import:knowledge-export');
  assert.deepEqual(rpc(k, 'memory.list', 'bob').memories[0].source_run_ids, []);
  assert.equal(rpc(k, 'skills.get', 'bob', { name: 'review' }).content, 'Body');
});
