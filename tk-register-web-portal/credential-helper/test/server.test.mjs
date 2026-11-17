import test from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { spawnSync } from 'node:child_process';
import { createHelperServer } from '../src/server.mjs';

test('startup requires internal token and does not disclose environment secrets', () => {
  assert.throws(() => createHelperServer({ token: '' }), /must be configured/);
  const result = spawnSync(process.execPath, ['src/server.mjs'], {
    cwd: new URL('..', import.meta.url), env: { PATH: process.env.PATH, RANDOM_SECRET: 'do-not-print-this' }, encoding: 'utf8',
  });
  assert.equal(result.status, 1);
  assert.ok(!result.stderr.includes('do-not-print-this'));
});

test('every endpoint requires bearer, accepts only JSON and scrubs upstream errors', async t => {
  let calls = 0;
  const server = createHelperServer({ token: 'internal-test-only', service: {
    close() {},
    async start(body) {
      calls++;
      if (body.fail) throw Object.assign(new Error('Bearer secret upstream password'), { code: 'upstream_rejected', status: 502 });
      return { owner_id: body.owner_id, flow_id: 'mock-flow' };
    },
  } });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => { server.closeAllConnections(); server.close(); });
  const base = `http://127.0.0.1:${server.address().port}`;
  for (const route of ['/healthz', '/start', '/exchange', '/import', '/verify', '/proxy-test']) {
    const reply = await fetch(base + route, { method: route === '/healthz' ? 'GET' : 'POST' });
    assert.equal(reply.status, 401);
  }
  assert.equal(calls, 0);
  const headers = { Authorization: 'Bearer internal-test-only', 'Content-Type': 'application/json' };
  assert.deepEqual(await (await fetch(base + '/healthz', { headers })).json(), { ok: true });
  const nonJSON = await fetch(base + '/start', { method: 'POST', headers: { Authorization: headers.Authorization }, body: '{}' });
  assert.equal(nonJSON.status, 415);
  const success = await fetch(base + '/start', { method: 'POST', headers, body: JSON.stringify({ owner_id: '42' }) });
  assert.equal(success.headers.get('cache-control'), 'no-store');
  assert.equal((await success.json()).owner_id, '42');
  const failed = await fetch(base + '/start', { method: 'POST', headers, body: '{"fail":true}' });
  assert.equal(failed.status, 502);
  const error = await failed.json();
  assert.equal(error.error.code, 'upstream_rejected');
  assert.ok(!JSON.stringify(error).includes('password'));
  const malformed = await fetch(base + '/start', { method: 'POST', headers, body: '{' });
  assert.equal(malformed.status, 400);
});
