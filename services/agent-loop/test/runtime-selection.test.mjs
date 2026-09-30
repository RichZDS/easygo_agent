import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { certificates, harness, json, response, tool, terminal } from './helpers.mjs';
let pki;
before(() => {
  pki = certificates();
});
after(() => pki.close());
test('caller runtime choice is durable, overrides model preference and participates in idempotency', async () => {
  const h = await harness(pki, {
    gateway: (b, res) =>
      json(
        res,
        b.id,
        b.params.request.messages.some((m) => m.role === 'tool')
          ? response('done')
          : response('', [tool('workshop_submit', { workflow: 'fixture', input: 'x', runtime: 'model-preference' })])
      ),
  });
  try {
    const session = await h.call('agent.session.create');
    const params = { session_id: session.id, input: 'x', idempotency_key: 'once', workshop_runtime: 'caller-choice' };
    const run = await h.call('agent.run.start', params);
    assert.equal(run.workshop_runtime, 'caller-choice');
    assert.equal((await terminal(h, run.id)).status, 'completed');
    assert.equal(h.workshop.calls[0].params.runtime, 'caller-choice');
    assert.equal((await h.call('agent.run.start', params)).id, run.id);
    // eslint-disable-next-line @typescript-eslint/no-unused-vars -- rest destructuring drops the runtime choice
    const { workshop_runtime, ...retryWithoutChoice } = params;
    const repeated = await h.call('agent.run.start', retryWithoutChoice);
    assert.equal(repeated.id, run.id);
    assert.equal(repeated.workshop_runtime, 'caller-choice');
    await assert.rejects(
      h.call('agent.run.start', { ...params, workshop_runtime: 'different' }),
      (e) => e.details.code === -32009 && e.details.data.code === 'idempotency_conflict'
    );
    assert.equal(h.workshop.calls.length, 1);
    for (const invalid of ['', 42, null, {}, 'x'.repeat(129)])
      await assert.rejects(
        h.call('agent.run.start', { ...params, idempotency_key: 'invalid', workshop_runtime: invalid })
      );
  } finally {
    await h.close();
  }
});
test('public runtime catalog forwards trusted namespace over workshop RPC', async () => {
  const catalog = [
    {
      name: 'note',
      runtime: 'pi-main',
      runtimes: [{ id: 'pi-main', engine: 'pi', model: 'chat', protocol: 'chat_completions', source: 'gateway' }],
    },
  ];
  const h = await harness(pki, { workshop: (b, res) => json(res, b.id, catalog) });
  try {
    assert.deepEqual(await h.call('agent.workshop.catalog'), catalog);
    assert.deepEqual(h.workshop.calls[0].params, { namespace: 'demo' });
    await assert.rejects(h.call('agent.workshop.catalog', { namespace: 'forbidden' }));
    assert.equal(h.workshop.calls.length, 1);
  } finally {
    await h.close();
  }
});

test('old SQLite schema migrates and chosen runtime survives restart', async () => {
  const { DatabaseSync } = await import('node:sqlite');
  const { mkdtempSync, rmSync } = await import('node:fs');
  const { tmpdir } = await import('node:os');
  const { join } = await import('node:path');
  const { Store } = await import('../dist/store.js');
  const dir = mkdtempSync(join(tmpdir(), 'runtime-migration-'));
  const path = join(dir, 'agent.sqlite');
  const old = new DatabaseSync(path);
  old.exec(
    'CREATE TABLE runs (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, namespace TEXT NOT NULL, session_id TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, input TEXT NOT NULL, idempotency_key TEXT NOT NULL, result TEXT, error TEXT, UNIQUE(namespace, session_id, idempotency_key))'
  );
  old.close();
  let store;
  try {
    store = new Store(path);
    const session = store.createSession('demo');
    const run = store.start('demo', session.id, 'x', 'once', 'pi-main');
    store.close();
    store = new Store(path);
    const same = store.start('demo', session.id, 'x', 'once');
    assert.equal(same.id, run.id);
    assert.equal(same.workshop_runtime, 'pi-main');
    assert.throws(
      () => store.start('demo', session.id, 'x', 'once', 'codex-main'),
      (e) => e.reason === 'idempotency_conflict'
    );
  } finally {
    store?.close();
    rmSync(dir, { recursive: true, force: true });
  }
});
