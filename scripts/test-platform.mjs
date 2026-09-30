#!/usr/bin/env node
// Local fixture-backed managed integration. Never reads provider credentials.
import assert from 'node:assert/strict';
import { once } from 'node:events';
import http from 'node:http';
import { mkdtemp, writeFile, readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { buildServices, exec, go, ports, root, sleep, testbed, until, loopServer } from './lib/procs.mjs';
import { createPKI } from './lib/pki.mjs';
import { localPlatform, writeConfigs } from './lib/config.mjs';
import { browser } from './lib/web.mjs';
const docker = process.env.EASYGO_DOCKER_TEST_BINARY,
  endpoint = process.env.EASYGO_DOCKER_TEST_ENDPOINT,
  image = process.env.EASYGO_DOCKER_TEST_IMAGE;
if (!docker || !endpoint || !image) throw Error('Explicit dedicated Docker test binary/endpoint/image required.');
const state = await mkdtemp('/tmp/egp-'),
  report = {
    state,
    checks: [],
    model_requests: 0,
    chat_requests: 0,
    native_requests: 0,
    paid_provider: false,
    max_active_provider: 0,
  };
const clients = [];
const bed = testbed(state, { report });
let model;
const check = (name) => {
  report.checks.push(name);
  console.log('PASS ' + name);
};
try {
  const certs = await createPKI(join(state, 'pki'));
  await buildServices(state);
  await exec(go, ['build', '-o', join(state, 'remote'), './cmd/easygo-remote'], { cwd: root });
  const [gp, wp, ap, hp] = await ports(4);
  const origin = `http://127.0.0.1:${hp}`;
  report.origin = origin;
  let beforeResponse,
    activeProvider = 0;
  model = http.createServer(async (req, res) => {
    let counted = false;
    try {
      let text = '';
      for await (const c of req) text += c;
      const p = JSON.parse(text);
      report.model_requests++;
      activeProvider++;
      counted = true;
      report.max_active_provider = Math.max(report.max_active_provider, activeProvider);
      await sleep(100);
      assert.equal(req.headers.authorization, 'Bearer fixture-key');
      assert.equal(p.model, 'fixture-model');
      if (beforeResponse) {
        const action = beforeResponse;
        beforeResponse = undefined;
        await action();
      }
      if (req.url === '/responses') {
        report.native_requests++;
        res.setHeader('Content-Type', 'application/json');
        res.end(
          JSON.stringify({
            id: 'fixture-' + report.model_requests,
            status: 'completed',
            error: null,
            output: [
              { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'fixture completed' }] },
            ],
            usage: { input_tokens: 7, output_tokens: 3 },
          })
        );
        return;
      }
      report.chat_requests++;
      const textOf = (c) => (typeof c === 'string' ? c : (c ?? []).map((b) => b.text ?? '').join(''));
      const messages = p.messages ?? [];
      const system = messages
        .filter((m) => m.role === 'system')
        .map((m) => textOf(m.content))
        .join('\n');
      const last = messages.at(-1);
      const input = textOf(last?.content);
      if (input.includes('HANG_MODEL')) {
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        res.write(
          'data: ' +
            JSON.stringify({ choices: [{ index: 0, delta: { content: 'waiting' }, finish_reason: null }] }) +
            '\n\n'
        );
        return;
      }
      let answer = 'PLATFORM_OK',
        tool;
      if (system.includes('Extract durable') || system.includes('Reconcile prior')) {
        const source = JSON.parse(input);
        const draft = Array.isArray(source)
          ? {
              kind: 'preference',
              content: 'Prefers concise answers',
              importance: 0.9,
              confidence: 1,
              source_run_ids: [source[0].run_id],
            }
          : source.extracted[0];
        answer = JSON.stringify({ memories: draft ? [draft] : [] });
      } else if (input.includes('RECALL_CHECK')) {
        assert.match(system, /concise answers/);
        answer = 'MEMORY_OK';
      } else if (last?.role === 'tool') {
        assert.match(input, /SKILL_SENTINEL/);
        answer = 'SKILL_OK';
      } else if (input.includes('LOAD_SKILL')) {
        assert.ok(p.tools.some((t) => t.function?.name === 'load_skill'));
        assert.ok(!JSON.stringify(messages).includes('SKILL_SENTINEL'));
        tool = {
          id: 'skill-' + report.model_requests,
          type: 'function',
          function: { name: 'load_skill', arguments: '{"name":"note"}' },
        };
      }
      const message = tool
        ? { role: 'assistant', content: null, tool_calls: [tool] }
        : { role: 'assistant', content: answer };
      const usage = { prompt_tokens: 20, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 4 } };
      if (!p.stream) {
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ choices: [{ message, finish_reason: tool ? 'tool_calls' : 'stop' }], usage }));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const delta = tool ? { tool_calls: [{ index: 0, ...tool }] } : { content: answer };
      for (const value of [
        { choices: [{ index: 0, delta, finish_reason: null }] },
        { choices: [{ index: 0, delta: {}, finish_reason: tool ? 'tool_calls' : 'stop' }] },
        { choices: [], usage },
      ])
        res.write('data: ' + JSON.stringify(value) + '\n\n');
      res.end('data: [DONE]\n\n');
    } catch (e) {
      console.error('Fixture assertion: ' + e.message);
      res.writeHead(500);
      res.end();
    } finally {
      if (counted) activeProvider--;
    }
  });
  model.listen(0, '127.0.0.1');
  await once(model, 'listening');
  const mp = model.address().port;
  const { gateway, workshop, loop } = await localPlatform(
    state,
    certs,
    { 'ai-gateway': gp, workshop: wp, 'agent-loop': ap, web: hp },
    {
      fixtureURL: `http://127.0.0.1:${mp}`,
      maxOutputTokens: 256,
      maxSteps: 4,
      consolidateIntervalMs: 86400000,
      sandbox: {
        mode: 'docker',
        docker_binary: docker,
        endpoint,
        image,
        owner: 'platform-' + state.split('/').at(-1),
        host_root: join(state, 'workshop-data'),
      },
    }
  );
  await writeConfigs(state, { gateway, workshop, loop });
  bed.start('gateway', join(state, 'gateway'), ['--config', join(state, 'gateway.json')], {
    DEEPSEEK_API_KEY: 'fixture-key',
  });
  bed.start('workshop', join(state, 'workshop'), ['--config', join(state, 'workshop.json')]);
  bed.start('loop', process.execPath, [loopServer, '--config', join(state, 'loop.json')], {
    EASYGO_ADMIN_PASSWORD: 'test-admin-password-v1',
  });
  for (const [name, p] of [
    ['ai-gateway', gp],
    ['workshop', wp],
    ['agent-loop', ap],
  ]) {
    const c = certs.client('client', name, p);
    clients.push(c);
    await until(() => c.health(), { retryErrors: true });
  }
  const admin = browser(origin),
    alice = browser(origin),
    bob = browser(origin);
  await admin.request('/api/login', { email: 'admin@example.test', password: 'test-admin-password-v1' });
  const au = (
    await alice.request('/api/register', { email: 'alice@example.test', password: 'alice-test-password' }, 'POST', 201)
  ).user;
  await bob.request('/api/register', { email: 'bob@example.test', password: 'bob-test-password' }, 'POST', 201);
  assert.equal((await alice.request('/api/wallet')).balance_micros, 0);
  const session = await alice.rpc('agent.session.create');
  const run = async (input, client = alice, sid = session.id) => {
    const r = await client.rpc('agent.run.start', { session_id: sid, input, idempotency_key: randomUUID() });
    return await until(async () => {
      const v = await client.rpc('agent.run.get', { run_id: r.id });
      return !['queued', 'running'].includes(v.status) && v;
    });
  };
  const prior = report.model_requests;
  const rejected = await run('NO_FUNDS');
  assert.equal(rejected.status, 'failed');
  assert.equal(report.model_requests, prior);
  check('zero balance rejects before any provider request');
  await bob.request(
    '/api/admin/credits',
    { user_id: au.id, amount_micros: 100000000, reason: 'forged', idempotency_key: 'forged' },
    'POST',
    403
  );
  await alice.request('/api/rpc', { method: 'agent.session.list', params: { namespace: 'forged' } }, 'POST', 400);
  await bob.request('/api/rpc', { method: 'agent.session.history', params: { session_id: session.id } }, 'POST', 400);
  check('public role and cross-account scope isolation');
  const credit = {
    user_id: au.id,
    amount_micros: 1000000000,
    reason: 'fixture acceptance',
    idempotency_key: 'grant-once',
  };
  await admin.request('/api/admin/credits', credit);
  await admin.request('/api/admin/credits', credit);
  assert.equal((await alice.request('/api/wallet')).balance_micros, 1000000000);
  const answer = await run('I prefer concise answers.');
  assert.equal(answer.status, 'completed');
  assert.match(JSON.stringify(answer.result), /PLATFORM_OK/);
  await until(async () => (await alice.request('/api/usage')).receipts.some((r) => r.status === 'settled'));
  assert.equal((await alice.request('/api/wallet')).balance_micros, 1000000000 - 25000);
  check('idempotent grant and exact raw token debit without double-counting cache');
  await alice.rpc('agent.memory.consolidate');
  const memories = await alice.rpc('agent.memory.list');
  assert.equal(memories.memories.length, 1);
  assert.equal((await bob.rpc('agent.memory.list')).memories.length, 0);
  const recalled = await run('RECALL_CHECK');
  assert.match(JSON.stringify(recalled.result), /MEMORY_OK/);
  await alice.rpc('agent.skills.upsert', {
    name: 'note',
    description: 'A note skill',
    content: 'SKILL_SENTINEL: write concise notes.',
  });
  const skill = await run('LOAD_SKILL');
  assert.equal(skill.status, 'completed');
  assert.match(JSON.stringify(skill.result), /SKILL_OK/);
  check('committed-run memory consolidation, scoped recall and lazy skill loading');
  const remote = await exec(
    join(state, 'remote'),
    ['--url', origin, '--email', 'alice@example.test', '--password-env', 'REMOTE_TEST_PASSWORD', '--sessions'],
    { env: { PATH: process.env.PATH, REMOTE_TEST_PASSWORD: 'alice-test-password' } }
  );
  assert.match(remote.stdout, new RegExp(session.id));
  check('old Go TUI network adapter authenticates against actual TS chain');
  const sentinel = join(state, 'host-sentinel');
  await writeFile(sentinel, 'host-only');
  const sibling = join(state, 'sibling');
  await writeFile(sibling, 'sibling-only');
  let task = await alice.rpc('workshop.submit', {
    workflow: 'proof',
    runtime: 'fixture',
    input: JSON.stringify({ mode: 'first', host_sentinel: sentinel, sibling }),
    idempotency_key: 'artifact-once',
  });
  async function terminalTask(id, timeout = 30000) {
    return until(
      async () => {
        const t = await alice.rpc('workshop.get', { task_id: id });
        return !['queued', 'running', 'cancelling'].includes(t.status) && t;
      },
      { timeout, interval: 750 }
    );
  }
  task = await terminalTask(task.id);
  assert.equal(task.status, 'succeeded', JSON.stringify(task));
  const proof = JSON.parse(
    await readFile(join(state, 'workshop-data/workspaces', task.id, 'isolation-proof.json'), 'utf8')
  );
  assert.ok(Object.values(proof.checks).every(Boolean));
  assert.equal(proof.limits.memory, '1073741824');
  task = await alice.rpc('workshop.resume', {
    task_id: task.id,
    input: JSON.stringify({ mode: 'resume', host_sentinel: sentinel, sibling }),
  });
  task = await terminalTask(task.id);
  assert.equal(task.status, 'succeeded');
  await until(
    async () =>
      (await alice.request('/api/usage')).receipts.filter(
        (r) => r.source === 'gateway.native' && r.status === 'settled'
      ).length === 2
  );
  const native = (await alice.request('/api/usage')).receipts.find((r) => r.source === 'gateway.native');
  assert.equal(native.charged_micros, 10000);
  assert.equal(native.settlement.usage.input_tokens, 7);
  check('real isolated container, UDS model relay, artifacts/resume and gateway-owned billing');
  const sleeping = await alice.rpc('workshop.submit', {
    workflow: 'proof',
    runtime: 'fixture',
    input: '{"mode":"sleep"}',
    idempotency_key: 'cancel-container',
  });
  await until(async () => {
    try {
      return await readFile(join(state, 'workshop-data/workspaces', sleeping.id, 'ready'), 'utf8');
    } catch {
      return false;
    }
  });
  await alice.rpc('workshop.cancel', { task_id: sleeping.id });
  assert.equal((await terminalTask(sleeping.id)).status, 'cancelled');
  check('public cancellation removes a running container and descendants');
  report.account = au;
  report.session = session.id;
  report.workshop_task = task.id;
  report.model_port = mp;
  report.ports = { gateway: gp, workshop: wp, agent: ap, web: hp };
  // Leave time for durable outbox delivery before checking clean accounting.
  await until(async () => (await alice.request('/api/wallet')).held_micros === 0);
  report.wallet = await alice.request('/api/wallet');
  report.usage = (await alice.request('/api/usage')).receipts;
  assert.ok(report.usage.every((r) => r.status === 'settled'));
  const soakSeconds = Number(process.env.EASYGO_PLATFORM_SOAK_SECONDS ?? 0);
  if (soakSeconds > 0) {
    assert.ok(Number.isInteger(soakSeconds) && soakSeconds <= 86400);
    const users = [];
    for (let i = 0; i < 8; i++) {
      const c = browser(origin);
      const u = (
        await c.request(
          '/api/register',
          { email: `soak${i}@example.test`, password: 'soak-test-password' },
          'POST',
          201
        )
      ).user;
      await admin.request('/api/admin/credits', {
        user_id: u.id,
        amount_micros: 1000000000,
        reason: 'fixture soak',
        idempotency_key: 'soak-grant-' + i,
      });
      users.push({ client: c, user: u, session: (await c.rpc('agent.session.create')).id });
    }
    if (soakSeconds > 60) {
      console.log('SOAK warmup: allowing auth/request rate window to reset');
      await sleep(60100);
    }
    const long = await alice.rpc('workshop.submit', {
      workflow: 'proof',
      runtime: 'fixture',
      input: JSON.stringify({
        mode: 'long',
        duration_seconds: Math.min(120, Math.max(1, soakSeconds - 5)),
        host_sentinel: sentinel,
        sibling,
      }),
      idempotency_key: 'long-workload',
    });
    const longStarted = Date.now();
    const longProof = terminalTask(long.id, 180000).then((value) => {
      assert.equal(value.status, 'succeeded', JSON.stringify(value));
      report.long_task = { elapsed_ms: Date.now() - longStarted, status: value.status };
    });
    const began = Date.now(),
      deadline = began + soakSeconds * 1000;
    let completed = 0,
      lastSave = 0;
    const latencies = [],
      samples = [];
    report.soak = { requested_seconds: soakSeconds, concurrency: 8, completed: 0, samples };
    while (Date.now() < deadline) {
      const batch = Date.now();
      await Promise.all(
        users.map(async (u, i) => {
          const start = Date.now();
          const result = await run('SOAK_' + completed + '_' + i, u.client, u.session);
          assert.equal(result.status, 'completed', JSON.stringify(result.error));
          latencies.push(Date.now() - start);
          completed++;
        })
      );
      report.soak.completed = completed;
      report.soak.elapsed_ms = Date.now() - began;
      if (Date.now() - lastSave >= 60000) {
        const memory = {};
        for (const c of bed.all) {
          const status = await readFile(`/proc/${c.child.pid}/status`, 'utf8');
          memory[c.name] = Number(status.match(/VmRSS:\s+(\d+)/)?.[1] ?? 0);
          assert.equal(c.child.exitCode, null);
        }
        samples.push({ elapsed_ms: Date.now() - began, rss_kib: memory, completed });
        lastSave = Date.now();
        await bed.save();
        console.log('SOAK ' + JSON.stringify(samples.at(-1)));
      }
      await sleep(Math.min(Math.max(0, 15000 - (Date.now() - batch)), Math.max(0, deadline - Date.now())));
    }
    await longProof;
    await until(async () => {
      const wallets = await Promise.all([alice, ...users.map((u) => u.client)].map((c) => c.request('/api/wallet')));
      return wallets.every((w) => w.held_micros === 0) && wallets;
    });
    const wallets = await Promise.all([alice, ...users.map((u) => u.client)].map((c) => c.request('/api/wallet')));
    const spent = 9000000000 - wallets.reduce((sum, w) => sum + w.balance_micros, 0);
    assert.equal(spent, report.chat_requests * 25000 + report.native_requests * 10000);
    latencies.sort((a, b) => a - b);
    report.soak.elapsed_ms = Date.now() - began;
    report.soak.p50_ms = latencies[Math.floor(latencies.length * 0.5)];
    report.soak.p95_ms = latencies[Math.floor(latencies.length * 0.95)];
    report.soak.max_ms = latencies.at(-1);
    report.soak.spent_micros = spent;
    check('bounded concurrent soak and long task completed with exact aggregate credit accounting');
  }
  report.pass = true;
  await bed.save();
  if (process.env.EASYGO_PLATFORM_KEEP === '1') {
    console.log('READY ' + JSON.stringify({ state, origin }));
    await new Promise((r) => {
      process.once('SIGTERM', r);
      process.once('SIGINT', r);
    });
  }
} catch (e) {
  report.pass = false;
  report.error = e.stack;
  console.error(e.stack);
  process.exitCode = 1;
} finally {
  clients.forEach((c) => c.close());
  await bed.stopAll();
  if (model) {
    model.closeAllConnections();
    await new Promise((r) => model.close(r));
  }
  await bed.save();
  console.log('REPORT ' + join(state, 'report.json'));
}
