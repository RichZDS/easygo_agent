import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import https from 'node:https';
import { DatabaseSync } from 'node:sqlite';
import { setTimeout } from 'node:timers/promises';
import { parseJSON } from '../src/strict-json.mjs';
import { parseJSON as builtParseJSON } from '../dist/strict-json.mjs';
import { tlsOptions } from '../dist/rpc.js';
import { certificates, harness, json, response, tool, start, terminal, raw } from './helpers.mjs';

let pki;
before(() => { pki = certificates(); });
after(() => pki.close());

test('standalone source and compiled strict parser accept JSON values and independently scoped keys', () => {
  const values = [null, true, false, 0, -1, 1.25e-30, '片😀\\\"\n', [], {}, { a: [{ same: 1 }, { same: 2 }], text: '{"a":1,"a":2}' }];
  for (const value of values) {
    const text = ` \r\n${JSON.stringify(value)}\t `;
    assert.deepEqual(parseJSON(text), value); assert.deepEqual(builtParseJSON(text), value);
  }
  assert.deepEqual(parseJSON('['.repeat(64) + '0' + ']'.repeat(64)), JSON.parse('['.repeat(64) + '0' + ']'.repeat(64)));
  assert.equal(Object.hasOwn(parseJSON('{"__proto__":1}'), '__proto__'), true);
});

test('strict parser rejects nested/escaped duplicate names and malformed JSON with constant sanitized errors', () => {
  const invalid = [
    '{"secret":1,"secret":2}', '{"a":{"secret":1,"secret":2}}', '[{"secret":1,"secret":2}]',
    String.raw`{"code":1,"\u0063ode":2}`, String.raw`{"a\\b":1,"a\u005cb":2}`,
    String.raw`{"😀":1,"\ud83d\ude00":2}`, '{"__proto__":1,"__proto__":2}',
    '['.repeat(65) + '0' + ']'.repeat(65), '{"a":'.repeat(65) + '0' + '}'.repeat(65),
    '', ' ', '{', '[', '{"a":}', '{"a":1,}', '[1,]', '{"a" 1}', '{"a":1 "b":2}',
    '01', '+1', '.1', '1.', '1e', '1e+', 'NaN', 'Infinity', 'undefined', 'true false', '{}[]',
    '"unterminated', String.raw`"\q"`, String.raw`"\u000x"`, '"raw\nline"', '\ufeff{}', '{} garbage',
    '/* comment */{}', "{'a':1}"
  ];
  for (const input of invalid) {
    for (const parser of [parseJSON, builtParseJSON]) assert.throws(() => parser(input), e => e instanceof SyntaxError && e.message === 'Invalid JSON');
  }
});

async function chunksRequest(endpoint, chunks) {
  return new Promise((resolve, reject) => {
    const req = https.request(endpoint.url, { ...tlsOptions(pki.tls('client')), method: 'POST', headers: { 'content-type': 'application/json' } }, res => {
      const chunks = []; res.on('data', c => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(Buffer.concat(chunks).toString()) }));
    });
    req.on('error', reject);
    void (async () => { for (const chunk of chunks) { req.write(chunk); await setTimeout(2); } req.end(); })().catch(reject);
  });
}
async function sendChunks(res, chunks, type = 'application/json') {
  res.writeHead(200, { 'content-type': type });
  for (const chunk of chunks) { if (res.destroyed) return; res.write(chunk); await setTimeout(2); }
  res.end();
}
function splitUnicode(text) {
  const bytes = Buffer.from(text); const at = bytes.indexOf(Buffer.from('片'));
  assert.ok(at >= 0);
  return [bytes.subarray(0, at + 1), bytes.subarray(at + 1, at + 2), bytes.subarray(at + 2)];
}

