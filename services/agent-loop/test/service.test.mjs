import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { startServer, parseConfig } from '../dist/server.js';
import { RpcClient } from '../dist/rpc.js';
import { certificates, harness, response, tool, json, sse, deferred, until, start, terminal, raw } from './helpers.mjs';

let pki;
before(() => {
  pki = certificates();
});
after(() => pki.close());
const envelope = (method, params = {}, id = 'test') => ({
  jsonrpc: '2.0',
  id,
  method,
  params: { namespace: 'demo', ...params },
});

const packDir = new URL('../../../packs/base', import.meta.url).pathname;
test(
  'pack assistant instructions reach model requests and missing roles reject startup',
  { skip: !existsSync(packDir) && 'needs the repository packs/base' },
  async () => {
    const h = await harness(pki, {
      config: { pack_dir: packDir },
      gateway: (b, res) => {
        assert.match(b.params.request.messages[0].content[0].text, /outcome and platform acceptance/);
        assert.equal(
          b.params.request.tools.some((t) => t.name === 'calculator'),
          false
        );
        json(res, b.id, response());
      },
    });
    try {
      const run = await start(h);
      assert.equal((await terminal(h, run.id)).status, 'completed');
      await assert.rejects(
        startServer({ ...h.config, database: join(h.dir, 'missing-role.sqlite'), pack_dir: join(h.dir, 'absent') }),
        /ENOENT/
      );
    } finally {
      await h.close();
    }
  }
);

test('harness closes its gateway and workshop fixtures when startServer fails', async () => {
  // Closed servers leave the active list a tick after close() calls back, and no test here
  // keeps one open, so the count must settle at zero both before and after.
  const listening = () => process.getActiveResourcesInfo().filter((type) => type === 'TCPServerWrap').length;
  await until(() => listening() === 0, 2000);
  await assert.rejects(harness(pki, { config: { pack_dir: join(pki.dir, 'absent') } }), /ENOENT/);
  await until(() => listening() === 0, 2000);
});

test('public mTLS surface: cert trust/pins, exact methods/namespaces, health and strict envelopes', async () => {
  const h = await harness(pki);
  try {
    assert.equal((await raw(pki, h.endpoint, undefined, 'client', '/healthz')).status, 200);
    assert.equal((await raw(pki, h.endpoint, undefined, 'loop', '/healthz')).status, 200);
    assert.equal((await raw(pki, h.endpoint, undefined, 'limited', '/healthz')).status, 403);
    for (const identity of [null, 'rogue'])
      await assert.rejects(raw(pki, h.endpoint, envelope('agent.session.create'), identity));
    for (const [identity, method, params] of [
      ['stranger', 'agent.session.create', {}],
      ['loop', 'agent.session.create', {}],
      ['limited', 'agent.session.create', {}],
      ['limited', 'agent.session.list', { namespace: 'other' }],
      ['client', 'agent.session.create', { namespace: 'unauthorized' }],
    ]) {
      const out = await raw(pki, h.endpoint, envelope(method, params), identity);
      assert.equal(out.status, 403);
      assert.equal(JSON.parse(out.body).error.code, -32003);
    }
    const badPin = new RpcClient(pki.tls('client'), { ...h.endpoint, peer_certificate_file: pki.cert('gateway') });
    await assert.rejects(badPin.call('agent.session.list', { namespace: 'demo' }), /certificate mismatch/);
    badPin.close();
    for (const value of [
      [],
      { ...envelope('agent.session.create'), id: 3 },
      { ...envelope('agent.session.create'), id: '' },
      { ...envelope('agent.session.create'), extra: true },
      { ...envelope('agent.session.create'), params: [] },
      { ...envelope('agent.session.create'), id: '片'.repeat(50) },
    ]) {
      const out = await raw(pki, h.endpoint, value);
      assert.equal(JSON.parse(out.body).error.code, -32600);
    }
    assert.equal(JSON.parse((await raw(pki, h.endpoint, '{bad')).body).error.code, -32700);
    assert.equal(
      JSON.parse((await raw(pki, h.endpoint, envelope('agent.session.create', { secret: 'x' }))).body).error.code,
      -32602
    );
    assert.equal(
      JSON.parse((await raw(pki, h.endpoint, envelope('agent.session.create', { namespace: 'bad space' }))).body).error
        .code,
      -32602
    );
    assert.equal((await raw(pki, h.endpoint, {}, 'client', '/v1/chat')).status, 404);
    assert.equal((await h.call('agent.session.list')).sessions.length, 0);
    assert.throws(() => parseConfig({ ...h.config, token: 'not-allowed' }));
  } finally {
    await h.close();
  }
});

