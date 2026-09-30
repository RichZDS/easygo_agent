#!/usr/bin/env node
// Opt-in, billable live smoke. The caller must supply DEEPSEEK_API_KEY.
// Only the gateway process gets that credential; native CLIs get task capabilities.
import { mkdtemp, writeFile, readFile, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { buildServices, loopServer, ports, root, testbed, until } from './lib/procs.mjs';
import { createPKI } from './lib/pki.mjs';
import { writeConfigs } from './lib/config.mjs';

const key = process.env.DEEPSEEK_API_KEY;
if (!key) throw new Error('Live test requires DEEPSEEK_API_KEY; this test makes billable requests.');
// Builds and PKI helpers must not inherit the provider credential.
delete process.env.DEEPSEEK_API_KEY;
const state = await mkdtemp(join(process.env.EASYGO_LIVE_STATE_ROOT ?? tmpdir(), 'easygo-deepseek-live-'));
const toolsOnly = process.env.EASYGO_LIVE_TOOLS_ONLY === '1';
const reportDir = process.env.EASYGO_LIVE_REPORT_DIR ?? state;
await mkdir(reportDir, { recursive: true });
const report = {
  state,
  model: 'deepseek-flash',
  live: true,
  tools_only: toolsOnly,
  checks: [],
  runtimes: [],
  protocols: [],
  source_commit: process.env.EASYGO_LIVE_COMMIT ?? 'working-tree',
};
const clients = [];
const scrub = (v) => JSON.parse(JSON.stringify(v).replaceAll(key, '[REDACTED]'));
const bed = testbed(state, {
  report,
  reportFile: join(reportDir, 'live-report.json'),
  reportMode: 0o600,
  redact: scrub,
});
const patience = { timeout: 15000, interval: 100, retryErrors: true };
function log(value) {
  process.stdout.write(JSON.stringify(scrub(value)) + '\n');
}
try {
  const certs = await createPKI(join(state, 'pki'), { namespaces: ['live'] });
  const { identity, grant, endpoint: ep } = certs;
  const [gatewayPort, workshopPort, loopPort] = await ports(3);
  function client(identityName, name, p) {
    const c = certs.client(identityName, name, p, { timeoutMs: 125000 });
    clients.push(c);
    return c;
  }
  const gateway = JSON.parse(await readFile(join(root, 'services/ai-gateway/config.deepseek.example.json'), 'utf8'));
  gateway.listen = `127.0.0.1:${gatewayPort}`;
  gateway.tls = identity('ai-gateway');
  gateway.authorization = [
    grant('client', ['health', 'gateway.models', 'gateway.generate']),
    grant('agent-loop', ['gateway.generate']),
    grant('workshop', ['gateway.native']),
  ];
  for (const [alias, m] of Object.entries(gateway.models))
    m.parameters = {
      max_output_tokens: 1024,
      ...(alias === 'responses' ? { reasoning: { effort: 'low' } } : { thinking: { type: 'disabled' } }),
    };
  const binaries = {
    codex: process.env.WORKSHOP_NATIVE_CODEX ?? 'codex',
    claude: process.env.WORKSHOP_NATIVE_CLAUDE ?? 'claude',
    pi: process.env.WORKSHOP_NATIVE_PI ?? 'pi',
    openclaw: process.env.WORKSHOP_NATIVE_OPENCLAW ?? 'openclaw',
  };
  const profile = (engine, protocol, gateway_model) => ({
    engine,
    protocol,
    gateway_model,
  });
  const workshop = {
    listen: `127.0.0.1:${workshopPort}`,
    tls: identity('workshop'),
    authorization: [
      grant('client', [
        'health',
        'workshop.workflows',
        'workshop.submit',
        'workshop.get',
        'workshop.resume',
        'workshop.events',
      ]),
      grant('agent-loop', ['workshop.workflows']),
    ],
    workshop: {
      root: join(state, 'workshop-data'),
      concurrency: 1,
      queue_capacity: 4,
      max_output_bytes: 4194304,
      engines: Object.fromEntries(Object.entries(binaries).map(([n, binary]) => [n, { binary }])),
      model_gateway: {
        ...ep('ai-gateway', gatewayPort),
        tls: identity('workshop'),
      },
      runtime_profiles: {
        codex: profile('codex', 'responses', 'responses'),
        claude: profile('claude', 'anthropic', 'claude'),
        pi: profile('pi', 'chat_completions', 'chat'),
        openclaw: profile('openclaw', 'chat_completions', 'chat'),
      },
      workflows: [
        {
          name: 'live-smoke',
          version: '1',
          instructions:
            'Follow the user exactly. Do not use tools, skills, external channels or subagents. Answer in at most ten words.',
          runtime: 'pi',
          allowed_runtimes: ['codex', 'claude', 'openclaw'],
          policy: 'read-only',
          timeout_seconds: 120,
        },
      ],
    },
  };
  workshop.authorization[0].methods.push('workshop.cancel');
  workshop.workshop.workflows.push({
    name: 'live-file',
    version: '1',
    instructions:
      'Use the available tools to create proof.txt in the current workspace containing exactly LIVE_FILE_OK with no trailing newline. Do not use network or install anything. Stop after writing, then reply DONE.',
    runtime: 'pi',
    allowed_runtimes: ['codex', 'claude', 'openclaw'],
    policy: 'workspace-write',
    timeout_seconds: 120,
    artifacts: ['proof.txt'],
  });
  const loop = {
    listen: `127.0.0.1:${loopPort}`,
    tls: identity('agent-loop'),
    authorization: [
      grant('client', ['health', 'agent.session.create', 'agent.run.start', 'agent.run.get', 'agent.workshop.catalog']),
    ],
    database: join(state, 'agent.sqlite'),
    gateway: ep('ai-gateway', gatewayPort),
    workshop: ep('workshop', workshopPort),
    model: 'chat',
    streaming: true,
    max_steps: 1,
    system_prompt: 'Reply briefly without tools.',
  };
  await writeConfigs(state, { gateway, workshop, loop });
  await buildServices(state);
  bed.start('gateway', join(state, 'gateway'), ['--config', join(state, 'gateway.json')], { DEEPSEEK_API_KEY: key });
  bed.start('workshop', join(state, 'workshop'), ['--config', join(state, 'workshop.json')]);
  bed.start('loop', process.execPath, [loopServer, '--config', join(state, 'loop.json')]);
  const g = client('client', 'ai-gateway', gatewayPort),
    w = client('client', 'workshop', workshopPort),
    a = client('client', 'agent-loop', loopPort);
  await Promise.all([g, w, a].map((c) => until(() => c.health(), patience)));
  if (!toolsOnly) {
    for (const alias of ['chat', 'responses', 'claude']) {
      const entry = { alias };
      const begin = Date.now();
      try {
        const r = await g.call('gateway.generate', {
          namespace: 'live',
          request: {
            model: alias,
            messages: [
              {
                role: 'user',
                content: [{ type: 'text', text: 'Reply exactly LIVE_OK.' }],
              },
            ],
            max_output_tokens: 256,
          },
          stream: true,
        });
        entry.pass = r.message.content.some((b) => b.type === 'text' && b.text.includes('LIVE_OK'));
        entry.finish_reason = r.finish_reason;
        entry.usage = r.usage;
      } catch (e) {
        entry.pass = false;
        entry.error = { message: e.message, code: e.code, data: e.data };
      }
      entry.elapsed_ms = Date.now() - begin;
      report.protocols.push(entry);
      log({ protocol: entry });
      await bed.save();
    }
    const session = await a.call('agent.session.create', { namespace: 'live' });
    const run = await a.call('agent.run.start', {
      namespace: 'live',
      session_id: session.id,
      input: 'Reply exactly MAIN_LOOP_OK.',
      idempotency_key: 'main-smoke',
    });
    const final = await until(
      async () => {
        const r = await a.call('agent.run.get', {
          namespace: 'live',
          run_id: run.id,
        });
        return !['queued', 'running'].includes(r.status) && r;
      },
      { ...patience, timeout: 125000 }
    );
    report.checks.push({
      name: 'main-loop',
      pass: final.status === 'completed' && JSON.stringify(final.result).includes('MAIN_LOOP_OK'),
      status: final.status,
      error: final.error,
    });
    log(report.checks.at(-1));
    const catalog = await a.call('agent.workshop.catalog', {
      namespace: 'live',
    });
    report.checks.push({
      name: 'runtime-catalog',
      pass: catalog[0]?.runtimes.length === 4,
    });
  }
  const allowed = process.env.EASYGO_LIVE_ENGINES?.split(',') ?? Object.keys(binaries);
  for (const engine of allowed) {
    const alias = workshop.workshop.runtime_profiles[engine].gateway_model;
    if (!toolsOnly && !report.protocols.find((p) => p.alias === alias)?.pass) {
      report.runtimes.push({ engine, skipped: 'gateway protocol failed' });
      continue;
    }
    const entry = { engine, turns: [] };
    report.runtimes.push(entry);
    const marker = 'FIRST_' + engine.toUpperCase() + '_OK';
    let task = await w.call('workshop.submit', {
      namespace: 'live',
      workflow: toolsOnly ? 'live-file' : 'live-smoke',
      runtime: engine,
      input: toolsOnly ? 'Create the file now.' : `Reply exactly ${marker}.`,
      idempotency_key: 'native-' + engine,
    });
    for (const turn of toolsOnly ? ['file'] : ['first', 'resume']) {
      const begin = Date.now();
      if (turn === 'resume')
        task = await w.call('workshop.resume', {
          namespace: 'live',
          task_id: task.id,
          input: 'Repeat exactly the marker from your previous answer. Do not add any words.',
        });
      const done = await until(
        async () => {
          const r = await w.call('workshop.get', {
            namespace: 'live',
            task_id: task.id,
          });
          return !['queued', 'running', 'cancelling'].includes(r.status) && r;
        },
        { ...patience, timeout: 130000 }
      );
      const latest = done.runs.at(-1);
      let artifact;
      if (toolsOnly && done.status === 'succeeded') {
        try {
          artifact = await readFile(join(state, 'workshop-data', 'workspaces', task.id, 'proof.txt'), 'utf8');
        } catch {}
      }
      const result = {
        turn,
        status: done.status,
        pass: done.status === 'succeeded' && (toolsOnly ? artifact === 'LIVE_FILE_OK' : latest.text?.includes(marker)),
        ...(toolsOnly ? { artifact_matches: artifact === 'LIVE_FILE_OK' } : {}),
        elapsed_ms: Date.now() - begin,
        text: latest.text,
        error: latest.error,
      };
      entry.turns.push(result);
      log({ engine, ...result });
      const events = await w.call('workshop.events', {
        namespace: 'live',
        task_id: task.id,
      });
      await writeFile(join(reportDir, engine + '-events.json'), JSON.stringify(scrub(events), null, 2), {
        mode: 0o600,
      });
      const sessions = events.filter((e) => e.kind === 'session').map((e) => e.session_id);
      entry.same_session = sessions.length > 0 && new Set(sessions).size === 1;
      await bed.save();
      if (!result.pass) break;
    }
  }
  report.pass =
    report.protocols.every((x) => x.pass) &&
    report.checks.every((x) => x.pass) &&
    report.runtimes.every(
      (x) => x.turns?.length === (toolsOnly ? 1 : 2) && x.turns.every((y) => y.pass) && x.same_session
    );
} catch (e) {
  report.error = { message: e.message, code: e.code };
  log({ fatal: report.error });
  process.exitCode = 1;
} finally {
  clients.forEach((c) => c.close());
  await bed.stopAll();
  try {
    const lines = (await readFile(join(state, 'gateway.log'), 'utf8'))
      .trim()
      .split('\n')
      .map((line) => {
        try {
          return JSON.parse(line);
        } catch {
          return null;
        }
      })
      .filter(Boolean);
    report.model_observations = lines.filter((l) => l.kind === 'model');
  } catch {}
  await bed.save();
  log({
    pass: report.pass,
    report: join(reportDir, 'live-report.json'),
    state,
  });
  if (!report.pass) process.exitCode = 1;
}
