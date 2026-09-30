import { $, button, latest, node } from './ui.js';

export const pages = Object.fromEntries(
  ['sessions', 'tasks', 'usage', 'ledger', 'users', 'pending'].map((name) => [
    name,
    { cursor: 0, previous: [], request: 0 },
  ])
);
export function resetPages() {
  for (const [name, p] of Object.entries(pages)) {
    p.cursor = 0;
    p.previous = [];
    p.request++;
    $(`${name}-pager`).replaceChildren();
  }
}
// Starts reading the current page; the check fails once the cursor moves or a newer read starts.
export function beginPage(name) {
  const p = pages[name],
    cursor = p.cursor,
    current = latest(p, 'request');
  return () => current() && p.cursor === cursor;
}
export async function changePage(name, cursor, previous, load) {
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
export function drawPager(name, next, load) {
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
