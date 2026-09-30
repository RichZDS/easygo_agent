#!/usr/bin/env node
// Opt-in, billable: a real model builds the six-file project from
// scripts/platform-project-spec.md inside an isolated task container, submitted
// through the authenticated platform like any user task, then the independent
// oracle judges the workspace. The provider key is read from a file and given
// only to the gateway process; it is never printed or written to evidence.
import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readFile, readdir, lstat } from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { join, resolve } from 'node:path';
import { randomUUID } from 'node:crypto';
import { buildServices, exec, loopServer, ports, root, testbed, until } from './lib/procs.mjs';
import { createPKI } from './lib/pki.mjs';
import { localPlatform, writeConfigs } from './lib/config.mjs';
import { browser } from './lib/web.mjs';
const env = (name) => {
  const v = process.env[name];
  if (!v) throw Error('Missing ' + name);
  return v;
};
const docker = env('EASYGO_DOCKER_TEST_BINARY'),
  endpoint = env('EASYGO_DOCKER_TEST_ENDPOINT');
const image = process.env.EASYGO_LIVE_RUNTIME_IMAGE ?? 'easygo-task-runtime:platform',
  runtime = process.env.EASYGO_LIVE_RUNTIME ?? 'codex',
  model = process.env.EASYGO_LIVE_MODEL ?? 'deepseek-flash';
// Coding CLIs write whole files in one response; the gateway clamps output to this cap.
const maxOutput = Number(process.env.EASYGO_LIVE_MAX_OUTPUT ?? 32768),
  repairs = Number(process.env.EASYGO_LIVE_REPAIR_ROUNDS ?? 1),
  budgetCredits = Number(process.env.EASYGO_LIVE_CREDITS ?? 3000),
  taskSeconds = Number(process.env.EASYGO_LIVE_TASK_SECONDS ?? 1500);
assert.ok(
  Number.isInteger(maxOutput) &&
    maxOutput >= 1024 &&
    maxOutput <= 131072 &&
    Number.isInteger(repairs) &&
    repairs >= 0 &&
    repairs <= 3 &&
    Number.isInteger(budgetCredits) &&
    budgetCredits > 0 &&
    budgetCredits <= 100000 &&
    Number.isInteger(taskSeconds) &&
    taskSeconds <= 3600
);
const key = (await readFile(env('EASYGO_LIVE_KEY_FILE'), 'utf8')).trim();
if (key.length < 16 || /\s/.test(key)) throw Error('Key file must hold one API key');
const parent = resolve(process.env.EASYGO_LIVE_STATE_ROOT ?? join(root, '..', 'easygo-live-project'));
await mkdir(parent, { recursive: true, mode: 0o700 });
const state = await mkdtemp(join(parent, 'run-')),
  owner = 'live-' + state.split('-').at(-1);
