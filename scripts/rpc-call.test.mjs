import test from 'node:test';
import assert from 'node:assert/strict';
import https from 'node:https';
import { readFileSync, mkdtempSync, statSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash, X509Certificate } from 'node:crypto';
import { once } from 'node:events';
import { createRPCClient } from './rpc-call.mjs';

test('operator client proves both identities, pins server, parses streams and refuses key overwrite', async (t) => {
  const temp = mkdtempSync(join(tmpdir(), 'easygo-pki-test-'));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const pki = join(temp, 'pki');
  execFileSync('bash', [new URL('./dev-pki.sh', import.meta.url).pathname, pki]);
  const fingerprint = (pem) => createHash('sha256').update(new X509Certificate(pem).raw).digest('hex');
  const publicPath = (name) => join(pki, 'public', `${name}.crt`);
  const key = readFileSync(join(pki, 'client', 'tls.key'));
  assert.equal(statSync(join(pki, 'client', 'tls.key')).mode & 0o777, 0o600);
  const overwrite = spawnSync('bash', [new URL('./dev-pki.sh', import.meta.url).pathname, pki]);
  assert.equal(overwrite.status, 1);
  assert.deepEqual(readFileSync(join(pki, 'client', 'tls.key')), key);
  assert.equal(
    new Set(['ai-gateway', 'agent-loop', 'workshop', 'client'].map((n) => fingerprint(readFileSync(publicPath(n)))))
      .size,
    4
  );

  let executions = 0;
  const trustedClient = fingerprint(readFileSync(publicPath('client')));
  const server = https.createServer(
    {
      key: readFileSync(join(pki, 'ai-gateway', 'tls.key')),
      cert: readFileSync(publicPath('ai-gateway')),
      ca: readFileSync(publicPath('ca')),
      requestCert: true,
      rejectUnauthorized: true,
      minVersion: 'TLSv1.3',
    },
    async (request, response) => {
      let raw = '';
      for await (const chunk of request) raw += chunk;
      const payload = raw ? JSON.parse(raw) : null;
      if (!request.socket.authorized || fingerprint(request.socket.getPeerCertificate().raw) !== trustedClient) {
        response.writeHead(403, { 'Content-Type': 'application/json' });
        response.end(
          JSON.stringify({ jsonrpc: '2.0', id: payload?.id ?? null, error: { code: -32003, message: 'Forbidden' } })
        );
        return;
      }
      executions++;
      if (request.url === '/healthz') {
        response.end('{"status":"ok"}');
        return;
      }
      const result = { jsonrpc: '2.0', id: payload.id, result: { ok: true } };
      if (payload.params.malformed) {
        const duplicate = `{"jsonrpc":"2.0","id":${JSON.stringify(payload.id)},"error":{"code":-32603,"code":-32602,"message":"rejected"}}`;
        const validError = JSON.stringify({
          jsonrpc: '2.0',
          id: payload.id,
          error: { code: -32602, message: 'rejected' },
        });
        const mode = payload.params.malformed;
        const stream = mode.startsWith('sse-');
        response.setHeader('Content-Type', stream ? 'text/event-stream' : 'application/json');
        if (stream) {
          response.write(`event: error\ndata: ${mode === 'sse-duplicate' ? duplicate : validError}\n\n`);
          if (mode === 'sse-utf8') response.write(Buffer.from([0xff]));
        } else if (mode === 'duplicate') response.write(duplicate);
        else {
          response.write(Buffer.from(`{"jsonrpc":"2.0","id":${JSON.stringify(payload.id)},"result":"`));
          response.write(Buffer.from([0xff]));
          response.write('"}');
        }
        response.end();
      } else if (payload.params.stream) {
        response.writeHead(200, { 'Content-Type': 'text/event-stream' });
        const frame = Buffer.from(
          `event: delta\ndata: ${JSON.stringify({ jsonrpc: '2.0', method: 'gateway.delta', params: { id: payload.id, event: { type: 'text_delta', delta: '你好' } } })}\n\n`
        );
        const split = frame.indexOf(Buffer.from('你好')) + 1;
        response.write(frame.subarray(0, split));
        await new Promise((resolve) => setImmediate(resolve));
        response.write(frame.subarray(split));
        if (!payload.params.truncate) response.write(`event: result\ndata: ${JSON.stringify(result)}\n\n`);
        response.end();
      } else {
        response.setHeader('Content-Type', 'application/json');
        response.end(JSON.stringify(result));
      }
    }
  );
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  const url = `https://127.0.0.1:${server.address().port}/rpc`;
  const configuration = {
    url,
    certFile: publicPath('client'),
    keyFile: join(pki, 'client', 'tls.key'),
    caFile: publicPath('ca'),
    peerCertificateFile: publicPath('ai-gateway'),
    timeoutMs: 5_000,
  };
  const client = createRPCClient(configuration);
  t.after(() => client.close());
  assert.deepEqual(await client.health(), { status: 'ok' });
  assert.deepEqual(await client.call('fixture', { namespace: 'demo' }), { ok: true });
  const deltas = [];
  assert.deepEqual(await client.call('fixture', { stream: true }, { onDelta: (event) => deltas.push(event.delta) }), {
    ok: true,
  });
  assert.deepEqual(deltas, ['你好']);
  await assert.rejects(client.call('fixture', { stream: true, truncate: true }), /terminal response/);
  for (const malformed of ['duplicate', 'utf8', 'sse-duplicate', 'sse-utf8']) {
    await assert.rejects(client.call('fixture', { malformed }), (error) => error.code !== -32602);
  }
  await assert.rejects(client.call('fixture', { malformed: 'sse-valid' }), (error) => error.code === -32602);
  const count = executions;
  const wrongPeer = createRPCClient({ ...configuration, peerCertificateFile: publicPath('workshop') });
  t.after(() => wrongPeer.close());
  await assert.rejects(wrongPeer.call('fixture', {}), /configured peer/);
  const unauthorized = createRPCClient({
    ...configuration,
    certFile: publicPath('workshop'),
    keyFile: join(pki, 'workshop', 'tls.key'),
  });
  t.after(() => unauthorized.close());
  await assert.rejects(unauthorized.call('fixture', {}), (error) => error.code === -32003);
  assert.equal(executions, count);
  await assert.rejects(
    new Promise((resolve, reject) => {
      const request = https.get(
        url,
        { ca: readFileSync(publicPath('ca')), rejectUnauthorized: true, agent: false },
        resolve
      );
      request.on('error', reject);
    })
  );
  assert.equal(executions, count);
});
