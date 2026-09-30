const $ = (id) => document.getElementById(id);
const state = {
  user: null,
  page: 'overview',
  session: null,
  run: null,
  task: null,
  catalog: [],
  timer: null,
  polling: false,
  generation: 0,
  register: false,
  grant: null,
  resolution: null,
  pending: [],
  memoryDraft: null,
  historyBefore: Number.MAX_SAFE_INTEGER,
  historyRequest: 0,
  taskRequest: 0,
};
const titles = {
  overview: '总览',
  chat: '对话',
  workshop: '任务工坊',
  memory: '长期记忆',
  skills: '技能库',
  wallet: '额度与用量',
  admin: '管理',
};
const labels = {
  queued: '排队中',
  running: '执行中',
  completed: '已完成',
  succeeded: '已完成',
  failed: '失败',
  canceled: '已取消',
  cancelled: '已取消',
  cancelling: '取消中',
  timed_out: '超时',
  interrupted: '已中断',
  reserved: '已预留',
  pending: '待核对',
  settled: '已结算',
  released: '已释放',
  resolved_settled: '人工结算',
  resolved_released: '人工释放',
  grant: '管理员发放',
};
const errors = {
  authentication_required: '请先登录。',
  invalid_credentials: '邮箱或密码不正确。',
  password_too_short: '密码至少需要 12 个字符。',
  registration_unavailable: '该邮箱暂不可注册，请尝试登录。',
  insufficient_credits: '可用额度不足，请联系管理员发放额度。',
  origin_forbidden: '请求来源校验失败，请从配置的站点地址重新打开。',
  admin_required: '此操作需要管理员权限。',
  rate_limited: '操作较频繁，请稍后重试。',
  registration_disabled: '当前未开放注册。',
  auth_busy: '登录请求较多，请稍后重试。',
};
let noticeTimer;
function notify(message) {
  $('notice').textContent = message;
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => {
    $('notice').textContent = '';
  }, 7000);
}
function fail(error) {
  notify(error.message || '请求失败，请重试。');
}
function credits(value) {
  const n = BigInt(value ?? 0),
    sign = n < 0n ? '-' : '',
    abs = n < 0n ? -n : n;
  const fraction = String(abs % 1000000n)
    .padStart(6, '0')
    .replace(/0+$/, '');
  return `${sign}${abs / 1000000n}${fraction ? `.${fraction}` : ''}`;
}
function micros(value, scale = 1000000n) {
  if (!/^\d+(\.\d{1,6})?$/.test(value)) throw Error('请输入最多六位小数的非负额度。');
  const [whole, fraction = ''] = value.split('.');
  const amount = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'));
  if ((amount * scale) % 1000000n) throw Error('费率精度超出每 token 的整数 microcredit。');
  const result = (amount * scale) / 1000000n;
  if (result > BigInt(Number.MAX_SAFE_INTEGER)) throw Error('额度过大。');
  return Number(result);
}
async function api(path, data, method = data === undefined ? 'GET' : 'POST') {
  const generation = state.generation;
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: data === undefined ? {} : { 'content-type': 'application/json' },
    ...(data === undefined ? {} : { body: JSON.stringify(data) }),
  });
  const result = await response.json();
  if (generation !== state.generation) throw Error('会话已切换，请重新读取。');
  if (!response.ok || result.error) {
    const code = result.error?.code || `http_${response.status}`;
    if (response.status === 401 && state.user) signedOut();
    throw Error(errors[code] || `请求失败：${code}`);
  }
  $('connection').textContent = '已连接';
  return result;
}
async function rpc(method, params = {}) {
  return (await api('/api/rpc', { method, params })).result;
}
function node(tag, text, cls) {
  const n = document.createElement(tag);
  if (text !== undefined) n.textContent = String(text);
  if (cls) n.className = cls;
  return n;
}
function empty(target, text) {
  target.replaceChildren(node('p', text, 'empty'));
}
function badge(status) {
  return node('span', labels[status] || status || '未知', `badge ${status || ''}`);
}
function button(text, action) {
  const b = node('button', text);
  b.type = 'button';
  b.onclick = () => busy(b, action);
  return b;
}
async function busy(control, action) {
  if (control.disabled) return;
  control.disabled = true;
  try {
    await action();
  } catch (e) {
    fail(e);
  } finally {
    if (control.isConnected) control.disabled = false;
  }
}
function bindForm(id, action) {
  $(id).onsubmit = (event) => {
    event.preventDefault();
    const submit = event.submitter || $(id).querySelector('button');
    void busy(submit, action);
  };
}
function list(target, items, render) {
  target.replaceChildren();
  if (!items.length) return empty(target, '还没有内容。');
  for (const item of items) target.append(render(item));
}
function row(title, subtitle, status, action) {
  const r = node('div', undefined, 'list-row'),
    content = node('div');
  content.append(action ? button(title, action) : node('span', title));
  if (subtitle) content.append(node('small', subtitle));
  r.append(content);
  if (status) r.append(badge(status));
  return r;
}
function asItems(value, key) {
  return Array.isArray(value) ? value : value?.[key] || value?.items || [];
}
function time(value) {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
}
function pretty(value) {
  return JSON.stringify(value, null, 2);
}
function signedOut() {
  clearInterval(state.timer);
  state.timer = null;
  state.generation++;
  state.user = null;
  state.grant = null;
  state.resolution = null;
  state.pending = [];
  state.memoryDraft = null;
  clearTimeout(noticeTimer);
  $('notice').textContent = '';
  chatAttempt = taskAttempt = null;
  state.session = state.run = state.task = null;
  resetPages();
  resetHistory();
  state.taskRequest++;
  $('shell').hidden = true;
  $('auth').hidden = false;
  for (const id of [
    'messages',
    'sessions',
    'tasks',
    'memory-list',
    'skills-list',
    'usage',
    'ledger',
    'users',
    'recent-tasks',
    'pending-receipts',
    'grant-user',
    'resolve-request',
    'artifacts',
  ])
    $(id).replaceChildren();
  document.querySelectorAll('.import-result').forEach((el) => {
    el.textContent = '';
  });
  for (const id of ['task-detail', 'task-output', 'task-events', 'run-events']) $(id).textContent = '';
  for (const id of [
    'memory-form',
    'skill-form',
    'grant-form',
    'chat-form',
    'task-form',
    'resume-form',
    'resolve-form',
    'memory-import',
    'skills-import',
  ])
    $(id).reset();
}
async function signedIn(user) {
  state.user = user;
  state.generation++;
  $('auth').hidden = true;
  $('shell').hidden = false;
  $('identity').textContent = user.email;
  $('admin-nav').hidden = user.role !== 'admin';
  $('auth-form').reset();
  $('auth-error').textContent = '';
  await page('overview');
  clearInterval(state.timer);
  state.timer = setInterval(() => {
    void poll();
  }, 4000);
}
async function page(name) {
  if (name === 'admin' && state.user?.role !== 'admin') return;
  state.page = name;
  $('page-title').textContent = titles[name];
  document.querySelectorAll('[data-view]').forEach((el) => {
    el.hidden = el.dataset.view !== name;
  });
  document.querySelectorAll('[data-page]').forEach((el) => {
    el.classList.toggle('active', el.dataset.page === name);
  });
  await refresh();
}
async function refresh() {
  const generation = state.generation;
  try {
    if (state.page === 'overview') {
      await wallet();
      if (generation === state.generation) await tasks(true);
    }
    if (state.page === 'wallet') await wallet(true);
    if (state.page === 'chat') {
      await sessions();
      await catalog();
      if (state.session) await history();
    }
    if (state.page === 'workshop') {
      await catalog();
      await tasks();
      if (state.task) await taskDetail();
    }
    if (state.page === 'memory') await memories();
    if (state.page === 'skills') await skills();
    if (state.page === 'admin') await admin();
  } catch (e) {
    $('connection').textContent = '读取失败';
    throw e;
  }
}
const pages = Object.fromEntries(
  ['sessions', 'tasks', 'usage', 'ledger', 'users', 'pending'].map((name) => [
    name,
    { cursor: 0, previous: [], request: 0 },
  ])
);
function resetPages() {
  for (const [name, p] of Object.entries(pages)) {
    p.cursor = 0;
    p.previous = [];
    p.request++;
    $(`${name}-pager`).replaceChildren();
  }
}
function beginPage(name) {
  const p = pages[name],
    cursor = p.cursor,
    request = ++p.request,
    generation = state.generation;
  return () => p.cursor === cursor && p.request === request && state.generation === generation;
}
async function changePage(name, cursor, previous, load) {
  const p = pages[name],
    old = { cursor: p.cursor, previous: p.previous };
  p.cursor = cursor;
  p.previous = previous;
  try {
    await load();
  } catch (error) {
    if (p.cursor === cursor && p.previous === previous) {
      p.cursor = old.cursor;
      p.previous = old.previous;
      p.request++;
    }
    throw error;
  }
}
function drawPager(name, next, load) {
  const p = pages[name];
  const first = button('回到首页', () => changePage(name, 0, [], load));
  first.id = `${name}-first`;
  first.disabled = !p.previous.length;
  const previous = button('上一页', () => changePage(name, p.previous.at(-1), p.previous.slice(0, -1), load));
  previous.id = `${name}-previous`;
  previous.disabled = !p.previous.length;
  const more = button('下一页', () => changePage(name, next, [...p.previous, p.cursor], load));
  more.id = `${name}-next`;
  more.disabled = !Number.isSafeInteger(next) || next <= p.cursor;
  $(`${name}-pager`).replaceChildren(first, previous, node('span', `第 ${p.previous.length + 1} 页`), more);
}
async function wallet(full = false) {
  const ledgerRead = full ? beginPage('ledger') : null,
    usageRead = full ? beginPage('usage') : null;
  const w = await api(`/api/wallet?after=${full ? pages.ledger.cursor : 0}`);
  if (full && (!ledgerRead() || !usageRead())) return;
  for (const prefix of ['overview', 'wallet'])
    for (const part of ['available', 'held', 'balance'])
      $(`${prefix}-${part}`).textContent = credits(w[`${part}_micros`]);
  if (!full) return;
  const u = await api(`/api/usage?offset=${pages.usage.cursor}`);
  if (!ledgerRead() || !usageRead()) return;
  const tbody = $('usage');
  tbody.replaceChildren();
  for (const r of u.receipts) {
    const tr = node('tr'),
      usage = r.settlement?.usage;
    const first = node('td', time(r.created_at));
    first.append(node('small', r.request_id));
    tr.append(
      first,
      node('td', r.model),
      node('td', usage?.known ? `${usage.input_tokens} / ${usage.output_tokens}` : '待确认'),
      node('td', usage?.known ? `${usage.cache_read_tokens ?? 0} / ${usage.cache_write_tokens ?? 0}` : '—'),
      node('td', r.status === 'pending' || r.status === 'reserved' ? '待结算' : credits(r.charged_micros))
    );
    const status = node('td');
    status.append(badge(r.status));
    if (r.resolution) {
      const manual = r.resolution.payload;
      const detail = node('details');
      detail.append(node('summary', '人工核对依据'));
      detail.append(
        node(
          'p',
          manual.decision === 'settle'
            ? `核对输入 / 输出：${manual.usage.input_tokens} / ${manual.usage.output_tokens} token`
            : '人工决定：释放冻结，不扣费'
        )
      );
      detail.append(node('p', `原因：${manual.reason}`));
      detail.append(node('small', '扣费以人工核对结果为准；原始用量独立保留。'));
      status.append(detail);
    }
    tr.append(status);
    tbody.append(tr);
  }
  drawPager('usage', u.next_offset, () => wallet(true));
  $('ledger').replaceChildren();
  for (const r of w.ledger) {
    const tr = node('tr');
    tr.dataset.seq = String(r.seq);
    for (const cell of [time(r.created_at), labels[r.kind] || r.kind, credits(r.amount_micros), r.reason])
      tr.append(node('td', cell));
    $('ledger').append(tr);
  }
  drawPager('ledger', w.next_after, () => wallet(true));
}
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
function resetHistory() {
  state.historyBefore = Number.MAX_SAFE_INTEGER;
  state.historyRequest++;
  $('history-pager').replaceChildren();
}
async function history() {
  const session = state.session,
    before = state.historyBefore,
    request = ++state.historyRequest;
  const data = await rpc('agent.session.history', { session_id: session, before, limit: 100 });
  if (session !== state.session || before !== state.historyBefore || request !== state.historyRequest) return;
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
  const latest = button('回到最新', async () => {
    resetHistory();
    await history();
  });
  latest.id = 'history-latest';
  latest.disabled = before === Number.MAX_SAFE_INTEGER;
  $('history-pager').replaceChildren(
    older,
    node('span', before === Number.MAX_SAFE_INTEGER ? '最新消息' : '正在查看历史消息'),
    latest
  );
}
async function catalog() {
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
async function tasks(recent = false) {
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
        if (state.page !== 'workshop') await page('workshop');
        else await taskDetail();
      }
    )
  );
  if (!recent) drawPager('tasks', data.next_offset, () => tasks());
}
async function taskDetail() {
  const task = state.task,
    request = ++state.taskRequest,
    t = await rpc('workshop.get', { task_id: task });
  if (state.task !== task || request !== state.taskRequest) return;
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
  if (state.task === task && request === state.taskRequest) $('task-events').textContent = pretty(events);
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
      .replace(/[\x00-\x1f\x7f<>:"|?*\u202a-\u202e\u2066-\u2069]/g, '_')
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
async function poll() {
  if (!state.user || state.polling || document.hidden) return;
  state.polling = true;
  try {
    if (state.page === 'chat' && state.run) {
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
    if (state.page === 'workshop' && state.task) await taskDetail();
  } catch {
    $('connection').textContent = '连接中断，稍后重试';
  } finally {
    state.polling = false;
  }
}
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
async function users() {
  const current = beginPage('users'),
    data = await api(`/api/admin/users?offset=${pages.users.cursor}`);
  if (!current()) return;
  list($('users'), data.users, (u) =>
    row(u.email, `${credits(u.balance)} 积分 · ${u.role === 'admin' ? '管理员' : '普通用户'}`, '', () => {
      $('grant-user').value = u.id;
    })
  );
  const selected = $('grant-user').value;
  $('grant-user').replaceChildren();
  for (const u of data.users) $('grant-user').append(new Option(u.email, u.id));
  if (data.users.some((u) => u.id === selected)) $('grant-user').value = selected;
  drawPager('users', data.next_offset, users);
}
async function pendingReceipts() {
  const current = beginPage('pending'),
    pending = await api(`/api/admin/usage/pending?offset=${pages.pending.cursor}`);
  if (!current()) return;
  const selected = state.pending[Number($('resolve-request').value)];
  state.pending = pending.receipts;
  list($('pending-receipts'), state.pending, (r) =>
    row(r.request_id, `${r.namespace} · 冻结 ${credits(r.reserved_micros)} 积分`, r.status)
  );
  $('resolve-request').replaceChildren();
  for (const [index, r] of state.pending.entries())
    $('resolve-request').append(new Option(`${r.request_id} · ${r.namespace}`, String(index)));
  const index = state.pending.findIndex(
    (r) => r.namespace === selected?.namespace && r.request_id === selected?.request_id
  );
  if (index >= 0) $('resolve-request').value = String(index);
  drawPager('pending', pending.next_offset, pendingReceipts);
}
async function admin() {
  await users();
  await pendingReceipts();
  const tariff = await api('/api/admin/tariff');
  $('tariff-version').textContent = `当前版本 v${tariff.version}`;
  $('tariff-input').value = credits(BigInt(tariff.input_micros) * 1000n);
  $('tariff-output').value = credits(BigInt(tariff.output_micros) * 1000n);
}
$('auth-switch').onclick = () => {
  state.register = !state.register;
  $('auth-title').textContent = state.register ? '创建你的账户' : '欢迎回来';
  $('auth-description').textContent = state.register
    ? '注册后初始额度为 0，联系管理员领取额度。'
    : '登录你的 EasyGo 工作台。';
  $('auth-submit').textContent = state.register ? '注册并进入' : '登录';
  $('auth-switch').textContent = state.register ? '已有账户？登录' : '还没有账户？注册';
  $('auth-form').elements.password.autocomplete = state.register ? 'new-password' : 'current-password';
  $('auth-error').textContent = '';
};
bindForm('auth-form', async () => {
  try {
    const data = await api(
      state.register ? '/api/register' : '/api/login',
      Object.fromEntries(new FormData($('auth-form')))
    );
    await signedIn(data.user);
  } catch (e) {
    $('auth-error').textContent = e.message;
  }
});
$('logout').onclick = () =>
  busy($('logout'), async () => {
    await api('/api/logout', {});
    signedOut();
  });
document.querySelectorAll('[data-page],[data-go]').forEach((el) => {
  el.onclick = () => busy(el, () => page(el.dataset.page || el.dataset.go));
});
$('refresh').onclick = () => busy($('refresh'), refresh);
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
let chatAttempt, taskAttempt;
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
  const signature = pretty(params);
  if (!chatAttempt || chatAttempt.signature !== signature) chatAttempt = { signature, key: crypto.randomUUID() };
  const run = await rpc('agent.run.start', { ...params, idempotency_key: chatAttempt.key });
  chatAttempt = null;
  state.run = run.id;
  $('chat-input').value = '';
  $('cancel-run').disabled = false;
  await poll();
});
$('cancel-run').onclick = () =>
  busy($('cancel-run'), async () => {
    if (state.run) await rpc('agent.run.cancel', { run_id: state.run });
    await poll();
  });
$('workflow').onchange = taskRuntimes;
bindForm('task-form', async () => {
  const params = {
    workflow: $('workflow').value,
    input: $('task-input').value,
    ...($('task-runtime').value ? { runtime: $('task-runtime').value } : {}),
  };
  const signature = pretty(params);
  if (!taskAttempt || taskAttempt.signature !== signature) taskAttempt = { signature, key: crypto.randomUUID() };
  const task = await rpc('workshop.submit', { ...params, idempotency_key: taskAttempt.key });
  taskAttempt = null;
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
bindForm('memory-form', async () => {
  await rpc('agent.memory.upsert', {
    ...($('memory-id').value ? { id: $('memory-id').value, expected_version: Number($('memory-version').value) } : {}),
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
bindForm('grant-form', async () => {
  const params = {
    user_id: $('grant-user').value,
    amount_micros: micros($('grant-amount').value),
    reason: $('grant-reason').value,
  };
  const signature = pretty(params);
  if (!state.grant || state.grant.signature !== signature) state.grant = { signature, key: crypto.randomUUID() };
  await api('/api/admin/credits', { ...params, idempotency_key: state.grant.key });
  state.grant = null;
  $('grant-amount').value = '';
  $('grant-reason').value = '';
  await admin();
  notify('额度已发放。');
});
bindForm('tariff-form', async () => {
  await api(
    '/api/admin/tariff',
    { input_micros: micros($('tariff-input').value, 1000n), output_micros: micros($('tariff-output').value, 1000n) },
    'PUT'
  );
  await admin();
  notify('新费率已发布。');
});
api('/api/me')
  .then((data) => signedIn(data.user))
  .catch((error) => {
    if (state.user) fail(error);
  });

$('resolve-decision').onchange = () => {
  $('resolve-usage').hidden = $('resolve-decision').value !== 'settle';
};
bindForm('resolve-form', async () => {
  const receipt = state.pending[Number($('resolve-request').value)];
  if (!receipt) throw Error('请先选择待核对请求。');
  const decision = $('resolve-decision').value;
  const params = {
    namespace: receipt.namespace,
    request_id: receipt.request_id,
    decision,
    reason: $('resolve-reason').value,
  };
  if (decision === 'settle') {
    params.usage = {
      known: true,
      input_tokens: Number($('resolve-input').value),
      output_tokens: Number($('resolve-output').value),
      cache_read_tokens: Number($('resolve-cache-read').value),
      cache_write_tokens: Number($('resolve-cache-write').value),
    };
    for (const [key, value] of Object.entries(params.usage))
      if (key !== 'known' && (!Number.isSafeInteger(value) || value < 0))
        throw Error('Token 数量必须为安全范围内的非负整数。');
  }
  const signature = pretty(params);
  if (!state.resolution || state.resolution.signature !== signature)
    state.resolution = { signature, key: crypto.randomUUID() };
  await api('/api/admin/usage/resolve', { ...params, idempotency_key: state.resolution.key });
  state.resolution = null;
  $('resolve-form').reset();
  $('resolve-usage').hidden = true;
  await admin();
  notify('对账完成，原始收据与处理记录已保留。');
});
