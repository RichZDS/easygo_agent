// Shared state, DOM helpers and formatting for the console modules.
export const $ = (id) => document.getElementById(id);
export const state = {
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
  pending: [],
  memoryDraft: null,
  historyBefore: Number.MAX_SAFE_INTEGER,
  historyRequest: 0,
  taskRequest: 0,
};
// Page-level actions that views trigger; app.js fills these in at startup.
export const shell = {
  page: async () => {},
  poll: async () => {},
  signedOut: () => {},
};
export const labels = {
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
let noticeTimer;
export function notify(message) {
  $('notice').textContent = message;
  clearTimeout(noticeTimer);
  noticeTimer = setTimeout(() => {
    $('notice').textContent = '';
  }, 7000);
}
export function clearNotice() {
  clearTimeout(noticeTimer);
  $('notice').textContent = '';
}
export function fail(error) {
  notify(error.message || '请求失败，请重试。');
}
export function credits(value) {
  const n = BigInt(value ?? 0),
    sign = n < 0n ? '-' : '',
    abs = n < 0n ? -n : n;
  const fraction = String(abs % 1000000n)
    .padStart(6, '0')
    .replace(/0+$/, '');
  return `${sign}${abs / 1000000n}${fraction ? `.${fraction}` : ''}`;
}
export function micros(value, scale = 1000000n) {
  if (!/^\d+(\.\d{1,6})?$/.test(value)) throw Error('请输入最多六位小数的非负额度。');
  const [whole, fraction = ''] = value.split('.');
  const amount = BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'));
  if ((amount * scale) % 1000000n) throw Error('费率精度超出每 token 的整数 microcredit。');
  const result = (amount * scale) / 1000000n;
  if (result > BigInt(Number.MAX_SAFE_INTEGER)) throw Error('额度过大。');
  return Number(result);
}
export function node(tag, text, cls) {
  const n = document.createElement(tag);
  if (text !== undefined) n.textContent = String(text);
  if (cls) n.className = cls;
  return n;
}
export function empty(target, text) {
  target.replaceChildren(node('p', text, 'empty'));
}
export function badge(status) {
  return node('span', labels[status] || status || '未知', `badge ${status || ''}`);
}
export function button(text, action) {
  const b = node('button', text);
  b.type = 'button';
  b.onclick = () => busy(b, action);
  return b;
}
export async function busy(control, action) {
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
export function bindForm(id, action) {
  $(id).onsubmit = (event) => {
    event.preventDefault();
    const submit = event.submitter || $(id).querySelector('button');
    void busy(submit, action);
  };
}
export function list(target, items, render) {
  target.replaceChildren();
  if (!items.length) return empty(target, '还没有内容。');
  for (const item of items) target.append(render(item));
}
export function row(title, subtitle, status, action) {
  const r = node('div', undefined, 'list-row'),
    content = node('div');
  content.append(action ? button(title, action) : node('span', title));
  if (subtitle) content.append(node('small', subtitle));
  r.append(content);
  if (status) r.append(badge(status));
  return r;
}
export function asItems(value, key) {
  return Array.isArray(value) ? value : value?.[key] || value?.items || [];
}
export function time(value) {
  return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—';
}
export function pretty(value) {
  return JSON.stringify(value, null, 2);
}

// Starts a request and returns a check that stays true only while no newer request has
// bumped owner[key] and the signed-in session is unchanged. Bumping owner[key] elsewhere
// discards whatever is in flight.
export function latest(owner, key) {
  const ticket = ++owner[key],
    generation = state.generation;
  return () => owner[key] === ticket && state.generation === generation;
}

// A retried submit with unchanged params reuses its idempotency key; a successful one or
// changed params start a new key. `slot` names the form the key belongs to.
const attempts = new Map();
export async function idempotentSubmit(slot, params, send) {
  const signature = pretty(params);
  if (attempts.get(slot)?.signature !== signature) attempts.set(slot, { signature, key: crypto.randomUUID() });
  const result = await send({ ...params, idempotency_key: attempts.get(slot).key });
  attempts.delete(slot);
  return result;
}
export function forgetAttempts() {
  attempts.clear();
}
