import type { Knowledge } from './knowledge/index.js';
import type { Loop } from './loop.js';
import type { Wallet } from './platform/store.js';
import type { Store } from './store.js';
import { fields, integer, namespace, object, RpcError, string } from './validation.js';

export interface MethodContext {
  store: Store;
  loop: Loop;
  knowledge?: Knowledge;
  platform?: { wallet: Wallet };
}
interface Method {
  // Params accepted besides `namespace`. Absent when the callee validates its own params.
  fields?: readonly string[];
  // Also reachable by signed-in browsers through the platform's /api/rpc.
  public?: boolean;
  // Checked before params, so a disabled feature is reported as such.
  requires?: 'knowledge' | 'platform';
  handler(ctx: MethodContext, params: Record<string, unknown>, ns: string): unknown | Promise<unknown>;
}

const knowledge = (
  fieldNames: readonly string[],
  handler: (k: Knowledge, ns: string, params: Record<string, unknown>) => unknown | Promise<unknown>
): Method => ({
  fields: fieldNames,
  public: true,
  requires: 'knowledge',
  handler: (ctx, params, ns) => handler(ctx.knowledge!, ns, params),
});
// Workshop calls are forwarded as-is; the workshop validates their params.
const FORWARDED_WORKSHOP_METHODS = [
  'workshop.submit',
  'workshop.get',
  'workshop.list',
  'workshop.cancel',
  'workshop.resume',
  'workshop.result',
  'workshop.events',
  'workshop.artifact',
  'workshop.message',
  'workshop.evidence',
];

const REGISTRY: Record<string, Method> = {
  'agent.workshop.catalog': { fields: [], public: true, handler: (ctx, _p, ns) => ctx.loop.workshopCatalog(ns) },
  'agent.session.create': { fields: [], public: true, handler: (ctx, _p, ns) => ctx.store.createSession(ns) },
  'agent.session.list': {
    fields: ['offset', 'limit'],
    public: true,
    handler: (ctx, p, ns) => ctx.store.listSessions(ns, integer(p.offset, 0, 2147483647), integer(p.limit, 20, 100)),
  },
  'agent.session.history': {
    fields: ['session_id', 'after', 'before', 'limit'],
    public: true,
    handler: (ctx, p, ns) =>
      ctx.store.history(
        ns,
        string(p.session_id),
        integer(p.after, 0, Number.MAX_SAFE_INTEGER),
        integer(p.limit, 100, 1000),
        p.before === undefined ? undefined : integer(p.before, 0, Number.MAX_SAFE_INTEGER, 1)
      ),
  },
  'agent.run.start': {
    fields: ['session_id', 'input', 'idempotency_key', 'workshop_runtime'],
    public: true,
    handler: (ctx, p, ns) => {
      ctx.loop.assertAvailable();
      const run = ctx.store.start(
        ns,
        string(p.session_id),
        string(p.input, 32768),
        string(p.idempotency_key, 256),
        p.workshop_runtime === undefined ? '' : string(p.workshop_runtime)
      );
      ctx.loop.kick();
      return run;
    },
  },
  'agent.run.get': { fields: ['run_id'], public: true, handler: (ctx, p, ns) => ctx.store.get(ns, string(p.run_id)) },
  'agent.run.cancel': {
    fields: ['run_id'],
    public: true,
    handler: (ctx, p, ns) => ctx.loop.cancel(ns, string(p.run_id)),
  },
  'agent.run.events': {
    fields: ['run_id', 'after', 'limit'],
    public: true,
    handler: (ctx, p, ns) =>
      ctx.store.events(ns, string(p.run_id), integer(p.after, 0, Number.MAX_SAFE_INTEGER), integer(p.limit, 100, 1000)),
  },
  ...Object.fromEntries(
    FORWARDED_WORKSHOP_METHODS.map((method): [string, Method] => [
      method,
      { public: true, handler: (ctx, params) => ctx.loop.forwardWorkshop(method, params) },
    ])
  ),
  'agent.memory.list': knowledge([], (k, ns) => k.listMemories(ns)),
  'agent.memory.upsert': knowledge(
    ['id', 'kind', 'content', 'importance', 'confidence', 'source_run_ids', 'expected_version'],
    (k, ns, p) => k.saveMemory(ns, p)
  ),
  'agent.memory.delete': knowledge(['id', 'expected_version'], (k, ns, p) => k.deleteEntry('memories', ns, p)),
  'agent.memory.consolidate': knowledge([], (k, ns) => k.consolidate(ns)),
  'agent.memory.import': knowledge(['entries', 'dry_run', 'provenance'], (k, ns, p) =>
    k.importEntries(ns, 'memory', p)
  ),
  'agent.skills.list': knowledge([], (k, ns) => k.listSkills(ns)),
  'agent.skills.get': knowledge(['name'], (k, ns, p) => k.getSkill(ns, p)),
  'agent.skills.upsert': knowledge(['name', 'description', 'content', 'expected_version'], (k, ns, p) =>
    k.saveSkill(ns, p)
  ),
  'agent.skills.delete': knowledge(['name', 'expected_version'], (k, ns, p) => k.deleteEntry('skills', ns, p)),
  'agent.skills.import': knowledge(['entries', 'dry_run', 'provenance'], (k, ns, p) =>
    k.importEntries(ns, 'skills', p)
  ),
  // Called by the AI gateway over mTLS only; never exposed to browsers.
  'platform.wallet.reserve': { requires: 'platform', handler: (ctx, params) => ctx.platform!.wallet.reserve(params) },
  'platform.wallet.settle': { requires: 'platform', handler: (ctx, params) => ctx.platform!.wallet.settle(params) },
};

export const METHODS = Object.keys(REGISTRY);
export const PUBLIC_METHODS = new Set(METHODS.filter((method) => REGISTRY[method]!.public));

export function callMethod(ctx: MethodContext, method: string, value: unknown): unknown | Promise<unknown> {
  const spec = Object.hasOwn(REGISTRY, method) ? REGISTRY[method] : undefined;
  if (!spec) throw new RpcError(-32601, 'method_not_found');
  if (spec.requires === 'knowledge' && !ctx.knowledge) throw new RpcError(-32601, 'knowledge_disabled');
  if (spec.requires === 'platform' && !ctx.platform) throw new RpcError(-32601, 'platform_disabled');
  if (!spec.fields) return spec.handler(ctx, object(value), '');
  const p = fields(value, ['namespace', ...spec.fields]);
  return spec.handler(ctx, p, namespace(p.namespace));
}
