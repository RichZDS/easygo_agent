import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Worker } from 'node:worker_threads';
import { PlatformStore, Wallet } from '../dist/platform/store.js';
import { createPlatform } from '../dist/platform/server.js';
import { RpcError } from '../dist/validation.js';

async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'platform-wallet-'));
  const file = join(dir, 'platform.db'), store = new PlatformStore(file), wallet = new Wallet(store);
  store.run("INSERT INTO accounts(id,email,password,namespace,role,created_at) VALUES('a','a@example.test','unused','account-a','user','now')");
  store.run("INSERT INTO accounts(id,email,password,namespace,role,created_at) VALUES('b','b@example.test','unused','account-b','user','now')");
  t.after(async () => { store.close(); await rm(dir, { recursive: true, force: true }); });
  return { dir, file, store, wallet };
}
const reserve = (request_id, extra = {}) => ({ namespace: 'account-a', request_id, fingerprint: `fp-${request_id}`, model: 'fixture', reserve_input_tokens: 70, reserve_output_tokens: 30, source: 'gateway', ...extra });
const settle = (request_id, extra = {}) => ({ namespace: 'account-a', request_id, usage: { known: true, input_tokens: 40, output_tokens: 20, cache_read_tokens: 10, cache_write_tokens: 5 }, outcome: 'complete', source: 'gateway', ...extra });
const grant = (store, amount, key = 'grant') => store.grant({ user_id: 'a', amount_micros: amount, reason: 'fixture', idempotency_key: key }, 'admin');
const reason = code => error => error instanceof RpcError && error.reason === code;

test('zero credit rejects dispatch; idempotent grants, reservations, snapshot and exact debit survive reopen', async t => {
  const { store, wallet, file } = await fixture(t);
  assert.throws(() => wallet.reserve(reserve('r')), reason('insufficient_credits'));
  assert.deepEqual(grant(store, 1_000_000), { ledger_seq: 1, duplicate: false });
  assert.equal(grant(store, 1_000_000).duplicate, true);
  assert.throws(() => grant(store, 2_000_000), reason('idempotency_conflict'));
  const first = wallet.reserve(reserve('r')); assert.equal(first.reserved_micros, 100_000);
  assert.equal(wallet.reserve(reserve('r')).duplicate, true);
  assert.throws(() => wallet.reserve(reserve('r', { fingerprint: 'changed' })), reason('idempotency_conflict'));
  assert.equal(store.walletView('account-a').available_micros, 900_000);
  store.setTariff({ input_micros: 9000, output_micros: 9000 }, 'admin');
  const receipt = wallet.settle(settle('r')); assert.equal(receipt.charged_micros, 60_000);
  assert.equal(wallet.settle(settle('r')).duplicate, true);
  assert.throws(() => wallet.settle(settle('r', { source: 'changed' })), reason('settlement_conflict'));
  assert.equal(store.walletView('account-a').balance_micros, 940_000);
  assert.equal(store.walletView('account-a').held_micros, 0);
  assert.equal(store.usage('account-a')[0].settlement.usage.cache_read_tokens, 10);
  assert.equal(store.walletView('account-b').balance_micros, 0);
  assert.throws(() => wallet.settle(settle('r', { namespace: 'account-b' })), reason('reservation_not_found'));
  const reopened = new PlatformStore(file);
  try { assert.equal(new Wallet(reopened).settle(settle('r')).duplicate, true); assert.equal(reopened.walletView('account-a').balance_micros, 940_000); } finally { reopened.close(); }
  assert.throws(() => store.run('UPDATE ledger SET amount_micros=0'), /append_only/);
});