const report = {
  state,
  runtime,
  model,
  image,
  max_output_tokens: maxOutput,
  budget_credits: budgetCredits,
  rounds: [],
  paid_provider: true,
};
const clients = [];
const bed = testbed(state, { report });
async function oracle(workspace) {
  const started = Date.now();
  const args = [
    '--host',
    endpoint,
    'run',
    '--rm',
    '--name',
    'oracle-' + randomUUID().slice(0, 12),
    '--network',
    'none',
    '--read-only',
    '--user',
    '1000:1000',
    '--cap-drop',
    'ALL',
    '--security-opt',
    'no-new-privileges=true',
    '--pids-limit',
    '128',
    '--memory',
    '512m',
    '--memory-swap',
    '512m',
    '--cpus',
    '1',
    '--tmpfs',
    '/tmp:rw,nosuid,nodev,noexec,size=67108864,mode=1777',
    '--mount',
    `type=bind,src=${workspace},dst=/project,readonly`,
    '--mount',
    `type=bind,src=${join(root, 'scripts/platform-project-oracle.mjs')},dst=/oracle.mjs,readonly`,
    '--entrypoint',
    '/usr/local/bin/node',
    image,
    '/oracle.mjs',
    '/project',
  ];
  let out,
    code = 0;
  try {
    out = (await exec(docker, args, { timeout: 90000, maxBuffer: 1 << 20 })).stdout;
  } catch (e) {
    out = e.stdout ?? '';
    code = e.code ?? 1;
  }
  let result;
  try {
    result = JSON.parse(out.trim().split('\n').at(-1));
  } catch {
    result = { ok: false, failure: 'oracle output unreadable' };
  }
  // A crashed or killed oracle cannot pass on a stale ok:true line.
  return { exit: code, wall_ms: Date.now() - started, ...result, ok: code === 0 && result.ok === true };
}
// Streams every regular file of any size; chunks overlap so a key cannot hide across a boundary.
async function fileHasKey(path) {
  let tail = '';
  for await (const chunk of createReadStream(path, { encoding: 'latin1', highWaterMark: 1 << 20 })) {
    const text = tail + chunk;
    if (text.includes(key)) return true;
    tail = text.slice(-(key.length - 1));
  }
  return false;
}
async function scanForKey(dir, found = []) {
  for (const name of await readdir(dir)) {
    const path = join(dir, name),
      info = await lstat(path);
    if (info.isDirectory()) await scanForKey(path, found);
    else if (info.isFile() && (await fileHasKey(path))) found.push(path);
  }
  return found;
}
try {
  const spec = await readFile(join(root, 'scripts/platform-project-spec.md'), 'utf8');
  const certs = await createPKI(join(state, 'pki'));
  await buildServices(state);
  const [gp, wp, ap, hp] = await ports(4);
  const origin = `http://127.0.0.1:${hp}`;
  const profiles = { codex: 'codex-responses', claude: 'claude-messages', pi: 'pi-main', openclaw: 'openclaw-main' };
  assert.ok(profiles[runtime], 'runtime must be codex, claude, pi or openclaw');
  const { gateway, workshop, loop } = await localPlatform(
    state,
    certs,
    { 'ai-gateway': gp, workshop: wp, 'agent-loop': ap, web: hp },
    {
      maxOutputTokens: maxOutput,
      concurrency: 4,
      consolidateIntervalMs: 86400000,
      sandbox: {
        mode: 'docker',
        docker_binary: docker,
        endpoint,
        image,
        owner,
        host_root: join(state, 'workshop-data'),
        memory_bytes: 2147483648,
        nano_cpus: 2000000000,
        pids_limit: 256,
        tmpfs_bytes: 268435456,
      },
    }
  );
  for (const [alias, m] of Object.entries(gateway.models)) {
    m.model = model;
    m.parameters = alias === 'responses' ? { reasoning: { effort: 'low' } } : { thinking: { type: 'disabled' } };
  }
  Object.assign(workshop.workshop, {
    concurrency: 1,
    queue_capacity: 4,
    workflows: [
      {
        name: 'build-project',
        version: '1',
        instructions:
          'Implement the software project specified in the user input inside the current workspace. Create exactly the files it lists, using Node.js built-ins only; nothing can be installed and there is no network. Run the project and its tests with node to check your work before finishing, then summarize what you built.',
        runtime: profiles[runtime],
        policy: 'workspace-write',
        timeout_seconds: taskSeconds,
        artifacts: [
          'package.json',
          'src/store.mjs',
          'src/server.mjs',
          'bin/server.mjs',
          'README.md',
          'tests/smoke.test.mjs',
        ],
      },
    ],
  });
  await writeConfigs(state, { gateway, workshop, loop });
  const adminPassword = 'live-admin-' + randomUUID();
  bed.start('gateway', join(state, 'gateway'), ['--config', join(state, 'gateway.json')], { DEEPSEEK_API_KEY: key });
  bed.start('workshop', join(state, 'workshop'), ['--config', join(state, 'workshop.json')]);
  bed.start('loop', process.execPath, [loopServer, '--config', join(state, 'loop.json')], {
    EASYGO_ADMIN_PASSWORD: adminPassword,
  });
  for (const [name, p] of [
    ['ai-gateway', gp],
    ['workshop', wp],
    ['agent-loop', ap],
  ]) {
    const c = certs.client('client', name, p);
    clients.push(c);
    await until(() => c.health(), { timeout: 30000, interval: 250, retryErrors: true });
  }
  const admin = browser(origin),
    user = browser(origin);
  await admin.request('/api/login', { email: 'admin@example.test', password: adminPassword });
  const account = (
    await user.request(
      '/api/register',
      { email: 'builder@example.test', password: 'builder-' + randomUUID() },
      'POST',
      201
    )
  ).user;
  // The grant bounds spend: admission stops once available credits run out. Only a
  // request whose actual usage exceeds its own conservative reservation can overshoot,
  // and that overage is recorded as debt, not dropped.
  await admin.request('/api/admin/credits', {
    user_id: account.id,
    amount_micros: budgetCredits * 1000000,
    reason: 'live project benchmark budget',
    idempotency_key: 'live-budget',
  });
  const catalog = await user.rpc('agent.workshop.catalog');
  report.catalog = catalog;
  const terminal = async (id) =>
    until(
      async () => {
        const t = await user.rpc('workshop.get', { task_id: id });
        return !['queued', 'running', 'cancelling'].includes(t.status) && t;
      },
      { timeout: (taskSeconds + 120) * 1000, interval: 5000 }
    );
  let task = await user.rpc('workshop.submit', {
    workflow: 'build-project',
    runtime: profiles[runtime],
    input: spec,
    idempotency_key: 'live-project',
  });
  const workspace = join(state, 'workshop-data/workspaces', task.id);
  report.task = task.id;
  for (let round = 0; round <= repairs; round++) {
    const began = Date.now();
    task = await terminal(task.id);
    const verdict = await oracle(workspace);
    const run = task.runs.at(-1);
    report.rounds.push({
      round,
      status: task.status,
      error: run?.error ?? null,
      elapsed_ms: Date.now() - began,
      artifacts: (run?.artifacts ?? []).map((a) => ({ path: a.path, size: a.size })),
      oracle: verdict,
    });
    await bed.save();
    console.log(
      `ROUND ${round} task=${task.status} oracle=${verdict.ok ? 'pass' : 'fail'} ${verdict.phase ?? ''} ${verdict.failure ?? ''}`
    );
    if (verdict.ok || round === repairs) break;
    const failure = `The independent acceptance test failed in phase "${verdict.phase ?? 'unknown'}": ${verdict.failure ?? 'no detail'}. Re-read the specification from the first message, fix the project in place, keep exactly the six files, run your own checks with node, then summarize.`;
    task = await user.rpc('workshop.resume', { task_id: task.id, input: failure });
  }
  await until(async () => (await user.request('/api/wallet')).held_micros === 0, { timeout: 120000, interval: 2000 });
  // A real agent can make well over one page (100) of model calls; read every page.
  const receipts = [];
  for (let offset = 0; offset !== null;) {
    const page = await user.request('/api/usage?offset=' + offset);
    receipts.push(...page.receipts);
    offset = page.next_offset;
  }
  assert.equal(new Set(receipts.map((r) => r.request_id)).size, receipts.length, 'usage pages overlap');
  const w = await user.request('/api/wallet');
  report.usage = {
    requests: receipts.length,
    statuses: Object.fromEntries(
      [...new Set(receipts.map((r) => r.status))].map((k) => [k, receipts.filter((r) => r.status === k).length])
    ),
    input_tokens: receipts.reduce((s, r) => s + (r.settlement?.usage?.input_tokens ?? 0), 0),
    cache_read_tokens: receipts.reduce((s, r) => s + (r.settlement?.usage?.cache_read_tokens ?? 0), 0),
    output_tokens: receipts.reduce((s, r) => s + (r.settlement?.usage?.output_tokens ?? 0), 0),
    charged_micros: receipts.reduce((s, r) => s + r.charged_micros, 0),
  };
  report.wallet = { balance_micros: w.balance_micros, held_micros: w.held_micros };
  assert.equal(budgetCredits * 1000000 - w.balance_micros, report.usage.charged_micros);
  report.pass = report.rounds.at(-1).oracle.ok === true;
  if (!report.pass) process.exitCode = 1;
} catch (e) {
  report.pass = false;
  report.error = String(e.stack).split(key).join('<REDACTED>');
  console.error(report.error);
  process.exitCode = 1;
} finally {
  clients.forEach((c) => c.close());
  await bed.stopAll();
  // After every process stopped: nothing under the state directory (task workspaces,
  // native HOMEs, service logs, configs, report) may contain the provider key.
  try {
    const leaked = await scanForKey(state);
    report.key_leaks = leaked.length;
    if (leaked.length) {
      report.pass = false;
      process.exitCode = 1;
      console.error('PROVIDER KEY FOUND IN ' + leaked.length + ' state file(s)');
    }
  } catch (e) {
    report.pass = false;
    report.key_scan_error = e.message;
    process.exitCode = 1;
  }
  await bed.save();
  console.log('REPORT ' + join(state, 'report.json'));
}
