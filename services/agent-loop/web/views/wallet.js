import { api } from '../api.js';
import { beginPage, drawPager, pages } from '../pager.js';
import { $, badge, credits, labels, node, time } from '../ui.js';

// Balance tiles on the overview and wallet pages; with `full`, also the usage and ledger tables.
export async function wallet(full = false) {
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

export const load = () => wallet(true);
export function bind() {}
