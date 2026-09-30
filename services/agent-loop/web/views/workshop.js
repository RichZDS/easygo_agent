import { rpc } from '../api.js';
import { beginPage, drawPager, pages } from '../pager.js';
import {
  $,
  asItems,
  badge,
  bindForm,
  busy,
  button,
  idempotentSubmit,
  latest,
  list,
  node,
  notify,
  pretty,
  row,
  shell,
  state,
} from '../ui.js';

// Workflows and runtimes feed both the task form here and the chat runtime picker.
export async function catalog() {
  state.catalog = asItems(await rpc('agent.workshop.catalog'), 'workflows');
  const selected = $('workflow').value;
  $('workflow').replaceChildren();
  for (const workflow of state.catalog) $('workflow').append(new Option(workflow.name, workflow.name));
  if (state.catalog.some((w) => w.name === selected)) $('workflow').value = selected;
  const runtimes = new Map();
  for (const w of state.catalog) for (const r of w.runtimes || []) runtimes.set(r.id, r);
  const current = $('chat-runtime').value;
  $('chat-runtime').replaceChildren(new Option('默认运行时', ''));
  for (const r of runtimes.values()) $('chat-runtime').append(new Option(`${r.id} · ${r.engine} / ${r.model}`, r.id));
  if (runtimes.has(current)) $('chat-runtime').value = current;
  taskRuntimes();
}
function taskRuntimes() {
  const current = $('task-runtime').value,
    w = state.catalog.find((w) => w.name === $('workflow').value);
  $('task-runtime').replaceChildren(new Option('默认运行时', ''));
  for (const r of w?.runtimes || []) $('task-runtime').append(new Option(`${r.id} · ${r.engine}`, r.id));
  if ((w?.runtimes || []).some((r) => r.id === current)) $('task-runtime').value = current;
}
// `recent` fills the overview's short list instead of the paged task list.
export async function tasks(recent = false) {
  const current = recent ? () => true : beginPage('tasks');
  const data = await rpc('workshop.list', { offset: recent ? 0 : pages.tasks.cursor, limit: recent ? 5 : 100 });
  if (!current()) return;
  list($(recent ? 'recent-tasks' : 'tasks'), data.tasks || [], (t) =>
    row(
      t.id.slice(0, 12),
      `${t.runtime || t.engine || '默认运行时'} · ${t.run_count ?? 0} 次执行`,
      t.status,
      async () => {
        state.task = t.id;
        $('task-output').textContent = '';
        if (state.page !== 'workshop') await shell.page('workshop');
        else await taskDetail();
      }
    )
  );
  if (!recent) drawPager('tasks', data.next_offset, () => tasks());
}
export async function taskDetail() {
  const task = state.task,
    current = latest(state, 'taskRequest'),
    t = await rpc('workshop.get', { task_id: task });
  if (state.task !== task || !current()) return;
  $('task-status').replaceChildren(badge(t.status));
  $('task-detail').textContent = pretty(t);
  $('cancel-task').disabled = !['queued', 'running', 'cancelling'].includes(t.status);
  $('resume-task').disabled = ['queued', 'running', 'cancelling'].includes(t.status);
  $('task-result').disabled = false;
  const run = t.runs?.at(-1);
  list($('artifacts'), run?.artifacts || [], (artifact) => {
    const item = row(artifact.path, `${artifact.size} bytes`, '');
    item.append(button('下载', () => downloadArtifact(task, run.id, artifact)));
    return item;
  });
  if (run?.artifacts_truncated)
    $('artifacts').append(node('p', '服务返回的产物列表已截断；这里只展示已登记并返回的文件。', 'muted'));
  const events = await rpc('workshop.events', { task_id: task });
  if (state.task === task && current()) $('task-events').textContent = pretty(events);
}
async function downloadArtifact(task, run, registered) {
  const max = 8 * 1024 * 1024;
  if (
    typeof registered.path !== 'string' ||
    !Number.isSafeInteger(registered.size) ||
    registered.size < 0 ||
    registered.size > max ||
    !/^[a-f0-9]{64}$/i.test(registered.sha256)
  )
    throw Error('产物登记信息无效或文件超过 8 MiB。');
  const artifact = await rpc('workshop.artifact', { task_id: task, run_id: run, path: registered.path });
  if (
    artifact.path !== registered.path ||
    artifact.size !== registered.size ||
    artifact.sha256 !== registered.sha256 ||
    typeof artifact.data_base64 !== 'string' ||
    artifact.data_base64.length > 4 * Math.ceil(max / 3) ||
    artifact.data_base64.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(artifact.data_base64)
  )
    throw Error('产物与登记记录不符或内容超限，请刷新后重试。');
  const data = Uint8Array.from(atob(artifact.data_base64), (c) => c.charCodeAt(0));
  if (data.byteLength !== registered.size) throw Error('产物大小不符，未下载。');
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', data)), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
  if (hash !== registered.sha256.toLowerCase()) throw Error('产物校验失败，文件可能已改变。');
  const filename =
    registered.path
      .split(/[\\/]/)
      .at(-1)
      // eslint-disable-next-line no-control-regex -- control characters are exactly what the filename must not keep
      .replace(/[\x00-\x1f\x7f<>:"|?*‪-‮⁦-⁩]/g, '_')
      .replace(/^[. ]+|[. ]+$/g, '') || 'artifact.bin';
  const url = URL.createObjectURL(new Blob([data], { type: 'application/octet-stream' }));
  const link = node('a');
  link.href = url;
  link.download = filename;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
  notify('文件已校验，已发起下载。');
}

export async function load() {
  await catalog();
  await tasks();
  if (state.task) await taskDetail();
}
export function bind() {
  $('workflow').onchange = taskRuntimes;
  bindForm('task-form', async () => {
    const params = {
      workflow: $('workflow').value,
      input: $('task-input').value,
      ...($('task-runtime').value ? { runtime: $('task-runtime').value } : {}),
    };
    const task = await idempotentSubmit('task', params, (p) => rpc('workshop.submit', p));
    state.task = task.id;
    await tasks();
    await taskDetail();
  });
  $('cancel-task').onclick = () =>
    busy($('cancel-task'), async () => {
      await rpc('workshop.cancel', { task_id: state.task });
      await taskDetail();
      await tasks();
    });
  bindForm('resume-form', async () => {
    await rpc('workshop.resume', { task_id: state.task, input: $('resume-input').value });
    $('resume-input').value = '';
    await taskDetail();
    await tasks();
  });
  $('task-result').onclick = () =>
    busy($('task-result'), async () => {
      const result = await rpc('workshop.result', { task_id: state.task, limit: 32768 });
      $('task-output').textContent = result.text || pretty(result);
    });
}
