// Local browser contract proof. Requires existing operator-provided Playwright/Chromium.
import assert from 'node:assert/strict';
import net from 'node:net';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createPlatform } from '../dist/platform/server.js';
import { PlatformStore } from '../dist/platform/store.js';
import { RpcError } from '../dist/validation.js';
if (!process.env.PLATFORM_PLAYWRIGHT_MODULE || !process.env.PLATFORM_CHROMIUM)
  throw Error('Provide existing PLATFORM_PLAYWRIGHT_MODULE and PLATFORM_CHROMIUM');
const { chromium } = await import(pathToFileURL(process.env.PLATFORM_PLAYWRIGHT_MODULE).href);
const root = await mkdtemp(join(tmpdir(), 'platform-pages-'));
const evidence = process.env.PLATFORM_BROWSER_EVIDENCE || (await mkdtemp(join(tmpdir(), 'platform-pages-evidence-')));
await mkdir(evidence, { recursive: true });
const probe = net.createServer();
await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve));
const port = probe.address().port;
await new Promise((resolve) => probe.close(resolve));
const origin = `http://127.0.0.1:${port}`;
process.env.PLATFORM_PAGES_ADMIN = 'fixture-admin-password';
const payload = Buffer.from('<!doctype html><script>window.__artifactXss=true</script>\nbyte:\0end', 'utf8');
const metadata = {
  path: 'reports/evil:page.html',
  size: payload.length,
  sha256: createHash('sha256').update(payload).digest('hex'),
};
const maxBytes = Buffer.alloc(8 * 1024 * 1024, 90);
const maxMetadata = {
  path: 'max.bin',
  size: maxBytes.length,
  sha256: createHash('sha256').update(maxBytes).digest('hex'),
};
const artifacts = [
  metadata,
  maxMetadata,
  ...['forbidden.txt', 'changed.txt', 'mismatch.txt', 'oversized.bin'].map((path) => ({
    ...metadata,
    path,
    ...(path === 'oversized.bin' ? { size: 8 * 1024 * 1024 + 1 } : {}),
  })),
];
let ns,
  app,
  browser,
  delayLatest = false,
  releaseLatest,
  latestStarted;
let historyRequests = 0,
  artifactRequests = 0;
