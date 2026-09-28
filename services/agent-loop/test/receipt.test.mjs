import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { certificates, harness, json, response, tool, start, terminal } from './helpers.mjs';
let pki;
before(() => { pki = certificates(); });
after(() => pki.close());

for (const method of ['submit', 'cancel', 'resume']) {
  for (const mode of ['null', 'namespace', 'id', 'status']) {
    test(`accepted ${method} with invalid ${mode} receipt stops further effects`, async () => {
      let accepted = 0;
      const args = method === 'submit' ? { workflow: 'fixture', input: 'x' } : { task_id: 'task', ...(method === 'resume' ? { input: 'x' } : {}) };
      const h = await harness(pki, {
        gateway: (b, res) => json(res, b.id, response('', [tool(`workshop_${method}`, args, `attempt-${b.params.request.messages.length}`), tool('workshop_submit', { workflow: 'fixture', input: 'must not run' }, 'later')])),
        workshop: (b, res) => {
          accepted++;
          const receipt = { id: 'task', namespace: 'demo', status: 'queued' };
          if (mode === 'namespace') receipt.namespace = 'other';
          if (mode === 'id') receipt.id = method === 'submit' ? '' : 'wrong-task';
          if (mode === 'status') receipt.status = 'unknown';
          json(res, b.id, mode === 'null' ? null : { ...receipt, text: 'PRIVATE_RECEIPT_SENTINEL' });
        }
      });
      try {
        const run = await start(h); const done = await terminal(h, run.id);
        assert.equal(done.status, 'failed'); assert.equal(done.error.code, 'uncertain_tool_outcome');
        assert.equal(accepted, 1); assert.equal(h.gateway.calls.length, 1);
        const history = await h.call('agent.session.history', { session_id: run.session_id });
        const events = await h.call('agent.run.events', { run_id: run.id });
        assert.ok(!JSON.stringify({ done, history, events }).includes('PRIVATE_RECEIPT_SENTINEL'));
      } finally { await h.close(); }
    });
  }
}

for (const [name, args, value] of [
  ['get', { task_id: 'task' }, { id: 'wrong', namespace: 'demo', status: 'succeeded' }],
  ['list', {}, { tasks: [{ id: 'task', namespace: 'other', status: 'succeeded' }] }],
  ['result', { task_id: 'task' }, { task_id: 'wrong', run_id: 'attempt' }],
  ['result', { task_id: 'task', run_id: 'attempt' }, { task_id: 'task', run_id: 'wrong' }]
]) test(`mismatched ${name} receipt is sanitized before model recovery`, async () => {
  const h = await harness(pki, {
    gateway: (b, res) => json(res, b.id, b.params.request.messages.some(m => m.role === 'tool') ? response('Read unavailable') : response('', [tool(`workshop_${name}`, args)])),
    workshop: (b, res) => json(res, b.id, { ...value, text: 'PRIVATE_RECEIPT_SENTINEL' })
  });
  try {
    const run = await start(h); assert.equal((await terminal(h, run.id)).status, 'completed');
    const history = await h.call('agent.session.history', { session_id: run.session_id });
    assert.equal(history.messages.find(m => m.role === 'tool').content[0].is_error, true);
    assert.ok(!JSON.stringify(history).includes('PRIVATE_RECEIPT_SENTINEL'));
    assert.ok(!JSON.stringify(h.gateway.calls).includes('PRIVATE_RECEIPT_SENTINEL'));
  } finally { await h.close(); }
});
