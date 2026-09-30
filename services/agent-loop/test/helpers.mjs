import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import https from 'node:https';
import { once } from 'node:events';
import { setTimeout } from 'node:timers/promises';
import { startServer } from '../dist/server.js';
import { RpcClient, tlsOptions, Authorizer } from '../dist/rpc.js';
import { METHODS } from '../dist/types.js';

export function certificates() {
  const dir = mkdtempSync(join(tmpdir(), 'agent-loop-test-'));
  const run = (...args) => execFileSync('openssl', args, { cwd: dir, stdio: 'ignore' });
  for (const ca of ['ca', 'rogue-ca'])
    run(
      'req',
      '-x509',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-keyout',
      `${ca}.key`,
      '-out',
      `${ca}.crt`,
      '-subj',
      `/CN=${ca}`,
      '-days',
      '1'
    );
  writeFileSync(
    join(dir, 'extensions'),
    'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth,clientAuth\n'
  );
  for (const name of ['loop', 'gateway', 'workshop', 'client', 'limited', 'stranger', 'rogue']) {
    run(
      'req',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-keyout',
      `${name}.key`,
      '-out',
      `${name}.csr`,
      '-subj',
      `/CN=${name}`
    );
    const ca = name === 'rogue' ? 'rogue-ca' : 'ca';
    run(
      'x509',
      '-req',
      '-in',
      `${name}.csr`,
      '-CA',
      `${ca}.crt`,
      '-CAkey',
      `${ca}.key`,
      '-CAcreateserial',
      '-out',
      `${name}.crt`,
      '-days',
      '1',
      '-extfile',
      'extensions'
    );
  }
  return {
    dir,
    cert: (n) => join(dir, `${n}.crt`),
    tls: (n) => ({ cert_file: join(dir, `${n}.crt`), key_file: join(dir, `${n}.key`), ca_file: join(dir, 'ca.crt') }),
    close: () => rmSync(dir, { recursive: true, force: true }),
  };
}
export const response = (text = 'done', blocks) => ({
  model: 'fixture',
  message: { role: 'assistant', content: blocks ?? [{ type: 'text', text }] },
  finish_reason: blocks?.some((b) => b.type === 'tool_call') ? 'tool_calls' : 'stop',
  usage: { known: true, input_tokens: 7, output_tokens: 3, cache_read_tokens: 2 },
  cost: { known: true, currency: 'USD', amount: 0.001 },
});
export const tool = (name, args, id = 'call-1') => ({ type: 'tool_call', id, name, arguments: args });
export const deferred = () => {
  let resolve;
  const promise = new Promise((r) => {
    resolve = r;
  });
  return { promise, resolve };
};
export async function until(fn, timeout = 5000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = await fn();
    if (value) return value;
    await setTimeout(15);
  }
  throw new Error('Condition timed out');
}
export function json(res, id, result) {
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ jsonrpc: '2.0', id, result }));
}
export async function sse(res, id, result) {
  res.writeHead(200, { 'content-type': 'text/event-stream' });
  const data = `event: delta\ndata: ${JSON.stringify({ jsonrpc: '2.0', method: 'gateway.delta', params: { id, event: { type: 'text_delta', delta: '片' } } })}\n\nevent: result\ndata: ${JSON.stringify({ jsonrpc: '2.0', id, result })}\n\n`;
  const bytes = Buffer.from(data);
  // Split both UTF8 codepoints and frame delimiters across transport chunks.
  for (let i = 0; i < bytes.length; i += 17) {
    if (res.destroyed) return;
    res.write(bytes.subarray(i, i + 17));
    await setTimeout(1);
  }
  res.end();
}
export async function fixture(pki, name, handler) {
  const authorization = new Authorizer([
    { id: 'loop', cert_file: pki.cert('loop'), methods: ['*'], namespaces: ['*'] },
  ]);
  const calls = [];
  const server = https.createServer({ ...tlsOptions(pki.tls(name)), requestCert: true }, async (req, res) => {
    try {
      const chunks = [];
      for await (const c of req) chunks.push(c);
      const body = JSON.parse(Buffer.concat(chunks).toString());
      authorization.authorize(req.socket, body.method, body.params.namespace);
      calls.push(body);
      await handler(body, res, req);
    } catch {
      if (!res.destroyed) {
        res.writeHead(500);
        res.end();
      }
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return {
    calls,
    server,
    endpoint: { url: `https://localhost:${server.address().port}/rpc`, peer_certificate_file: pki.cert(name) },
    close: () =>
      new Promise((r) => {
        server.close(r);
        server.closeAllConnections();
      }),
  };
}
export async function harness(pki, options = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'agent-loop-db-'));
  const gateway = await fixture(
    pki,
    'gateway',
    options.gateway ??
      (async (body, res) => {
        if (body.params.stream) await sse(res, body.id, response());
        else json(res, body.id, response());
      })
  );
  const workshop = await fixture(
    pki,
    'workshop',
    options.workshop ??
      ((body, res) => {
        const task = { id: body.params.task_id ?? 'task', namespace: body.params.namespace, status: 'succeeded' };
        const result =
          body.method === 'workshop.list'
            ? { tasks: [task], offset: body.params.offset, next_offset: null }
            : body.method === 'workshop.result'
              ? {
                  task_id: task.id,
                  run_id: body.params.run_id ?? 'attempt',
                  text: '',
                  offset: 0,
                  next_offset: 0,
                  total_bytes: 0,
                  eof: true,
                }
              : body.method === 'workshop.workflows'
                ? []
                : task;
        json(res, body.id, result);
      })
  );
  const config = {
    listen: '127.0.0.1:0',
    tls: pki.tls('loop'),
    database: join(dir, 'agent.sqlite'),
    authorization: [
      { id: 'client', cert_file: pki.cert('client'), methods: [...METHODS, 'health'], namespaces: ['demo', 'other'] },
      { id: 'limited', cert_file: pki.cert('limited'), methods: ['agent.session.list'], namespaces: ['demo'] },
      { id: 'loop', cert_file: pki.cert('loop'), methods: ['health'], namespaces: [] },
    ],
    gateway: gateway.endpoint,
    workshop: workshop.endpoint,
    streaming: false,
    ...options.config,
  };
  let running;
  try {
    running = await startServer(config);
  } catch (error) {
    // Leaving the fixtures listening keeps the test file alive until its timeout.
    await gateway.close();
    await workshop.close();
    rmSync(dir, { recursive: true, force: true });
    throw error;
  }
  const endpoint = { url: `https://localhost:${running.address.port}/rpc`, peer_certificate_file: pki.cert('loop') };
  const client = new RpcClient(pki.tls('client'), endpoint);
  return {
    dir,
    config,
    running,
    client,
    endpoint,
    gateway,
    workshop,
    call: (method, params = {}) => client.call(method, { namespace: 'demo', ...params }),
    close: async () => {
      client.close();
      await running.close();
      await gateway.close();
      await workshop.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}
export async function start(h, input = 'hello', key = 'key', session) {
  session ??= (await h.call('agent.session.create')).id;
  return h.call('agent.run.start', { session_id: session, input, idempotency_key: key });
}
export async function terminal(h, id) {
  return until(async () => {
    const r = await h.call('agent.run.get', { run_id: id });
    return !['queued', 'running'].includes(r.status) && r;
  });
}
export function raw(pki, endpoint, body, identity = 'client', path = '/rpc') {
  return new Promise((resolve, reject) => {
    const options = identity
      ? tlsOptions(pki.tls(identity))
      : { ca: readFileSync(pki.cert('ca')), minVersion: 'TLSv1.3', rejectUnauthorized: true };
    const req = https.request(
      new URL(path, endpoint.url),
      { ...options, method: path === '/healthz' ? 'GET' : 'POST', headers: { 'content-type': 'application/json' } },
      (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks).toString() }));
      }
    );
    req.on('error', reject);
    req.end(body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body));
  });
}