for (const streaming of [false, true])
  test(`real gateway ${streaming ? 'SSE' : 'nonstream'} tool loop preserves provider state, usage and workshop scope/key`, async () => {
    const opaque = { protocol: 'fixture', value: { signed: ['opaque', 1], nested: { x: true } } };
    const h = await harness(pki, {
      config: { streaming },
      gateway: async (b, res) => {
        const messages = b.params.request.messages;
        assert.equal(b.params.stream, streaming);
        const reply = messages.some((m) => m.role === 'tool')
          ? response('final')
          : response('', [
              { type: 'reasoning', text: 'reasoning', provider_state: opaque },
              tool('workshop_get', {}, 'local-error'),
              tool('workshop_submit', { workflow: 'fixture', input: 'work' }, 'submit'),
            ]);
        if (messages.some((m) => m.role === 'tool')) {
          assert.deepEqual(messages.find((m) => m.role === 'assistant').content[0].provider_state, opaque);
          assert.deepEqual(JSON.parse(messages.find((m) => m.role === 'tool').content[0].text), {
            code: 'invalid_string',
          });
        }
        if (streaming) await sse(res, b.id, reply);
        else json(res, b.id, reply);
      },
    });
    try {
      const run = await start(h);
      const done = await terminal(h, run.id);
      assert.equal(done.status, 'completed');
      assert.equal(done.result.message.content[0].text, 'final');
      assert.equal(h.gateway.calls.length, 2);
      assert.equal(h.workshop.calls.length, 1);
      assert.deepEqual(h.workshop.calls[0].params, {
        namespace: 'demo',
        workflow: 'fixture',
        input: 'work',
        idempotency_key: `${run.id}:submit`,
      });
      const history = await h.call('agent.session.history', { session_id: run.session_id });
      assert.deepEqual(
        history.messages.map((m) => m.role),
        ['user', 'assistant', 'tool', 'tool', 'assistant']
      );
      assert.deepEqual(history.messages[1].content[0].provider_state, opaque);
      assert.equal(history.messages[1].metadata.usage.cache_read_tokens, 2);
      assert.equal(history.messages[4].metadata.cost.amount, 0.001);
      const events = await h.call('agent.run.events', { run_id: run.id });
      assert.equal(events.events.filter((e) => e.kind === 'terminal').length, 1);
      assert.equal(
        events.events.some((e) => e.kind === 'delta' && e.data.provisional),
        streaming
      );
    } finally {
      await h.close();
    }
  });

test('idempotency, pagination and namespace isolation apply before any data access', async () => {
  const h = await harness(pki);
  try {
    const run = await start(h);
    const same = await start(h, 'hello', 'key', run.session_id);
    assert.equal(run.id, same.id);
    await assert.rejects(start(h, 'changed', 'key', run.session_id), (e) => e.details.code === -32009);
    for (const [method, params] of [
      ['agent.run.get', { run_id: run.id }],
      ['agent.run.cancel', { run_id: run.id }],
      ['agent.run.events', { run_id: run.id }],
      ['agent.session.history', { session_id: run.session_id }],
      ['agent.run.start', { session_id: run.session_id, input: 'x', idempotency_key: 'x' }],
    ]) {
      await assert.rejects(h.call(method, { ...params, namespace: 'other' }), (e) => e.details.code === -32004);
    }
    assert.equal((await terminal(h, run.id)).status, 'completed');
    await h.call('agent.session.create');
    const page = await h.call('agent.session.list', { limit: 1 });
    assert.equal(page.sessions.length, 1);
    assert.equal(page.next_offset, 1);
    const history = await h.call('agent.session.history', { session_id: run.session_id, limit: 1 });
    assert.equal(history.messages.length, 1);
    assert.ok(history.next_after);
    const next = await h.call('agent.session.history', { session_id: run.session_id, after: history.next_after });
    assert.equal(next.messages[0].role, 'assistant');
    for (const limit of [-1, 1.5, 1001])
      await assert.rejects(h.call('agent.session.history', { session_id: run.session_id, limit }));
  } finally {
    await h.close();
  }
});

test('per-session FIFO and bounded concurrency across sessions', async () => {
  const gate = deferred();
  let active = 0,
    peak = 0;
  const entered = [];
  const h = await harness(pki, {
    config: { concurrency: 2 },
    gateway: async (b, res) => {
      active++;
      peak = Math.max(peak, active);
      entered.push(b.params.request.messages.filter((m) => m.role === 'user').at(-1).content[0].text);
      await gate.promise;
      active--;
      json(res, b.id, response());
    },
  });
  try {
    const a = await start(h, 'A1');
    await until(() => entered.length === 1);
    const a2 = await start(h, 'A2', 'second', a.session_id);
    const b = await start(h, 'B1');
    const c = await start(h, 'C1');
    await until(() => entered.length === 2);
    assert.deepEqual(entered, ['A1', 'B1']);
    assert.equal((await h.call('agent.run.get', { run_id: a2.id })).status, 'queued');
    assert.equal((await h.call('agent.run.get', { run_id: c.id })).status, 'queued');
    gate.resolve();
    for (const run of [a, a2, b, c]) assert.equal((await terminal(h, run.id)).status, 'completed');
    assert.equal(peak, 2);
    assert.ok(entered.indexOf('A2') > entered.indexOf('A1'));
    const a2Request = h.gateway.calls.find((b) => b.params.request.messages.some((m) => m.content[0]?.text === 'A2'));
    assert.deepEqual(
      a2Request.params.request.messages.map((m) => m.role),
      ['user', 'assistant', 'user']
    );
  } finally {
    gate.resolve();
    await h.close();
  }
});

