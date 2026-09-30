#!/usr/bin/env node
// Independent black-box acceptance. Never imports generated source or smoke tests.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { promises as fs } from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { performance } from 'node:perf_hooks';

const TOKEN = 'oracle-dummy-token-not-a-secret';
const STARTUP_MS = 3000;
const REQUEST_MS = 2000;
const TOTAL_MS = 45000;
const REQUIRED = [
  'package.json',
  'src/store.mjs',
  'src/server.mjs',
  'bin/server.mjs',
  'README.md',
  'tests/smoke.test.mjs',
];
const children = new Set();
const abort = new AbortController();
const startedAt = performance.now();
const completed = [];
let assertions = 0;
let phase = 'layout';
let server;
let temporary;
let fatal;
let reported = false;

function check(condition, label) {
  if (!condition) throw new Error(label);
  assertions += 1;
}
function equal(actual, expected, label) {
  try {
    assert.deepStrictEqual(actual, expected);
  } catch {
    throw new Error(label);
  }
  assertions += 1;
}
function groupKill(child) {
  if (!child.pid) return;
  try {
    process.kill(-child.pid, 'SIGKILL');
  } catch (error) {
    if (error.code !== 'ESRCH') fatal ??= new Error('child cleanup failed');
  }
}
function failAll(label) {
  fatal ??= new Error(label);
  abort.abort();
  for (const record of children) groupKill(record.child);
}
function report(error) {
  if (reported) return;
  reported = true;
  process.stdout.write(
    JSON.stringify({
      ok: !error,
      assertions_passed: assertions,
      completed,
      duration_ms: Math.round(performance.now() - startedAt),
      ...(error ? { phase, failure: String(error.message).slice(0, 240) } : {}),
    }) + '\n'
  );
}
const totalTimer = setTimeout(() => failAll('total deadline exceeded'), TOTAL_MS);
const hardTimer = setTimeout(() => {
  failAll('hard cleanup deadline exceeded');
  report(fatal);
  process.exit(1);
}, TOTAL_MS + 5000);
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => failAll('oracle interrupted'));

async function stop(record) {
  if (!record) return;
  record.stopping = true;
  groupKill(record.child);
  await Promise.race([record.exited, new Promise((resolve) => setTimeout(resolve, 500))]);
  record.child.stdout.destroy();
  record.child.stderr.destroy();
  record.child.unref();
  children.delete(record);
}

