import { api } from './api.js';
import { resetPages } from './pager.js';
import { $, busy, bindForm, clearNotice, fail, forgetAttempts, shell, state } from './ui.js';
import * as admin from './views/admin.js';
import * as chat from './views/chat.js';
import * as knowledge from './views/knowledge.js';
import * as overview from './views/overview.js';
import * as wallet from './views/wallet.js';
import * as workshop from './views/workshop.js';

const titles = {
  overview: '总览',
  chat: '对话',
  workshop: '任务工坊',
  memory: '长期记忆',
  skills: '技能库',
  wallet: '额度与用量',
  admin: '管理',
};
const views = { overview, chat, workshop, memory: knowledge, skills: knowledge, wallet, admin };

function signedOut() {
  clearInterval(state.timer);
  state.timer = null;
  state.generation++;
  state.user = null;
  state.pending = [];
  state.memoryDraft = null;
  clearNotice();
  forgetAttempts();
  state.session = state.run = state.task = null;
  resetPages();
  chat.resetHistory();
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
  try {
    await views[state.page].load();
  } catch (e) {
    $('connection').textContent = '读取失败';
    throw e;
  }
}
async function poll() {
  if (!state.user || state.polling || document.hidden) return;
  state.polling = true;
  try {
    if (state.page === 'chat' && state.run) await chat.pollRun();
    if (state.page === 'workshop' && state.task) await workshop.taskDetail();
  } catch {
    $('connection').textContent = '连接中断，稍后重试';
  } finally {
    state.polling = false;
  }
}
Object.assign(shell, { page, poll, signedOut });

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
for (const view of new Set(Object.values(views))) view.bind();
api('/api/me')
  .then((data) => signedIn(data.user))
  .catch((error) => {
    if (state.user) fail(error);
  });