test('unknown/uncertain stays pending, rejected releases, overage stops admission, disabled still settles', async t => {
  const { store, wallet } = await fixture(t); grant(store, 1_000_000);
  wallet.reserve(reserve('unknown'));
  const unknown = settle('unknown', { usage: { known: false, input_tokens: 0, output_tokens: 0 } });
  assert.equal(wallet.settle(unknown).status, 'pending'); assert.equal(wallet.settle(unknown).duplicate, true);
  assert.throws(() => wallet.settle(settle('unknown')), reason('settlement_conflict'));
  wallet.reserve(reserve('uncertain')); assert.equal(wallet.settle(settle('uncertain', { outcome: 'uncertain' })).status, 'settled');
  assert.equal(wallet.settle(settle('uncertain', { outcome: 'uncertain' })).duplicate, true);
  assert.equal(store.usage('account-a')[0].settlement.usage.input_tokens, 40);
  wallet.reserve(reserve('rejected'));
  assert.equal(wallet.settle(settle('rejected', { outcome: 'rejected', usage: { known: false, input_tokens: 0, output_tokens: 0 }, provider_status: 429 })).status, 'released');
  assert.equal(store.walletView('account-a').held_micros, 100_000);
  wallet.reserve(reserve('overage'));
  store.run('UPDATE accounts SET disabled=1 WHERE id=?', 'a');
  assert.equal(wallet.settle(settle('overage', { usage: { known: true, input_tokens: 1500, output_tokens: 0 } })).charged_micros, 1_500_000);
  assert.throws(() => wallet.reserve(reserve('disabled')), reason('account_unavailable'));
  store.run('UPDATE accounts SET disabled=0 WHERE id=?', 'a');
  assert.equal(store.walletView('account-a').balance_micros, -560_000);
  assert.equal(store.walletView('account-a').available_micros, -660_000);
  assert.throws(() => wallet.reserve(reserve('blocked')), reason('insufficient_credits'));
});

test('strict integer, overflow, cache subset and rejected-usage validation leaves wallet unchanged', async t => {
  const { store, wallet } = await fixture(t); grant(store, Number.MAX_SAFE_INTEGER);
  assert.throws(() => grant(store, 1, 'overflow'), reason('credit_overflow'));
  for (const value of [-1, 1.1, Number.MAX_SAFE_INTEGER + 1, '2', null]) assert.throws(() => wallet.reserve(reserve('bad', { reserve_input_tokens: value })), reason('invalid_integer'));
  assert.throws(() => wallet.reserve(reserve('overflow', { reserve_input_tokens: Number.MAX_SAFE_INTEGER })), reason('credit_overflow'));
  wallet.reserve(reserve('valid'));
  assert.throws(() => wallet.settle(settle('valid', { usage: { known: true, input_tokens: 10, output_tokens: 1, cache_read_tokens: 8, cache_write_tokens: 4 } })), reason('invalid_cache_subset'));
  assert.throws(() => wallet.settle(settle('valid', { outcome: 'rejected' })), reason('rejected_usage_conflict'));
  assert.throws(() => wallet.settle(settle('valid', { usage: { known: false, input_tokens: 1, output_tokens: 0 } })), reason('unknown_usage_has_counts'));
  assert.equal(store.walletView('account-a').held_micros, 100_000);
  assert.equal(store.usage('account-a')[0].settlement, null);
});

test('manual reconciliation is atomic, snapshot priced, audited, idempotent and preserves original raw receipt', async t => {
  const { store, wallet } = await fixture(t); grant(store, 1_000_000);
  wallet.reserve(reserve('pending'));
  const unknown = settle('pending', { outcome: 'uncertain', usage: { known: false, input_tokens: 0, output_tokens: 0 } });
  wallet.settle(unknown);
  const original = store.usage('account-a')[0].settlement;
  store.setTariff({ input_micros: 5000, output_micros: 5000 }, 'admin');
  const resolution = { namespace: 'account-a', request_id: 'pending', decision: 'settle', usage: { known: true, input_tokens: 20, output_tokens: 10 }, reason: 'Verified provider receipt', idempotency_key: 'reconcile-1' };
  assert.equal(wallet.resolve(resolution, 'admin').charged_micros, 30_000);
  assert.equal(wallet.resolve(resolution, 'admin').duplicate, true);
  assert.throws(() => wallet.resolve({ ...resolution, reason: 'changed' }, 'admin'), reason('idempotency_conflict'));
  assert.throws(() => wallet.resolve({ ...resolution, idempotency_key: 'different' }, 'admin'), reason('reservation_not_pending'));
  assert.deepEqual(store.usage('account-a')[0].settlement, original);
  assert.equal(store.usage('account-a')[0].resolution.payload.usage.input_tokens, 20);
  assert.equal(store.walletView('account-a').balance_micros, 970_000);
  assert.equal(store.walletView('account-a').held_micros, 0);
  assert.equal(wallet.settle(unknown).duplicate, true);
  assert.throws(() => wallet.settle(settle('pending')), reason('settlement_conflict'));
  wallet.reserve(reserve('orphan'));
  const release = { namespace: 'account-a', request_id: 'orphan', decision: 'release', reason: 'Provider confirms no dispatch', idempotency_key: 'reconcile-2' };
  assert.equal(wallet.resolve(release, 'admin').status, 'resolved_released');
  assert.equal(wallet.resolve(release, 'admin').duplicate, true);
  assert.equal(wallet.settle(settle('orphan')).late, true);
  assert.equal(store.pending().length, 0);
  assert.equal(store.walletView('account-a').balance_micros, 970_000);
  assert.throws(() => store.run('UPDATE resolutions SET payload=?', '{}'), /append_only/);
  assert.throws(() => store.run('UPDATE reservations SET settlement=? WHERE request_id=?', '{}', 'pending'), /immutable_receipt/);
  assert.equal(store.all("SELECT * FROM audit WHERE action LIKE 'resolved_%'").length, 2);
});

