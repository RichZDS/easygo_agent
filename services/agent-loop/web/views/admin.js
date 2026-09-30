import { api } from '../api.js';
import { beginPage, drawPager, pages } from '../pager.js';
import { $, bindForm, credits, idempotentSubmit, list, micros, notify, row, state } from '../ui.js';

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

export async function load() {
  await users();
  await pendingReceipts();
  const tariff = await api('/api/admin/tariff');
  $('tariff-version').textContent = `当前版本 v${tariff.version}`;
  $('tariff-input').value = credits(BigInt(tariff.input_micros) * 1000n);
  $('tariff-output').value = credits(BigInt(tariff.output_micros) * 1000n);
}
export function bind() {
  bindForm('grant-form', async () => {
    const params = {
      user_id: $('grant-user').value,
      amount_micros: micros($('grant-amount').value),
      reason: $('grant-reason').value,
    };
    await idempotentSubmit('grant', params, (p) => api('/api/admin/credits', p));
    $('grant-amount').value = '';
    $('grant-reason').value = '';
    await load();
    notify('额度已发放。');
  });
  bindForm('tariff-form', async () => {
    await api(
      '/api/admin/tariff',
      { input_micros: micros($('tariff-input').value, 1000n), output_micros: micros($('tariff-output').value, 1000n) },
      'PUT'
    );
    await load();
    notify('新费率已发布。');
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
    await idempotentSubmit('resolution', params, (p) => api('/api/admin/usage/resolve', p));
    $('resolve-form').reset();
    $('resolve-usage').hidden = true;
    await load();
    notify('对账完成，原始收据与处理记录已保留。');
  });
}
