import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createToolRegistry } from '../dist/tools/index.js';
import { Loop } from '../dist/loop.js';
import { RpcError } from '../dist/validation.js';

const run = { id: 'loop-run', namespace: 'demo' };
const signal = () => new AbortController().signal;
const receipt = { namespace: 'demo', task_id: 'task', run_id: 'attempt' };
const evidence = {
  id: 'proof',
  check: 'unit',
  command: ['/pack/checks/unit.sh'],
  exit_code: 0,
  timed_out: false,
  duration_ms: 12,
  output_bytes: 3,
  output_truncated: false,
  workspace_sha256: 'a'.repeat(64),
  time: new Date().toISOString(),
};
const list = () => ({
  ...receipt,
  acceptance_state: 'passed',
  false_green: false,
  evidence: [structuredClone(evidence)],
});
const page = () => ({
  ...receipt,
  evidence_id: 'proof',
  text: '好',
  offset: 0,
  next_offset: 3,
  total_bytes: 3,
  eof: true,
});
function setup(reply) {
  const calls = [];
  const registry = createToolRegistry({
    call: async (method, params) => {
      calls.push({ method, params });
      return typeof reply === 'function' ? reply(method, params) : structuredClone(reply);
    },
  });
  return {
    calls,
    registry,
    execute: (name, args, id = 'call') => registry.execute('assistant', { name, arguments: args, id }, run, signal()),
  };
}

test('reply uses trusted scope and deterministic key; evidence validates requested and returned identities', async () => {
  const h = setup({ ...receipt, id: 'message', sequence: 1 });
  await h.execute('workshop_reply', { task_id: 'task', text: 'Continue' });
  assert.deepEqual(h.calls, [
    {
      method: 'workshop.message',
      params: { namespace: 'demo', task_id: 'task', text: 'Continue', idempotency_key: 'loop-run:call' },
    },
  ]);
  const e = setup(list());
  assert.deepEqual(await e.execute('workshop_evidence', { task_id: 'task' }), list());
  assert.equal(e.calls[0].params.limit, 8192);
  assert.deepEqual(
    await setup(page()).execute('workshop_evidence', {
      task_id: 'task',
      run_id: 'attempt',
      evidence_id: 'proof',
      limit: 4,
    }),
    page()
  );
});

test('reply and evidence reject local parameter boundaries without RPC', async () => {
  const cases = [
    ['workshop_reply', {}],
    ['workshop_reply', { task_id: 'task', text: '' }],
    ['workshop_reply', { task_id: 'task', text: '字'.repeat(2731) }],
    ['workshop_reply', { task_id: 'task', text: 'x', namespace: 'other' }],
    ['workshop_reply', { task_id: 'task', text: 'x', idempotency_key: 'forged' }],
    ...[-1, 1.5, Number.MAX_SAFE_INTEGER + 1].map((offset) => ['workshop_evidence', { task_id: 'task', offset }]),
    ...[0, 3, 32769, 4.5].map((limit) => ['workshop_evidence', { task_id: 'task', limit }]),
    ['workshop_evidence', { task_id: 'task', run_id: '' }],
    ['workshop_evidence', { task_id: 'task', evidence_id: 7 }],
  ];
  for (const [name, args] of cases) {
    const h = setup({});
    await assert.rejects(h.execute(name, args), (e) => e.code === -32602);
    assert.equal(h.calls.length, 0);
  }
  for (const limit of [4, 32768])
    await setup(page()).execute('workshop_evidence', { task_id: 'task', evidence_id: 'proof', limit });
  await setup({ ...receipt, id: 'm', sequence: 1 }).execute('workshop_reply', {
    task_id: 'task',
    text: 'a'.repeat(8192),
  });
});