for (const decision of ['release', 'settle']) test(`late provider receipt after admin ${decision} preserves raw usage without changing credits`, async t => {
  const { store, wallet, file } = await fixture(t); grant(store, 1_000_000);
  const reserved = wallet.reserve(reserve('late'));
  const manual = { namespace: 'account-a', request_id: 'late', decision, ...(decision === 'settle' ? { usage: { known: true, input_tokens: 20, output_tokens: 10 } } : {}), reason: 'Manual provider verification', idempotency_key: 'manual-late' };
  const resolved = wallet.resolve(manual, 'admin');
  // A different active request must keep its hold; late receipt must not release it.
  wallet.reserve(reserve('other'));
  const before = store.walletView('account-a'), resolution = store.resolution(reserved.reservation_id);
  const late = settle('late', { usage: { known: true, input_tokens: 200, output_tokens: 80, cache_read_tokens: 40, cache_write_tokens: 10 } });
  const accepted = wallet.settle(late);
  assert.deepEqual(accepted, { reservation_id: reserved.reservation_id, status: resolved.status, charged_micros: resolved.charged_micros, duplicate: false, late: true });
  assert.deepEqual(store.walletView('account-a'), before);
  assert.equal(before.held_micros, 100_000);
  assert.deepEqual(store.resolution(reserved.reservation_id), resolution);
  const usage = store.usage('account-a').find(r => r.request_id === 'late');
  assert.deepEqual(usage.settlement.usage, late.usage);
  assert.equal(usage.status, resolved.status); assert.equal(usage.charged_micros, decision === 'settle' ? 30_000 : 0);
  assert.equal(store.all("SELECT * FROM audit WHERE action='late_receipt' AND subject=?", reserved.reservation_id).length, 1);
  assert.equal(wallet.settle(late).duplicate, true);
  assert.throws(() => wallet.settle(settle('late')), reason('settlement_conflict'));
  assert.deepEqual(store.walletView('account-a'), before);
  assert.equal(store.all("SELECT * FROM audit WHERE action='late_receipt' AND subject=?", reserved.reservation_id).length, 1);
  const reopened = new PlatformStore(file);
  try {
    assert.equal(new Wallet(reopened).settle(late).duplicate, true);
    assert.deepEqual(reopened.walletView('account-a'), before);
    assert.deepEqual(reopened.usage('account-a').find(r => r.request_id === 'late').settlement.usage, late.usage);
  } finally { reopened.close(); }
});

test('independent SQLite connections race for limited credit without overspend', async t => {
  const { store, file } = await fixture(t); grant(store, 1_000_000);
  const module = new URL('../dist/platform/store.js', import.meta.url).href;
  const run = index => new Promise((resolve, reject) => {
    const worker = new Worker(`const {parentPort,workerData}=require('node:worker_threads'); (async()=>{const {PlatformStore,Wallet}=await import(workerData.module); const s=new PlatformStore(workerData.file),w=new Wallet(s);let admitted=0,rejected=0;for(let i=0;i<20;i++){try{w.reserve({namespace:'account-a',request_id:workerData.index+'-'+i,fingerprint:'f',model:'fixture',reserve_input_tokens:70,reserve_output_tokens:30,source:'gateway'});admitted++;}catch(e){if(e.reason!=='insufficient_credits')throw e;rejected++;}}s.close();parentPort.postMessage({admitted,rejected});})().catch(e=>{throw e})`, { eval: true, workerData: { module, file, index } });
    worker.once('message', resolve); worker.once('error', reject); worker.once('exit', code => { if (code) reject(Error(`worker exit ${code}`)); });
  });
  const results = await Promise.all([run(1), run(2), run(3), run(4)]);
  assert.equal(results.reduce((n, r) => n + r.admitted, 0), 10);
  assert.equal(results.reduce((n, r) => n + r.rejected, 0), 70);
  assert.equal(store.walletView('account-a').available_micros, 0);
  assert.equal(store.walletView('account-a').held_micros, 1_000_000);
});

