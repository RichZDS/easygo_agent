// A browser session against the platform web API: keeps the login cookie and sends
// the Origin header the CSRF check expects.
import assert from 'node:assert/strict';

export function browser(origin) {
  let cookie = '';
  return {
    // Fails unless the response status is expected.
    async request(path, body, method = body === undefined ? 'GET' : 'POST', expected = 200) {
      const r = await fetch(origin + path, {
        method,
        headers: { 'Content-Type': 'application/json', Origin: origin, ...(cookie ? { Cookie: cookie } : {}) },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const set = r.headers.get('set-cookie');
      if (set) cookie = set.split(';')[0];
      const value = await r.json();
      assert.equal(r.status, expected, JSON.stringify(value));
      return value;
    },
    async rpc(method, params = {}) {
      return (await this.request('/api/rpc', { method, params })).result;
    },
  };
}
