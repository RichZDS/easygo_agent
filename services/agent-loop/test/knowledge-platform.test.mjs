import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { createServer } from 'node:net';
import { spawn } from 'node:child_process';
import { Knowledge } from '../dist/knowledge/index.js';
import { callMethod } from '../dist/methods.js';

// This also runs after foreman integrates platform + Store outbox. In an isolated
// worker checkout, an explicit compiled snapshot can be supplied for the proof.
const dist = process.env.EASYGO_KNOWLEDGE_PLATFORM_DIST ?? fileURLToPath(new URL('../dist', import.meta.url));
// The Go half runs the remote TUI adapter test from the repository root.
const repo = resolve(fileURLToPath(new URL('../../../', import.meta.url)));
const go =
  process.env.EASYGO_GO_BIN ??
  (process.env.PATH ?? '')
    .split(delimiter)
    .filter(Boolean)
    .map((dir) => join(dir, 'go'))
    .find((file) => existsSync(file));
test(
  'actual TS public auth/Store completion outbox/Knowledge plus Go remote adapter',
  {
    skip:
      !existsSync(join(dist, 'platform/server.js')) ||
      (!go && 'needs Go: set EASYGO_GO_BIN or put go on PATH') ||
      (!existsSync(join(repo, 'internal/remotetui')) && 'needs the repository internal/remotetui'),
    timeout: 60000,
  },
  async (t) => {
    const { createPlatform } = await import(pathToFileURL(join(dist, 'platform/server.js')).href);
    const { Store } = await import(pathToFileURL(join(dist, 'store.js')).href);
    const root = mkdtempSync(join(tmpdir(), 'knowledge-platform-'));
    const store = new Store(join(root, 'core.db'));
    const k = new Knowledge(
      { database: join(root, 'knowledge.db') },
      {
        generate: async () => {
          throw new Error('No model calls in this integration proof');
        },
      }
    );
    t.after(async () => {
      await k.close();
      store.close();
      rmSync(root, { recursive: true, force: true });
    });
    assert.equal(typeof store.knowledgePending, 'function', 'foreman completion outbox is required');
    const probe = createServer();
    await new Promise((r) => probe.listen(0, '127.0.0.1', r));
    const port = probe.address().port;
    await new Promise((r) => probe.close(r));
    const origin = `http://127.0.0.1:${port}`;
    const platform = await createPlatform(
      {
        listen: `127.0.0.1:${port}`,
        database: join(root, 'platform.db'),
        public_origin: origin,
        secure_cookies: false,
        registration: true,
      },
      {
        rpc: async (method, p) => {
          if (method.startsWith('agent.memory.') || method.startsWith('agent.skills.'))
            return callMethod({ knowledge: k }, method, p);
          switch (method) {
            case 'agent.session.create':
              return store.createSession(p.namespace);
            case 'agent.session.list':
              return store.listSessions(p.namespace, p.offset ?? 0, p.limit ?? 100);
            case 'agent.session.history':
              return store.history(p.namespace, p.session_id, p.after ?? 0, p.limit ?? 1000);
            default:
              throw new Error('Unexpected fixture method');
          }
        },
      }
    );
    t.after(() => platform.close());
    async function request(path, body, cookie = '') {
      const r = await fetch(origin + path, {
        method: 'POST',
        headers: { origin, 'content-type': 'application/json', cookie },
        body: JSON.stringify(body),
      });
      return { status: r.status, body: await r.json(), cookie: r.headers.get('set-cookie')?.split(';')[0] };
    }
    const password = 'dummy-integration-password';
    const alice = await request('/api/register', { email: 'alice@example.test', password });
    assert.equal(alice.status, 201);
    const bob = await request('/api/register', { email: 'bob@example.test', password });
    assert.equal(bob.status, 201);
    assert.notEqual(alice.body.user.namespace, bob.body.user.namespace);
    const ns = alice.body.user.namespace;
    const s = store.createSession(ns);
    const run = store.start(ns, s.id, 'successful fixture', 'success');
    store.begin(run);
    const response = {
      model: 'fixture',
      message: { role: 'assistant', content: [{ type: 'text', text: 'completed fixture' }] },
      finish_reason: 'stop',
      usage: { known: true, input_tokens: 1, output_tokens: 1 },
      cost: { known: true, amount: 0 },
    };
    store.finish(run, 'completed', response.message ? [response.message] : [], response);
    const failed = store.start(ns, s.id, 'failed fixture', 'failed');
    store.begin(failed);
    store.finish(failed, 'failed', undefined, undefined, { code: 'fixture', message: 'failed' });
    const pending = store.knowledgePending();
    assert.equal(pending.length, 1);
    assert.equal(pending[0].run_id, run.id);
    assert(!JSON.stringify(pending[0]).includes('failed fixture'));
    for (const item of pending) {
      k.recordCompleted(item.namespace, item.run_id, item.messages);
      k.recordCompleted(item.namespace, item.run_id, item.messages);
      store.acknowledgeKnowledge(item.run_id);
    }
    assert.equal(store.knowledgePending().length, 0);
    const history = await request(
      '/api/rpc',
      { method: 'agent.session.history', params: { session_id: s.id } },
      alice.cookie
    );
    assert.equal(history.status, 200);
    assert.equal(history.body.result.runs.length, 2);
    const cross = await request(
      '/api/rpc',
      { method: 'agent.session.history', params: { session_id: s.id } },
      bob.cookie
    );
    assert.notEqual(cross.status, 200);
    await request(
      '/api/rpc',
      {
        method: 'agent.skills.upsert',
        params: { name: 'alice-private', description: 'private', content: 'private-body' },
      },
      alice.cookie
    );
    const denied = await request(
      '/api/rpc',
      { method: 'agent.skills.get', params: { name: 'alice-private' } },
      bob.cookie
    );
    assert.notEqual(denied.status, 200);
    const injected = await request(
      '/api/rpc',
      { method: 'agent.skills.get', params: { name: 'alice-private', namespace: ns } },
      bob.cookie
    );
    assert.notEqual(injected.status, 200);
    const command = spawn(go, ['test', './internal/remotetui', '-run', '^TestPlatformIntegration$', '-count=1', '-v'], {
      cwd: repo,
      env: {
        ...process.env,
        EASYGO_REMOTE_TEST_URL: origin,
        EASYGO_REMOTE_TEST_EMAIL: 'alice@example.test',
        EASYGO_REMOTE_TEST_PASSWORD: password,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let output = '';
    command.stdout.on('data', (b) => (output += b));
    command.stderr.on('data', (b) => (output += b));
    const exit = await new Promise((r, j) => {
      command.once('error', j);
      command.once('close', r);
    });
    assert.equal(exit, 0, output);
    assert(output.includes('--- PASS: TestPlatformIntegration'));
  }
);
