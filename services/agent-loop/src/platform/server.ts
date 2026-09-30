import http, { type IncomingMessage, type ServerResponse } from 'node:http';
import { createHash, randomBytes, scrypt, timingSafeEqual } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fields, object, string, RpcError } from '../validation.js';
import { API_BODY_ERRORS, closeOnce, listen, parseListen, readJsonBody } from '../http.js';
import { PUBLIC_METHODS } from '../methods.js';
import type { Row } from '../sqlite.js';
import { PlatformStore, Wallet, count } from './store.js';
import { createClientAddress } from './client-address.mjs';

export interface PlatformConfig {
  listen: string;
  database: string;
  public_origin: string;
  secure_cookies: boolean;
  registration: boolean;
  bootstrap_admin?: { email: string; password_env: string };
  trusted_proxies?: string[];
}
const SESSION_MS = 7 * 24 * 60 * 60 * 1000;
const digest = (s: string) => createHash('sha256').update(s).digest('hex');
function email(value: unknown): string {
  const e = string(value, 254).trim().toLowerCase();
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(e)) throw new RpcError(-32602, 'invalid_email');
  return e;
}
function password(value: unknown): string {
  const p = string(value, 1024);
  if (p.length < 12) throw new RpcError(-32602, 'password_too_short');
  return p;
}
const derive = (p: string, salt: string) =>
  new Promise<Buffer>((resolve, reject) => scrypt(p, salt, 64, (e, k) => (e ? reject(e) : resolve(k))));
async function hashPassword(p: string) {
  const salt = randomBytes(16).toString('hex');
  return `${salt}:${(await derive(p, salt)).toString('hex')}`;
}
async function matches(p: string, hash: string) {
  const [salt, expected] = hash.split(':');
  const actual = await derive(p, salt!);
  const bytes = Buffer.from(expected!, 'hex');
  return bytes.length === actual.length && timingSafeEqual(bytes, actual);
}
function userView(a: Row) {
  return {
    id: a.id,
    email: a.email,
    namespace: a.namespace,
    role: a.role,
    disabled: Boolean(a.disabled),
    created_at: a.created_at,
  };
}
function token(req: IncomingMessage) {
  const cookies = (req.headers.cookie ?? '')
    .split(';')
    .map((v) => v.trim())
    .filter((v) => v.startsWith('platform_session='));
  if (cookies.length !== 1) return '';
  const value = cookies[0]!.slice('platform_session='.length);
  return /^[a-f0-9]{64}$/.test(value) ? value : '';
}
async function body(req: IncomingMessage) {
  const value = await readJsonBody(req, { maxBytes: 256 * 1024, errors: API_BODY_ERRORS });
  try {
    return object(value);
  } catch {
    throw new RpcError(-32602, 'invalid_json');
  }
}

