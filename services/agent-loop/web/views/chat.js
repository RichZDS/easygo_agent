import { errors, rpc } from '../api.js';
import { beginPage, drawPager, pages } from '../pager.js';
import {
  $,
  badge,
  bindForm,
  busy,
  button,
  empty,
  idempotentSubmit,
  labels,
  latest,
  list,
  node,
  notify,
  pretty,
  row,
  shell,
  state,
  time,
} from '../ui.js';
import { wallet } from './wallet.js';
import { catalog } from './workshop.js';

async function sessions() {
  const current = beginPage('sessions');
  const data = await rpc('agent.session.list', { offset: pages.sessions.cursor, limit: 100 });
  if (!current()) return;
  list($('sessions'), data.sessions || [], (s) =>
    row(s.id.slice(0, 12), time(s.created_at), '', async () => {
      state.session = s.id;
      state.run = null;
      resetHistory();
      $('session-title').textContent = `会话 ${s.id.slice(0, 12)}`;
      $('run-events').textContent = '';
      await history();
    })
  );
  drawPager('sessions', data.next_offset, sessions);
}
export function resetHistory() {
  state.historyBefore = Number.MAX_SAFE_INTEGER;
  state.historyRequest++;
  $('history-pager').replaceChildren();
}
async function history() {
  const session = state.session,
    before = state.historyBefore,
    current = latest(state, 'historyRequest');
  const data = await rpc('agent.session.history', { session_id: session, before, limit: 100 });
  if (session !== state.session || before !== state.historyBefore || !current()) return;
  const active = (data.runs || []).find((r) => ['queued', 'running'].includes(r.status));
  if (!state.run && active) {
    state.run = active.id;
    $('run-status').replaceChildren(badge(active.status));
    $('cancel-run').disabled = false;
  }
  const box = $('messages');
  box.replaceChildren();
  for (const message of data.messages || []) {
    const bubble = node('div', undefined, `message ${message.role}`);
    bubble.dataset.seq = String(message.seq ?? '');
    bubble.append(node('small', { user: '你', assistant: 'Agent', tool: '工具' }[message.role] || message.role));
    for (const b of message.content || [])
      bubble.append(
        node('div', b.text || (b.type === 'tool_call' ? `调用 ${b.name}\n${pretty(b.arguments)}` : `[${b.type}]`))
      );
    box.append(bubble);
  }
  if (!box.childElementCount) empty(box, '这一页没有消息。');
  const older = button('更早消息', async () => {
    state.historyBefore = data.previous_before;
    await history();
  });
  older.id = 'history-older';
  older.disabled =
    !Number.isSafeInteger(data.previous_before) || data.previous_before <= 0 || data.previous_before >= before;
  const newest = button('回到最新', async () => {
    resetHistory();
    await history();
  });
  newest.id = 'history-latest';
  newest.disabled = before === Number.MAX_SAFE_INTEGER;
  $('history-pager').replaceChildren(
    older,
    node('span', before === Number.MAX_SAFE_INTEGER ? '最新消息' : '正在查看历史消息'),
    newest
  );
}
// The chat half of the page poll: follows the active run until it ends.
export async function pollRun() {
  const id = state.run,
    run = await rpc('agent.run.get', { run_id: id });
  if (state.run !== id) return;
  $('run-status').replaceChildren(badge(run.status));
  $('cancel-run').disabled = !['queued', 'running'].includes(run.status);
  $('run-events').textContent = pretty(await rpc('agent.run.events', { run_id: id, limit: 1000 }));
  if (state.historyBefore === Number.MAX_SAFE_INTEGER) await history();
  if (!['queued', 'running'].includes(run.status)) {
    state.run = null;
    if (run.error) {
      const code = run.error.upstream?.data?.code || run.error.code;
      notify(errors[code] || `执行${labels[run.status] || run.status}：${code || '请查看事件'}`);
    }
    await wallet();
  }
}

export async function load() {
  await sessions();
  await catalog();
  if (state.session) await history();
}
export function bind() {
  $('new-session').onclick = () =>
    busy($('new-session'), async () => {
      const s = await rpc('agent.session.create');
      state.session = s.id;
      state.run = null;
      resetHistory();
      $('session-title').textContent = `会话 ${s.id.slice(0, 12)}`;
      await sessions();
      await history();
    });
  bindForm('chat-form', async () => {
    if (!state.session) {
      const s = await rpc('agent.session.create');
      state.session = s.id;
      resetHistory();
      $('session-title').textContent = `会话 ${s.id.slice(0, 12)}`;
      await sessions();
    }
    const params = {
      session_id: state.session,
      input: $('chat-input').value,
      ...($('chat-runtime').value ? { workshop_runtime: $('chat-runtime').value } : {}),
    };
    const run = await idempotentSubmit('chat', params, (p) => rpc('agent.run.start', p));
    state.run = run.id;
    $('chat-input').value = '';
    $('cancel-run').disabled = false;
    await shell.poll();
  });
  $('cancel-run').onclick = () =>
    busy($('cancel-run'), async () => {
      if (state.run) await rpc('agent.run.cancel', { run_id: state.run });
      await shell.poll();
    });
}