test('assistant storage barrier failure prevents workshop side effects', async () => {
  const gate = deferred();
  const h = await harness(pki, {
    gateway: async (b, res) => {
      await gate.promise;
      json(res, b.id, response('', [tool('workshop_submit', { workflow: 'fixture', input: 'x' })]));
    },
  });
  try {
    const run = await start(h);
    await until(() => h.gateway.calls.length === 1);
    const db = new DatabaseSync(h.config.database);
    db.exec(
      "CREATE TRIGGER fail_assistant BEFORE INSERT ON messages WHEN json_extract(NEW.message, '$.role')='assistant' BEGIN SELECT RAISE(ABORT,'test failure'); END"
    );
    db.close();
    gate.resolve();
    assert.equal((await terminal(h, run.id)).status, 'failed');
    assert.equal(h.workshop.calls.length, 0);
    assert.deepEqual(
      (await h.call('agent.session.history', { session_id: run.session_id })).messages.map((m) => m.role),
      ['user']
    );
  } finally {
    gate.resolve();
    await h.close();
  }
});

test('tool result storage barrier failure prevents second tool and next model; failed context does not poison later runs', async () => {
  let h;
  h = await harness(pki, {
    gateway: (b, res) => {
      const text = b.params.request.messages.at(-1).content[0].text;
      if (text === 'followup') {
        assert.deepEqual(
          b.params.request.messages.map((m) => m.role),
          ['user']
        );
        json(res, b.id, response());
      } else
        json(
          res,
          b.id,
          response('', [
            tool('workshop_submit', { workflow: 'fixture', input: 'x' }, 'first'),
            tool('workshop_submit', { workflow: 'fixture', input: 'x' }, 'second'),
          ])
        );
    },
    workshop: (b, res) => {
      const db = new DatabaseSync(h.config.database);
      db.exec(
        "CREATE TRIGGER fail_tool BEFORE INSERT ON messages WHEN json_extract(NEW.message, '$.role')='tool' BEGIN SELECT RAISE(ABORT,'test failure'); END"
      );
      db.close();
      json(res, b.id, { id: 'task', namespace: b.params.namespace, status: 'succeeded' });
    },
  });
  try {
    const run = await start(h);
    assert.equal((await terminal(h, run.id)).status, 'failed');
    assert.equal(h.workshop.calls.length, 1);
    assert.equal(h.gateway.calls.length, 1);
    const db = new DatabaseSync(h.config.database);
    db.exec('DROP TRIGGER fail_tool');
    db.close();
    const next = await start(h, 'followup', 'followup', run.session_id);
    assert.equal((await terminal(h, next.id)).status, 'completed');
  } finally {
    await h.close();
  }
});

test('cancel aborts gateway socket, cancels queued runs, and cannot be converted to success', async () => {
  const closed = deferred();
  const h = await harness(pki, {
    gateway: (_b, res) => {
      res.on('close', () => closed.resolve());
    },
  });
  try {
    const run = await start(h);
    await until(() => h.gateway.calls.length === 1);
    const queued = await start(h, 'queued', 'next', run.session_id);
    assert.equal((await h.call('agent.run.cancel', { run_id: queued.id })).status, 'canceled');
    assert.equal((await h.call('agent.run.cancel', { run_id: run.id })).status, 'canceled');
    await closed.promise;
    assert.equal((await terminal(h, run.id)).status, 'canceled');
    assert.equal(h.gateway.calls.length, 1);
    assert.equal(h.workshop.calls.length, 0);
  } finally {
    await h.close();
  }
});

test('cancel during workshop aborts tool socket and prevents next tool', async () => {
  const closed = deferred();
  const h = await harness(pki, {
    gateway: (b, res) =>
      json(
        res,
        b.id,
        response('', [tool('workshop_get', { task_id: 'one' }, 'one'), tool('workshop_get', { task_id: 'two' }, 'two')])
      ),
    workshop: (_b, res) => res.on('close', () => closed.resolve()),
  });
  try {
    const run = await start(h);
    await until(() => h.workshop.calls.length === 1);
    await h.call('agent.run.cancel', { run_id: run.id });
    await closed.promise;
    assert.equal((await terminal(h, run.id)).status, 'canceled');
    assert.equal(h.workshop.calls.length, 1);
  } finally {
    await h.close();
  }
});

test('explicit read rejection is a committed model-visible error with sanitized upstream code', async () => {
  const secret = 'ARBITRARY_ERROR_BODY_MUST_NOT_SURVIVE';
  const h = await harness(pki, {
    gateway: (b, res) => {
      const result = b.params.request.messages.find((m) => m.role === 'tool');
      if (result) {
        assert.equal(result.content[0].is_error, true);
        assert.deepEqual(JSON.parse(result.content[0].text), {
          code: 'upstream_error',
          upstream: { code: -32004, data: { code: 'task_not_found' } },
        });
      }
      json(
        res,
        b.id,
        result ? response('Task was not found') : response('', [tool('workshop_get', { task_id: 'bad' })])
      );
    },
    workshop: (b, res) => {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(
        JSON.stringify({
          jsonrpc: '2.0',
          id: b.id,
          error: { code: -32004, message: secret, data: { code: 'task_not_found', body: secret } },
        })
      );
    },
  });
  try {
    const run = await start(h);
    const done = await terminal(h, run.id);
    assert.equal(done.status, 'completed');
    assert.equal(h.workshop.calls.length, 1);
    assert.equal(h.gateway.calls.length, 2);
    const history = await h.call('agent.session.history', { session_id: run.session_id });
    const events = await h.call('agent.run.events', { run_id: run.id });
    assert.equal(JSON.stringify([history, events, done]).includes(secret), false);
  } finally {
    await h.close();
  }
});