test('reply malformed identities and uncertain errors are never recoverable; explicit rejections are', async () => {
  for (const change of [{ namespace: 'other' }, { task_id: 'other' }, { id: 3 }, { sequence: 0 }, { sequence: 1.5 }]) {
    const h = setup({ ...receipt, id: 'm', sequence: 1, ...change });
    await assert.rejects(
      h.execute('workshop_reply', { task_id: 'task', text: 'x' }),
      (e) => e.reason === 'uncertain_tool_outcome' && !h.registry.recoverable('assistant', 'workshop_reply', e)
    );
  }
  for (const code of [-32602, -32004, -32003, -32009, -32029, -32603, -32000]) {
    const h = setup(() => {
      throw new RpcError(-32000, 'upstream_error', 'upstream', { code });
    });
    await assert.rejects(
      h.execute('workshop_reply', { task_id: 'task', text: 'x' }),
      (e) => h.registry.recoverable('assistant', 'workshop_reply', e) === ![-32603, -32000].includes(code)
    );
  }
});

test('evidence rejects forged identities, invalid metadata, inconsistent pagination and oversized replies', async () => {
  for (const change of [
    { namespace: 'other' },
    { task_id: 'other' },
    { run_id: 'other' },
    { acceptance_state: 'pending' },
    { false_green: 'false' },
    { evidence: [{ ...evidence, exit_code: '0' }] },
    { evidence: [{ ...evidence, workspace_sha256: 'invalid' }] },
    { evidence: [evidence, evidence] },
    { extra: 'x'.repeat(256 * 1024) },
  ]) {
    const h = setup({ ...list(), ...change });
    await assert.rejects(
      h.execute('workshop_evidence', { task_id: 'task', run_id: 'attempt' }),
      (e) => e.reason === 'upstream_error'
    );
  }
  for (const change of [
    { namespace: 'other' },
    { task_id: 'other' },
    { run_id: 'other' },
    { evidence_id: 'other' },
    { text: 3 },
    { offset: 1 },
    { next_offset: 2 },
    { total_bytes: 2 },
    { eof: false },
    { text: 'a'.repeat(8193) },
  ]) {
    const h = setup({ ...page(), ...change });
    await assert.rejects(
      h.execute('workshop_evidence', { task_id: 'task', run_id: 'attempt', evidence_id: 'proof' }),
      (e) => e.reason === 'upstream_error'
    );
  }
});

test('get and list validate optional P1 run summary enums and types', async () => {
  const summary = {
    id: 'attempt',
    status: 'succeeded',
    outcome: 'submitted',
    acceptance_state: 'passed',
    false_green: false,
    evidence_count: 1,
  };
  for (const method of ['get', 'list'])
    for (const change of [
      {},
      { outcome: 'success' },
      { acceptance_state: 'pending' },
      { false_green: 0 },
      { evidence_count: -1 },
      { evidence_count: 9 },
    ]) {
      const task = { id: 'task', namespace: 'demo', status: 'succeeded', runs: [{ ...summary, ...change }] };
      const h = setup(method === 'list' ? { tasks: [task] } : task);
      const execute = h.execute(`workshop_${method}`, method === 'list' ? {} : { task_id: 'task' });
      if (Object.keys(change).length) await assert.rejects(execute, (e) => e.reason === 'upstream_error');
      else await execute;
    }
});

test('Web RPC forwarding permits message/evidence and rejects other workshop methods', async () => {
  const calls = [];
  const receiver = {
    assertAvailable() {},
    workshop: {
      call: async (...args) => {
        calls.push(args);
        return 'ok';
      },
    },
    background: { signal: signal() },
  };
  for (const method of ['workshop.message', 'workshop.evidence'])
    assert.equal(
      await Loop.prototype.workshopCall.call(receiver, method, { namespace: 'demo', task_id: 'task' }),
      'ok'
    );
  await assert.rejects(
    Loop.prototype.workshopCall.call(receiver, 'workshop.delete', {}),
    (e) => e.reason === 'method_not_found'
  );
  assert.equal(calls.length, 2);
});