export async function createPlatform(
  config: PlatformConfig,
  callbacks: { rpc(method: string, params: Record<string, unknown>): Promise<unknown> }
) {
  fields(config, [
    'listen',
    'database',
    'public_origin',
    'secure_cookies',
    'registration',
    'bootstrap_admin',
    'trusted_proxies',
  ]);
  const clientAddress = createClientAddress(config.trusted_proxies);
  const origin = new URL(config.public_origin);
  if (
    !['http:', 'https:'].includes(origin.protocol) ||
    origin.origin !== config.public_origin ||
    origin.username ||
    origin.password
  )
    throw new Error('public_origin must be an HTTP(S) origin');
  if (
    typeof config.registration !== 'boolean' ||
    typeof config.secure_cookies !== 'boolean' ||
    (origin.protocol === 'https:' && !config.secure_cookies)
  )
    throw new Error('Explicit registration/secure_cookies required; HTTPS requires secure cookies');
  const address = parseListen(config.listen, '127.0.0.1');
  string(config.database, 4096);
  const store = new PlatformStore(config.database),
    wallet = new Wallet(store);
  try {
    if (config.bootstrap_admin) {
      const p = fields(config.bootstrap_admin, ['email', 'password_env']);
      const address = email(p.email),
        env = string(p.password_env, 128);
      if (!/^[A-Z][A-Z0-9_]*$/.test(env)) throw new Error('Invalid bootstrap password environment name');
      const existing = store.accountByEmail(address);
      if (existing && existing.role !== 'admin') throw new Error('Bootstrap email already belongs to a non-admin');
      if (!existing) {
        const hash = await hashPassword(password(process.env[env]));
        store.transaction(() => {
          const id = store.createAccount(address, hash, 'admin');
          store.audit('bootstrap', 'admin_created', id);
        });
      }
    }
  } catch (e) {
    store.close();
    throw e;
  }
  const dummyHash = await hashPassword(randomBytes(32).toString('hex'));
  const rates = new Map<string, { until: number; count: number }>();
  function rate(key: string, max: number) {
    const now = Date.now();
    if (rates.size > 4096) for (const [k, r] of rates) if (r.until <= now) rates.delete(k);
    let r = rates.get(key);
    if (!r || r.until <= now) {
      if (rates.size >= 8192) throw new RpcError(-32029, 'rate_limited');
      r = { until: now + 60_000, count: 0 };
      rates.set(key, r);
    }
    if (++r.count > max) throw new RpcError(-32029, 'rate_limited');
  }
  function authenticate(req: IncomingMessage) {
    const session = token(req);
    const a = session && store.authenticate(digest(session), Date.now());
    if (!a) throw new RpcError(-32001, 'authentication_required');
    return a;
  }
  function cookie(res: ServerResponse, value: string, maxAge: number) {
    res.setHeader(
      'set-cookie',
      `platform_session=${value}; Path=/; HttpOnly; SameSite=Strict; Max-Age=${maxAge}${config.secure_cookies ? '; Secure' : ''}`
    );
  }
  function session(req: IncomingMessage, res: ServerResponse, a: Row) {
    const value = randomBytes(32).toString('hex');
    store.transaction(() => {
      store.sessionPrune(String(a.id), digest(token(req)), Date.now());
      store.sessionCreate(digest(value), String(a.id), Date.now() + SESSION_MS);
    });
    cookie(res, value, SESSION_MS / 1000);
  }
  let authInFlight = 0,
    closing = false;
  const assets = new Map([
    ['/', ['index.html', 'text/html; charset=utf-8']],
    ['/app.js', ['app.js', 'text/javascript; charset=utf-8']],
    ['/style.css', ['style.css', 'text/css; charset=utf-8']],
  ]);
  function json(res: ServerResponse, value: unknown, status = 200) {
    res.writeHead(status, { 'content-type': 'application/json; charset=utf-8' });
    res.end(JSON.stringify(value));
  }
  async function handle(req: IncomingMessage, res: ServerResponse) {
    res.setHeader('cache-control', 'no-store');
    res.setHeader('x-content-type-options', 'nosniff');
    res.setHeader('referrer-policy', 'no-referrer');
    res.setHeader(
      'content-security-policy',
      "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
    );
    try {
      if (closing) throw new RpcError(-32029, 'shutting_down');
      const url = new URL(req.url ?? '/', config.public_origin);
      const path = url.pathname,
        method = req.method ?? '';
      const client = clientAddress(req);
      rate(`ip:${client}`, 300);
      if (method === 'GET' && assets.has(path)) {
        const [file, content] = assets.get(path)!;
        const data = await readFile(new URL(`../../web/${file}`, import.meta.url));
        res.writeHead(200, { 'content-type': content! });
        res.end(data);
        return;
      }
      if (!path.startsWith('/api/')) {
        json(res, { error: { code: 'not_found' } }, 404);
        return;
      }
      if (
        ['POST', 'PUT', 'DELETE', 'PATCH'].includes(method) &&
        (req.headers.origin !== origin.origin ||
          (req.headers['sec-fetch-site'] && !['same-origin', 'none'].includes(String(req.headers['sec-fetch-site']))))
      )
        throw new RpcError(-32003, 'origin_forbidden');
      if (method === 'POST' && (path === '/api/register' || path === '/api/login')) {
        rate(`auth-ip:${client}`, 20);
        const p = fields(await body(req), ['email', 'password']);
        const address = email(p.email),
          pass = password(p.password);
        rate(`auth-email:${digest(address)}`, 10);
        if (authInFlight >= 4) throw new RpcError(-32029, 'auth_busy');
        authInFlight++;
        try {
          if (path === '/api/register') {
            if (!config.registration) throw new RpcError(-32003, 'registration_disabled');
            const hash = await hashPassword(pass);
            const a = store.transaction(() => {
              if (store.accountByEmail(address)) throw new RpcError(-32009, 'registration_unavailable');
              const id = store.createAccount(address, hash, 'user');
              store.audit(id, 'registered', id);
              return store.accountById(id)!;
            });
            session(req, res, a);
            json(res, { user: userView(a) }, 201);
          } else {
            const a = store.accountByEmail(address);
            const ok = await matches(pass, String(a?.password ?? dummyHash));
            if (!a || !ok || a.disabled) throw new RpcError(-32001, 'invalid_credentials');
            session(req, res, a);
            store.audit(String(a.id), 'login', String(a.id));
            json(res, { user: userView(a) });
          }
        } finally {
          authInFlight--;
        }
        return;
      }
      const a = authenticate(req),
        ns = String(a.namespace);
      rate(`user:${a.id}`, 240);
      if (method === 'POST' && path === '/api/logout') {
        fields(await body(req), []);
        store.transaction(() => {
          store.sessionRevoke(digest(token(req)));
          store.audit(String(a.id), 'logout', String(a.id));
        });
        cookie(res, '', 0);
        json(res, { ok: true });
        return;
      }
      if (method === 'GET' && path === '/api/me') {
        json(res, { user: userView(a), registration: config.registration });
        return;
      }
      if (method === 'GET' && path === '/api/wallet') {
        json(res, store.walletView(ns, count(Number(url.searchParams.get('after') ?? 0))));
        return;
      }
      if (method === 'GET' && path === '/api/usage') {
        const offset = count(Number(url.searchParams.get('offset') ?? 0), 1_000_000);
        const rows = store.usage(ns, offset, 101);
        json(res, { receipts: rows.slice(0, 100), next_offset: rows.length > 100 ? offset + 100 : null });
        return;
      }
      if (method === 'POST' && path === '/api/rpc') {
        const p = fields(await body(req), ['method', 'params']);
        const name = string(p.method),
          params = object(p.params);
        if (!PUBLIC_METHODS.has(name)) throw new RpcError(-32601, 'method_not_found');
        if ('namespace' in params) throw new RpcError(-32602, 'namespace_is_server_bound');
        const result = await callbacks.rpc(name, { ...params, namespace: ns });
        json(res, { result });
        return;
      }
      if (path.startsWith('/api/admin/')) {
        if (a.role !== 'admin') throw new RpcError(-32003, 'admin_required');
        if (method === 'GET' && path === '/api/admin/usage/pending') {
          const offset = count(Number(url.searchParams.get('offset') ?? 0), 1_000_000);
          const rows = store.pending(offset, 101);
          json(res, { receipts: rows.slice(0, 100), next_offset: rows.length > 100 ? offset + 100 : null });
          return;
        }
        if (method === 'POST' && path === '/api/admin/usage/resolve') {
          json(res, wallet.resolve(await body(req), String(a.id)));
          return;
        }
        if (method === 'GET' && path === '/api/admin/users') {
          const offset = count(Number(url.searchParams.get('offset') ?? 0), 1_000_000);
          const rows = store.usersPage(offset, 101);
          json(res, { users: rows.slice(0, 100), next_offset: rows.length > 100 ? offset + 100 : null });
          return;
        }
        if (method === 'POST' && path === '/api/admin/credits') {
          json(res, store.grant(await body(req), String(a.id)));
          return;
        }
        if (method === 'GET' && path === '/api/admin/tariff') {
          json(res, store.tariff());
          return;
        }
        if (method === 'PUT' && path === '/api/admin/tariff') {
          json(res, store.setTariff(await body(req), String(a.id)));
          return;
        }
      }
      json(res, { error: { code: 'not_found' } }, 404);
    } catch (e) {
      const err = e instanceof RpcError ? e : new RpcError(-32603, 'internal_error');
      // REST statuses for the browser; /rpc keeps JSON-RPC's 200-with-error-body convention.
      const status =
        err.code === -32001
          ? 401
          : err.code === -32003
            ? 403
            : err.code === -32029
              ? 429
              : err.code === -32009
                ? 409
                : err.code === -32603
                  ? 500
                  : 400;
      if (!res.destroyed && !res.headersSent)
        json(
          res,
          {
            error: {
              code: /^[a-z][a-z0-9_]{0,63}$/.test(err.reason) ? err.reason : 'request_failed',
              rpc_code: err.code,
            },
          },
          status
        );
    }
  }
  const server = http.createServer({ maxHeaderSize: 16384 }, (req, res) => {
    void handle(req, res);
  });
  server.requestTimeout = 30_000;
  server.headersTimeout = 15_000;
  server.timeout = 120_000;
  try {
    await listen(server, address);
  } catch (e) {
    store.close();
    throw e;
  }
  return {
    server,
    address: server.address(),
    wallet,
    close: closeOnce(server, async (listenerClosed) => {
      closing = true;
      await listenerClosed;
      while (authInFlight) await new Promise((resolve) => setTimeout(resolve, 10));
      store.close();
    }),
  };
}
