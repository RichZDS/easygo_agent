#!/usr/bin/env node
// Small operator client, also used by the real three-process integration proof.
import https from 'node:https';
import tls from 'node:tls';
import { readFileSync } from 'node:fs';
import { createHash, randomUUID, timingSafeEqual, X509Certificate } from 'node:crypto';
import { parseJSON } from '../services/agent-loop/src/strict-json.mjs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export class RPCError extends Error {
  constructor(error) {
    super(error.message);
    this.name = 'RPCError';
    this.code = error.code;
    this.data = error.data;
  }
}

export function createRPCClient(config) {
  const endpoint = new URL(config.url);
  if (endpoint.protocol !== 'https:' || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) {
    throw new Error('RPC endpoint must be HTTPS without embedded credentials, query or fragment');
  }
  const digest = value => createHash('sha256').update(value).digest();
  const expected = digest(new X509Certificate(readFileSync(config.peerCertificateFile)).raw);
  const agent = new https.Agent({
    keepAlive: true,
    maxSockets: 16,
    cert: readFileSync(config.certFile),
    key: readFileSync(config.keyFile),
    ca: readFileSync(config.caFile),
    minVersion: 'TLSv1.3',
    rejectUnauthorized: true,
    checkServerIdentity(host, certificate) {
      const hostnameError = tls.checkServerIdentity(host, certificate);
      if (hostnameError) return hostnameError;
      if (!certificate.raw || !timingSafeEqual(expected, digest(certificate.raw))) {
        return new Error('RPC server certificate does not match the configured peer');
      }
      return undefined;
    },
  });
  const timeoutMs = config.timeoutMs ?? 120_000;
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) throw new Error('timeout must be positive milliseconds');

  function envelope(value, id) {
    if (!value || value.jsonrpc !== '2.0' || value.id !== id || (Object.hasOwn(value, 'result') === Object.hasOwn(value, 'error'))) {
      throw new Error('Invalid or mismatched RPC response');
    }
    if (Object.hasOwn(value, 'error')) {
      if (!Number.isInteger(value.error?.code) || typeof value.error?.message !== 'string') throw new Error('Invalid RPC error');
      throw new RPCError(value.error);
    }
    return value.result;
  }

  function exchange(method, params, options = {}) {
    const id = options.id ?? randomUUID();
    if (typeof id !== 'string' || !id || Buffer.byteLength(id) > 128) throw new Error('Invalid RPC request id');
    const target = new URL(endpoint);
    const health = method === null;
    if (health) target.pathname = '/healthz';
    const body = health ? null : Buffer.from(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
    if (body && body.length > 16 * 1024 * 1024) throw new Error('RPC request too large');
    return new Promise((resolveCall, rejectCall) => {
      let settled = false;
      let timer;
      const finish = (error, value) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (error) rejectCall(error); else resolveCall(value);
      };
      const request = https.request(target, {
        agent,
        method: health ? 'GET' : 'POST',
        signal: options.signal,
        headers: body ? { 'Content-Type': 'application/json', 'Content-Length': body.length } : {},
      }, response => {
        const isSSE = String(response.headers['content-type'] ?? '').startsWith('text/event-stream');
        let bytes = 0;
        let buffer = '';
        let terminal = false;
        let result;
        let terminalError;
        const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
        const consume = frame => {
          const lines = frame.split('\n');
          let event = '';
          const data = [];
          for (const raw of lines) {
            const line = raw.replace(/\r$/, '');
            if (line.startsWith('event:')) event = line.slice(6).trim();
            if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''));
          }
          if (!data.length) return;
          if (terminal) throw new Error('RPC event after terminal response');
          const message = parseJSON(data.join('\n'));
          if (event === 'delta') {
            if (message.jsonrpc !== '2.0' || message.method !== 'gateway.delta' || message.params?.id !== id || !message.params.event) {
              throw new Error('Invalid RPC delta');
            }
            options.onDelta?.(message.params.event);
          } else if (event === 'result' || event === 'error') {
            try { result = envelope(message, id); }
            catch (error) { if (!(error instanceof RPCError)) throw error; terminalError = error; }
            terminal = true;
          } else {
            throw new Error('Unknown RPC stream event');
          }
        };
        response.on('data', chunk => {
          if (settled) return;
          try {
            bytes += chunk.length;
            if (bytes > (isSSE ? 64 : 16) * 1024 * 1024) throw new Error('RPC response too large');
            buffer += decoder.decode(chunk, { stream: true });
            if (isSSE) {
              buffer = buffer.replace(/\r\n/g, '\n');
              let separator;
              while ((separator = buffer.indexOf('\n\n')) >= 0) {
                const frame = buffer.slice(0, separator);
                if (Buffer.byteLength(frame) > 1024 * 1024) throw new Error('RPC event too large');
                buffer = buffer.slice(separator + 2);
                consume(frame);
              }
              if (Buffer.byteLength(buffer) > 1024 * 1024) throw new Error('RPC event too large');
            }
          } catch (error) {
            finish(error);
            response.destroy();
            request.destroy();
          }
        });
        response.on('end', () => {
          if (settled) return;
          try {
            buffer += decoder.decode();
            if (isSSE) {
              if (buffer.trim() || !terminal) throw new Error('RPC stream ended without a complete terminal response');
            } else {
              const value = parseJSON(buffer);
              if (health) {
                if (response.statusCode !== 200 || value?.status !== 'ok') throw new Error('Service is not healthy');
                result = value;
              } else {
                result = envelope(value, id);
              }
            }
            if (response.statusCode !== 200) throw new Error(`Unexpected RPC HTTP status ${response.statusCode}`);
            if (terminalError) throw terminalError;
            finish(null, result);
          } catch (error) { finish(error); }
        });
        response.on('aborted', () => finish(new Error('RPC response aborted')));
        response.on('error', error => finish(error));
      });
      timer = setTimeout(() => request.destroy(new Error('RPC deadline exceeded')), timeoutMs);
      request.on('error', error => finish(error));
      request.end(body);
    });
  }
  return {
    call: (method, params, options) => exchange(method, params, options),
    health: options => exchange(null, null, options),
    close: () => agent.destroy(),
  };
}

