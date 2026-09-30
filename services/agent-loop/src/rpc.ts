import https from 'node:https';
import { checkServerIdentity, type TLSSocket } from 'node:tls';
import { X509Certificate, createHash, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { parseJSON } from './strict-json.mjs';
import type { Authorization, Endpoint, TLSConfig } from './types.js';
import { object, RpcError } from './validation.js';

function fingerprint(raw: Buffer): string {
  return createHash('sha256').update(raw).digest('hex');
}
function certFingerprint(file: string): string {
  return fingerprint(new X509Certificate(readFileSync(file)).raw);
}
export function tlsOptions(tls: TLSConfig) {
  return {
    cert: readFileSync(tls.cert_file),
    key: readFileSync(tls.key_file),
    ca: readFileSync(tls.ca_file),
    minVersion: 'TLSv1.3' as const,
    rejectUnauthorized: true,
  };
}
export class Authorizer {
  private entries: Array<Authorization & { fingerprint: string }>;
  constructor(entries: Authorization[]) {
    this.entries = entries.map((e) => ({ ...e, fingerprint: certFingerprint(e.cert_file) }));
  }
  authorize(socket: TLSSocket, method: string, ns?: string): string {
    if (!socket.authorized) throw new RpcError(-32003, 'forbidden');
    const raw = socket.getPeerCertificate().raw;
    const peer = raw && fingerprint(raw);
    const entry = this.entries.find(
      (e) =>
        e.fingerprint === peer &&
        (e.methods.includes(method) || e.methods.includes('*')) &&
        (ns === undefined || e.namespaces.includes(ns) || e.namespaces.includes('*'))
    );
    if (!entry) throw new RpcError(-32003, 'forbidden');
    return entry.id;
  }
}

const MAX_RESPONSE = 16 * 1024 * 1024;
const MAX_FRAME = 1024 * 1024;
export class RpcClient {
  private agent: https.Agent;
  private url: URL;
  constructor(
    tls: TLSConfig,
    endpoint: Endpoint,
    private timeoutMs = 120_000
  ) {
    this.url = new URL(endpoint.url);
    if (
      this.url.protocol !== 'https:' ||
      this.url.pathname !== '/rpc' ||
      this.url.search ||
      this.url.hash ||
      this.url.username ||
      this.url.password
    )
      throw new Error('RPC endpoint must be an HTTPS /rpc URL');
    const pinned = certFingerprint(endpoint.peer_certificate_file);
    this.agent = new https.Agent({
      ...tlsOptions(tls),
      keepAlive: false,
      maxCachedSessions: 0,
      checkServerIdentity(host, cert) {
        const error = checkServerIdentity(host, cert);
        if (error) return error;
        if (!cert.raw || fingerprint(cert.raw) !== pinned) return new Error('RPC peer certificate mismatch');
      },
    });
  }
  close() {
    this.agent.destroy();
  }
  async call<T = unknown>(
    method: string,
    params: Record<string, unknown>,
    signal?: AbortSignal,
    onDelta?: (event: unknown) => void
  ): Promise<T> {
    const id = randomUUID();
    const body = JSON.stringify({ jsonrpc: '2.0', id, method, params });
    const deadline = AbortSignal.timeout(this.timeoutMs);
    const combined = signal ? AbortSignal.any([signal, deadline]) : deadline;
    combined.throwIfAborted();
    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const finish = (err?: unknown, value?: T) => {
        if (settled) return;
        settled = true;
        if (err) reject(err);
        else resolve(value as T);
      };
      const req = https.request(
        this.url,
        {
          method: 'POST',
          agent: this.agent,
          signal: combined,
          headers: { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body) },
        },
        (res) => {
          let bytes = 0;
          let buffer = '';
          const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
          const streaming = res.headers['content-type']?.split(';')[0] === 'text/event-stream';
          let terminal = false;
          let terminalError: RpcError | undefined;
          let result: T | undefined;
          const envelope = (value: unknown): T => {
            const o = object(value);
            if (o.jsonrpc !== '2.0' || o.id !== id || 'result' in o === 'error' in o)
              throw new RpcError(-32000, 'invalid_upstream_envelope');
            if ('error' in o) {
              const e = object(o.error);
              if (!Number.isSafeInteger(e.code) || typeof e.message !== 'string')
                throw new RpcError(-32000, 'invalid_upstream_error');
              // Error messages and arbitrary data may contain provider bodies or secrets.
              // Preserve only the numeric RPC code and a bounded machine-readable code.
              const data =
                e.data && typeof e.data === 'object' && !Array.isArray(e.data)
                  ? (e.data as Record<string, unknown>)
                  : undefined;
              const reason =
                typeof data?.code === 'string' && /^[a-z][a-z0-9_]{0,63}$/.test(data.code) ? data.code : undefined;
              throw new RpcError(-32000, 'upstream_error', 'Upstream RPC failed', {
                code: e.code,
                ...(reason ? { data: { code: reason } } : {}),
              });
            }
            if (res.statusCode !== 200) throw new RpcError(-32000, 'upstream_http_error');
            return o.result as T;
          };
          const frame = (value: string) => {
            if (Buffer.byteLength(value) > MAX_FRAME) throw new RpcError(-32000, 'upstream_frame_too_large');
            const lines = value.split(/\r?\n/);
            const eventLines = lines.filter((l) => l.startsWith('event:'));
            const data = lines
              .filter((l) => l.startsWith('data:'))
              .map((l) => l.slice(5).replace(/^ /, ''))
              .join('\n');
            if (!data && eventLines.length === 0) return; // SSE comments/heartbeats
            if (terminal || eventLines.length !== 1) throw new RpcError(-32000, 'invalid_upstream_stream');
            const event = eventLines[0]!.slice(6).trim();
            const valueParsed: unknown = parseJSON(data);
            if (event === 'delta') {
              const o = object(valueParsed);
              const p = object(o.params);
              if (o.jsonrpc !== '2.0' || o.method !== 'gateway.delta' || p.id !== id || !onDelta)
                throw new RpcError(-32000, 'invalid_upstream_delta');
              const delta = object(p.event);
              if (
                !['text_delta', 'reasoning_delta', 'tool_call_delta'].includes(String(delta.type)) ||
                (delta.index !== undefined &&
                  (!Number.isSafeInteger(delta.index) || Number(delta.index) < 0 || Number(delta.index) >= 128)) ||
                (delta.delta !== undefined && typeof delta.delta !== 'string') ||
                ['id', 'name'].some(
                  (k) =>
                    delta[k] !== undefined &&
                    (typeof delta[k] !== 'string' || Buffer.byteLength(String(delta[k])) > 128)
                )
              ) {
                throw new RpcError(-32000, 'invalid_upstream_delta');
              }
              onDelta(delta);
            } else if (event === 'result' || event === 'error') {
              const o = object(valueParsed);
              if ((event === 'result') !== 'result' in o) throw new RpcError(-32000, 'invalid_upstream_stream');
              try {
                result = envelope(o);
              } catch (error) {
                // A safe rejection is trusted only once the entire SSE stream has
                // ended cleanly; a malformed tail must not permit another write.
                if (event !== 'error' || !(error instanceof RpcError) || error.reason !== 'upstream_error') throw error;
                terminalError = error;
              }
              terminal = true;
            } else throw new RpcError(-32000, 'invalid_upstream_stream');
          };
          const consume = () => {
            let boundary: RegExpExecArray | null;
            while ((boundary = /\r?\n\r?\n/.exec(buffer))) {
              const part = buffer.slice(0, boundary.index);
              buffer = buffer.slice(boundary.index + boundary[0].length);
              frame(part);
            }
            if (Buffer.byteLength(buffer) > MAX_FRAME) throw new RpcError(-32000, 'upstream_frame_too_large');
          };
          res.on('data', (chunk: Buffer) => {
            try {
              combined.throwIfAborted();
              bytes += chunk.length;
              if (bytes > MAX_RESPONSE) throw new RpcError(-32000, 'upstream_response_too_large');
              buffer += decoder.decode(chunk, { stream: true });
              if (streaming) consume();
            } catch (error) {
              finish(error);
              req.destroy();
              res.destroy();
            }
          });
          res.on('end', () => {
            if (settled) return;
            try {
              combined.throwIfAborted();
              buffer += decoder.decode();
              if (streaming) {
                consume();
                if (buffer.trim() || !terminal) throw new RpcError(-32000, 'incomplete_upstream_stream');
                finish(terminalError, result);
              } else {
                if (res.headers['content-type']?.split(';')[0] !== 'application/json')
                  throw new RpcError(-32000, 'invalid_upstream_content_type');
                finish(undefined, envelope(parseJSON(buffer)));
              }
            } catch (error) {
              finish(error);
            }
          });
          res.on('error', (error) => finish(error));
          res.on('aborted', () => finish(new RpcError(-32000, 'incomplete_upstream_response')));
        }
      );
      req.on('error', (error) => finish(combined.aborted ? combined.reason : error));
      req.end(body);
    });
  }
}