test('unknown or model-controlled scope/tool parameters recover without forbidden effects; duplicate IDs halt', async () => {
  for (const calls of [
    [tool('workshop_submit', { workflow: 'x', input: 'x', namespace: 'other' })],
    [tool('workshop_submit', { workflow: 'x', input: 'x', idempotency_key: 'evil' })],
    [tool('unknown', {})],
    [tool('workshop_cancel', { task_id: 'a' }), tool('workshop_cancel', { task_id: 'b' })],
  ]) {
    const h = await harness(pki, {
      gateway: (b, res) =>
        json(
          res,
          b.id,
          b.params.request.messages.some((m) => m.role === 'tool')
            ? response('Tool error acknowledged')
            : response('', calls)
        ),
    });
    try {
      const run = await start(h);
      assert.equal((await terminal(h, run.id)).status, calls.length > 1 ? 'failed' : 'completed');
      assert.equal(h.workshop.calls.length, 0);
      if (calls.length === 1) {
        const history = await h.call('agent.session.history', { session_id: run.session_id });
        assert.equal(history.messages.find((m) => m.role === 'tool').content[0].is_error, true);
      }
    } finally {
      await h.close();
    }
  }
});

test('step cap permits exactly one final no-tools turn', async () => {
  const h = await harness(pki, {
    config: { max_steps: 1 },
    gateway: (b, res) => {
      if (b.params.request.tool_choice === 'none') {
        assert.equal(b.params.request.tools, undefined);
        json(res, b.id, response('limited final'));
      } else json(res, b.id, response('', [tool('workshop_get', {})]));
    },
  });
  try {
    const run = await start(h);
    const done = await terminal(h, run.id);
    assert.equal(done.status, 'completed');
    assert.equal(h.gateway.calls.length, 2);
    assert.equal(done.result.message.content[0].text, 'limited final');
  } finally {
    await h.close();
  }
  const bad = await harness(pki, {
    config: { max_steps: 0 },
    gateway: (b, res) => json(res, b.id, response('', [tool('workshop_cancel', { task_id: 'x' })])),
  });
  try {
    const run = await start(bad);
    const done = await terminal(bad, run.id);
    assert.equal(done.error.code, 'step_limit');
    assert.equal(bad.workshop.calls.length, 0);
  } finally {
    await bad.close();
  }
});

test('byte-budget compaction uses independent no-tools turn, keeps original raw transcript and persists usage', async () => {
  const input = '片'.repeat(2000);
  const h = await harness(pki, {
    config: { context_bytes: 5000, streaming: true },
    gateway: async (b, res) => {
      if (b.params.request.messages[0].role === 'system') {
        assert.equal(b.params.stream, false);
        assert.equal(b.params.request.tool_choice, 'none');
        assert.equal(b.params.request.tools, undefined);
        json(res, b.id, response('short checkpoint'));
      } else {
        assert.ok(Buffer.byteLength(JSON.stringify(b.params.request)) <= 5000);
        await sse(res, b.id, response());
      }
    },
  });
  try {
    const run = await start(h, input);
    assert.equal((await terminal(h, run.id)).status, 'completed');
    assert.equal(h.gateway.calls.length, 2);
    const history = await h.call('agent.session.history', { session_id: run.session_id });
    assert.equal(history.messages[0].content[0].text, input);
    assert.ok(
      (await h.call('agent.run.events', { run_id: run.id })).events.some(
        (e) => e.kind === 'compaction_usage' && e.data.cost.amount === 0.001
      )
    );
  } finally {
    await h.close();
  }
});

test('summary that exceeds budget fails explicitly without another model/tool call', async () => {
  const h = await harness(pki, {
    config: { context_bytes: 5000 },
    gateway: (b, res) => json(res, b.id, response('x'.repeat(10000))),
  });
  try {
    const run = await start(h, 'x'.repeat(10000));
    const done = await terminal(h, run.id);
    assert.equal(done.error.code, 'context_budget_exceeded');
    assert.equal(h.gateway.calls.length, 1);
  } finally {
    await h.close();
  }
});