test('public ingress rejects duplicate envelope/namespace/input and deep/trailing JSON before mutation', async () => {
  const h = await harness(pki);
  try {
    const bodies = [
      '{"jsonrpc":"2.0","id":"duplicate-create","method":"agent.run.cancel","method":"agent.session.create","params":{"namespace":"unauthorized","namespace":"demo"}}',
      '{"jsonrpc":"2.0","id":"ns","method":"agent.session.create","params":{"namespace":"unauthorized","namespace":"demo"}}',
      String.raw`{"jsonrpc":"2.0","id":"escaped","method":"agent.session.create","params":{"namespace":"unauthorized","\u006eamespace":"demo"}}`,
      '{"jsonrpc":"2.0","id":"nested","method":"agent.session.create","params":{"namespace":"demo","extra":{"a":1,"a":2}}}',
      '{"jsonrpc":"2.0","id":"deep","method":"agent.session.create","params":{"namespace":"demo","extra":' + '['.repeat(64) + '0' + ']'.repeat(64) + '}}',
      '{"jsonrpc":"2.0","id":"tail","method":"agent.session.create","params":{"namespace":"demo"}}{}'
    ];
    for (const body of bodies) {
      const result = await raw(pki, h.endpoint, body);
      assert.equal(result.status, 400); assert.equal(JSON.parse(result.body).error.code, -32700);
    }
    assert.equal((await h.call('agent.session.list')).sessions.length, 0);
    const session = await h.call('agent.session.create');
    const body = `{"jsonrpc":"2.0","id":"run","method":"agent.run.start","params":{"namespace":"demo","session_id":${JSON.stringify(session.id)},"input":"first","input":"second","idempotency_key":"key"}}`;
    assert.equal((await raw(pki, h.endpoint, body)).status, 400);
    const db = new DatabaseSync(h.config.database); assert.equal(db.prepare('SELECT count(*) AS count FROM runs').get().count, 0); db.close();
    assert.equal(h.gateway.calls.length, 0);
  } finally { await h.close(); }
});

test('public ingress rejects malformed/truncated UTF8 but accepts valid codepoints split across chunks', async () => {
  const h = await harness(pki);
  const prefix = Buffer.from('{"jsonrpc":"2.0","id":"');
  const suffix = Buffer.from('","method":"agent.session.create","params":{"namespace":"demo"}}');
  try {
    for (const chunks of [
      [prefix, Buffer.from([0xe7]), Buffer.from([0x28, 0x87]), suffix],
      [prefix, Buffer.from([0xff]), suffix],
      [Buffer.concat([prefix, Buffer.from('id'), suffix]), Buffer.from([0xe7])]
    ]) {
      const result = await chunksRequest(h.endpoint, chunks); assert.equal(result.status, 400); assert.equal(result.body.error.code, -32700);
    }
    assert.equal((await h.call('agent.session.list')).sessions.length, 0);
    const valid = await chunksRequest(h.endpoint, [prefix, Buffer.from([0xe7]), Buffer.from([0x89]), Buffer.concat([Buffer.from([0x87]), suffix])]);
    assert.equal(valid.status, 200); assert.equal(valid.body.id, '片');
    assert.equal((await h.call('agent.session.list')).sessions.length, 1);
  } finally { await h.close(); }
});