async function main() {
  const args = {};
  const names = new Set(['url', 'cert', 'key', 'ca', 'peer', 'method', 'params', 'timeout-ms']);
  for (let i = 2; i < process.argv.length; i++) {
    const key = process.argv[i];
    if (key === '--health') { args.health = true; continue; }
    if (!key.startsWith('--') || !names.has(key.slice(2)) || process.argv[i + 1] === undefined) {
      throw new Error('Usage: rpc-call.mjs --url HTTPS_RPC --cert CLIENT_CERT --key CLIENT_KEY --ca CA_CERT --peer SERVER_CERT --method METHOD --params JSON [--health]');
    }
    args[key.slice(2)] = process.argv[++i];
  }
  for (const name of ['url', 'cert', 'key', 'ca', 'peer']) if (!args[name]) throw new Error(`--${name} is required`);
  if (!args.health && !args.method) throw new Error('--method is required');
  const client = createRPCClient({
    url: args.url, certFile: args.cert, keyFile: args.key, caFile: args.ca, peerCertificateFile: args.peer,
    timeoutMs: args['timeout-ms'] ? Number(args['timeout-ms']) : undefined,
  });
  try {
    const result = args.health ? await client.health() : await client.call(args.method, parseJSON(args.params ?? '{}'), {
      onDelta: event => process.stdout.write(`${JSON.stringify({ event: 'delta', data: event })}\n`),
    });
    process.stdout.write(`${JSON.stringify({ result })}\n`);
  } finally { client.close(); }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => {
    process.stderr.write(`${JSON.stringify({ error: { message: error.message, code: error.code, data: error.data } })}\n`);
    process.exitCode = 1;
  });
}
