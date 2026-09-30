import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { ToolRegistry, createToolRegistry } from '../dist/tools/index.js';
import { systemPrompt } from '../dist/tools/roles.js';
import { parseConfig } from '../dist/server.js';
import { RpcError } from '../dist/validation.js';

test('registry rejects duplicate names across roles and providers', () => {
  const entry = {
    definition: { name: 'same', parameters: { properties: {} } },
    roles: ['assistant'],
    mutating: false,
    execute: async () => true,
    recoverable: () => false,
  };
  assert.throws(() => new ToolRegistry([entry, { ...entry, roles: ['foreman'] }]), /Duplicate tool/);
  assert.throws(() => createToolRegistry({}, { tools: () => [{ name: 'workshop_get' }] }), /Duplicate tool/);
});

test('role filtering applies to both advertised tools and execution', async () => {
  let calls = 0;
  const entry = (name) => ({
    definition: { name, parameters: { properties: {} } },
    roles: [name],
    mutating: false,
    execute: async () => ++calls,
    recoverable: () => false,
  });
  const registry = new ToolRegistry([entry('assistant'), entry('foreman')]);
  assert.deepEqual(
    registry.definitions('assistant').map((t) => t.name),
    ['assistant']
  );
  assert.deepEqual(
    registry.definitions('foreman').map((t) => t.name),
    ['foreman']
  );
  await assert.rejects(
    registry.execute('assistant', { name: 'foreman', arguments: {} }, {}, new AbortController().signal),
    (e) => e.reason === 'unknown_tool'
  );
  assert.equal(calls, 0);
  assert.equal(
    await registry.execute('assistant', { name: 'assistant', arguments: {} }, {}, new AbortController().signal),
    1
  );
});

test('knowledge entries retain scope and recoverable missing-skill errors', async () => {
  const registry = createToolRegistry(
    {},
    {
      tools: () => [{ name: 'load_skill', parameters: { properties: { name: { type: 'string' } } } }],
      execute: async (call, ns) => {
        assert.equal(ns, 'demo');
        throw new RpcError(-32004, 'skill_not_found');
      },
    }
  );
  assert.equal(
    registry.definitions('assistant').some((t) => t.name === 'calculator'),
    false
  );
  assert.deepEqual(registry.definitions('foreman'), []);
  await assert.rejects(
    registry.execute(
      'assistant',
      { name: 'load_skill', arguments: { name: 'missing' } },
      { namespace: 'demo' },
      new AbortController().signal
    ),
    (e) => registry.recoverable('assistant', 'load_skill', e)
  );
});

test('assistant pack reads UTF-8 with byte limits and rejects missing, oversized or conflicting configuration', () => {
  const dir = mkdtempSync(join(tmpdir(), 'loop-role-'));
  const config = {
    tls: { cert_file: 'cert', key_file: 'key', ca_file: 'ca' },
    authorization: [],
    gateway: { peer_certificate_file: 'peer' },
    workshop: { peer_certificate_file: 'peer' },
  };
  try {
    mkdirSync(join(dir, 'roles'));
    assert.throws(() => systemPrompt({ pack_dir: dir }), /ENOENT/);
    const file = join(dir, 'roles', 'assistant.md');
    writeFileSync(file, 'Role instruction 角色说明');
    assert.equal(systemPrompt({ pack_dir: dir }), 'Role instruction 角色说明');
    assert.equal(systemPrompt({}), undefined);
    assert.equal(systemPrompt({ system_prompt: 'legacy' }), 'legacy');
    assert.throws(() => parseConfig({ ...config, pack_dir: dir, system_prompt: '' }), /conflicts/);
    assert.throws(() => systemPrompt({ pack_dir: dir, system_prompt: '' }), /conflicts/);
    writeFileSync(file, 'a'.repeat(16384));
    assert.equal(systemPrompt({ pack_dir: dir }).length, 16384);
    writeFileSync(file, '字'.repeat(5462));
    assert.throws(() => systemPrompt({ pack_dir: dir }), /16 KiB/);
    writeFileSync(file, Buffer.from([0xff]));
    assert.throws(() => systemPrompt({ pack_dir: dir }), /encoded data/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