// Same input and model behavior as the independent review's red reproducer:
// the model would resubmit with a new trusted call ID if given a tool error.
for (const mode of ['duplicate', 'escaped', 'nested', 'deep', 'trailing', 'utf8', 'utf8-tail', 'sse-duplicate', 'sse-tail', 'sse-utf8-tail', 'sse-json-whitespace', 'sse-disconnect']) test(`accepted mutation with ${mode} JSON cannot reach safe-retry classification`, async () => {
  let accepted = 0;
  const h = await harness(pki, {
    gateway: (b, res) => {
      const errors = b.params.request.messages.filter(m => m.role === 'tool');
      json(res, b.id, errors.length >= 2 ? response('done') : response('', [tool('workshop_submit', { workflow: 'fixture', input: 'same intended side effect' }, `attempt-${errors.length + 1}`)]));
    },
    workshop: async (b, res) => {
      accepted++;
      const id = JSON.stringify(b.id);
      const duplicate = `{"jsonrpc":"2.0","id":${id},"error":{"code":-32603,"code":-32602,"message":"ambiguous rejection"}}`;
      const rejected = `{"jsonrpc":"2.0","id":${id},"error":{"code":-32602,"message":"rejected"}}`;
      if (mode === 'utf8') {
        await sendChunks(res, [Buffer.from(`{"jsonrpc":"2.0","id":${id},"error":{"code":-32602,"message":"`), Buffer.from([0xe7]), Buffer.from([0x28, 0x87]), Buffer.from('"}}')]); return;
      }
      if (mode === 'utf8-tail') { await sendChunks(res, [Buffer.from(rejected), Buffer.from([0xe7])]); return; }
      if (mode.startsWith('sse-')) {
        const data = mode === 'sse-duplicate' ? duplicate : rejected;
        const frame = `event: error\ndata: ${mode === 'sse-json-whitespace' ? '\u00a0' : ''}${data}\n\n`;
        if (mode === 'sse-disconnect') {
          res.writeHead(200, { 'content-type': 'text/event-stream' }); res.write(frame); await setTimeout(5); res.destroy(); return;
        }
        const chunks = [Buffer.from(frame)];
        if (mode === 'sse-tail') chunks.push(Buffer.from('event: error\ndata: {} garbage\n\n'));
        if (mode === 'sse-utf8-tail') chunks.push(Buffer.from([0xe7]));
        await sendChunks(res, chunks, 'text/event-stream'); return;
      }
      res.writeHead(200, { 'content-type': 'application/json' });
      if (mode === 'duplicate') res.end(duplicate);
      if (mode === 'escaped') res.end(duplicate.replace('"code":-32602', '"\\u0063ode":-32602'));
      if (mode === 'nested') res.end(`{"jsonrpc":"2.0","id":${id},"error":{"code":-32602,"message":"rejected","data":{"code":"first","code":"last"}}}`);
      if (mode === 'deep') res.end(rejected.slice(0, -2) + ',"data":' + '['.repeat(64) + '0' + ']'.repeat(64) + '}}');
      if (mode === 'trailing') res.end(rejected + '{}');
    }
  });
  try {
    const run = await start(h); const done = await terminal(h, run.id);
    assert.equal(done.status, 'failed'); assert.equal(done.error.code, 'uncertain_tool_outcome');
    assert.equal(accepted, 1); assert.equal(h.gateway.calls.length, 1); assert.equal(h.workshop.calls.length, 1);
  } finally { await h.close(); }
});

for (const stream of [false, true]) test(`valid split UTF8 ${stream ? 'SSE' : 'unary'} response remains valid`, async () => {
  const h = await harness(pki, { config: { streaming: stream }, gateway: async (b, res) => {
    const body = JSON.stringify({ jsonrpc: '2.0', id: b.id, result: response('片') });
    await sendChunks(res, splitUnicode(stream ? `event: result\ndata: ${body}\n\n` : body), stream ? 'text/event-stream' : 'application/json');
  }});
  try { const run = await start(h); const done = await terminal(h, run.id); assert.equal(done.status, 'completed'); assert.equal(done.result.message.content[0].text, '片'); }
  finally { await h.close(); }
});

test('duplicate SSE delta keys fail before any provisional event or assistant commit', async () => {
  const h = await harness(pki, { config: { streaming: true }, gateway: (b, res) => {
    res.writeHead(200, { 'content-type': 'text/event-stream' });
    res.end(`event: delta\ndata: {"jsonrpc":"2.0","method":"gateway.delta","params":{"id":${JSON.stringify(b.id)},"event":{"type":"text_delta","delta":"first","delta":"second"}}}\n\n`);
  }});
  try {
    const run = await start(h); assert.equal((await terminal(h, run.id)).status, 'failed');
    assert.equal((await h.call('agent.run.events', { run_id: run.id })).events.some(e => e.kind === 'delta'), false);
    assert.deepEqual((await h.call('agent.session.history', { session_id: run.session_id })).messages.map(m => m.role), ['user']);
  } finally { await h.close(); }
});

test('valid SSE pre-acceptance rejection still recovers after clean stream end', async () => {
  const h = await harness(pki, { gateway: (b, res) => json(res, b.id, b.params.request.messages.some(m => m.role === 'tool') ? response('Submission rejected') : response('', [tool('workshop_submit', { workflow: 'fixture', input: 'x' })])), workshop: async (b, res) => {
    await sendChunks(res, [Buffer.from(`event: error\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: b.id, error: { code: -32602, message: 'rejected' } })}\n\n`)], 'text/event-stream');
  }});
  try { const run = await start(h); assert.equal((await terminal(h, run.id)).status, 'completed'); assert.equal(h.gateway.calls.length, 2); assert.equal(h.workshop.calls.length, 1); }
  finally { await h.close(); }
});