const event = (sequence, kind = 'crew.message', extra = {}) => ({
  sequence,
  run_id: 'attempt',
  time: '2026-09-29T00:00:00Z',
  kind,
  ...(kind === 'crew.message'
    ? { message: { id: `m-${sequence}`, direction: 'from_worker', kind: 'report', text: 'Progress' } }
    : {}),
  ...extra,
});
const task = () => ({
  namespace: 'demo',
  id: 'task',
  status: 'succeeded',
  run_ids: ['old', 'attempt'],
  runs: [
    {
      id: 'attempt',
      status: 'succeeded',
      outcome: 'submitted',
      acceptance_state: 'passed',
      false_green: false,
      evidence_count: 1,
    },
  ],
});
function messages(events, summary = task()) {
  return setup((method, params) =>
    method === 'workshop.get'
      ? structuredClone(summary)
      : structuredClone(typeof events === 'function' ? events(params) : events)
  );
}

test('messages validate scoped summary first, retain ordered crew/state events and skip native output', async () => {
  const events = [
    event(1, 'text', { text: 'native output' }),
    event(2, 'state', { text: 'running' }),
    event(3),
    event(4, 'crew.read', { read: { message_ids: ['reply'] } }),
    event(5, 'acceptance', { acceptance: { state: 'passed', false_green: false } }),
    event(6, 'result', { text: 'done', usage: { input_tokens: 1, output_tokens: 2 } }),
  ];
  const h = messages(events);
  const result = await h.execute('workshop_messages', { task_id: 'task' });
  assert.deepEqual(
    h.calls.map((c) => c.method),
    ['workshop.get', 'workshop.events']
  );
  assert.deepEqual(h.calls[1].params, { namespace: 'demo', task_id: 'task', after: 0 });
  assert.deepEqual(
    result.events.map((e) => e.sequence),
    [2, 3, 4, 5]
  );
  assert.equal(result.next, 6);
  assert.equal(result.truncated, false);
  const empty = await messages([]).execute('workshop_messages', { task_id: 'task', after: 12 });
  assert.equal(empty.next, 12);
  const historical = await messages([event(1, 'state', { run_id: 'old', text: 'queued' })]).execute(
    'workshop_messages',
    { task_id: 'task' }
  );
  assert.equal(historical.events[0].run_id, 'old');
});

test('message page cursors preserve all events without duplicates across byte truncation', async () => {
  const events = Array.from({ length: 12 }, (_, i) =>
    event(i + 1, 'crew.message', {
      message: { id: `m-${i}`, direction: 'from_worker', kind: 'report', text: '字'.repeat(2000) },
    })
  );
  const h = messages((params) => events.filter((e) => e.sequence > params.after));
  let after = 0;
  const seen = [];
  for (let i = 0; i < 12; i++) {
    const page = await h.execute('workshop_messages', { task_id: 'task', after });
    assert.ok(Buffer.byteLength(JSON.stringify(page)) <= 32768);
    seen.push(...page.events.map((e) => e.sequence));
    assert.ok(page.next > after);
    after = page.next;
    if (!page.truncated) break;
  }
  assert.deepEqual(
    seen,
    events.map((e) => e.sequence)
  );
});

test('oversized escaped single message is a marked bounded preview with advancing cursor', async () => {
  const h = messages([
    event(1, 'crew.message', {
      message: { id: 'large', direction: 'from_worker', kind: 'report', text: '\0'.repeat(8192) },
    }),
  ]);
  const result = await h.execute('workshop_messages', { task_id: 'task' });
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= 32768);
  assert.equal(result.events[0].text_truncated, true);
  assert.equal(result.next, 1);
});