async function apiFixture(t, extra = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'platform-http-'));
  process.env.PLATFORM_TEST_ADMIN_PASSWORD = 'fixture-admin-password';
  const seen = [], sessions = new Map();
  const config = { listen: '127.0.0.1:0', database: join(dir, 'db.sqlite'), public_origin: 'http://platform.test', secure_cookies: false, registration: true, bootstrap_admin: { email: 'admin@example.test', password_env: 'PLATFORM_TEST_ADMIN_PASSWORD' }, ...extra };
  const app = await createPlatform(config, { async rpc(method, params) {
    seen.push({ method, params });
    if (method === 'agent.session.create') { const id = `s-${sessions.size}`; sessions.set(id, params.namespace); return { id }; }
    if (method === 'agent.session.history') { if (sessions.get(params.session_id) !== params.namespace) throw new RpcError(-32004, 'session_not_found'); return { messages: [] }; }
    if (method === 'agent.run.get') throw Error('secret upstream password should never be returned');
    return [];
  } });
  t.after(async () => { await app.close(); delete process.env.PLATFORM_TEST_ADMIN_PASSWORD; await rm(dir, { recursive: true, force: true }); });
  const base = `http://127.0.0.1:${app.address.port}`;
  async function call(path, data, cookie, options = {}) {
    const res = await fetch(base + path, { method: options.method ?? (data === undefined ? 'GET' : 'POST'), headers: { origin: 'http://platform.test', ...(data === undefined ? {} : { 'content-type': 'application/json' }), ...(cookie ? { cookie } : {}), ...options.headers }, ...(data === undefined ? {} : { body: typeof data === 'string' ? data : JSON.stringify(data) }) });
    const text = await res.text(); let json; try { json = JSON.parse(text); } catch { json = undefined; }
    return { status: res.status, data: json, text, headers: res.headers, cookie: res.headers.get('set-cookie')?.split(';')[0] };
  }
  const register = address => call('/api/register', { email: address, password: 'fixture-user-password' });
  return { app, call, register, seen, base, file: config.database };
}

test('HTTP auth, zero signup, cookies, CSRF, role checks, namespace binding, allowlist and logout revocation', async t => {
  const { call, register, seen } = await apiFixture(t);
  assert.equal((await call('/api/me')).status, 401);
  assert.equal((await call('/api/register', { email: 'x@example.test', password: 'fixture-password', role: 'admin' })).status, 400);
  assert.equal((await call('/api/register', { email: 'x@example.test', password: 'fixture-password' }, null, { headers: { origin: 'https://evil.test' } })).status, 403);
  const alice = await register('Alice@example.test'), bob = await register('bob@example.test');
  assert.equal(alice.status, 201); assert.equal(alice.data.user.role, 'user'); assert.equal(alice.data.user.email, 'alice@example.test');
  assert.match(alice.headers.get('set-cookie'), /HttpOnly; SameSite=Strict/);
  assert.equal((await call('/api/wallet', undefined, alice.cookie)).data.balance_micros, 0);
  assert.equal((await call('/api/admin/users', undefined, alice.cookie)).status, 403);
  assert.equal((await call('/api/admin/credits', { user_id: alice.data.user.id, amount_micros: 1000, reason: 'steal', idempotency_key: 'evil' }, alice.cookie)).status, 403);
  const denied = await call('/api/rpc', { method: 'platform.wallet.reserve', params: {} }, alice.cookie); assert.equal(denied.status, 400); assert.equal(seen.length, 0);
  assert.equal((await call('/api/rpc', { method: 'agent.session.list', params: { namespace: bob.data.user.namespace } }, alice.cookie)).status, 400);
  const session = await call('/api/rpc', { method: 'agent.session.create', params: {} }, alice.cookie);
  assert.equal(seen.at(-1).params.namespace, alice.data.user.namespace);
  assert.equal((await call('/api/rpc', { method: 'agent.session.history', params: { session_id: session.data.result.id } }, bob.cookie)).data.error.code, 'session_not_found');
  const error = await call('/api/rpc', { method: 'agent.run.get', params: { run_id: 'x' } }, alice.cookie); assert.equal(error.status, 500); assert.doesNotMatch(error.text, /secret|password/);
  assert.equal((await call('/api/logout', {}, alice.cookie, { headers: { origin: '' } })).status, 403);
  assert.equal((await call('/api/logout', {}, alice.cookie)).status, 200);
  assert.equal((await call('/api/me', undefined, alice.cookie)).status, 401);
  const login = await call('/api/login', { email: 'alice@example.test', password: 'fixture-user-password' }); assert.equal(login.status, 200); assert.notEqual(login.cookie, alice.cookie);
  assert.equal((await call('/api/me', undefined, bob.cookie)).status, 200);
  assert.equal((await call('/api/login', { email: 'alice@example.test', password: 'wrong-password' })).status, 401);
});