for (const mode of ['missing', 'duplicate', 'wrong-id', 'oversize', 'error'])
  test(`SSE ${mode} terminal is rejected; provisional deltas do not commit assistant or tools`, async () => {
    const h = await harness(pki, {
      config: { streaming: true },
      gateway: (b, res) => {
        res.writeHead(200, { 'content-type': 'text/event-stream' });
        res.write(
          `event: delta\ndata: ${JSON.stringify({ jsonrpc: '2.0', method: 'gateway.delta', params: { id: b.id, event: { type: 'text_delta', delta: 'provisional' } } })}\n\n`
        );
        const frame = `event: result\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: mode === 'wrong-id' ? 'wrong' : b.id, result: response('', [tool('workshop_cancel', { task_id: 'x' })]) })}\n\n`;
        if (mode === 'duplicate') res.write(frame + frame);
        if (mode === 'wrong-id') res.write(frame);
        if (mode === 'oversize') res.write('data: ' + 'x'.repeat(1024 * 1024 + 1));
        if (mode === 'error')
          res.write(
            `event: error\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: b.id, error: { code: -32000, message: 'fixture failure', data: { code: 'upstream_error' } } })}\n\n`
          );
        res.end();
      },
    });
    try {
      const run = await start(h);
      assert.equal((await terminal(h, run.id)).status, 'failed');
      assert.equal(h.workshop.calls.length, 0);
      assert.deepEqual(
        (await h.call('agent.session.history', { session_id: run.session_id })).messages.map((m) => m.role),
        ['user']
      );
    } finally {
      await h.close();
    }
  });

test('active database owner excludes second instance; graceful shutdown interrupts and releases ownership', async () => {
  const h = await harness(pki, { gateway: () => {} });
  try {
    await assert.rejects(startServer(h.config), /active owner/);
    const run = await start(h);
    await until(() => h.gateway.calls.length === 1);
    await h.running.close();
    const next = await startServer(h.config);
    try {
      const client = new RpcClient(pki.tls('client'), {
        ...h.endpoint,
        url: `https://localhost:${next.address.port}/rpc`,
      });
      const saved = await client.call('agent.run.get', { namespace: 'demo', run_id: run.id });
      assert.equal(saved.status, 'interrupted');
      client.close();
    } finally {
      await next.close();
    }
  } finally {
    await h.close();
  }
});

