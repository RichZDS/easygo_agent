#!/usr/bin/env node
// Opt-in, billable P1 harness sample: real coding CLIs (Codex, Claude Code, Pi,
// OpenClaw) run on DeepSeek through the metered gateway route inside Docker task
// containers. Each must follow packs/base/roles/worker.md: report through
// easygo-crew and finish with `easygo-crew submit`. The platform then runs the
// base pack's npm-test acceptance check in a separate check container.
// The provider key is read from a file and given only to the gateway process;
// after the run, every file under the state directory is scanned for it.
// Unless EASYGO_LIVE_KEEP=1, task workspaces, binaries, the image overlay and
// the throwaway PKI are deleted after that scan; report.json and logs stay.
import assert from 'node:assert/strict';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { once } from 'node:events';
import net from 'node:net';
import { mkdir, mkdtemp, writeFile, readFile, readdir, lstat, chmod, rm } from 'node:fs/promises';
import { openSync, closeSync, createReadStream } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { createRPCClient } from './rpc-call.mjs';
const exec = promisify(execFile),
  root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const env = (name) => {
  const v = process.env[name];
  if (!v) throw Error('Missing ' + name);
  return v;
};
const go = process.env.EASYGO_GO_BIN ?? 'go',
  docker = env('EASYGO_DOCKER_TEST_BINARY'),
  endpoint = env('EASYGO_DOCKER_TEST_ENDPOINT');
const baseImage = process.env.EASYGO_LIVE_BASE_IMAGE ?? 'easygo-task-runtime:platform',
  image = 'easygo-task-runtime:acl-live';
const profiles = { codex: 'codex-responses', claude: 'claude-messages', pi: 'pi-main', openclaw: 'openclaw-main' };
const runtimes = (process.env.EASYGO_LIVE_RUNTIMES ?? 'codex,claude,pi,openclaw').split(',').filter(Boolean);
for (const r of runtimes) assert.ok(profiles[r], 'runtimes must be codex, claude, pi or openclaw');
const taskSeconds = Number(process.env.EASYGO_LIVE_TASK_SECONDS ?? 900),
  model = process.env.EASYGO_LIVE_MODEL ?? 'deepseek-flash';
assert.ok(Number.isInteger(taskSeconds) && taskSeconds >= 60 && taskSeconds <= 3600);
const key = (await readFile(env('EASYGO_LIVE_KEY_FILE'), 'utf8')).trim();
if (key.length < 16 || /\s/.test(key)) throw Error('Key file must hold one API key');
// The per-run relay socket path must stay under 108 bytes: keep the state root short.
const parent = resolve(process.env.EASYGO_LIVE_STATE_ROOT ?? '/tmp/eglive');
await mkdir(parent, { recursive: true, mode: 0o700 });
const state = await mkdtemp(join(parent, 'r-')),
  owner = 'acl-live-' + state.split('-').at(-1);