test('admin grants/usage/tariff API, strict JSON/body bounds, static CSP and rate limits', async t => {
  const { call, register, app } = await apiFixture(t);
  const user = await register('user@example.test');
  const admin = await call('/api/login', { email: 'admin@example.test', password: 'fixture-admin-password' });
  const users = await call('/api/admin/users', undefined, admin.cookie); assert.equal(users.data.users.length, 2); assert.doesNotMatch(users.text, /password|session_hash/);
  const payload = { user_id: user.data.user.id, amount_micros: 1_000_000, reason: 'test allocation', idempotency_key: 'allocation' };
  assert.equal((await call('/api/admin/credits', payload, admin.cookie)).data.duplicate, false);
  assert.equal((await call('/api/admin/credits', payload, admin.cookie)).data.duplicate, true);
  app.wallet.reserve(reserve('metered', { namespace: user.data.user.namespace }));
  app.wallet.settle(settle('metered', { namespace: user.data.user.namespace }));
  const wallet = await call('/api/wallet', undefined, user.cookie); assert.equal(wallet.data.balance_micros, 940_000); assert.equal(wallet.data.ledger.length, 2);
  const usage = await call('/api/usage', undefined, user.cookie); assert.equal(usage.data.receipts[0].settlement.usage.input_tokens, 40);
  assert.equal((await call('/api/usage', undefined, admin.cookie)).data.receipts.length, 0);
  assert.equal((await call('/api/admin/tariff', { input_micros: 2000, output_micros: 3000 }, admin.cookie, { method: 'PUT' })).data.version, 2);
  assert.equal((await call('/api/rpc', '{"method":"agent.session.list","method":"agent.run.start","params":{}}', user.cookie)).status, 400);
  assert.equal((await call('/api/rpc', { method: 'agent.session.list', params: {}, excess: true }, user.cookie)).status, 400);
  assert.equal((await call('/api/rpc', JSON.stringify({ method: 'agent.session.list', params: { huge: 'x'.repeat(270000) } }), user.cookie)).status, 400);
  app.wallet.reserve(reserve('orphan', { namespace: user.data.user.namespace }));
  const pending = await call('/api/admin/usage/pending', undefined, admin.cookie); assert.equal(pending.data.receipts.length, 1);
  const resolution = { namespace: user.data.user.namespace, request_id: 'orphan', decision: 'release', reason: 'fixture no charge', idempotency_key: 'manual-1' };
  assert.equal((await call('/api/admin/usage/resolve', resolution, user.cookie)).status, 403);
  assert.equal((await call('/api/admin/usage/resolve', resolution, admin.cookie)).data.status, 'resolved_released');
  assert.equal((await call('/api/admin/usage/resolve', resolution, admin.cookie)).data.duplicate, true);
  const html = await call('/'); assert.equal(html.status, 200); assert.match(html.text, /lang="zh-CN"/); assert.match(html.headers.get('content-security-policy'), /frame-ancestors 'none'/);
  assert.equal((await call('/app.js')).status, 200); assert.equal((await call('/style.css')).status, 200); assert.equal((await call('/src/platform/server.ts')).status, 404);
  const script = await readFile(new URL('../web/app.js', import.meta.url), 'utf8'); assert.doesNotMatch(script, /innerHTML|outerHTML|insertAdjacentHTML/);
  let last; for (let i = 0; i < 12; i++) last = await call('/api/login', { email: 'missing@example.test', password: 'incorrect-fixture' });
  assert.equal(last.status, 429);
});