async function launch(project, dataFile) {
  if (fatal) throw fatal;
  const child = spawn(process.execPath, [path.join(project, 'bin/server.mjs')], {
    cwd: project,
    env: {
      PATH: '/usr/local/bin:/usr/bin:/bin',
      HOME: path.join(temporary, 'home'),
      TMPDIR: temporary,
      PORT: '0',
      APP_DATA_FILE: dataFile,
      APP_TOKEN: TOKEN,
    },
    detached: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const record = { child, port: null, stopping: false, stdout: '', stdoutBytes: 0, stderrBytes: 0, fault: null };
  record.exited = new Promise((resolve) => child.once('exit', resolve));
  children.add(record);
  await new Promise((resolve, reject) => {
    let settled = false;
    const finish = (error) => {
      if (error) {
        record.fault ??= error;
        fatal ??= error;
        groupKill(child);
      }
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      abort.signal.removeEventListener('abort', interrupted);
      error ? reject(error) : resolve();
    };
    const interrupted = () => finish(fatal ?? new Error('startup interrupted'));
    const timer = setTimeout(() => finish(new Error('startup deadline exceeded')), STARTUP_MS);
    abort.signal.addEventListener('abort', interrupted, { once: true });
    child.on('error', () => finish(new Error('server launch failed')));
    child.on('exit', () => {
      if (!record.stopping) finish(new Error('server exited unexpectedly'));
    });
    child.stdout.on('data', (chunk) => {
      record.stdoutBytes += chunk.length;
      if (record.stdoutBytes > 16384) return finish(new Error('child stdout limit exceeded'));
      record.stdout += chunk.toString('utf8');
      const newline = record.stdout.indexOf('\n');
      if (newline < 0) return;
      if (record.stdout.slice(newline + 1) !== '') return finish(new Error('extra child stdout'));
      try {
        const value = JSON.parse(record.stdout.slice(0, newline));
        if (
          Object.keys(value).sort().join(',') !== 'port' ||
          !Number.isInteger(value.port) ||
          value.port < 1 ||
          value.port > 65535
        )
          throw new Error();
        record.port = value.port;
        finish();
      } catch {
        finish(new Error('invalid startup JSON line'));
      }
    });
    child.stderr.on('data', (chunk) => {
      record.stderrBytes += chunk.length;
      if (record.stderrBytes > 65536) finish(new Error('child stderr limit exceeded'));
    });
  });
  return record;
}

async function request(method, target, options = {}) {
  if (fatal) throw fatal;
  if (!server || server.fault) throw server?.fault ?? new Error('server unavailable');
  const auth = Object.hasOwn(options, 'auth') ? options.auth : TOKEN;
  const payload = Object.hasOwn(options, 'raw')
    ? options.raw
    : Object.hasOwn(options, 'body')
      ? JSON.stringify(options.body)
      : undefined;
  const headers = {
    ...(auth === null ? {} : { Authorization: `Bearer ${auth}` }),
    ...(payload === undefined ? {} : { 'Content-Type': 'application/json' }),
    ...options.headers,
  };
  if (options.chunked) headers['Transfer-Encoding'] = 'chunked';
  else if (payload !== undefined) headers['Content-Length'] = Buffer.byteLength(payload);
  const response = await new Promise((resolve, reject) => {
    const req = http.request(
      {
        hostname: '127.0.0.1',
        port: server.port,
        path: target,
        method,
        headers,
        signal: AbortSignal.any([abort.signal, AbortSignal.timeout(REQUEST_MS)]),
      },
      (res) => {
        const chunks = [];
        let size = 0;
        res.on('data', (chunk) => {
          size += chunk.length;
          if (size > 131072) {
            req.destroy();
            reject(new Error('HTTP response limit exceeded'));
          } else chunks.push(chunk);
        });
        res.on('error', () => reject(new Error('HTTP response interrupted')));
        res.on('end', () => {
          try {
            const type = String(res.headers['content-type'] ?? '')
              .split(';')[0]
              .trim()
              .toLowerCase();
            check(type === 'application/json', `${method} ${target}: JSON content type required`);
            resolve({ status: res.statusCode, body: JSON.parse(Buffer.concat(chunks).toString('utf8')) });
          } catch (error) {
            reject(error instanceof SyntaxError ? new Error('HTTP response is not JSON') : error);
          }
        });
      }
    );
    req.on('error', () => reject(new Error(`${method} ${target}: request failed or timed out`)));
    if (options.chunked && payload !== undefined) {
      const data = Buffer.from(payload);
      req.write(data.subarray(0, Math.floor(data.length / 2)));
      req.end(data.subarray(Math.floor(data.length / 2)));
    } else req.end(payload);
  });
  if (fatal) throw fatal;
  if (server.fault) throw server.fault;
  return response;
}

async function rejected(method, target, status, code, options) {
  const response = await request(method, target, options);
  equal(response.status, status, `${method} ${target}: expected ${status}`);
  equal(response.body, { error: code }, `${method} ${target}: error contract`);
}
function taskShape(task, label) {
  check(task !== null && typeof task === 'object' && !Array.isArray(task), `${label}: task object`);
  equal(Object.keys(task).sort(), ['done', 'id', 'title', 'version'], `${label}: exact task fields`);
  check(typeof task.id === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(task.id), `${label}: opaque ID`);
  check(
    typeof task.title === 'string' &&
      task.title === task.title.trim() &&
      task.title.length >= 1 &&
      task.title.length <= 80,
    `${label}: title`
  );
  check(
    typeof task.done === 'boolean' && Number.isSafeInteger(task.version) && task.version >= 1,
    `${label}: state/version`
  );
}
const tasks = new Map();
const order = [];
function expectedTasks(done) {
  return order.map((id) => tasks.get(id)).filter((task) => task && (done === undefined || task.done === done));
}
async function create(key, title, options = {}) {
  const response = await request('POST', '/tasks', {
    headers: { 'Idempotency-Key': key },
    body: { title },
    ...options,
  });
  equal(response.status, 201, 'fresh POST status');
  taskShape(response.body, 'fresh task');
  check(!order.includes(response.body.id), 'fresh task ID must never be reused');
  equal(response.body, { id: response.body.id, title: title.trim(), done: false, version: 1 }, 'fresh task content');
  tasks.set(response.body.id, response.body);
  order.push(response.body.id);
  return response.body;
}
async function list(query = '', done, offset = 0, limit = 20) {
  const response = await request('GET', `/tasks${query}`);
  equal(response.status, 200, 'list status');
  const all = expectedTasks(done);
  equal(response.body, { items: all.slice(offset, offset + limit), total: all.length }, 'stable filtered pagination');
}
async function replay(key, title, task) {
  const response = await request('POST', '/tasks', { headers: { 'Idempotency-Key': key }, body: { title } });
  equal(response.status, 200, 'idempotent POST status');
  equal(response.body, task, 'idempotent POST returns current task');
}
async function patch(task, done) {
  const response = await request('PATCH', `/tasks/${task.id}`, { body: { done, expected_version: task.version } });
  equal(response.status, 200, 'PATCH status');
  const updated = { ...task, done, version: task.version + 1 };
  equal(response.body, updated, 'PATCH exact version increment');
  tasks.set(task.id, updated);
  return updated;
}
async function restart(project, dataFile) {
  await stop(server); // SIGKILL, even after fully acknowledged writes.
  server = await launch(project, dataFile);
  const health = await request('GET', '/health', { auth: null });
  equal(health.status, 200, 'restart health status');
  equal(health.body, { ok: true }, 'restart health');
  await list('?limit=100', undefined, 0, 100);
  for (const task of expectedTasks()) {
    const response = await request('GET', `/tasks/${task.id}`);
    equal(response.status, 200, 'restart GET task status');
    equal(response.body, task, 'restart exact task durability');
  }
}

async function main() {
  check(process.platform === 'linux', 'run oracle inside the Linux runtime container');
  check(process.argv.length === 3, 'usage: node platform-project-oracle.mjs PROJECT_DIR');
  const project = path.resolve(process.argv[2]);
  for (const [directory, expected] of [
    ['', ['package.json', 'src', 'bin', 'README.md', 'tests']],
    ['src', ['store.mjs', 'server.mjs']],
    ['bin', ['server.mjs']],
    ['tests', ['smoke.test.mjs']],
  ]) {
    let info;
    let entries;
    try {
      info = await fs.lstat(path.join(project, directory));
      entries = await fs.readdir(path.join(project, directory));
    } catch {
      throw new Error(`missing project directory: ${directory || '.'}`);
    }
    check(info.isDirectory() && !info.isSymbolicLink(), `invalid project directory: ${directory || '.'}`);
    equal(
      entries.filter((name) => directory !== '' || name !== '.workshop-home').sort(),
      expected.sort(),
      `exact project layout: ${directory || '.'}`
    );
  }
  for (const relative of REQUIRED) {
    let info;
    try {
      info = await fs.lstat(path.join(project, relative));
    } catch {
      throw new Error(`missing required file: ${relative}`);
    }
    check(
      info.isFile() && !info.isSymbolicLink() && info.size > 0 && info.size <= 1048576,
      `invalid required file: ${relative}`
    );
  }
  let pkg;
  try {
    pkg = JSON.parse(await fs.readFile(path.join(project, 'package.json'), 'utf8'));
  } catch {
    throw new Error('invalid package.json');
  }
  equal(pkg.type, 'module', 'package module mode');
  equal(pkg.scripts?.start, 'node bin/server.mjs', 'package start script');
  equal(pkg.scripts?.test, 'node --test tests/smoke.test.mjs', 'package test script');
  for (const field of ['dependencies', 'devDependencies', 'optionalDependencies']) {
    check(
      pkg[field] === undefined ||
        (pkg[field] !== null &&
          !Array.isArray(pkg[field]) &&
          typeof pkg[field] === 'object' &&
          Object.keys(pkg[field]).length === 0),
      'no third-party dependencies'
    );
  }
  try {
    await fs.lstat(path.join(project, 'node_modules'));
    throw new Error('node_modules is forbidden');
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  completed.push(phase);
  temporary = await fs.mkdtemp('/tmp/platform-oracle-');
  await fs.mkdir(path.join(temporary, 'home'));
  const dataFile = path.join(temporary, 'state.json');
  phase = 'startup';
  server = await launch(project, dataFile);
  completed.push(phase);
  phase = 'health_auth';
  for (const auth of [null, 'wrong-dummy-token']) {
    const response = await request('GET', '/health', { auth });
    equal(response.status, 200, 'public health status');
    equal(response.body, { ok: true }, 'public health shape');
    await rejected('GET', '/tasks', 401, 'unauthorized', { auth });
    await rejected('POST', '/tasks', 401, 'unauthorized', {
      auth,
      headers: { 'Idempotency-Key': 'unauthorized' },
      body: { title: 'must not exist' },
    });
  }
  await rejected('GET', '/unknown', 404, 'not_found');
  await rejected('PUT', '/tasks', 405, 'method_not_allowed');
  await list();
  completed.push(phase);
  phase = 'strict_validation';
  await rejected('POST', '/tasks', 400, 'invalid_request', { body: { title: 'missing key' } });
  for (const key of ['', 'invalid key', 'x'.repeat(129)])
    await rejected('POST', '/tasks', 400, 'invalid_request', {
      headers: { 'Idempotency-Key': key },
      body: { title: 'bad key' },
    });
  for (const raw of [
    '',
    '{',
    'null',
    '[]',
    '"primitive"',
    'true',
    '{}',
    '{"title":2}',
    '{"title":null}',
    '{"title":"   "}',
    '{"title":"ok","extra":1}',
    JSON.stringify({ title: 'x'.repeat(81) }),
    JSON.stringify({ title: '🙂'.repeat(41) }),
  ]) {
    await rejected('POST', '/tasks', 400, 'invalid_request', {
      headers: { 'Idempotency-Key': 'invalid-then-valid' },
      raw,
    });
  }
  await rejected('POST', '/tasks', 415, 'unsupported_media_type', {
    headers: { 'Idempotency-Key': 'invalid-then-valid', 'Content-Type': 'text/plain' },
    raw: '{"title":"valid"}',
  });
  await rejected('POST', '/tasks?unexpected=1', 400, 'invalid_request', {
    headers: { 'Idempotency-Key': 'invalid-then-valid' },
    body: { title: 'valid' },
  });
  const oversized = JSON.stringify({ title: 'oversized' });
  await rejected('POST', '/tasks', 413, 'payload_too_large', {
    headers: { 'Idempotency-Key': 'invalid-then-valid' },
    raw: oversized + ' '.repeat(8193 - Buffer.byteLength(oversized)),
    chunked: true,
  });
  // Distinguish UTF-8 bytes from JavaScript character counts: this title is
  // valid, but the complete JSON body crosses the byte limit before 8192 chars.
  const unicodeOversized = JSON.stringify({ title: '🙂'.repeat(40) });
  await rejected('POST', '/tasks', 413, 'payload_too_large', {
    headers: { 'Idempotency-Key': 'invalid-then-valid' },
    raw: unicodeOversized + ' '.repeat(8193 - Buffer.byteLength(unicodeOversized)),
    chunked: true,
  });
  await list(); // No rejected request may have created a task.
  await create('invalid-then-valid', ' recovered ');
  const exact = JSON.stringify({ title: 'exact body limit' });
  await create('exact-limit', 'exact body limit', {
    raw: exact + ' '.repeat(8192 - Buffer.byteLength(exact)),
    chunked: true,
  });
  await create('max-title', 'x'.repeat(80));
  await create('unicode-title', '🙂'.repeat(40));
  completed.push(phase);

  phase = 'crud_versions';
  const alpha = await create('alpha-key', '  Alpha task  ');
  let beta = await create('beta-key', 'Beta task');
  const gamma = await create('gamma-key', 'Gamma task');
  await replay('alpha-key', ' Alpha task ', alpha);
  await rejected('POST', '/tasks', 409, 'conflict', {
    headers: { 'Idempotency-Key': 'alpha-key' },
    body: { title: 'different' },
  });
  await create('duplicate-title-key', 'Beta task');
  for (const task of [alpha, beta, gamma]) {
    const response = await request('GET', `/tasks/${task.id}`);
    equal(response.status, 200, 'GET task status');
    equal(response.body, task, 'GET exact task');
  }
  await rejected('GET', '/tasks/never-created', 404, 'not_found');
  await rejected('PATCH', '/tasks/never-created', 404, 'not_found', { body: { done: true, expected_version: 1 } });
  await rejected('DELETE', '/tasks/never-created', 404, 'not_found', { body: { expected_version: 1 } });
  for (const body of [
    {},
    { done: true },
    { expected_version: 1 },
    { done: 'false', expected_version: 1 },
    { done: true, expected_version: 0 },
    { done: true, expected_version: -1 },
    { done: true, expected_version: 1.5 },
    { done: true, expected_version: '1' },
    { done: true, expected_version: 9007199254740992 },
    { done: true, expected_version: 1, extra: true },
  ]) {
    await rejected('PATCH', `/tasks/${beta.id}`, 400, 'invalid_request', { body });
  }
  for (const body of [
    {},
    { expected_version: null },
    { expected_version: 0 },
    { expected_version: 1.5 },
    { expected_version: '1' },
    { expected_version: 1, extra: true },
  ]) {
    await rejected('DELETE', `/tasks/${gamma.id}`, 400, 'invalid_request', { body });
  }
  for (const method of ['PATCH', 'DELETE']) {
    const raw = JSON.stringify(
      method === 'PATCH' ? { done: true, expected_version: beta.version } : { expected_version: beta.version }
    );
    await rejected(method, `/tasks/${beta.id}`, 413, 'payload_too_large', {
      raw: raw + ' '.repeat(8193 - Buffer.byteLength(raw)),
      chunked: true,
    });
    await rejected(method, `/tasks/${beta.id}`, 401, 'unauthorized', { raw, auth: 'wrong-dummy-token' });
    await rejected(method, `/tasks/${beta.id}?unexpected=1`, 400, 'invalid_request', { raw });
  }
  await rejected('GET', `/tasks/${beta.id}?unexpected=1`, 400, 'invalid_request');
  await rejected('DELETE', `/tasks/${alpha.id}`, 409, 'conflict', { body: { expected_version: alpha.version + 1 } });
  beta = await patch(beta, true);
  await rejected('PATCH', `/tasks/${beta.id}`, 409, 'conflict', {
    body: { done: false, expected_version: beta.version - 1 },
  });
  beta = await patch(beta, true); // Even a no-op state change increments version.
  const patchResponses = await Promise.all(
    Array.from({ length: 6 }, () =>
      request('PATCH', `/tasks/${beta.id}`, { body: { done: true, expected_version: beta.version } })
    )
  );
  equal(patchResponses.filter((response) => response.status === 200).length, 1, 'one concurrent version update wins');
  const updatedBeta = { ...beta, version: beta.version + 1 };
  for (const response of patchResponses) {
    if (response.status === 200) equal(response.body, updatedBeta, 'concurrent PATCH result');
    else {
      equal(response.status, 409, 'concurrent stale PATCH status');
      equal(response.body, { error: 'conflict' }, 'concurrent stale PATCH error');
    }
  }
  beta = updatedBeta;
  tasks.set(beta.id, beta);
  await replay('beta-key', ' Beta task ', beta);
  completed.push(phase);

  phase = 'pagination';
  // More than 20 tasks distinguishes the default limit from an unbounded list.
  for (let index = 0; index < 13; index += 1) await create(`page-${index}`, `page task ${index}`);
  await list();
  await list('?offset=1&limit=2', undefined, 1, 2);
  await list('?done=true&offset=0&limit=1', true, 0, 1);
  await list('?done=false&offset=1&limit=2', false, 1, 2);
  await list('?offset=1000000&limit=100', undefined, 1000000, 100);
  for (const query of [
    'done=',
    'done=TRUE',
    'done=1',
    'done=true&done=false',
    'limit=0',
    'limit=101',
    'limit=-1',
    'limit=1.5',
    'limit=1e2',
    'limit=01',
    'limit=',
    'limit=1&limit=2',
    'offset=-1',
    'offset=1000001',
    'offset=1.5',
    'offset=01',
    'offset=',
    'offset=0&offset=1',
    'unknown=1',
  ]) {
    await rejected('GET', `/tasks?${query}`, 400, 'invalid_request');
  }
  completed.push(phase);

  phase = 'delete_replay';
  const deleted = await request('DELETE', `/tasks/${alpha.id}`, { body: { expected_version: alpha.version } });
  equal(deleted.status, 200, 'DELETE status');
  equal(deleted.body, { id: alpha.id, deleted: true }, 'DELETE result');
  tasks.delete(alpha.id);
  await rejected('GET', `/tasks/${alpha.id}`, 404, 'not_found');
  await rejected('DELETE', `/tasks/${alpha.id}`, 404, 'not_found', { body: { expected_version: alpha.version } });
  await rejected('PATCH', `/tasks/${alpha.id}`, 404, 'not_found', {
    body: { done: true, expected_version: alpha.version },
  });
  await rejected('POST', '/tasks', 409, 'conflict', {
    headers: { 'Idempotency-Key': 'alpha-key' },
    body: { title: 'Alpha task' },
  });
  await list('?limit=100', undefined, 0, 100);
  completed.push(phase);

  phase = 'restart_durability';
  await restart(project, dataFile);
  await replay('beta-key', 'Beta task', beta);
  await rejected('POST', '/tasks', 409, 'conflict', {
    headers: { 'Idempotency-Key': 'alpha-key' },
    body: { title: 'Alpha task' },
  });
  completed.push(phase);

  phase = 'concurrent_idempotency';
  const concurrent = await Promise.all(
    Array.from({ length: 12 }, () =>
      request('POST', '/tasks', {
        headers: { 'Idempotency-Key': 'concurrent-key' },
        body: { title: 'concurrent task' },
      })
    )
  );
  equal(concurrent.filter((response) => response.status === 201).length, 1, 'one concurrent POST creates');
  const winner = concurrent.find((response) => response.status === 201).body;
  taskShape(winner, 'concurrent task');
  equal(winner, { id: winner.id, title: 'concurrent task', done: false, version: 1 }, 'concurrent task shape');
  check(!order.includes(winner.id), 'concurrent task fresh ID');
  for (const response of concurrent) {
    check(response.status === 200 || response.status === 201, 'concurrent POST status');
    equal(response.body, winner, 'concurrent same-key result identity');
  }
  order.push(winner.id);
  tasks.set(winner.id, winner);
  await list('?limit=100', undefined, 0, 100);
  completed.push(phase);

  phase = 'second_restart';
  await restart(project, dataFile);
  await replay('concurrent-key', ' concurrent task ', winner);
  await replay('beta-key', 'Beta task', beta);
  await rejected('POST', '/tasks', 409, 'conflict', {
    headers: { 'Idempotency-Key': 'alpha-key' },
    body: { title: 'Alpha task' },
  });
  completed.push(phase);
  if (server.fault) throw server.fault;
}

let failure;
try {
  await main();
} catch (error) {
  failure = fatal ?? error;
} finally {
  for (const record of [...children]) await stop(record);
  if (temporary) {
    // Child processes are stopped before deleting their state. The hard guard
    // remains armed during cleanup, so a hostile file tree cannot hang forever.
    try {
      await fs.rm(temporary, { recursive: true, force: true });
    } catch {
      failure ??= new Error('temporary state cleanup failed');
    }
  }
  failure ??= fatal;
  clearTimeout(totalTimer);
  clearTimeout(hardTimer);
}
report(failure);
process.exitCode = failure ? 1 : 0;
