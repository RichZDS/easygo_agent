// Real deployed-stack browser acceptance. No fixture RPC handlers or server imports.
// Required: PLATFORM_ORIGIN, PLATFORM_ADMIN_EMAIL, PLATFORM_ADMIN_PASSWORD,
// PLATFORM_PLAYWRIGHT_MODULE, PLATFORM_CHROMIUM, PLATFORM_BROWSER_EVIDENCE.
// The target must be explicitly configured with the offline provider/workshop fixtures.
import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const required = name => { const value = process.env[name]; if (!value) throw Error(`Missing ${name}`); return value; };
const origin = new URL(required('PLATFORM_ORIGIN')).origin;
const adminEmail = required('PLATFORM_ADMIN_EMAIL'), adminPassword = required('PLATFORM_ADMIN_PASSWORD');
const evidence = resolve(required('PLATFORM_BROWSER_EVIDENCE'));
const { chromium } = await import(pathToFileURL(required('PLATFORM_PLAYWRIGHT_MODULE')).href);
const executablePath = required('PLATFORM_CHROMIUM');
const suffix = randomUUID().slice(0, 8), password = `live-test-${randomUUID()}`;
const email = `live-${suffix}@example.test`, otherEmail = `other-live-${suffix}@example.test`;
const redact = value => String(value).split(adminPassword).join('<REDACTED>').split(password).join('<REDACTED>');
const began = Date.now(), report = { origin, started_at: new Date().toISOString(), credentials: '<REDACTED: environment only>', steps: [], screenshots: [], page_errors: [], limitations: [], pass: false };
await mkdir(evidence, { recursive: true, mode: 0o700 });
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
let browser, user, admin, other, currentStep, sessionId, taskId, taskRunId, firstReplyRun;
const terminal = status => !['queued', 'running', 'cancelling'].includes(status);
function matchResponse(response, path, method) {
  if (new URL(response.url()).pathname !== path) return false;
  if (!method) return true;
  try { return response.request().postDataJSON()?.method === method; } catch { return false; }
}
async function observed(page, path, action, method) {
  const responsePromise = page.waitForResponse(response => matchResponse(response, path, method));
  await action(); const response = await responsePromise; const data = await response.json();
  assert.equal(response.ok(), true, `UI request ${path}: ${data.error?.code || response.status()}`);
  assert.equal(data.error, undefined, `UI request ${method || path} returned ${data.error?.code}`);
  return data;
}
async function uiRPC(page, method, action) { return (await observed(page, '/api/rpc', action, method)).result; }
async function nav(page, view) {
  await page.bringToFront(); await page.locator(`[data-page="${view}"]`).click();
  await page.locator(`[data-view="${view}"]`).waitFor({ state: 'visible' });
}
async function screenshot(page, name) {
  const file = `${name}.png`; await page.screenshot({ path: join(evidence, file), fullPage: true, mask: [page.locator('input[type="password"]')] }); report.screenshots.push(file);
}
async function pageFor() {
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  const page = await context.newPage(); page.setDefaultTimeout(30_000); page.on('pageerror', e => report.page_errors.push(redact(e.message)));
  await page.goto(origin); return page;
}
async function register(page, address) {
  await page.locator('#auth-switch').click(); await page.locator('[name=email]').fill(address); await page.locator('[name=password]').fill(password);
  const data = await observed(page, '/api/register', () => page.locator('#auth-submit').click());
  await page.locator('#shell').waitFor({ state: 'visible' }); return data.user;
}
async function walletFromUI(page) {
  const balances = page.waitForResponse(r => matchResponse(r, '/api/wallet'));
  const receipts = page.waitForResponse(r => matchResponse(r, '/api/usage'));
  await nav(page, 'wallet');
  const [w, u] = await Promise.all([balances, receipts]);
  assert.equal(w.status(), 200); assert.equal(u.status(), 200);
  return { wallet: await w.json(), usage: await u.json() };
}
async function step(name, fn) {
  currentStep = name; const started = Date.now(); console.log(`STEP ${name}`);
  try { const details = await fn(); report.steps.push({ name, pass: true, elapsed_ms: Date.now() - started, ...(details ? { details } : {}) }); console.log(`PASS ${name} ${Date.now() - started}ms`); }
  catch (error) { report.steps.push({ name, pass: false, elapsed_ms: Date.now() - started, error: redact(error.message) }); throw error; }
}
// Deliberate exceptions to UI-only interaction: real RPC history population and
// adversarial cross-account requests, both explicitly required by the work order.
async function directRPC(page, method, params) {
  return page.evaluate(async ({ method, params }) => {
    const response = await fetch('/api/rpc', { method: 'POST', credentials: 'same-origin', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ method, params }) });
    return { status: response.status, body: await response.json() };
  }, { method, params });
}
async function directOK(page, method, params) {
  const response = await directRPC(page, method, params);
  assert.equal(response.status, 200, `${method}: ${response.body.error?.code || response.status}`);
  assert.equal(response.body.error, undefined); return response.body.result;
}
async function waitTaskUI(page, wanted, timeout = 60_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const detail = await page.locator('#task-detail').textContent();
    try { const task = JSON.parse(detail); if (task.status === wanted) return task; if (terminal(task.status) && task.status !== wanted) throw Error(`Workshop ended ${task.status}: ${JSON.stringify(task.runs?.map(r => ({ status: r.status, error: r.error })))}`); }
    catch (e) { if (e.message.startsWith('Workshop ended')) throw e; }
    await sleep(500);
  }
  throw Error(`Task UI did not reach ${wanted}`);
}
try {
  browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] });
  user = await pageFor(); let identity;
  await step('register_zero_balance_rejection', async () => {
    await screenshot(user, 'login-desktop'); identity = await register(user, email); report.user = { email, namespace: identity.namespace, id: identity.id };
    await user.waitForFunction(() => document.getElementById('overview-balance').textContent === '0');
    await nav(user, 'chat'); await user.locator('#chat-runtime').selectOption('fixture');
    await uiRPC(user, 'agent.session.create', () => user.locator('#new-session').click());
    await user.locator('#chat-input').fill(`ZERO_CREDIT_${suffix}`);
    await uiRPC(user, 'agent.run.start', () => user.locator('#send-chat').click());
    await user.waitForFunction(() => /(?:额度|积分)不足/.test(document.getElementById('notice').textContent), null, { timeout: 25_000 });
    const visible = await user.locator('#notice').textContent();
    const data = await walletFromUI(user); assert.equal(data.wallet.balance_micros, 0); assert.equal(data.wallet.held_micros, 0); assert.equal(data.usage.receipts.length, 0);
    report.limitations.push('Provider invocation counter is not exposed by the public Web contract. This browser run asserts the insufficient-credit failure and no wallet receipt, but does not independently assert provider-call count. The local stack launcher has its own provider-count assertion.');
    return { visible_error: visible, initial_balance_micros: 0, provider_call_count: { asserted: false, reason: 'not observable from public browser interface' } };
  });
  await step('admin_grant_real_transport_retry_idempotency', async () => {
    admin = await pageFor(); await admin.locator('[name=email]').fill(adminEmail); await admin.locator('[name=password]').fill(adminPassword);
    await observed(admin, '/api/login', () => admin.locator('#auth-submit').click()); await admin.locator('#shell').waitFor({ state: 'visible' });
    await nav(admin, 'admin');
    for (let i = 0; i < 100 && !await admin.locator(`#grant-user option[value="${identity.id}"]`).count(); i++) {
      await admin.waitForTimeout(100);
      if (await admin.locator(`#grant-user option[value="${identity.id}"]`).count()) break;
      if (await admin.locator('#users-next').isDisabled()) throw Error('Newly registered user missing from administrator pages');
      await observed(admin, '/api/admin/users', () => admin.locator('#users-next').click());
    }
    await admin.locator('#grant-user').selectOption(identity.id); await admin.locator('#grant-amount').fill('1000'); await admin.locator('#grant-reason').fill(`live-browser-${suffix}`);
    let first = true, originalKey, firstReceipt;
    await admin.route('**/api/admin/credits', async route => {
      if (!first) { assert.equal(route.request().postDataJSON().idempotency_key, originalKey); await route.continue(); return; }
      first = false; originalKey = route.request().postDataJSON().idempotency_key;
      const actual = await route.fetch(); assert.equal(actual.status(), 200); firstReceipt = await actual.json();
      // Server really commits the first grant; lose only its response to exercise UI retry.
      await route.abort('failed');
    });
    await admin.locator('#grant-form button').click(); await admin.waitForFunction(() => document.getElementById('notice').textContent.includes('fetch'));
    assert.equal(firstReceipt.duplicate, false);
    const replay = await observed(admin, '/api/admin/credits', () => admin.locator('#grant-form button').click());
    assert.equal(replay.duplicate, true); assert.equal(replay.ledger_seq, firstReceipt.ledger_seq); await admin.unroute('**/api/admin/credits');
    const balances = await walletFromUI(user); assert.equal(balances.wallet.balance_micros, 1_000_000_000); assert.equal(balances.wallet.ledger.filter(r => r.kind === 'grant').length, 1);
    return { amount_micros: 1_000_000_000, replay_duplicate: true, grant_ledger_entries: 1, fault: 'aborted first successful real grant response; retried through UI' };
  });
  await step('runtime_chat_exact_raw_token_debit', async () => {
    await nav(user, 'chat'); const session = await uiRPC(user, 'agent.session.create', () => user.locator('#new-session').click()); sessionId = session.id;
    await user.locator('#chat-runtime').selectOption('fixture'); await user.locator('#chat-input').fill(`LIVE_BROWSER_${suffix}`);
    firstReplyRun = await uiRPC(user, 'agent.run.start', () => user.locator('#send-chat').click());
    assert.equal(firstReplyRun.workshop_runtime, 'fixture');
    await user.waitForFunction(() => document.getElementById('messages').textContent.includes('PLATFORM_OK'), null, { timeout: 30_000 });
    await user.waitForFunction(() => document.getElementById('run-status').textContent.includes('已完成'));
    const data = await walletFromUI(user); const receipt = data.usage.receipts.find(r => r.status === 'settled');
    assert.ok(receipt); assert.equal(receipt.settlement.usage.known, true);
    const usage = receipt.settlement.usage, charged = (usage.input_tokens + usage.output_tokens) * 1000;
    assert.equal(receipt.charged_micros, charged); assert.equal(data.wallet.balance_micros, 1_000_000_000 - charged); assert.equal(data.wallet.held_micros, 0);
    assert.equal(await user.locator('#usage tr').count(), 1);
    return { session_id: sessionId, run_id: firstReplyRun.id, usage, charged_micros: charged, balance_micros: data.wallet.balance_micros };
  });
  await step('docker_workshop_ui_artifact_download_resume_cancel', async () => {
    await nav(user, 'workshop'); await user.locator('#workflow').selectOption('proof'); await user.locator('#task-runtime').selectOption('fixture');
    await user.locator('#task-input').fill(JSON.stringify({ mode: 'first' }));
    const created = await uiRPC(user, 'workshop.submit', () => user.locator('#task-form button').click()); taskId = created.id;
    const completed = await waitTaskUI(user, 'succeeded'); taskRunId = completed.runs.at(-1).id;
    const expected = completed.runs.at(-1).artifacts.find(a => a.path === 'artifact.txt'); assert.ok(expected);
    const downloaded = user.waitForEvent('download'); await user.locator('#artifacts .list-row').filter({ hasText: 'artifact.txt' }).getByRole('button').click();
    const download = await downloaded; assert.equal(download.suggestedFilename(), 'artifact.txt');
    const file = join(evidence, 'downloaded-artifact.txt'); await download.saveAs(file); const bytes = await readFile(file);
    assert.equal(bytes.toString('utf8'), 'own-artifact'); const sha256 = createHash('sha256').update(bytes).digest('hex'); assert.equal(sha256, expected.sha256); assert.equal(bytes.length, expected.size);
    await user.locator('#resume-input').fill(JSON.stringify({ mode: 'resume' })); await uiRPC(user, 'workshop.resume', () => user.locator('#resume-task').click());
    await user.waitForFunction(() => { try { return JSON.parse(document.getElementById('task-detail').textContent).run_count >= 2; } catch { return false; } });
    const resumed = await waitTaskUI(user, 'succeeded'); assert.equal(resumed.run_count, 2); taskRunId = resumed.runs.at(-1).id;
    await user.locator('#task-input').fill(JSON.stringify({ mode: 'sleep' })); const sleeping = await uiRPC(user, 'workshop.submit', () => user.locator('#task-form button').click());
    await user.waitForFunction(id => { try { const t = JSON.parse(document.getElementById('task-detail').textContent); return t.id === id && t.status === 'running'; } catch { return false; } }, sleeping.id);
    await sleep(1200); await uiRPC(user, 'workshop.cancel', () => user.locator('#cancel-task').click());
    const canceled = await waitTaskUI(user, 'cancelled'); assert.equal(canceled.id, sleeping.id);
    return { task_id: taskId, run_count: resumed.run_count, artifact: { file: 'downloaded-artifact.txt', size: bytes.length, sha256 }, canceled_task_id: sleeping.id, canceled_status: canceled.status };
  });
  await step('real_history_over_100_messages_reverse_pagination', async () => {
    // Keep the UI on overview while populating so its run polling does not consume the rate budget.
    await nav(user, 'overview'); const runIds = [];
    for (let index = 0; index < 51; index++) {
      const started = Date.now(); const run = await directOK(user, 'agent.run.start', { session_id: sessionId, input: `HISTORY_${suffix}_${index}`, idempotency_key: randomUUID(), workshop_runtime: 'fixture' });
      let done;
      for (let poll = 0; poll < 40; poll++) { await sleep(750); done = await directOK(user, 'agent.run.get', { run_id: run.id }); if (terminal(done.status)) break; }
      assert.equal(done.status, 'completed', `History run failed: ${done.error?.code}`); runIds.push(run.id);
      if ((index + 1) % 10 === 0) console.log(`PROGRESS history ${index + 1}/51`);
      await sleep(Math.max(0, 1800 - (Date.now() - started)));
    }
    const full = await directOK(user, 'agent.session.history', { session_id: sessionId, after: 0, limit: 1000 });
    assert.ok(full.messages.length > 100); const expected = full.messages.map(m => m.seq);
    await nav(user, 'chat'); await user.locator('#sessions button').filter({ hasText: sessionId.slice(0, 12) }).click();
    await user.waitForFunction(() => document.querySelectorAll('#messages .message').length === 100);
    const newest = await user.locator('#messages .message').evaluateAll(nodes => nodes.map(n => Number(n.dataset.seq))); const gathered = [...newest];
    while (!await user.locator('#history-older').isDisabled()) {
      const before = await user.locator('#messages .message').first().getAttribute('data-seq'); await user.locator('#history-older').click();
      await user.waitForFunction(prior => document.querySelector('#messages .message')?.dataset.seq !== prior, before);
      gathered.push(...await user.locator('#messages .message').evaluateAll(nodes => nodes.map(n => Number(n.dataset.seq))));
    }
    assert.equal(new Set(gathered).size, gathered.length); assert.deepEqual([...gathered].sort((a, b) => a - b), expected);
    await user.locator('#history-latest').click(); await user.waitForFunction(first => document.querySelector('#messages .message')?.dataset.seq === String(first), newest[0]);
    assert.deepEqual(await user.locator('#messages .message').evaluateAll(nodes => nodes.map(n => Number(n.dataset.seq))), newest);
    return { generated_additional_runs: runIds.length, messages: expected.length, pages: Math.ceil(expected.length / 100), no_duplicate_or_missing_seq: true, population: 'real authenticated RPC exception authorized by blueprint, 1800ms minimum between submissions' };
  });
  await step('second_account_ui_and_direct_rpc_isolation', async () => {
    other = await pageFor(); await register(other, otherEmail); await nav(other, 'chat'); await other.waitForFunction(() => document.getElementById('sessions').textContent.includes('还没有内容'));
    assert.equal(await other.locator('#sessions button').count(), 0); await nav(other, 'workshop'); await other.waitForFunction(() => document.getElementById('tasks').textContent.includes('还没有内容'));
    assert.equal(await other.locator('#tasks button').count(), 0); assert.equal(await other.locator('#artifacts button').count(), 0);
    const denied = [];
    for (const [method, params] of [['agent.session.history', { session_id: sessionId }], ['workshop.get', { task_id: taskId }], ['workshop.artifact', { task_id: taskId, run_id: taskRunId, path: 'artifact.txt' }]]) {
      const response = await directRPC(other, method, params); assert.ok(response.status >= 400); assert.ok(response.body.error); assert.equal(response.body.result, undefined); denied.push({ method, status: response.status, code: response.body.error.code });
    }
    return { second_user: otherEmail, no_foreign_ui_records: true, direct_denials: denied };
  });
  await step('all_main_pages_desktop_and_mobile_screenshots', async () => {
    for (const [size, viewport] of [['desktop', { width: 1440, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
      await user.setViewportSize(viewport); await admin.setViewportSize(viewport);
      for (const view of ['overview', 'chat', 'workshop', 'memory', 'skills', 'wallet']) {
        await nav(user, view); await user.waitForTimeout(250);
        assert.equal(await user.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `Horizontal overflow: ${view} ${size}`);
        await screenshot(user, `${view}-${size}`);
      }
      await nav(admin, 'admin'); await admin.waitForTimeout(250); assert.equal(await admin.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `Horizontal overflow: admin ${size}`); await screenshot(admin, `admin-${size}`);
    }
    assert.deepEqual(report.page_errors, []); return { desktop: '1440x1000', mobile: '390x844', main_pages_per_viewport: 7, page_errors: 0 };
  });
  report.pass = true;
} catch (error) {
  report.error = { step: currentStep, message: redact(error.message), stack: redact(error.stack) };
  console.error(`FAIL ${currentStep}: ${redact(error.message)}`); process.exitCode = 1;
  for (const [name, page] of [['user', user], ['admin', admin], ['other', other]]) if (page && !page.isClosed()) { try { await screenshot(page, `failure-${name}`); } catch { /* Preserve the original failure. */ } }
} finally {
  await browser?.close(); report.elapsed_ms = Date.now() - began; report.finished_at = new Date().toISOString();
  await writeFile(join(evidence, 'report.json'), redact(JSON.stringify(report, null, 2)), { mode: 0o600 });
  console.log(JSON.stringify({ pass: report.pass, elapsed_ms: report.elapsed_ms, completed_steps: report.steps.filter(s => s.pass).length, report: join(evidence, 'report.json') }));
}
