import { $, shell, state } from './ui.js';

export const errors = {
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
export async function api(path, data, method = data === undefined ? 'GET' : 'POST') {
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
    if (response.status === 401 && state.user) shell.signedOut();
    throw Error(errors[code] || `请求失败：${code}`);
  }
  $('connection').textContent = '已连接';
  return result;
}
export async function rpc(method, params = {}) {
  return (await api('/api/rpc', { method, params })).result;
}
