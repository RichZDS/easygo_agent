import { rpc } from '../api.js';
import { $, asItems, bindForm, busy, button, list, notify, pretty, row, state } from '../ui.js';

async function memories() {
  const data = await rpc('agent.memory.list');
  list($('memory-list'), asItems(data, 'memories'), (m) => {
    const r = row(m.kind || '记忆', m.content, '', () => {
      state.memoryDraft = m;
      $('memory-id').value = m.id;
      $('memory-version').value = m.version;
      $('memory-kind').value = m.kind;
      $('memory-content').value = m.content;
    });
    r.append(
      button('删除', async () => {
        if (!confirm('删除这条记忆？')) return;
        await rpc('agent.memory.delete', { id: m.id, expected_version: m.version });
        await memories();
      })
    );
    return r;
  });
}
async function skills() {
  const data = await rpc('agent.skills.list');
  list($('skills-list'), asItems(data, 'skills'), (s) => {
    const r = row(s.name, s.description, '', async () => {
      const result = await rpc('agent.skills.get', { name: s.name });
      const skill = result.skill || result;
      $('skill-name').value = skill.name;
      $('skill-description').value = skill.description || '';
      $('skill-body').value = skill.content || '';
      $('skill-version').value = skill.version;
    });
    r.append(
      button('删除', async () => {
        if (!confirm('删除这个技能？')) return;
        await rpc('agent.skills.delete', { name: s.name, expected_version: s.version });
        await skills();
      })
    );
    return r;
  });
}

// One module serves both the memory and the skills page.
export const load = () => (state.page === 'memory' ? memories() : skills());
export function bind() {
  bindForm('memory-form', async () => {
    await rpc('agent.memory.upsert', {
      ...($('memory-id').value
        ? { id: $('memory-id').value, expected_version: Number($('memory-version').value) }
        : {}),
      ...(state.memoryDraft && $('memory-id').value === state.memoryDraft.id
        ? {
            importance: state.memoryDraft.importance,
            confidence: state.memoryDraft.confidence,
            source_run_ids: state.memoryDraft.source_run_ids,
          }
        : {}),
      kind: $('memory-kind').value,
      content: $('memory-content').value,
    });
    $('memory-form').reset();
    await memories();
    notify('记忆已保存。');
  });
  $('consolidate').onclick = () =>
    busy($('consolidate'), async () => {
      await rpc('agent.memory.consolidate');
      await memories();
      notify('记忆整理请求已完成。');
    });
  bindForm('skill-form', async () => {
    await rpc('agent.skills.upsert', {
      name: $('skill-name').value,
      description: $('skill-description').value,
      content: $('skill-body').value,
      ...($('skill-version').value ? { expected_version: Number($('skill-version').value) } : {}),
    });
    const saved = await rpc('agent.skills.get', { name: $('skill-name').value });
    $('skill-version').value = saved.version;
    await skills();
    notify('技能已保存。');
  });
  for (const kind of ['memory', 'skills'])
    bindForm(`${kind}-import`, async () => {
      const payload = JSON.parse($(`${kind}-import`).elements.payload.value);
      const result = await rpc(`agent.${kind}.import`, payload);
      $(`${kind}-import`).querySelector('.import-result').textContent = pretty(result);
      await (kind === 'memory' ? memories() : skills());
      notify(payload.dry_run === false ? '导入已写入。' : '导入校验完成，尚未写入。');
    });
}