test('registration disabled and secure cookies remain explicit', async t => {
  const { call } = await apiFixture(t, { registration: false, public_origin: 'https://platform.test', secure_cookies: true });
  const data = { email: 'new@example.test', password: 'fixture-password' };
  assert.equal((await call('/api/register', data, null, { headers: { origin: 'https://platform.test' } })).data.error.code, 'registration_disabled');
  const login = await call('/api/login', { email: 'admin@example.test', password: 'fixture-admin-password' }, null, { headers: { origin: 'https://platform.test' } });
  assert.match(login.headers.get('set-cookie'), /; Secure/);
});

test('read pagination exposes all rows with exact end cursors and namespace-bound artifact dispatch', async t => {
  const { call, register, app, seen, file } = await apiFixture(t);
  const user = await register('paging@example.test');
  const admin = await call('/api/login', { email: 'admin@example.test', password: 'fixture-admin-password' });
  const store = new PlatformStore(file);
  try {
    store.grant({ user_id: user.data.user.id, amount_micros: 1_000_001, reason: 'paging fixture', idempotency_key: 'seed' }, 'fixture');
    store.transaction(() => { for (let i = 0; i < 103; i++) store.run("INSERT INTO accounts(id,email,password,namespace,role,created_at) VALUES(?,?,?,?,'user','now')", `seed-${i}`, `seed${i}@example.test`, 'unused', `seed-${i}`); });
    for (let i = 0; i < 105; i++) {
      app.wallet.reserve(reserve(`page-${i}`, { namespace: user.data.user.namespace, reserve_input_tokens: 0, reserve_output_tokens: 0 }));
      app.wallet.settle(settle(`page-${i}`, { namespace: user.data.user.namespace, usage: { known: true, input_tokens: 0, output_tokens: 0 } }));
      app.wallet.reserve(reserve(`pending-${i}`, { namespace: user.data.user.namespace, reserve_input_tokens: 0, reserve_output_tokens: 0 }));
    }
  } finally { store.close(); }
  const ledger1 = (await call('/api/wallet', undefined, user.cookie)).data;
  const ledger2 = (await call(`/api/wallet?after=${ledger1.next_after}`, undefined, user.cookie)).data;
  assert.equal(ledger1.ledger.length, 100); assert.equal(ledger2.ledger.length, 6); assert.equal(ledger2.next_after, null);
  assert.equal(new Set([...ledger1.ledger, ...ledger2.ledger].map(r => r.seq)).size, 106);
  assert.equal(ledger2.balance_micros, 1_000_001);
  const receipts = [];
  for (let offset = 0; offset !== null;) { const page = (await call(`/api/usage?offset=${offset}`, undefined, user.cookie)).data; receipts.push(...page.receipts); offset = page.next_offset; }
  assert.equal(receipts.length, 210); assert.equal(new Set(receipts.map(r => r.request_id)).size, 210);
  const empty = (await call('/api/usage', undefined, admin.cookie)).data; assert.deepEqual(empty, { receipts: [], next_offset: null });
  for (const [route, key] of [['users', 'users'], ['usage/pending', 'receipts']]) {
    const first = (await call(`/api/admin/${route}`, undefined, admin.cookie)).data;
    const last = (await call(`/api/admin/${route}?offset=${first.next_offset}`, undefined, admin.cookie)).data;
    assert.equal(first[key].length, 100); assert.equal(first.next_offset, 100); assert.equal(last[key].length, 5); assert.equal(last.next_offset, null);
  }
  const artifact = await call('/api/rpc', { method: 'workshop.artifact', params: { task_id: 'task', run_id: 'run', path: 'result.txt' } }, user.cookie);
  assert.equal(artifact.status, 200); assert.equal(seen.at(-1).params.namespace, user.data.user.namespace); assert.equal(seen.at(-1).method, 'workshop.artifact');
  assert.equal((await call('/api/rpc', { method: 'workshop.artifact', params: { namespace: 'other', task_id: 'task', path: 'result.txt' } }, user.cookie)).status, 400);
});
