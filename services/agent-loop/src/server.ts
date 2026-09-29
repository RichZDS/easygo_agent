import https from 'node:https';
import type { IncomingMessage, ServerResponse } from 'node:http';
import type { TLSSocket } from 'node:tls';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { Authorizer, tlsOptions } from './rpc.js';
import { Store } from './store.js';
import { Loop } from './loop.js';
import { Knowledge } from './knowledge/index.js';
import { createPlatform } from './platform/server.js';
import { parseJSON } from './strict-json.mjs';
import { METHODS, type Config } from './types.js';
import { fields, integer, namespace, object, rpcFailure, RpcError, string } from './validation.js';

export function parseConfig(value: unknown): Config {
  const c = fields(value, ['listen', 'tls', 'authorization', 'database', 'gateway', 'workshop', 'model', 'streaming', 'max_steps', 'context_bytes', 'concurrency', 'system_prompt','pack_dir','platform','knowledge']);
  const tls = fields(c.tls, ['cert_file', 'key_file', 'ca_file']);
  for (const k of ['cert_file', 'key_file', 'ca_file']) string(tls[k], 4096);
  if (!Array.isArray(c.authorization)) throw new Error('authorization must be an array');
  for (const value of c.authorization) {
    const a = fields(value, ['id', 'cert_file', 'methods', 'namespaces']);
    string(a.id); string(a.cert_file, 4096);
    for (const field of ['methods', 'namespaces']) {
      if (!Array.isArray(a[field]) || (a[field] as unknown[]).some(v => typeof v !== 'string' || v.length === 0)) throw new Error('Invalid authorization list');
    }
  }
  for (const [name, port] of [['gateway', 8441], ['workshop', 8443]] as const) {
    const ep = fields(c[name], ['url', 'peer_certificate_file']);
    string(ep.peer_certificate_file, 4096);
    ep.url ??= `https://${name === 'gateway' ? 'ai-gateway' : name}:${port}/rpc`;
    string(ep.url, 4096);
  }
  for (const k of ['listen', 'database', 'model']) if (c[k] !== undefined) string(c[k], 4096);
  if (c.system_prompt !== undefined && (typeof c.system_prompt !== 'string' || Buffer.byteLength(c.system_prompt) > 65536)) throw new Error('Invalid system_prompt');
  if (c.pack_dir !== undefined) {
    string(c.pack_dir, 4096);
    if (c.system_prompt !== undefined) throw new Error('pack_dir conflicts with system_prompt');
  }
  if (c.streaming !== undefined && typeof c.streaming !== 'boolean') throw new Error('Invalid streaming');
  integer(c.concurrency, 4, 32, 1); integer(c.max_steps, 20, 100, 0); integer(c.context_bytes, 128000, 4 * 1024 * 1024, 512);
  if(c.knowledge!==undefined){const k=fields(c.knowledge,['database','profile_limit','consolidate_interval_ms']);string(k.database,4096);integer(k.profile_limit,5,20,1);integer(k.consolidate_interval_ms,86400000,2147483647,1000);}
  return c as unknown as Config;
}
async function readBody(req: IncomingMessage): Promise<unknown> {
  if (req.headers['content-type']?.split(';')[0] !== 'application/json') throw new RpcError(-32600, 'invalid_content_type');
  let bytes = 0; const chunks: Buffer[] = [];
  for await (const chunk of req) {
    bytes += chunk.length;
    if (bytes > 256 * 1024) throw new RpcError(-32600, 'request_too_large');
    chunks.push(chunk);
  }
  try { return parseJSON(new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Buffer.concat(chunks))); }
  catch { throw new RpcError(-32700, 'parse_error'); }
}
function listenAddress(value: string): { host: string; port: number } {
  const match = /^(?:\[([^\]]+)\]|([^:]*)):(\d+)$/.exec(value);
  if (!match || Number(match[3]) > 65535) throw new Error('listen must be host:port');
  return { host: match[1] || match[2] || '0.0.0.0', port: Number(match[3]) };
}
export async function startServer(input: Config) {
  const config = parseConfig(input);
  const address = listenAddress(config.listen ?? ':8442');
  const authorization = new Authorizer(config.authorization);
  const options = tlsOptions(config.tls);
  const store = new Store(config.database ?? '/data/agent.sqlite');
  let loop: Loop;
  try { loop = new Loop(config, store); } catch (error) { store.close(); throw error; }
  let closing = false, ready = false;
  let platform: Awaited<ReturnType<typeof createPlatform>> | undefined;
  let knowledge: Knowledge | undefined;
  function dispatch(method: string, value: unknown): unknown | Promise<unknown> {
    if (method==='platform.wallet.reserve' || method==='platform.wallet.settle') {
      if(!platform) throw new RpcError(-32601,'platform_disabled');
      return method.endsWith('.reserve') ? platform.wallet.reserve(value) : platform.wallet.settle(value);
    }
    if(method.startsWith('agent.memory.') || method.startsWith('agent.skills.')) {
      if(!knowledge) throw new RpcError(-32601,'knowledge_disabled');
      return knowledge.dispatch(method,value);
    }
    if(method.startsWith('workshop.')) return loop.workshopCall(method,object(value));
    const allowed: Record<string, string[]> = {
      'agent.session.create': [], 'agent.session.list': ['offset', 'limit'], 'agent.session.history': ['session_id', 'after', 'before', 'limit'],
      'agent.workshop.catalog': [], 'agent.run.start': ['session_id', 'input', 'idempotency_key', 'workshop_runtime'], 'agent.run.get': ['run_id'], 'agent.run.cancel': ['run_id'], 'agent.run.events': ['run_id', 'after', 'limit']
    };
    if (!METHODS.includes(method)) throw new RpcError(-32601, 'method_not_found');
    const p = fields(value, ['namespace', ...allowed[method]!]);
    const ns = namespace(p.namespace);
    switch (method) {
      case 'agent.workshop.catalog': return loop.workshopCatalog(ns);
      case 'agent.session.create': return store.createSession(ns);
      case 'agent.session.list': return store.listSessions(ns, integer(p.offset, 0, 2147483647), integer(p.limit, 20, 100));
      case 'agent.session.history': return store.history(ns, string(p.session_id), integer(p.after, 0, Number.MAX_SAFE_INTEGER), integer(p.limit, 100, 1000), p.before===undefined?undefined:integer(p.before,0,Number.MAX_SAFE_INTEGER,1));
      case 'agent.run.start': {
        loop.assertAvailable();
        const run = store.start(ns, string(p.session_id), string(p.input, 32768), string(p.idempotency_key, 256), p.workshop_runtime === undefined ? '' : string(p.workshop_runtime));
        loop.kick(); return run;
      }
      case 'agent.run.get': return store.get(ns, string(p.run_id));
      case 'agent.run.cancel': return loop.cancel(ns, string(p.run_id));
      case 'agent.run.events': return store.events(ns, string(p.run_id), integer(p.after, 0, Number.MAX_SAFE_INTEGER), integer(p.limit, 100, 1000));
    }
  }
  async function handle(req: IncomingMessage, res: ServerResponse) {
    let id: string | null = null; let identity: string | undefined; let method: string | undefined; let ns: string | undefined; let errorCode: string | undefined;
    const started = Date.now();
    try {
      if (closing || !ready) throw new RpcError(-32029, 'shutting_down');
      if (req.url === '/healthz' && req.method === 'GET') {
        identity = authorization.authorize(req.socket as TLSSocket, 'health'); method = 'health'; loop.assertAvailable();
        res.writeHead(200, { 'content-type': 'application/json' }); res.end('{"status":"ok"}'); return;
      }
      if (req.url !== '/rpc' || req.method !== 'POST') { res.writeHead(404); res.end(); return; }
      const value = await readBody(req);
      let envelope: Record<string, unknown>;
      try {
        envelope = fields(value, ['jsonrpc', 'id', 'method', 'params']);
        if (envelope.jsonrpc !== '2.0') throw new Error('Invalid version');
        id = string(envelope.id); method = string(envelope.method); object(envelope.params);
      } catch { id = null; throw new RpcError(-32600, 'invalid_request'); }
      ns = namespace(object(envelope.params).namespace);
      identity = authorization.authorize(req.socket as TLSSocket, method, ns);
      const result = await dispatch(method, envelope.params);
      res.writeHead(200, { 'content-type': 'application/json' }); res.end(JSON.stringify({ jsonrpc: '2.0', id, result }));
    } catch (error) {
      const failure = rpcFailure(error); errorCode = failure.reason;
      if (!res.destroyed) {
        res.writeHead(failure.code === -32003 ? 403 : [-32600, -32602, -32700].includes(failure.code) ? 400 : 200, { 'content-type': 'application/json' });
        res.end(JSON.stringify({ jsonrpc: '2.0', id, error: failure.wire() }));
      }
    } finally {
      // Never log params, prompts, keys, or upstream error messages.
      console.info(JSON.stringify({ rpc_id: id, identity, method, namespace: ns, elapsed_ms: Date.now() - started, error_code: errorCode }));
    }
  }
  try {
    if(config.knowledge) {
      knowledge=new Knowledge(config.knowledge,{generate:(ns,messages)=>loop.knowledgeGenerate(ns,messages)});
      loop.attachKnowledge(knowledge);
    }
    if(config.platform) platform=await createPlatform(config.platform,{rpc:async(method,params)=>{if(!ready||closing)throw new RpcError(-32029,'starting_or_stopping');return await dispatch(method,params);}});
  }catch(error){await knowledge?.close();await loop.close();store.close();throw error;}
  const server = https.createServer({ ...options, requestCert: true, maxHeaderSize: 16384 }, (req, res) => { void handle(req, res); });
  server.requestTimeout = 30_000; server.headersTimeout = 15_000; server.timeout = 30_000;
  try {
    await new Promise<void>((resolve, reject) => { server.once('error', reject); server.listen(address.port, address.host, () => { server.removeListener('error', reject); resolve(); }); });
  } catch (error) { await platform?.close();await knowledge?.close(); await loop.close(); store.close(); throw error; }
  ready=true;
  loop.startKnowledge();
  loop.kick();
  let closePromise: Promise<void> | undefined;
  return {
    server, address: server.address(), platform_address:platform?.address,
    close(): Promise<void> {
      closePromise ??= (async () => {
        closing = true;
        const closed = new Promise<void>((resolve, reject) => server.close(err => err ? reject(err) : resolve()));
        server.closeAllConnections();
        await loop.close(); await platform?.close(); store.close(); await closed;
      })();
      return closePromise;
    }
  };
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const index = process.argv.indexOf('--config');
  if (index < 0 || !process.argv[index + 1]) { console.error('Usage: node dist/server.js --config FILE'); process.exitCode = 1; }
  else {
    try {
      const running = await startServer(parseConfig(JSON.parse(readFileSync(process.argv[index + 1]!, 'utf8'))));
      console.info(JSON.stringify({ event: 'listening', address: running.address }));
      for (const signal of ['SIGTERM', 'SIGINT'] as const) process.once(signal, () => { void running.close().catch(() => { process.exitCode = 1; }); });
    } catch (error) { console.error(JSON.stringify({ event: 'startup_failed', code: error instanceof RpcError ? error.reason : 'invalid_config_or_storage' })); process.exitCode = 1; }
  }
}