const messages = Array.from({ length: 1205 }, (_, index) => ({
  seq: index + 1,
  role: 'assistant',
  content: [{ type: 'text', text: `历史消息 ${index + 1}` }],
}));
const sessionRows = Array.from({ length: 120 }, (_, index) => ({
  id: `session-${String(index).padStart(4, '0')}`,
  created_at: '2026-09-28T00:00:00Z',
}));
const taskRows = Array.from({ length: 105 }, (_, index) => ({
  id: `task-${String(index).padStart(4, '0')}`,
  status: 'succeeded',
  runtime: 'fixture-runtime',
  run_count: 1,
}));
const paginate = (rows, p, key) => {
  const offset = p.offset || 0,
    limit = p.limit || 100;
  return {
    [key]: rows.slice(offset, offset + limit),
    next_offset: offset + limit < rows.length ? offset + limit : null,
  };
};
async function dispatch(method, p) {
  if (p.namespace !== ns) return method === 'workshop.list' ? { tasks: [], next_offset: null } : [];
  if (method === 'agent.workshop.catalog') return [];
  if (method === 'agent.session.list') return paginate(sessionRows, p, 'sessions');
  if (method === 'agent.session.history') {
    historyRequests++;
    const all = p.session_id === sessionRows[0].id ? messages : messages.slice(0, 1);
    const eligible = all.filter((m) => m.seq < p.before),
      page = eligible.slice(-p.limit);
    const response = {
      messages: page,
      previous_before: eligible.length > p.limit ? page[0].seq : null,
      runs: p.session_id === sessionRows[0].id ? [{ id: 'active-fixture', status: 'running' }] : [],
    };
    if (delayLatest && p.before === Number.MAX_SAFE_INTEGER) {
      delayLatest = false;
      latestStarted();
      await new Promise((resolve) => {
        releaseLatest = resolve;
      });
    }
    return response;
  }
  if (method === 'agent.run.get') return { id: 'active-fixture', status: 'running' };
  if (method === 'agent.run.events') return { events: [] };
  if (method === 'workshop.list') return paginate(taskRows, p, 'tasks');
  if (method === 'workshop.get')
    return {
      ...taskRows.find((t) => t.id === p.task_id),
      runs: [{ id: 'artifact-run', status: 'succeeded', artifacts }],
    };
  if (method === 'workshop.events') return [];
  if (method === 'workshop.artifact') {
    artifactRequests++;
    assert.equal(p.task_id, taskRows[0].id);
    assert.equal(p.run_id, 'artifact-run');
    const registered = artifacts.find((a) => a.path === p.path);
    if (!registered || p.path === 'forbidden.txt') throw new RpcError(-32003, 'artifact_forbidden');
    if (p.path === 'mismatch.txt')
      return { ...registered, path: 'different.txt', data_base64: payload.toString('base64') };
    const bytes =
      p.path === 'max.bin' ? maxBytes : p.path === 'changed.txt' ? Buffer.alloc(payload.length, 65) : payload;
    return { ...registered, data_base64: bytes.toString('base64') };
  }
  throw new RpcError(-32601, 'method_not_found');
}
try {
  const database = join(root, 'platform.db');
  app = await createPlatform(
    {
      listen: `127.0.0.1:${port}`,
      database,
      public_origin: origin,
      secure_cookies: false,
      registration: true,
      bootstrap_admin: { email: 'admin@example.test', password_env: 'PLATFORM_PAGES_ADMIN' },
    },
    { rpc: dispatch }
  );
  browser = await chromium.launch({
    executablePath: process.env.PLATFORM_CHROMIUM,
    headless: true,
    args: ['--no-sandbox'],
  });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  await page.addInitScript(() => {
    window.__blobTypes = [];
    const original = URL.createObjectURL;
    URL.createObjectURL = function (blob) {
      window.__blobTypes.push(blob.type);
      return original.call(this, blob);
    };
  });
  const nav = (name) => page.locator(`[data-page="${name}"]`).click();
  await page.goto(origin);
  await page.locator('#auth-switch').click();
  await page.locator('[name=email]').fill('pages@example.test');
  await page.locator('[name=password]').fill('fixture-user-password');
  await page.locator('#auth-submit').click();
  await page.locator('#shell').waitFor({ state: 'visible' });
  const user = await page.evaluate(async () => (await (await fetch('/api/me')).json()).user);
  ns = user.namespace;
  const store = new PlatformStore(database);
  try {
    store.grant(
      { user_id: user.id, amount_micros: 1_000_001, reason: 'exact points fixture', idempotency_key: 'paging-seed' },
      'fixture'
    );
    store.transaction(() => {
      for (let i = 0; i < 103; i++)
        store.run(
          "INSERT INTO accounts(id,email,password,namespace,role,created_at) VALUES(?,?,?,?,'user','now')",
          `seed-${i}`,
          `seed${i}@example.test`,
          'unused',
          `seed-${i}`
        );
    });
    for (let i = 0; i < 125; i++) {
      app.wallet.reserve({
        namespace: ns,
        request_id: `usage-${i}`,
        fingerprint: `usage-${i}`,
        model: 'fixture',
        reserve_input_tokens: 0,
        reserve_output_tokens: 0,
        source: 'fixture',
      });
      app.wallet.settle({
        namespace: ns,
        request_id: `usage-${i}`,
        usage: { known: true, input_tokens: 0, output_tokens: 0 },
        outcome: 'complete',
        source: 'fixture',
      });
    }
    for (let i = 0; i < 105; i++)
      app.wallet.reserve({
        namespace: ns,
        request_id: `pending-${i}`,
        fingerprint: `pending-${i}`,
        model: 'fixture',
        reserve_input_tokens: 0,
        reserve_output_tokens: 0,
        source: 'fixture',
      });
  } finally {
    store.close();
  }
  await nav('chat');
  await page.waitForFunction(() => document.querySelectorAll('#sessions .list-row').length === 100);
  const firstSessions = await page.locator('#sessions button').allTextContents();
  await page.locator('#sessions-next').click();
  await page.waitForFunction(() => document.querySelectorAll('#sessions .list-row').length === 20);
  assert.equal(new Set([...firstSessions, ...(await page.locator('#sessions button').allTextContents())]).size, 120);
  assert.equal(await page.locator('#sessions-next').isDisabled(), true);
  await page.locator('#sessions-first').click();
  await page.locator('#sessions button').first().click();
  await page.waitForFunction(() => document.querySelector('#messages .message')?.dataset.seq === '1106');
  assert.equal(await page.locator('#messages .message').count(), 100);
  // Hold an old latest-page response while the user chooses an older cursor.
  const started = new Promise((resolve) => {
    latestStarted = resolve;
  });
  delayLatest = true;
  await page.locator('#refresh').click();
  await started;
  await page.locator('#history-older').click();
  await page.waitForFunction(() => document.querySelector('#messages .message')?.dataset.seq === '1006');
  const lateResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith('/api/rpc') &&
      response.request().postDataJSON()?.method === 'agent.session.history' &&
      response.request().postDataJSON()?.params.before === Number.MAX_SAFE_INTEGER
  );
  releaseLatest();
  await lateResponse;
  await page.waitForTimeout(100);
  assert.equal(await page.locator('#messages .message').first().getAttribute('data-seq'), '1006');
  const beforePolling = historyRequests;
  await page.waitForTimeout(4300);
  assert.equal(historyRequests, beforePolling);
  assert.equal(await page.locator('#messages .message').first().getAttribute('data-seq'), '1006');
  const seqs = new Set(
    await page.locator('#messages .message').evaluateAll((nodes) => nodes.map((n) => Number(n.dataset.seq)))
  );
  while (!(await page.locator('#history-older').isDisabled())) {
    const previous = await page.locator('#messages .message').first().getAttribute('data-seq');
    await page.locator('#history-older').click();
    await page.waitForFunction(
      (prior) => document.querySelector('#messages .message')?.dataset.seq !== prior,
      previous
    );
    for (const seq of await page
      .locator('#messages .message')
      .evaluateAll((nodes) => nodes.map((n) => Number(n.dataset.seq)))) {
      assert.equal(seqs.has(seq), false);
      seqs.add(seq);
    }
  }
  assert.equal(seqs.size, 1105);
  assert.equal(Math.min(...seqs), 1);
  await page.locator('#history-latest').click();
  await page.waitForFunction(() => document.querySelector('#messages .message')?.dataset.seq === '1106');
  assert.equal(await page.locator('#history-latest').isDisabled(), true);
  await page.locator('#history-older').click();
  await page.waitForFunction(() => document.querySelector('#messages .message')?.dataset.seq === '1006');
  await page.locator('#sessions button').nth(1).click();
  await page.waitForFunction(() => document.querySelector('#messages .message')?.dataset.seq === '1');
  assert.equal(await page.locator('#history-latest').isDisabled(), true);
  await nav('workshop');
  await page.waitForFunction(() => document.querySelectorAll('#tasks .list-row').length === 100);
  await page.locator('#tasks-next').click();
  await page.waitForFunction(() => document.querySelectorAll('#tasks .list-row').length === 5);
  assert.equal(await page.locator('#tasks-next').isDisabled(), true);
  await page.locator('#tasks-previous').click();
  await page.locator('#tasks button').first().click();
  await page.waitForFunction(() => document.querySelectorAll('#artifacts button').length === 6);
  let downloads = 0;
  page.on('download', () => downloads++);
  const downloaded = page.waitForEvent('download');
  await page.locator('#artifacts .list-row').filter({ hasText: metadata.path }).getByRole('button').click();
  const download = await downloaded;
  assert.equal(download.suggestedFilename(), 'evil_page.html');
  assert.deepEqual(await readFile(await download.path()), payload);
  assert.deepEqual(await page.evaluate(() => window.__blobTypes), ['application/octet-stream']);
  assert.equal(await page.evaluate(() => window.__artifactXss), undefined);
  const maxDownloaded = page.waitForEvent('download');
  await page.locator('#artifacts .list-row').filter({ hasText: 'max.bin' }).getByRole('button').click();
  const maxDownload = await maxDownloaded;
  assert.equal(maxDownload.suggestedFilename(), 'max.bin');
  assert.deepEqual(await readFile(await maxDownload.path()), maxBytes);
  assert.deepEqual(await page.evaluate(() => window.__blobTypes), [
    'application/octet-stream',
    'application/octet-stream',
  ]);
  for (const [path, expected] of [
    ['forbidden.txt', 'artifact_forbidden'],
    ['changed.txt', '校验失败'],
    ['mismatch.txt', '登记记录不符'],
    ['oversized.bin', '超过 8 MiB'],
  ]) {
    await page.locator('#artifacts .list-row').filter({ hasText: path }).getByRole('button').click();
    await page.waitForFunction((text) => document.getElementById('notice').textContent.includes(text), expected);
  }
  assert.equal(downloads, 2);
  assert.equal(artifactRequests, 5);
  await page.screenshot({ path: join(evidence, 'artifact-desktop.png'), fullPage: true });
  await nav('wallet');
  await page.waitForFunction(
    () =>
      document.querySelectorAll('#usage tr').length === 100 && document.querySelectorAll('#ledger tr').length === 100
  );
  assert.equal(await page.locator('#wallet-balance').textContent(), '1.000001');
  const usageIds = new Set();
  for (;;) {
    for (const id of await page.locator('#usage td:first-child small').allTextContents()) {
      assert.equal(usageIds.has(id), false);
      usageIds.add(id);
    }
    if (await page.locator('#usage-next').isDisabled()) break;
    const previous = await page.locator('#usage td:first-child small').first().textContent();
    await page.locator('#usage-next').click();
    await page.waitForFunction((prior) => document.querySelector('#usage td small')?.textContent !== prior, previous);
  }
  assert.equal(usageIds.size, 230);
  assert.equal(await page.locator('#usage tr').count(), 30);
  const firstLedger = await page.locator('#ledger tr').evaluateAll((rows) => rows.map((r) => r.dataset.seq));
  await page.locator('#ledger-next').click();
  await page.waitForFunction(() => document.querySelectorAll('#ledger tr').length === 26);
  assert.equal(await page.locator('#ledger-next').isDisabled(), true);
  assert.equal(
    new Set([
      ...firstLedger,
      ...(await page.locator('#ledger tr').evaluateAll((rows) => rows.map((r) => r.dataset.seq))),
    ]).size,
    126
  );
  await page.locator('#ledger-previous').click();
  await page.waitForFunction(() => document.querySelectorAll('#ledger tr').length === 100);
  assert.deepEqual(await page.locator('#ledger tr').evaluateAll((rows) => rows.map((r) => r.dataset.seq)), firstLedger);
  await page.locator('#usage-first').click();
  await page.waitForFunction(() => document.querySelectorAll('#usage tr').length === 100);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  await page.screenshot({ path: join(evidence, 'pagination-mobile.png'), fullPage: true });
  await page.locator('#logout').click();
  await page.locator('#auth-switch').click();
  await page.locator('[name=email]').fill('admin@example.test');
  await page.locator('[name=password]').fill('fixture-admin-password');
  await page.locator('#auth-submit').click();
  await page.locator('#shell').waitFor({ state: 'visible' });
  await nav('admin');
  await page.waitForFunction(() => document.querySelectorAll('#users .list-row').length === 100);
  await page.locator('#users-next').click();
  await page.waitForFunction(() => document.querySelectorAll('#users .list-row').length === 5);
  assert.equal(await page.locator('#users-next').isDisabled(), true);
  assert.equal(await page.locator('#grant-user option').count(), 5);
  await page.locator('#pending-next').click();
  await page.waitForFunction(() => document.querySelectorAll('#pending-receipts .list-row').length === 5);
  assert.equal(await page.locator('#resolve-request option').count(), 5);
  assert.equal(await page.locator('#pending-next').isDisabled(), true);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
  assert.deepEqual(errors, []);
  console.log(
    JSON.stringify({
      result: 'passed',
      sessions: 120,
      tasks: 105,
      messages: 1205,
      usage: usageIds.size,
      ledger: 126,
      admin_users: 105,
      pending: 105,
      stale_history_discarded: true,
      older_page_preserved_during_poll: true,
      exact_six_digit_points: true,
      verified_downloads: downloads,
      largest_download_bytes: maxBytes.length,
      rejected_artifacts: 4,
      browser_errors: errors.length,
      evidence,
    })
  );
} finally {
  releaseLatest?.();
  await browser?.close();
  await app?.close();
  delete process.env.PLATFORM_PAGES_ADMIN;
  await rm(root, { recursive: true, force: true });
}
