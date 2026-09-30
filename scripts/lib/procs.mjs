// Plumbing shared by the end-to-end scripts: builds, service processes with
// per-service logs, free loopback ports, bounded polling and the JSON report.
import { spawn, execFile } from 'node:child_process';
import { once } from 'node:events';
import { closeSync, openSync } from 'node:fs';
import { writeFile } from 'node:fs/promises';
import net from 'node:net';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

export const root = resolve(fileURLToPath(new URL('../..', import.meta.url)));
export const exec = promisify(execFile);
export const go = process.env.EASYGO_GO_BIN ?? 'go';
export const loopServer = join(root, 'services/agent-loop/dist/server.js');
export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
export const running = (record) => record.child.exitCode === null && record.child.signalCode === null;

// Builds the gateway and workshop binaries into dir, then the TypeScript loop in place.
export async function buildServices(dir, { loop = true } = {}) {
  await exec(go, ['build', '-o', join(dir, 'gateway'), './cmd/server'], { cwd: join(root, 'services/ai-gateway') });
  await exec(go, ['build', '-o', join(dir, 'workshop'), './cmd/server'], { cwd: join(root, 'services/workshop') });
  if (loop) await exec('npm', ['run', 'build'], { cwd: join(root, 'services/agent-loop') });
}

// Distinct free loopback ports: every listener stays open until all are allocated.
export async function ports(count) {
  const servers = [];
  try {
    for (let i = 0; i < count; i++) {
      const server = net.createServer();
      server.listen(0, '127.0.0.1');
      await once(server, 'listening');
      servers.push(server);
    }
    return servers.map((server) => server.address().port);
  } finally {
    await Promise.all(servers.map((server) => new Promise((resolve) => server.close(resolve))));
  }
}

// Polls fn until it returns something truthy. retryErrors treats a throw as "not yet"
// (a service that is still starting); check(label) runs before every poll and throws
// to give up early.
export async function until(
  fn,
  { timeout = 20_000, interval = 250, label = 'condition', retryErrors = false, check } = {}
) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    check?.(label);
    try {
      const result = await fn();
      if (result) return result;
    } catch (error) {
      if (!retryErrors) throw error;
      last = error;
    }
    await sleep(interval);
  }
  throw Error(`Timed out waiting for ${label}${last ? `: ${last.message}` : ''}`);
}

// The processes and report of one run. A service starts from the repository root with
// only PATH, LANG and the given environment, and logs stdout and stderr to
// <state>/<name>.log. Processes still running when Node exits are killed.
export function testbed(
  state,
  { env: shared = {}, stopTimeout = 12_000, report, reportFile = join(state, 'report.json'), reportMode, redact } = {}
) {
  const all = [],
    latest = new Map();
  process.on('exit', () => {
    for (const record of all) if (running(record)) record.child.kill('SIGKILL');
  });
  function start(name, bin, args, env = {}) {
    const fd = openSync(join(state, `${name}.log`), 'a', 0o600);
    const child = spawn(bin, args, {
      cwd: root,
      env: { PATH: process.env.PATH, LANG: 'C.UTF-8', ...shared, ...env },
      stdio: ['ignore', fd, fd],
    });
    closeSync(fd);
    const record = {
      name,
      bin,
      args,
      env,
      child,
      done: new Promise((resolve) => {
        child.once('error', (error) => resolve({ error: error.message }));
        child.once('exit', (code, signal) => resolve({ code, signal }));
      }),
    };
    all.push(record);
    latest.set(name, record);
    return record;
  }
  // Resolves to how the process ended: { code, signal }, { error } when it could not
  // start, or { error: 'shutdown_timeout' } when it had to be killed after stopTimeout.
  async function stop(record, signal = 'SIGTERM') {
    if (running(record)) record.child.kill(signal);
    let timer;
    const result = await Promise.race([
      record.done,
      new Promise((resolve) => {
        timer = setTimeout(() => {
          record.child.kill('SIGKILL');
          resolve({ error: 'shutdown_timeout' });
        }, stopTimeout);
      }),
    ]);
    clearTimeout(timer);
    return result;
  }
  // Stops every process started so far, newest first.
  async function stopAll() {
    const results = [];
    for (const record of [...all].reverse()) results.push({ name: record.name, ...(await stop(record)) });
    return results;
  }
  // Starts name again with the command and environment it was last started with.
  function relaunch(name) {
    const { bin, args, env } = latest.get(name);
    return start(name, bin, args, env);
  }
  const save = () =>
    writeFile(reportFile, JSON.stringify(redact ? redact(report) : report, null, 2), { mode: reportMode });
  return { all, start, stop, stopAll, relaunch, get: (name) => latest.get(name), save };
}