test('process death: expired owner interrupts running work and restores queued FIFO without tool replay', async () => {
  const hold = deferred();
  const h = await harness(pki, {
    gateway: (b, res) =>
      json(
        res,
        b.id,
        b.params.request.messages.at(-1).content[0].text === 'crash'
          ? response('', [tool('workshop_submit', { workflow: 'fixture', input: 'side effect' })])
          : response()
      ),
    workshop: async (b, res) => {
      await hold.promise;
      json(res, b.id, { id: 'accepted', namespace: b.params.namespace, status: 'running' });
    },
  });
  let child;
  try {
    await h.running.close();
    const configFile = join(h.dir, 'config.json');
    writeFileSync(configFile, JSON.stringify(h.config));
    child = spawn(process.execPath, ['dist/server.js', '--config', configFile], {
      cwd: new URL('..', import.meta.url),
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    child.stdout.on('data', (c) => {
      stdout += c;
    });
    const address = await until(() => {
      const line = stdout.split('\n').find((s) => s.includes('"event":"listening"'));
      return line && JSON.parse(line).address;
    });
    const client = new RpcClient(pki.tls('client'), { ...h.endpoint, url: `https://localhost:${address.port}/rpc` });
    const call = (m, p = {}) => client.call(m, { namespace: 'demo', ...p });
    const session = await call('agent.session.create');
    const active = await call('agent.run.start', { session_id: session.id, input: 'crash', idempotency_key: 'one' });
    await until(() => h.workshop.calls.length === 1);
    const queued = await call('agent.run.start', { session_id: session.id, input: 'after', idempotency_key: 'two' });
    child.kill('SIGKILL');
    await once(child, 'exit');
    child = undefined;
    client.close();
    await assert.rejects(startServer(h.config), /active owner/);
    // Simulate clock advancing past the persisted lease only after confirmed process death.
    const db = new DatabaseSync(h.config.database);
    db.exec('UPDATE ownership SET expires=0');
    db.close();
    const next = await startServer(h.config);
    const nextClient = new RpcClient(pki.tls('client'), {
      ...h.endpoint,
      url: `https://localhost:${next.address.port}/rpc`,
    });
    try {
      assert.equal(
        (await nextClient.call('agent.run.get', { namespace: 'demo', run_id: active.id })).status,
        'interrupted'
      );
      const done = await until(async () => {
        const run = await nextClient.call('agent.run.get', { namespace: 'demo', run_id: queued.id });
        return run.status === 'completed' && run;
      });
      assert.equal(done.status, 'completed');
      assert.equal(h.gateway.calls.length, 2);
      assert.equal(h.workshop.calls.length, 1);
    } finally {
      nextClient.close();
      await next.close();
    }
  } finally {
    if (child) {
      child.kill('SIGKILL');
      await once(child, 'exit');
    }
    hold.resolve();
    await h.close();
  }
});

test('ownership fencing prevents an expired owner from executing tools after late gateway response', async () => {
  const gate = deferred();
  const h = await harness(pki, {
    gateway: async (b, res) => {
      await gate.promise;
      json(res, b.id, response('', [tool('workshop_cancel', { task_id: 'x' })]));
    },
  });
  try {
    await start(h);
    await until(() => h.gateway.calls.length === 1);
    const db = new DatabaseSync(h.config.database);
    db.exec("UPDATE ownership SET owner='replacement', expires=9999999999999");
    db.close();
    gate.resolve();
    await setTimeout(50);
    assert.equal(h.workshop.calls.length, 0);
    assert.notEqual((await raw(pki, h.endpoint, undefined, 'client', '/healthz')).body, '{"status":"ok"}');
  } finally {
    gate.resolve();
    await h.close();
  }
});

test('RPC client deadline closes outstanding HTTPS request', async () => {
  const closed = deferred();
  const h = await harness(pki, { gateway: (_b, res) => res.on('close', () => closed.resolve()) });
  const client = new RpcClient(pki.tls('loop'), h.gateway.endpoint, 75);
  try {
    await assert.rejects(
      client.call('gateway.generate', { namespace: 'demo', request: {} }),
      (e) => e.name === 'TimeoutError'
    );
    await closed.promise;
  } finally {
    client.close();
    await h.close();
  }
});

test('remaining workshop tools map exact trusted scope and bounded RPC fields', async () => {
  const calls = [
    tool('workshop_catalog', {}, 'catalog'),
    tool('workshop_get', { task_id: 'task' }, 'get'),
    tool('workshop_list', { offset: 2, limit: 3 }, 'list'),
    tool('workshop_cancel', { task_id: 'task' }, 'cancel'),
    tool('workshop_resume', { task_id: 'task', input: 'continue' }, 'resume'),
    tool('workshop_result', { task_id: 'task', run_id: 'attempt', offset: 1, limit: 512 }, 'result'),
  ];
  const h = await harness(pki, {
    gateway: (b, res) =>
      json(res, b.id, b.params.request.messages.some((m) => m.role === 'tool') ? response() : response('', calls)),
  });
  try {
    const run = await start(h);
    assert.equal((await terminal(h, run.id)).status, 'completed');
    assert.deepEqual(
      h.workshop.calls.map((c) => c.method),
      ['workshop.workflows', 'workshop.get', 'workshop.list', 'workshop.cancel', 'workshop.resume', 'workshop.result']
    );
    assert.deepEqual(
      h.workshop.calls.map((c) => c.params),
      [
        { namespace: 'demo' },
        { namespace: 'demo', task_id: 'task' },
        { namespace: 'demo', offset: 2, limit: 3 },
        { namespace: 'demo', task_id: 'task' },
        { namespace: 'demo', task_id: 'task', input: 'continue' },
        { namespace: 'demo', task_id: 'task', run_id: 'attempt', offset: 1, limit: 512 },
      ]
    );
  } finally {
    await h.close();
  }
});

test('terminal transaction rolls back final assistant and context together on storage failure', async () => {
  const gate = deferred();
  const h = await harness(pki, {
    gateway: async (b, res) => {
      await gate.promise;
      json(res, b.id, response());
    },
  });
  try {
    const run = await start(h);
    await until(() => h.gateway.calls.length === 1);
    const db = new DatabaseSync(h.config.database);
    db.exec(
      "CREATE TRIGGER fail_context BEFORE UPDATE OF context ON sessions BEGIN SELECT RAISE(ABORT,'test failure'); END"
    );
    gate.resolve();
    const done = await terminal(h, run.id);
    assert.equal(done.status, 'failed');
    assert.deepEqual(
      (await h.call('agent.session.history', { session_id: run.session_id })).messages.map((m) => m.role),
      ['user']
    );
    assert.equal(db.prepare('SELECT context FROM sessions WHERE id=?').get(run.session_id).context, '[]');
    db.close();
    assert.equal(
      (await h.call('agent.run.events', { run_id: run.id })).events.filter((e) => e.kind === 'terminal').length,
      1
    );
  } finally {
    gate.resolve();
    await h.close();
  }
});

test('wrong pinned gateway certificate fails run without a workshop side effect', async () => {
  const h = await harness(pki);
  try {
    await h.running.close();
    const config = { ...h.config, gateway: { ...h.gateway.endpoint, peer_certificate_file: pki.cert('workshop') } };
    const service = await startServer(config);
    const client = new RpcClient(pki.tls('client'), {
      ...h.endpoint,
      url: `https://localhost:${service.address.port}/rpc`,
    });
    try {
      const local = { call: (m, p = {}) => client.call(m, { namespace: 'demo', ...p }) };
      const run = await start(local);
      assert.equal((await terminal(local, run.id)).status, 'failed');
      assert.equal(h.gateway.calls.length, 0);
      assert.equal(h.workshop.calls.length, 0);
    } finally {
      client.close();
      await service.close();
    }
  } finally {
    await h.close();
  }
});

test('completed context, history and idempotency survive a service restart', async () => {
  const h = await harness(pki);
  try {
    const first = await start(h);
    assert.equal((await terminal(h, first.id)).status, 'completed');
    await h.running.close();
    const service = await startServer(h.config);
    const client = new RpcClient(pki.tls('client'), {
      ...h.endpoint,
      url: `https://localhost:${service.address.port}/rpc`,
    });
    try {
      const local = { call: (m, p = {}) => client.call(m, { namespace: 'demo', ...p }) };
      assert.equal((await start(local, 'hello', 'key', first.session_id)).id, first.id);
      const next = await start(local, 'second input', 'second', first.session_id);
      assert.equal((await terminal(local, next.id)).status, 'completed');
      assert.equal(h.gateway.calls.length, 2);
      assert.deepEqual(
        h.gateway.calls[1].params.request.messages.map((m) => m.role),
        ['user', 'assistant', 'user']
      );
      assert.equal((await local.call('agent.session.history', { session_id: first.session_id })).messages.length, 4);
    } finally {
      client.close();
      await service.close();
    }
  } finally {
    await h.close();
  }
});

test('malformed SSE delta is rejected before committing any assistant', async () => {
  const h = await harness(pki, {
    config: { streaming: true },
    gateway: (b, res) => {
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      res.end(
        `event: delta\ndata: ${JSON.stringify({ jsonrpc: '2.0', method: 'gateway.delta', params: { id: b.id, event: { type: 'not_an_ai_event' } } })}\n\n`
      );
    },
  });
  try {
    const run = await start(h);
    assert.equal((await terminal(h, run.id)).error.code, 'invalid_upstream_delta');
  } finally {
    await h.close();
  }
});

test('workshop result/list limits match server bounds in model schemas and execution', async () => {
  const limits = [
    ['workshop_result', 4, 32768, { task_id: 'task' }],
    ['workshop_list', 1, 100, {}],
  ];
  for (const [name, min, max, args] of limits) {
    for (const limit of [0, min - 1, max + 1, min + 0.5, min, max, undefined]) {
      const valid = limit === undefined || (limit >= min && limit <= max && Number.isInteger(limit));
      const h = await harness(pki, {
        gateway: (b, res) => {
          const schema = b.params.request.tools.find((t) => t.name === name).parameters.properties.limit;
          assert.deepEqual(schema, { type: 'integer', minimum: min, maximum: max });
          json(
            res,
            b.id,
            b.params.request.messages.some((m) => m.role === 'tool')
              ? response()
              : response('', [tool(name, { ...args, ...(limit === undefined ? {} : { limit }) })])
          );
        },
      });
      try {
        const run = await start(h);
        const done = await terminal(h, run.id);
        assert.equal(done.status, 'completed', `${name} limit ${limit}`);
        assert.equal(h.workshop.calls.length, valid ? 1 : 0);
        if (valid) assert.equal(h.workshop.calls[0].params.limit, limit ?? (name === 'workshop_list' ? 20 : 8192));
        else {
          const history = await h.call('agent.session.history', { session_id: run.session_id });
          const errorResult = history.messages.find((m) => m.role === 'tool').content[0];
          assert.equal(errorResult.is_error, true);
          assert.equal(JSON.parse(errorResult.text).code, 'invalid_integer');
        }
      } finally {
        await h.close();
      }
    }
  }
});

for (const [name, args, code] of [
  ['workshop_get', {}, 'invalid_string'],
  ['calculator', {}, 'unknown_tool'],
  ['workshop_get', { task_id: 42 }, 'invalid_string'],
  ['workshop_get', null, 'invalid_params'],
])
  test(`${name} ordinary argument error commits before the next model completes`, async () => {
    let h;
    h = await harness(pki, {
      gateway: (b, res) => {
        const message = b.params.request.messages.find((m) => m.role === 'tool');
        if (message) {
          assert.equal(message.content[0].is_error, true);
          assert.equal(JSON.parse(message.content[0].text).code, code);
          // Inspect durable SQLite from the upstream callback, before replying to the
          // second model call: seeing the error in memory alone is insufficient.
          const db = new DatabaseSync(h.config.database);
          const persisted = JSON.parse(
            db.prepare('SELECT message FROM messages ORDER BY seq DESC LIMIT 1').get().message
          );
          db.close();
          assert.deepEqual(persisted, message);
        }
        json(
          res,
          b.id,
          message ? response('I can explain or correct this tool error.') : response('', [tool(name, args)])
        );
      },
    });
    try {
      const run = await start(h);
      assert.equal((await terminal(h, run.id)).status, 'completed');
      assert.equal(h.gateway.calls.length, 2);
      assert.equal(h.workshop.calls.length, 0);
    } finally {
      await h.close();
    }
  });

test('failed error-result commit prevents the next tool and model call', async () => {
  const gate = deferred();
  const h = await harness(pki, {
    gateway: async (b, res) => {
      await gate.promise;
      json(
        res,
        b.id,
        response('', [
          tool('workshop_get', {}, 'bad'),
          tool('workshop_submit', { workflow: 'fixture', input: 'must not execute' }, 'later'),
        ])
      );
    },
  });
  try {
    const run = await start(h);
    await until(() => h.gateway.calls.length === 1);
    const db = new DatabaseSync(h.config.database);
    db.exec(
      "CREATE TRIGGER fail_error_result BEFORE INSERT ON messages WHEN json_extract(NEW.message, '$.content[0].is_error')=1 BEGIN SELECT RAISE(ABORT,'test failure'); END"
    );
    db.close();
    gate.resolve();
    assert.equal((await terminal(h, run.id)).status, 'failed');
    assert.equal(h.gateway.calls.length, 1);
    assert.equal(h.workshop.calls.length, 0);
    assert.deepEqual(
      (await h.call('agent.session.history', { session_id: run.session_id })).messages.map((m) => m.role),
      ['user', 'assistant']
    );
  } finally {
    gate.resolve();
    await h.close();
  }
});

for (const [name, args] of [
  ['workshop_reply', { task_id: 'task', text: 'accepted side effect' }],
  ['workshop_submit', { workflow: 'fixture', input: 'accepted side effect' }],
  ['workshop_resume', { task_id: 'task', input: 'accepted side effect' }],
  ['workshop_cancel', { task_id: 'task' }],
])
  test(`${name} accepted then disconnected has uncertain outcome and never retries or executes later tools`, async () => {
    const accepted = [];
    const h = await harness(pki, {
      gateway: (b, res) =>
        json(
          res,
          b.id,
          response('', [
            tool(name, args, 'first'),
            tool('workshop_submit', { workflow: 'fixture', input: 'must not execute' }, 'later'),
          ])
        ),
      workshop: (b, res) => {
        accepted.push(b);
        res.destroy();
      },
    });
    try {
      const run = await start(h);
      const done = await terminal(h, run.id);
      assert.equal(done.status, 'failed');
      assert.equal(done.error.code, 'uncertain_tool_outcome');
      assert.equal(accepted.length, 1);
      assert.equal(h.gateway.calls.length, 1);
      assert.equal(h.workshop.calls.length, 1);
      const history = await h.call('agent.session.history', { session_id: run.session_id });
      assert.equal(history.messages.at(-1).content[0].is_error, true);
      assert.equal(JSON.parse(history.messages.at(-1).content[0].text).code, 'uncertain_tool_outcome');
      if (name === 'workshop_submit') assert.equal(accepted[0].params.idempotency_key, `${run.id}:first`);
    } finally {
      await h.close();
    }
  });

test('mutating RPC internal/execution and malformed responses fail closed and retain only sanitized codes', async () => {
  for (const mode of [-32603, -32000, 'malformed', 'invalid-data-code']) {
    const secret = 'UNTRUSTED_ERROR_BODY_OR_KEY';
    const h = await harness(pki, {
      gateway: (b, res) =>
        json(
          res,
          b.id,
          response('', [
            tool('workshop_submit', { workflow: 'fixture', input: 'x' }, 'first'),
            tool('workshop_cancel', { task_id: 'task' }, 'later'),
          ])
        ),
      workshop: (b, res) => {
        res.writeHead(200, { 'content-type': 'application/json' });
        if (mode === 'malformed') res.end('[]');
        else
          res.end(
            JSON.stringify({
              jsonrpc: '2.0',
              id: b.id,
              error: {
                code: typeof mode === 'number' ? mode : -32000,
                message: secret,
                data: { code: mode === 'invalid-data-code' ? secret : 'execution_error', body: secret },
              },
            })
          );
      },
    });
    try {
      const run = await start(h);
      const done = await terminal(h, run.id);
      assert.equal(done.status, 'failed');
      assert.equal(done.error.code, 'uncertain_tool_outcome');
      assert.equal(h.workshop.calls.length, 1);
      assert.equal(h.gateway.calls.length, 1);
      if (typeof mode === 'number')
        assert.deepEqual(done.error.upstream, { code: mode, data: { code: 'execution_error' } });
      if (mode === 'invalid-data-code') assert.deepEqual(done.error.upstream, { code: -32000 });
      const history = await h.call('agent.session.history', { session_id: run.session_id });
      const events = await h.call('agent.run.events', { run_id: run.id });
      assert.equal(JSON.stringify([done, history, events]).includes(secret), false);
    } finally {
      await h.close();
    }
  }
});

test('explicit mutating pre-acceptance rejection codes recover through a committed tool error', async () => {
  for (const code of [-32602, -32004, -32003, -32009, -32029]) {
    const h = await harness(pki, {
      gateway: (b, res) => {
        const result = b.params.request.messages.find((m) => m.role === 'tool');
        if (result) {
          assert.equal(result.content[0].is_error, true);
          assert.equal(JSON.parse(result.content[0].text).upstream.code, code);
        }
        json(
          res,
          b.id,
          result
            ? response('Submission was explicitly rejected.')
            : response('', [tool('workshop_submit', { workflow: 'fixture', input: 'x' })])
        );
      },
      workshop: (b, res) => {
        res.writeHead(code === -32003 ? 403 : 200, { 'content-type': 'application/json' });
        res.end(
          JSON.stringify({
            jsonrpc: '2.0',
            id: b.id,
            error: { code, message: 'Rejected before acceptance', data: { code: 'rejected' } },
          })
        );
      },
    });
    try {
      const run = await start(h);
      assert.equal((await terminal(h, run.id)).status, 'completed');
      assert.equal(h.gateway.calls.length, 2);
      assert.equal(h.workshop.calls.length, 1);
    } finally {
      await h.close();
    }
  }
});
