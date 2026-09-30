import type { IncomingMessage } from 'node:http';
import type { Server } from 'node:net';
import { parseJSON } from './strict-json.mjs';
import { RpcError } from './validation.js';

type Failure = readonly [code: number, reason: string];
export interface BodyErrors {
  contentType: Failure;
  tooLarge: Failure;
  invalidJson: Failure;
}
// The two entry points report the same body problems with different codes on purpose.
// /rpc speaks JSON-RPC 2.0 to services: a bad transport envelope is an invalid request
// (-32600) and an unparsable body is a parse error (-32700), as that spec defines them.
export const RPC_BODY_ERRORS: BodyErrors = {
  contentType: [-32600, 'invalid_content_type'],
  tooLarge: [-32600, 'request_too_large'],
  invalidJson: [-32700, 'parse_error'],
};
// /api/* serves the browser console: every body problem is bad input (-32602), which the
// platform turns into HTTP 400 with the reason as the error code the console shows.
export const API_BODY_ERRORS: BodyErrors = {
  contentType: [-32602, 'json_required'],
  tooLarge: [-32602, 'body_too_large'],
  invalidJson: [-32602, 'invalid_json'],
};

// Reads a bounded application/json body and parses it strictly. The decoder keeps a
// leading byte order mark (ignoreBOM), so the strict parser rejects it on both entry points.
export async function readJsonBody(
  req: IncomingMessage,
  { maxBytes, errors }: { maxBytes: number; errors: BodyErrors }
): Promise<unknown> {
  const fail = ([code, reason]: Failure) => new RpcError(code, reason);
  if (req.headers['content-type']?.split(';')[0] !== 'application/json') throw fail(errors.contentType);
  if (Number(req.headers['content-length'] ?? 0) > maxBytes) throw fail(errors.tooLarge);
  const chunks: Buffer[] = [];
  let bytes = 0;
  for await (const chunk of req) {
    bytes += chunk.length;
    if (bytes > maxBytes) throw fail(errors.tooLarge);
    chunks.push(chunk);
  }
  try {
    return parseJSON(new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Buffer.concat(chunks)));
  } catch {
    throw fail(errors.invalidJson);
  }
}

export interface ListenAddress {
  host: string;
  port: number;
}
// `host:port`, `[v6]:port` or `:port` (which binds defaultHost).
export function parseListen(value: string, defaultHost: string): ListenAddress {
  const match = /^(?:\[([^\]]+)\]|([^:]*)):(\d+)$/.exec(value);
  if (!match || Number(match[3]) > 65535) throw new Error('listen must be host:port');
  return { host: match[1] || match[2] || defaultHost, port: Number(match[3]) };
}

export function listen(server: Server, { host, port }: ListenAddress): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, host, () => {
      server.removeListener('error', reject);
      resolve();
    });
  });
}

interface Closable {
  close(callback: (error?: Error) => void): unknown;
  closeAllConnections(): void;
}
// Returns an idempotent close(). The first call stops the listener, drops open connections
// and hands `shutdown` a promise for the listener being fully closed, so each entry point
// decides whether to release its own resources before or after that.
export function closeOnce(server: Closable, shutdown: (listenerClosed: Promise<void>) => Promise<void>) {
  let closing: Promise<void> | undefined;
  return (): Promise<void> => {
    closing ??= shutdown(
      new Promise<void>((resolve, reject) => {
        server.close((error) => (error ? reject(error) : resolve()));
        server.closeAllConnections();
      })
    );
    return closing;
  };
}