const report = {
  scope: 'P1 harness live sample',
  state,
  model,
  base_image: baseImage,
  image,
  runtimes: [],
  paid_provider: true,
  pass: false,
};
const children = [],
  clients = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function save() {
  await writeFile(join(state, 'report.json'), JSON.stringify(report, null, 2));
}
async function port() {
  const s = net.createServer();
  s.listen(0, '127.0.0.1');
  await once(s, 'listening');
  const p = s.address().port;
  await new Promise((r) => s.close(r));
  return p;
}
async function until(fn, timeout, interval) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const result = await fn();
    if (result) return result;
    await sleep(interval);
  }
  throw Error('Timed out waiting for condition');
}
function start(name, bin, args, extra = {}) {
  const fd = openSync(join(state, name + '.log'), 'a', 0o600);
  const child = spawn(bin, args, {
    cwd: root,
    env: { PATH: process.env.PATH, LANG: 'C.UTF-8', ...extra },
    stdio: ['ignore', fd, fd],
  });
  closeSync(fd);
  const entry = {
    name,
    child,
    done: new Promise((r) => {
      child.once('error', (e) => r({ error: e.message }));
      child.once('exit', (code, signal) => r({ code, signal }));
    }),
  };
  children.push(entry);
  return entry;
}
async function stop(e) {
  if (e.child.exitCode === null && e.child.signalCode === null) e.child.kill('SIGTERM');
  let timer;
  await Promise.race([
    e.done,
    new Promise((r) => {
      timer = setTimeout(() => {
        e.child.kill('SIGKILL');
        r();
      }, 15000);
    }),
  ]);
  clearTimeout(timer);
}
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
const spec = `Create a small Node.js ES module project in the current workspace. Use Node.js built-ins only: nothing can be installed and there is no network.

1. package.json with "type": "module" and "scripts": {"test": "node --test"}.
2. src/slug.js exporting slugify(text): lowercase the text; replace every run of characters that are not a-z or 0-9 with a single hyphen; remove leading and trailing hyphens; if the result is empty return "item".
3. test/slug.test.js using node:test and node:assert with at least four cases, including one that returns "item".

Run npm test yourself before you finish, and report through easygo-crew as your instructions describe.`;
try {
  await exec('bash', [join(root, 'scripts/dev-pki.sh'), join(state, 'pki')]);
  await exec(go, ['build', '-o', join(state, 'gateway'), './cmd/server'], { cwd: join(root, 'services/ai-gateway') });
  await exec(go, ['build', '-o', join(state, 'workshop'), './cmd/server'], { cwd: join(root, 'services/workshop') });
  // Layer the integrated easygo-crew client onto the pinned runtime image instead
  // of rebuilding its multi-gigabyte CLI layer.
  const overlay = join(state, 'overlay');
  await mkdir(overlay);
  await exec(go, ['build', '-trimpath', '-o', join(overlay, 'easygo-crew'), './cmd/easygo-crew'], {
    cwd: join(root, 'services/workshop'),
    env: { ...process.env, CGO_ENABLED: '0' },
  });
  await chmod(join(overlay, 'easygo-crew'), 0o755);
  await writeFile(join(overlay, 'Dockerfile'), `FROM ${baseImage}\nCOPY easygo-crew /usr/local/bin/easygo-crew\n`);
  await exec(docker, ['--host', endpoint, 'build', '-q', '-t', image, overlay], { maxBuffer: 1 << 20 });
  report.image_id = (
    await exec(docker, ['--host', endpoint, 'image', 'inspect', '--format', '{{.Id}}', image])
  ).stdout.trim();
  const [gp, wp] = await Promise.all([port(), port()]);
  const identity = (n) => ({
    cert_file: join(state, 'pki', n, 'tls.crt'),
    key_file: join(state, 'pki', n, 'tls.key'),
    ca_file: join(state, 'pki/public/ca.crt'),
  });
  const cert = (n) => join(state, 'pki/public', n + '.crt');
  const ep = (n, p) => ({ url: `https://127.0.0.1:${p}/rpc`, peer_certificate_file: cert(n) });
  const grant = (id, methods, namespaces = ['live']) => ({ id, cert_file: cert(id), methods, namespaces });
  const gateway = JSON.parse(await readFile(join(root, 'services/ai-gateway/config.deepseek.example.json'), 'utf8'));
  Object.assign(gateway, {
    listen: `127.0.0.1:${gp}`,
    tls: identity('ai-gateway'),
    authorization: [grant('client', ['health']), grant('workshop', ['gateway.native'], ['*'])],
  });
  for (const [alias, m] of Object.entries(gateway.models)) {
    m.model = model;
    m.parameters = alias === 'responses' ? { reasoning: { effort: 'low' } } : { thinking: { type: 'disabled' } };
  }
  const example = JSON.parse(
    await readFile(join(root, 'services/workshop/config.runtimes.example.json'), 'utf8')
  ).workshop;
  const workshop = {
    listen: `127.0.0.1:${wp}`,
    tls: identity('workshop'),
    authorization: [
      grant('client', [
        'health',
        'workshop.workflows',
        'workshop.submit',
        'workshop.get',
        'workshop.events',
        'workshop.evidence',
        'workshop.result',
      ]),
    ],
    workshop: {
      root: join(state, 'w'),
      concurrency: 1,
      queue_capacity: 4,
      max_output_bytes: 4194304,
      pack_dir: join(root, 'packs/base'),
      model_gateway: { ...ep('ai-gateway', gp), tls: identity('workshop') },
      sandbox: {
        mode: 'docker',
        docker_binary: docker,
        endpoint,
        image,
        owner,
        host_root: join(state, 'w'),
        memory_bytes: 2147483648,
        nano_cpus: 2000000000,
        pids_limit: 256,
        tmpfs_bytes: 268435456,
      },
      engines: example.engines,
      runtime_profiles: example.runtime_profiles,
      workflows: [
        {
          name: 'live-crew',
          version: '1',
          instructions: 'Implement the task in the user input inside the current workspace.',
          runtime: profiles[runtimes[0]],
          allowed_runtimes: Object.values(profiles),
          policy: 'workspace-write',
          timeout_seconds: taskSeconds,
          artifacts: ['package.json', 'src/slug.js', 'test/slug.test.js'],
          acceptance: {
            checks: [{ name: 'npm-test', command: ['/bin/sh', '/pack/checks/npm-test.sh'], timeout_seconds: 300 }],
          },
        },
      ],
    },
  };
  for (const [name, config] of Object.entries({ gateway, workshop }))
    await writeFile(join(state, name + '.json'), JSON.stringify(config), { mode: 0o600 });
  start('gateway', join(state, 'gateway'), ['--config', join(state, 'gateway.json')], { DEEPSEEK_API_KEY: key });
  start('workshop', join(state, 'workshop'), ['--config', join(state, 'workshop.json')]);
  const tls = identity('client');
  const client = (n, p) => {
    const c = createRPCClient({
      url: ep(n, p).url,
      certFile: tls.cert_file,
      keyFile: tls.key_file,
      caFile: tls.ca_file,
      peerCertificateFile: cert(n),
      timeoutMs: 125000,
    });
    clients.push(c);
    return c;
  };
  const gw = client('ai-gateway', gp),
    ws = client('workshop', wp);
  for (const c of [gw, ws])
    await until(
      async () => {
        try {
          return await c.health();
        } catch {
          return false;
        }
      },
      30000,
      250
    );
  const call = (method, params) => ws.call(method, { namespace: 'live', ...params });
  for (const runtime of runtimes) {
    const began = Date.now();
    const entry = { runtime, profile: profiles[runtime] };
    report.runtimes.push(entry);
    try {
      let task = await call('workshop.submit', {
        workflow: 'live-crew',
        runtime: profiles[runtime],
        input: spec,
        idempotency_key: 'live-' + runtime,
      });
      entry.task_id = task.id;
      task = await until(
        async () => {
          const t = await call('workshop.get', { task_id: task.id });
          return !['queued', 'running', 'cancelling'].includes(t.status) && t;
        },
        (taskSeconds + 360) * 1000,
        5000
      );
      const run = task.runs.at(-1);
      Object.assign(entry, {
        status: task.status,
        error: run?.error ?? null,
        outcome: run?.outcome ?? null,
        acceptance_state: run?.acceptance_state ?? null,
        false_green: run?.false_green ?? null,
        elapsed_ms: Date.now() - began,
      });
      const events = [];
      for (let after = 0; ;) {
        const page = await call('workshop.events', { task_id: task.id, after });
        events.push(...page);
        if (page.length < 1000) break;
        after = page.at(-1).sequence;
      }
      entry.messages = events
        .filter((e) => e.kind === 'crew.message')
        .map((e) => ({
          sequence: e.sequence,
          direction: e.message.direction,
          kind: e.message.kind,
          claims: e.message.claims ?? null,
          text: e.message.text.slice(0, 400),
        }));
      entry.model_calls_usage = events.filter((e) => e.usage).map((e) => e.usage);
      const evidence = await call('workshop.evidence', { task_id: task.id });
      entry.evidence = evidence.evidence.map((e) => ({
        check: e.check,
        exit_code: e.exit_code,
        timed_out: e.timed_out,
        duration_ms: e.duration_ms,
        output_bytes: e.output_bytes,
        workspace_sha256: e.workspace_sha256,
      }));
      for (const e of evidence.evidence) {
        const page = await call('workshop.evidence', { task_id: task.id, evidence_id: e.id, limit: 4096 });
        entry.check_output_head = page.text.slice(0, 2000);
      }
      const submit = entry.messages.filter((m) => m.direction === 'from_worker' && m.kind === 'submit').at(-1);
      entry.claimed_tests = submit?.claims?.tests ?? null;
      // The harness passes when the real CLI used the channel to hand off and the
      // platform ran its own check; whether the model's tests pass is model quality.
      entry.harness_pass =
        entry.outcome === 'submitted' &&
        ['passed', 'failed'].includes(entry.acceptance_state) &&
        entry.evidence.length >= 1 &&
        entry.false_green === (entry.claimed_tests === 'pass' && entry.acceptance_state === 'failed');
    } catch (e) {
      entry.harness_pass = false;
      entry.failure = String(e.message).split(key).join('<REDACTED>');
    }
    await save();
    console.log(
      `RUNTIME ${runtime} status=${entry.status} outcome=${entry.outcome} acceptance=${entry.acceptance_state} claimed=${entry.claimed_tests} false_green=${entry.false_green} harness=${entry.harness_pass ? 'pass' : 'fail'} ${Math.round((entry.elapsed_ms ?? 0) / 1000)}s`
    );
  }
  report.pass = report.runtimes.every((r) => r.harness_pass);
  if (!report.pass) process.exitCode = 1;
} catch (e) {
  report.pass = false;
  report.error = String(e.stack).split(key).join('<REDACTED>');
  console.error(report.error);
  process.exitCode = 1;
} finally {
  clients.forEach((c) => c.close());
  for (const c of children.reverse()) await stop(c);
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
  if (process.env.EASYGO_LIVE_KEEP !== '1')
    for (const name of ['w', 'overlay', 'pki', 'gateway', 'workshop']) {
      const p = join(state, name);
      await exec('chmod', ['-R', 'u+w', p]).catch(() => {});
      await rm(p, { recursive: true, force: true }).catch((e) => {
        report.cleanup_error = e.message;
      });
    }
  await save();
  console.log('REPORT ' + join(state, 'report.json'));
}