test('full upstream pages advance through filtered output and mark continuation; large read receipts stay bounded', async () => {
  const result = await messages(
    Array.from({ length: 1000 }, (_, i) => event(i + 1, 'text', { text: 'output' }))
  ).execute('workshop_messages', { task_id: 'task' });
  assert.deepEqual(result.events, []);
  assert.equal(result.next, 1000);
  assert.equal(result.truncated, true);
  const reads = await messages([
    event(1, 'crew.read', {
      read: { message_ids: Array.from({ length: 1000 }, (_, i) => `message-${i}-${'x'.repeat(100)}`) },
    }),
  ]).execute('workshop_messages', { task_id: 'task' });
  assert.ok(Buffer.byteLength(JSON.stringify(reads)) <= 32768);
  assert.equal(reads.events[0].message_ids_truncated, true);
  assert.equal(reads.next, 1);
  const submit = event(1, 'crew.message', {
    message: { id: 'submission', direction: 'from_worker', kind: 'submit', text: 'Ready', claims: { tests: 'pass' } },
  });
  assert.equal(
    (await messages([submit]).execute('workshop_messages', { task_id: 'task' })).events[0].message.claims.tests,
    'pass'
  );
});

test('messages reject forged scopes, unknown runs, bad enums/types and invalid trailing events as a whole', async () => {
  for (const change of [
    { namespace: 'other' },
    { id: 'other' },
    { run_ids: undefined },
    { run_ids: ['attempt', 'attempt'] },
    { run_ids: ['old'] },
  ]) {
    const h = messages([event(1)], { ...task(), ...change });
    await assert.rejects(h.execute('workshop_messages', { task_id: 'task' }), (e) => e.reason === 'upstream_error');
    assert.equal(h.calls.length, 1);
  }
  const invalid = [
    { run_id: 'foreign' },
    { kind: 'invented' },
    { time: 7 },
    { text: 4 },
    { namespace: 'other' },
    { task_id: 'other' },
    { usage: { input_tokens: -1, output_tokens: 0 } },
    { message: { id: 'x', direction: 'from_worker', kind: 'note', text: 'x' } },
    { message: { id: 'x', direction: 'from_worker', kind: 'submit', text: 'x', claims: { tests: 'green' } } },
    { message: { id: 'x', direction: 'to_worker', kind: 'report', text: 'x' } },
    { message: { id: 'x', direction: 'from_worker', kind: 'report', text: 'x', claims: { tests: 'pass' } } },
  ];
  for (const change of invalid) {
    const h = messages([event(1, 'crew.message', change)]);
    await assert.rejects(h.execute('workshop_messages', { task_id: 'task' }), (e) => e.reason === 'upstream_error');
  }
  for (const events of [
    [event(1), event(1)],
    [event(2), event(1)],
    [event(0)],
    [event(1, 'state', { text: 'invalid' })],
    [event(1, 'acceptance', { acceptance: { state: 'pending' } })],
    [event(1, 'acceptance', { acceptance: { state: 'skipped' } })],
    [event(1, 'crew.read', { read: { message_ids: [1] } })],
  ])
    await assert.rejects(
      messages(events).execute('workshop_messages', { task_id: 'task' }),
      (e) => e.reason === 'upstream_error'
    );
  const prefix = Array.from({ length: 10 }, (_, i) =>
    event(i + 1, 'crew.message', {
      message: { id: `m-${i}`, direction: 'from_worker', kind: 'report', text: 'x'.repeat(8000) },
    })
  );
  await assert.rejects(
    messages([...prefix, event(11, 'crew.message', { run_id: 'foreign' })]).execute('workshop_messages', {
      task_id: 'task',
    }),
    (e) => e.reason === 'upstream_error'
  );
  for (const args of [
    {},
    { task_id: 'task', after: -1 },
    { task_id: 'task', after: 0.5 },
    { task_id: 'task', after: Number.MAX_SAFE_INTEGER + 1 },
    { task_id: 'task', namespace: 'other' },
  ]) {
    const h = messages([]);
    await assert.rejects(h.execute('workshop_messages', args), (e) => e.code === -32602);
    assert.equal(h.calls.length, 0);
  }
});
