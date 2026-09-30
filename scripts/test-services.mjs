#!/usr/bin/env node
// Real isolated processes and TLS sockets. Model and coding CLI are local fixtures.
import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import { once } from 'node:events';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { buildServices, exec, loopServer, ports, sleep, testbed, until } from './lib/procs.mjs';
import { createPKI } from './lib/pki.mjs';
import { writeConfigs } from './lib/config.mjs';

const state = await mkdtemp(join(tmpdir(), 'easygo-rpc-e2e-'));
const clients = [];
const log = (message) => process.stdout.write(`${message}\n`);
let upstream;
const report = { state, checks: [], paid_models: false, real_subprocess: true, real_mtls: true };
const bed = testbed(state, { report });
const checked = (name) => {
  report.checks.push(name);
  log(`PASS ${name}`);
};
const proof = { timeout: 15_000, interval: 30, label: 'proof', retryErrors: true };

try {
  const certs = await createPKI(join(state, 'pki'), { namespaces: [] });
  const rogue = await createPKI(join(state, 'untrusted-pki'));
  const expiredCert = join(state, 'expired-client.crt');
  await exec('openssl', [
    'x509',
    '-req',
    '-sha256',
    '-days',
    '0',
    '-in',
    join(certs.dir, 'client/request.csr'),
    '-CA',
    certs.cert('ca'),
    '-CAkey',
    join(certs.dir, '.ca/ca.key'),
    '-CAserial',
    join(certs.dir, '.ca/serial'),
    '-extfile',
    join(certs.dir, 'client/extensions.cnf'),
    '-out',
    expiredCert,
  ]);
  await buildServices(state);
  checked('each service builds independently');

  const textOf = (content) =>
    typeof content === 'string' ? content : (content ?? []).map((b) => b.text ?? '').join('');
  let providerCalls = 0,
    canceledProvider = 0;
  const started = new Set(),
    tasks = new Set();
  const modelHandler = async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    const input = JSON.parse(body);
    providerCalls++;
    if (req.headers.authorization !== 'Bearer fixture-provider-key') {
      res.writeHead(401);
      res.end('{}');
      return;
    }
    const prompt = input.messages
      .filter((m) => m.role === 'user')
      .map((m) => textOf(m.content))
      .join('\n');
    if (prompt.includes('CANCEL_ME') || prompt.includes('RESTART_ME')) {
      const key = prompt.includes('CANCEL_ME') ? 'cancel' : 'restart';
      started.add(key);
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      res.write(
        `data: ${JSON.stringify({ choices: [{ index: 0, delta: { role: 'assistant', content: 'provisional' }, finish_reason: null }] })}\n\n`
      );
      res.on('close', () => canceledProvider++);
      return;
    }
    let content = 'DIRECT_OK',
      tool;
    if (prompt.includes('NATIVE_REUSE')) {
      assert.equal(input.model, 'fixture-model');
      content = 'NATIVE_REUSED';
    }
    const last = input.messages.filter((m) => m.role === 'tool').at(-1);
    if (prompt.includes('MAKE_ARTIFACT')) {
      if (!last) tool = { name: 'workshop_catalog', arguments: '{}' };
      else {
        const result = JSON.parse(last.content);
        if (Array.isArray(result))
          tool = { name: 'workshop_submit', arguments: JSON.stringify({ workflow: 'proof', input: 'write proof' }) };
        else {
          assert.ok(result.id, `Unexpected tool output: ${last.content}`);
          tasks.add(result.id);
          if (result.status === 'succeeded') {
            assert.equal(
              result.runs.at(-1).artifacts[0].sha256,
              createHash('sha256').update('verified artifact').digest('hex')
            );
            content = 'VERIFIED_WORKSHOP_ARTIFACT';
          } else {
            assert.ok(['queued', 'running'].includes(result.status), `Unexpected task status ${result.status}`);
            tool = { name: 'workshop_get', arguments: JSON.stringify({ task_id: result.id }) };
            await sleep(15);
          }
        }
      }
    }
    if (prompt.includes('RECOVER_TOOL_ERROR')) {
      if (!last) tool = { name: 'workshop_get', arguments: JSON.stringify({}) };
      else {
        assert.match(last.content, /invalid_string/);
        content = 'RECOVERED_TOOL_ERROR';
      }
    }
    const id = `call-${providerCalls}`;
    const message = tool
      ? { role: 'assistant', content: null, tool_calls: [{ id, type: 'function', function: tool }] }
      : { role: 'assistant', content };
    const usage = { prompt_tokens: 20, completion_tokens: 4, total_tokens: 24 };
    if (!input.stream) {
      res.setHeader('Content-Type', 'application/json');
      res.end(
        JSON.stringify({
          id: `response-${providerCalls}`,
          choices: [{ message, finish_reason: tool ? 'tool_calls' : 'stop' }],
          usage,
        })
      );
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    const delta = tool
      ? { role: 'assistant', tool_calls: [{ index: 0, id, type: 'function', function: tool }] }
      : { role: 'assistant', content };
    res.write(
      `data: ${JSON.stringify({ id: `response-${providerCalls}`, choices: [{ index: 0, delta, finish_reason: null }] })}\n\n`
    );
    res.write(
      `data: ${JSON.stringify({ choices: [{ index: 0, delta: {}, finish_reason: tool ? 'tool_calls' : 'stop' }] })}\n\n`
    );
    res.write(`data: ${JSON.stringify({ choices: [], usage })}\n\n`);
    res.end('data: [DONE]\n\n');
  };
  upstream = http.createServer((req, res) => {
    modelHandler(req, res).catch((error) => {
      process.stderr.write(`Model fixture assertion failed: ${error.message}\n`);
      if (!res.headersSent) res.writeHead(500);
      res.end();
    });
  });
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  const [gatewayPort, agentPort, workshopPort, secondAgentPort] = await ports(4);
  const { identity, cert: publicCert, grant, endpoint } = certs;
  function client(name, target, port, changes = {}) {
    const c = certs.client(name, target, port, { timeoutMs: 5_000, ...changes });
    clients.push(c);
    return c;
  }
  const fixture = join(state, 'coding-fixture');
  await writeFile(
    fixture,
    `#!/usr/bin/python3\nimport json,pathlib,sys\n_ = sys.stdin.read()\npathlib.Path('proof.txt').write_text('verified artifact')\nprint(json.dumps({'type':'thread.started','thread_id':'11111111-2222-4333-8444-555555555555'}))\nprint(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':'verified artifact'}}))\nprint(json.dumps({'type':'turn.completed','usage':{'input_tokens':1,'output_tokens':2}}))\n`,
    { mode: 0o700 }
  );
  const piFixture = join(state, 'pi-fixture');
  await writeFile(
    piFixture,
    `#!/usr/bin/python3
import json,os,pathlib,sys,urllib.request
_ = sys.stdin.read()
assert 'EASYGO_RPC_TEST_PROVIDER_KEY' not in os.environ
config=json.loads((pathlib.Path(os.environ['PI_CODING_AGENT_DIR'])/'models.json').read_text())['providers']['easygo']
request=urllib.request.Request(config['baseUrl']+'/chat/completions',data=json.dumps({'model':'untrusted-override','messages':[{'role':'user','content':'NATIVE_REUSE'}]}).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+os.environ['EASYGO_RUNTIME_API_KEY']})
with urllib.request.urlopen(request,timeout=5) as response: result=json.load(response)
assert result['choices'][0]['message']['content']=='NATIVE_REUSED'
pathlib.Path('reuse.txt').write_text('NATIVE_REUSED')
print(json.dumps({'type':'session','id':'11111111-2222-4333-8444-555555555555'}))
print(json.dumps({'type':'message_end','message':{'role':'assistant','stopReason':'stop','content':[{'type':'text','text':'NATIVE_REUSED'}],'usage':{'input':1,'output':1}}}))
print(json.dumps({'type':'agent_end'}))
print(json.dumps({'type':'agent_settled'}))
`,
    { mode: 0o700 }
  );
  const gatewayConfig = {
    listen: `127.0.0.1:${gatewayPort}`,
    tls: identity('ai-gateway'),
    authorization: [
      grant('ai-gateway', ['health']),
      grant('agent-loop', ['health', 'gateway.generate', 'gateway.models'], ['*']),
      grant('client', ['gateway.models'], ['demo']),
      grant('workshop', ['gateway.native'], ['demo']),
      {
        id: 'untrusted-ca-client',
        cert_file: rogue.cert('client'),
        methods: ['gateway.models'],
        namespaces: ['demo'],
      },
      { id: 'expired-client', cert_file: expiredCert, methods: ['gateway.models'], namespaces: ['demo'] },
    ],
    models: {
      chat: {
        protocol: 'chat_completions',
        endpoint: `http://127.0.0.1:${upstream.address().port}/chat/completions`,
        model: 'fixture-model',
        api_key_env: 'EASYGO_RPC_TEST_PROVIDER_KEY',
      },
    },
  };
  const workshopConfig = {
    listen: `127.0.0.1:${workshopPort}`,
    tls: identity('workshop'),
    authorization: [
      grant('workshop', ['health']),
      grant(
        'agent-loop',
        [
          'health',
          'workshop.workflows',
          'workshop.submit',
          'workshop.get',
          'workshop.list',
          'workshop.cancel',
          'workshop.resume',
          'workshop.result',
          'workshop.events',
        ],
        ['*']
      ),
    ],
    workshop: {
      root: join(state, 'workshop-data'),
      concurrency: 1,
      queue_capacity: 4,
      engines: { codex: { binary: fixture }, pi: { binary: piFixture } },
      model_gateway: { ...endpoint('ai-gateway', gatewayPort), tls: identity('workshop') },
      runtime_profiles: { 'pi-main': { engine: 'pi', protocol: 'chat_completions', gateway_model: 'chat' } },
      workflows: [
        {
          name: 'proof',
          version: '1',
          instructions: 'Create proof.txt.',
          engine: 'codex',
          model: 'fixture',
          policy: 'workspace-write',
          timeout_seconds: 5,
          artifacts: ['proof.txt'],
        },
        {
          name: 'reuse',
          version: '1',
          instructions: 'Reuse main model',
          runtime: 'pi-main',
          policy: 'workspace-write',
          timeout_seconds: 10,
          artifacts: ['reuse.txt'],
        },
      ],
    },
  };
  const agentMethods = [
    'agent.workshop.catalog',
    'agent.session.create',
    'agent.session.list',
    'agent.session.history',
    'agent.run.start',
    'agent.run.get',
    'agent.run.cancel',
    'agent.run.events',
  ];
  const agentConfig = {
    listen: `127.0.0.1:${agentPort}`,
    tls: identity('agent-loop'),
    authorization: [grant('agent-loop', ['health']), grant('client', agentMethods, ['demo'])],
    database: join(state, 'agent-data/agent.sqlite'),
    gateway: endpoint('ai-gateway', gatewayPort),
    workshop: endpoint('workshop', workshopPort),
    model: 'chat',
    streaming: true,
    max_steps: 64,
    context_bytes: 1_000_000,
    concurrency: 2,
    system_prompt: 'Use configured tools to complete the request.',
  };
  await writeConfigs(state, { gateway: gatewayConfig, workshop: workshopConfig, agent: agentConfig });
  const gatewayChild = bed.start('gateway', join(state, 'gateway'), ['--config', join(state, 'gateway.json')], {
    EASYGO_RPC_TEST_PROVIDER_KEY: 'fixture-provider-key',
  });
  const workshopChild = bed.start('workshop', join(state, 'workshop'), ['--config', join(state, 'workshop.json')]);
  let agentChild = bed.start('agent', process.execPath, [loopServer, '--config', join(state, 'agent.json')]);
  const gatewayHealth = client('ai-gateway', 'ai-gateway', gatewayPort);
  const workshopHealth = client('workshop', 'workshop', workshopPort);
  const agentHealth = client('agent-loop', 'agent-loop', agentPort);
  const app = client('client', 'agent-loop', agentPort);
  const broker = client('agent-loop', 'workshop', workshopPort);
  const readonlyGateway = client('client', 'ai-gateway', gatewayPort);
  await Promise.all([gatewayHealth, workshopHealth, agentHealth].map((c) => until(() => c.health(), proof)));
  checked('three independent processes accept only their configured mTLS identities');

  assert.deepEqual((await readonlyGateway.call('gateway.models', { namespace: 'demo' })).models, ['chat']);
  const before = providerCalls;
  await assert.rejects(
    readonlyGateway.call('gateway.generate', { namespace: 'demo', request: { model: 'chat', messages: [] } })
  );
  await assert.rejects(readonlyGateway.call('gateway.models', { namespace: 'other' }));
  await assert.rejects(app.call('agent.session.create', { namespace: 'other' }));
  await assert.rejects(
    app.call('workshop.submit', { namespace: 'demo', workflow: 'proof', input: 'x', idempotency_key: 'bad' })
  );
  assert.equal(providerCalls, before);
  const wrongPeer = client('client', 'ai-gateway', gatewayPort, { peerCertificateFile: publicCert('workshop') });
  await assert.rejects(wrongPeer.call('gateway.models', { namespace: 'demo' }));
  const unknownPeer = client('workshop', 'ai-gateway', gatewayPort);
  await assert.rejects(unknownPeer.call('gateway.models', { namespace: 'demo' }));
  const untrusted = client('client', 'ai-gateway', gatewayPort, {
    certFile: rogue.identity('client').cert_file,
    keyFile: rogue.identity('client').key_file,
  });
  await assert.rejects(untrusted.call('gateway.models', { namespace: 'demo' }));
  const expired = client('client', 'ai-gateway', gatewayPort, { certFile: expiredCert });
  await assert.rejects(expired.call('gateway.models', { namespace: 'demo' }));
  await assert.rejects(
    new Promise((resolve, reject) => {
      https
        .get(`https://127.0.0.1:${gatewayPort}/healthz`, { ca: readFileSync(publicCert('ca')), agent: false }, resolve)
        .on('error', reject);
    })
  );
  checked(
    'no certificate, untrusted CA, expired leaf, unknown leaf, wrong server pin, forbidden method and namespace are rejected'
  );

  const session = await app.call('agent.session.create', { namespace: 'demo' });
  const startParams = {
    namespace: 'demo',
    session_id: session.id,
    input: 'MAKE_ARTIFACT',
    idempotency_key: 'proof-once',
  };
  const run = await app.call('agent.run.start', startParams);
  assert.equal((await app.call('agent.run.start', startParams)).id, run.id);
  await assert.rejects(app.call('agent.run.start', { ...startParams, input: 'different input' }));
  const completed = await until(async () => {
    const r = await app.call('agent.run.get', { namespace: 'demo', run_id: run.id });
    return ['completed', 'failed', 'canceled', 'interrupted'].includes(r.status) ? r : false;
  }, proof);
  assert.equal(completed.status, 'completed', JSON.stringify(completed.error));
  assert.match(JSON.stringify(completed.result), /VERIFIED_WORKSHOP_ARTIFACT/);
  assert.equal(tasks.size, 1);
  const history = await app.call('agent.session.history', { namespace: 'demo', session_id: session.id });
  assert.ok(history.messages.some((m) => JSON.stringify(m).includes('tool_result')));
  assert.ok((await app.call('agent.run.events', { namespace: 'demo', run_id: run.id })).events.length > 0);
  assert.equal((await broker.call('workshop.list', { namespace: 'other' })).tasks.length, 0);
  checked('client → TS loop → gateway → workshop → real child artifact → durable final answer and idempotency');

  const catalog = await app.call('agent.workshop.catalog', { namespace: 'demo' });
  assert.equal(catalog.find((w) => w.name === 'reuse').runtimes[0].source, 'gateway');
  const reuseParams = {
    namespace: 'demo',
    workflow: 'reuse',
    input: 'run',
    runtime: 'pi-main',
    idempotency_key: 'reuse-once',
  };
  const reuseTask = await broker.call('workshop.submit', reuseParams);
  const reuseResult = await until(async () => {
    const r = await broker.call('workshop.get', { namespace: 'demo', task_id: reuseTask.id });
    return ['succeeded', 'failed'].includes(r.status) ? r : false;
  }, proof);
  assert.equal(reuseResult.status, 'succeeded', JSON.stringify(reuseResult));
  assert.equal(reuseResult.engine, 'pi');
  assert.equal(reuseResult.model, 'chat');
  assert.equal(reuseResult.runtime, 'pi-main');
  assert.equal(reuseResult.runs[0].text, 'NATIVE_REUSED');
  assert.equal((await broker.call('workshop.submit', reuseParams)).id, reuseTask.id);
  checked('selected runtime subprocess reuses main gateway model through scoped relay without provider credentials');

  const recoverySession = await app.call('agent.session.create', { namespace: 'demo' });
  const recoveryRun = await app.call('agent.run.start', {
    namespace: 'demo',
    session_id: recoverySession.id,
    input: 'RECOVER_TOOL_ERROR',
    idempotency_key: 'safe-error',
  });
  const recovered = await until(async () => {
    const r = await app.call('agent.run.get', { namespace: 'demo', run_id: recoveryRun.id });
    return ['completed', 'failed'].includes(r.status) ? r : false;
  }, proof);
  assert.equal(recovered.status, 'completed', JSON.stringify(recovered.error));
  assert.match(JSON.stringify(recovered.result), /RECOVERED_TOOL_ERROR/);
  checked('ordinary tool errors are committed and returned to the model for a final answer');

  const cancelSession = await app.call('agent.session.create', { namespace: 'demo' });
  const cancelRun = await app.call('agent.run.start', {
    namespace: 'demo',
    session_id: cancelSession.id,
    input: 'CANCEL_ME',
    idempotency_key: 'cancel',
  });
  await until(() => started.has('cancel'), proof);
  await app.call('agent.run.cancel', { namespace: 'demo', run_id: cancelRun.id });
  await until(
    async () => (await app.call('agent.run.get', { namespace: 'demo', run_id: cancelRun.id })).status === 'canceled',
    proof
  );
  await until(() => canceledProvider > 0, proof);
  checked('cancel propagates through two RPC hops to the blocked provider socket');

  const restartSession = await app.call('agent.session.create', { namespace: 'demo' });
  const restartRun = await app.call('agent.run.start', {
    namespace: 'demo',
    session_id: restartSession.id,
    input: 'RESTART_ME',
    idempotency_key: 'restart',
  });
  await until(() => started.has('restart'), proof);
  await bed.stop(agentChild, 'SIGKILL');
  log('Waiting for the crashed instance ownership lease to expire (31 seconds).');
  await sleep(31_000);
  agentChild = bed.start('agent-restarted', process.execPath, [loopServer, '--config', join(state, 'agent.json')]);
  await until(() => agentHealth.health(), proof);
  assert.equal((await app.call('agent.run.get', { namespace: 'demo', run_id: restartRun.id })).status, 'interrupted');
  assert.equal((await app.call('agent.run.get', { namespace: 'demo', run_id: run.id })).status, 'completed');
  await writeFile(
    join(state, 'second-agent.json'),
    JSON.stringify({ ...agentConfig, listen: `127.0.0.1:${secondAgentPort}` })
  );
  const duplicate = bed.start('agent-duplicate', process.execPath, [
    loopServer,
    '--config',
    join(state, 'second-agent.json'),
  ]);
  const duplicateExit = await Promise.race([
    duplicate.done,
    sleep(5_000).then(() => {
      throw new Error('Duplicate database owner remained running');
    }),
  ]);
  assert.notEqual(duplicateExit.code, 0);
  await agentHealth.health();
  checked('crash recovery retains history, marks active work interrupted and excludes a second database owner');

  for (const c of clients) c.close();
  for (const child of [agentChild, gatewayChild, workshopChild]) assert.equal((await bed.stop(child)).code, 0);
  checked('all three services shut down cleanly');
  await bed.save();
  log(`Evidence: ${state}`);
} catch (error) {
  report.error = error.message;
  await bed.save();
  process.stderr.write(`FAIL ${error.stack}\nEvidence: ${state}\n`);
  process.exitCode = 1;
} finally {
  for (const c of clients) c.close();
  await bed.stopAll();
  upstream?.closeAllConnections();
  upstream?.close();
}
