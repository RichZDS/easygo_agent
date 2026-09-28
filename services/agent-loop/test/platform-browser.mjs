// Opt-in browser proof. Existing Playwright and Chromium paths are supplied by the operator.
// No installs, external providers, or production endpoints are used.
import assert from 'node:assert/strict';
import net from 'node:net';
import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { randomUUID } from 'node:crypto';
import { createPlatform } from '../dist/platform/server.js';
import { RpcError } from '../dist/validation.js';
if (!process.env.PLATFORM_PLAYWRIGHT_MODULE || !process.env.PLATFORM_CHROMIUM) throw Error('Set PLATFORM_PLAYWRIGHT_MODULE and PLATFORM_CHROMIUM to existing installations');
const { chromium } = await import(pathToFileURL(process.env.PLATFORM_PLAYWRIGHT_MODULE).href);
const root = await mkdtemp(join(tmpdir(), 'platform-browser-'));
const evidence = process.env.PLATFORM_BROWSER_EVIDENCE || await mkdtemp(join(tmpdir(), 'platform-browser-evidence-'));
await mkdir(evidence, { recursive: true });
const probe = net.createServer(); await new Promise(resolve => probe.listen(0, '127.0.0.1', resolve)); const port = probe.address().port; await new Promise(resolve => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
process.env.PLATFORM_BROWSER_ADMIN = 'fixture-admin-password';
const sessions = new Map(), runs = new Map(), tasks = new Map(), memories = new Map(), skills = new Map();
let app;
function owned(map, id, ns) { const value = map.get(id); if (!value || value.namespace !== ns) throw new RpcError(-32004, 'not_found'); return value; }
const catalog = [{ name: 'write-note', engine: 'fixture', runtime: 'fixture-runtime', model: 'fixture', runtimes: [{ id: 'fixture-runtime', engine: 'fixture', model: 'fixture' }] }];
async function dispatch(method, p) {
  if (method === 'agent.workshop.catalog') return catalog;
  if (method === 'agent.session.create') { const s = { id: randomUUID(), namespace: p.namespace, created_at: new Date().toISOString(), messages: [] }; sessions.set(s.id, s); return s; }
  if (method === 'agent.session.list') return { sessions: [...sessions.values()].filter(s => s.namespace === p.namespace) };
  if (method === 'agent.session.history') { const s = owned(sessions, p.session_id, p.namespace); return { messages: s.messages, runs: [...runs.values()].filter(r => r.session_id === s.id) }; }
  if (method === 'agent.run.start') {
    const s = owned(sessions, p.session_id, p.namespace), id = randomUUID();
    app.wallet.reserve({ namespace: p.namespace, request_id: id, fingerprint: 'fixture', model: 'fixture', reserve_input_tokens: 100, reserve_output_tokens: 100, source: 'fixture' });
    app.wallet.settle({ namespace: p.namespace, request_id: id, usage: { known: true, input_tokens: 20, output_tokens: 10 }, outcome: 'complete', source: 'fixture' });
    s.messages.push({ role: 'user', content: [{ type: 'text', text: p.input }] }, { role: 'assistant', content: [{ type: 'text', text: '任务完成 <img src=x onerror="window.__xss=true">' }] });
    const r = { id, namespace: p.namespace, session_id: s.id, status: 'completed' }; runs.set(id, r); return r;
  }
  if (method === 'agent.run.get') return owned(runs, p.run_id, p.namespace);
  if (method === 'agent.run.events') { owned(runs, p.run_id, p.namespace); return { events: [{ seq: 1, kind: 'terminal', data: { status: 'completed' } }] }; }
  if (method === 'workshop.list') return { tasks: [...tasks.values()].filter(t => t.namespace === p.namespace) };
  if (method === 'workshop.submit') { const t = { id: randomUUID(), namespace: p.namespace, status: 'running', runtime: p.runtime, run_count: 1 }; tasks.set(t.id, t); return t; }
  if (method === 'workshop.get') return owned(tasks, p.task_id, p.namespace);
  if (method === 'workshop.events') { owned(tasks, p.task_id, p.namespace); return [{ seq: 1, kind: 'fixture-event' }]; }
  if (method === 'workshop.cancel') { const t = owned(tasks, p.task_id, p.namespace); t.status = 'cancelled'; return t; }
  if (method === 'workshop.resume') { const t = owned(tasks, p.task_id, p.namespace); t.status = 'succeeded'; t.run_count++; return t; }
  if (method === 'workshop.result') { owned(tasks, p.task_id, p.namespace); return { text: '# fixture artifact\nCompleted result', eof: true }; }
  if (method === 'agent.memory.list') return { memories: [...memories.values()].filter(m => m.namespace === p.namespace) };
  if (method === 'agent.memory.upsert') {
    const id = p.id || randomUUID(), old = memories.get(id);
    if (old && (old.namespace !== p.namespace || old.version !== p.expected_version)) throw new RpcError(-32009, 'version_conflict');
    const m = { ...p, id, version: (old?.version || 0) + 1 }; memories.set(id, m); return m;
  }
  if (method === 'agent.memory.delete') { const m = owned(memories, p.id, p.namespace); assert.equal(m.version, p.expected_version); memories.delete(p.id); return { deleted: true }; }
  if (method === 'agent.skills.list') return { skills: [...skills.values()].filter(s => s.namespace === p.namespace) };
  if (method === 'agent.skills.get') return owned(skills, `${p.namespace}:${p.name}`, p.namespace);
  if (method === 'agent.skills.upsert') {
    assert.equal(typeof p.content, 'string'); assert.equal(p.body, undefined);
    const id = `${p.namespace}:${p.name}`, old = skills.get(id);
    if (old && old.version !== p.expected_version) throw new RpcError(-32009, 'version_conflict');
    const s = { ...p, version: (old?.version || 0) + 1 }; skills.set(id, s); return s;
  }
  if (method === 'agent.skills.delete') { const id = `${p.namespace}:${p.name}`, s = owned(skills, id, p.namespace); assert.equal(s.version, p.expected_version); skills.delete(id); return { deleted: true }; }
  if (method.endsWith('.import')) { assert.ok(Array.isArray(p.entries)); return { dry_run: p.dry_run !== false, entries: p.entries }; }
  if (method === 'agent.memory.consolidate') return { processed: 0 };
  throw new RpcError(-32601, 'method_not_found');
}
let browser;
try {
  app = await createPlatform({ listen: `127.0.0.1:${port}`, database: join(root, 'platform.db'), public_origin: origin, secure_cookies: false, registration: true, bootstrap_admin: { email: 'admin@example.test', password_env: 'PLATFORM_BROWSER_ADMIN' } }, { rpc: dispatch });
  browser = await chromium.launch({ executablePath: process.env.PLATFORM_CHROMIUM, headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const jsErrors = []; page.on('pageerror', e => jsErrors.push(e.message));
  page.on('dialog', dialog => dialog.accept());
  const navigate = name => page.locator(`[data-page="${name}"]`).click();
  const login = async (email, password, register = false) => { if (register) await page.locator('#auth-switch').click(); await page.locator('[name="email"]').fill(email); await page.locator('[name="password"]').fill(password); await page.locator('#auth-submit').click(); await page.locator('#shell').waitFor({ state: 'visible' }); };
  await page.goto(origin); await page.screenshot({ path: join(evidence, 'login-desktop.png') });
  await login('user@example.test', 'fixture-user-password', true);
  await page.waitForFunction(() => document.getElementById('overview-available').textContent === '0');
  const user = await page.evaluate(async () => (await (await fetch('/api/me')).json()).user);
  await page.locator('#logout').click(); await page.locator('#auth-switch').click();
  await login('admin@example.test', 'fixture-admin-password'); await navigate('admin');
  await page.locator('#grant-user').selectOption(user.id); await page.locator('#grant-amount').fill('10.5'); await page.locator('#grant-reason').fill('browser fixture');
  await page.locator('#grant-form button').click(); await page.waitForFunction(() => document.getElementById('users').textContent.includes('10.5 credits'));
  app.wallet.reserve({ namespace: user.namespace, request_id: 'browser-pending', fingerprint: 'pending', model: 'fixture', reserve_input_tokens: 100, reserve_output_tokens: 0, source: 'fixture' });
  app.wallet.settle({ namespace: user.namespace, request_id: 'browser-pending', usage: { known: false, input_tokens: 0, output_tokens: 0 }, outcome: 'uncertain', source: 'fixture' });
  await page.locator('#refresh').click(); await page.locator('#resolve-request').selectOption('0'); await page.locator('#resolve-decision').selectOption('settle');
  await page.locator('#resolve-input').fill('15'); await page.locator('#resolve-output').fill('5'); await page.locator('#resolve-reason').fill('fixture provider receipt'); await page.locator('#resolve-form button').click();
  await page.waitForFunction(() => document.getElementById('pending-receipts').textContent.includes('还没有内容'));
  app.wallet.reserve({ namespace: user.namespace, request_id: 'browser-late-receipt', fingerprint: 'late', model: 'fixture', reserve_input_tokens: 100, reserve_output_tokens: 0, source: 'fixture' });
  await page.locator('#refresh').click(); await page.locator('#resolve-request').selectOption('0'); await page.locator('#resolve-decision').selectOption('release');
  await page.locator('#resolve-reason').fill('fixture manual release before provider reply'); await page.locator('#resolve-form button').click();
  await page.waitForFunction(() => document.getElementById('pending-receipts').textContent.includes('还没有内容'));
  assert.equal(app.wallet.settle({ namespace: user.namespace, request_id: 'browser-late-receipt', usage: { known: true, input_tokens: 37, output_tokens: 13 }, outcome: 'complete', source: 'fixture' }).late, true);
  await page.screenshot({ path: join(evidence, 'admin-desktop.png') });
  await page.locator('#logout').click(); await login('user@example.test', 'fixture-user-password');
  await page.waitForFunction(() => document.getElementById('overview-available').textContent === '10.48');
  await page.screenshot({ path: join(evidence, 'overview-desktop.png') });
  await navigate('chat'); await page.locator('#chat-runtime').selectOption('fixture-runtime'); await page.locator('#chat-input').fill('写一份测试文档'); await page.locator('#send-chat').click();
  await page.waitForFunction(() => document.getElementById('messages').textContent.includes('任务完成'));
  assert.equal(await page.locator('#messages img').count(), 0); assert.equal(await page.evaluate(() => window.__xss), undefined);
  await navigate('workshop'); await page.locator('#task-runtime').selectOption('fixture-runtime'); await page.locator('#task-input').fill('create fixture artifact'); await page.locator('#task-form button').click();
  await page.waitForFunction(() => document.getElementById('task-status').textContent.includes('执行中'));
  await page.locator('#cancel-task').click(); await page.waitForFunction(() => document.getElementById('task-status').textContent.includes('已取消'));
  await page.locator('#resume-input').fill('finish'); await page.locator('#resume-task').click(); await page.waitForFunction(() => document.getElementById('task-status').textContent.includes('已完成'));
  await page.locator('#task-result').click(); await page.waitForFunction(() => document.getElementById('task-output').textContent.includes('fixture artifact'));
  await navigate('memory'); await page.locator('#memory-content').fill('偏好简洁中文'); await page.locator('#memory-form button.primary').click(); await page.locator('#memory-list button').first().click(); await page.locator('#memory-content').fill('偏好简洁中文，保留事实'); await page.locator('#memory-form button.primary').click();
  await page.waitForFunction(() => document.getElementById('memory-list').textContent.includes('保留事实'));
  assert.equal([...memories.values()][0].version, 2);
  await navigate('skills'); await page.locator('#skill-name').fill('fixture-skill'); await page.locator('#skill-description').fill('Fixture skill'); await page.locator('#skill-body').fill('# Safe skill\nUse fixtures only.'); await page.locator('#skill-form button.primary').click();
  await page.locator('#skills-list button').first().click(); await page.locator('#skill-body').fill('# Safe skill\nUpdated fixture.'); await page.locator('#skill-form button.primary').click();
  await page.waitForFunction(() => document.getElementById('skill-version').value === '2');
  assert.equal([...skills.values()][0].version, 2);
  await navigate('wallet'); await page.waitForFunction(() => document.getElementById('wallet-balance').textContent === '10.45');
  await page.waitForFunction(() => document.getElementById('usage').textContent.includes('人工结算'));
  assert.match(await page.locator('#usage').textContent(), /人工结算/);
  const lateRow = page.locator('#usage tr').filter({ hasText: 'browser-late-receipt' });
  assert.match(await lateRow.textContent(), /37 \/ 13/);
  assert.equal((await lateRow.locator('td').nth(4).textContent()).trim(), '0');
  await lateRow.locator('summary').click(); assert.equal(await lateRow.getByText('人工决定：释放冻结，不扣费', { exact: true }).isVisible(), true);
  const manualRow = page.locator('#usage tr').filter({ hasText: 'browser-pending' });
  assert.equal((await manualRow.locator('td').nth(2).textContent()).trim(), '待确认');
  await manualRow.locator('summary').click(); assert.equal(await manualRow.getByText('核对输入 / 输出：15 / 5 token', { exact: true }).isVisible(), true);
 await page.screenshot({ path: join(evidence, 'wallet-desktop.png') });
  await page.setViewportSize({ width: 390, height: 844 }); await navigate('overview');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: join(evidence, 'overview-mobile.png'), fullPage: true });
  await navigate('wallet'); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: join(evidence, 'wallet-mobile.png'), fullPage: true });
  assert.deepEqual(jsErrors, []);
  console.log(JSON.stringify({ result: 'passed', viewport_desktop: '1440x1000', viewport_mobile: '390x844', browser_errors: jsErrors.length, evidence, proof: ['register_zero_balance', 'admin_grant', 'pending_resolution', 'late_raw_receipt_separate_from_manual_charge', 'chat_metered_fixture', 'model_html_as_text', 'runtime_task_cancel_resume_result', 'versioned_memory_edit', 'versioned_skill_edit', 'usage_ledger', 'responsive_overflow'] }));
} finally { await browser?.close(); await app?.close(); delete process.env.PLATFORM_BROWSER_ADMIN; await rm(root, { recursive: true, force: true }); }
