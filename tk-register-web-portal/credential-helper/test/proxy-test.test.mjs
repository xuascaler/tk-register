import test from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createCredentialService } from '../src/service.mjs';
import { createHelperServer } from '../src/server.mjs';
import { createTransport } from '../src/transport.mjs';

const proxy = 'socks5h://mock-user:mock-password@proxy.example:1080';

test('stateless proxy test uses only the supplied proxy and fixed IP endpoint, without allocating flows', async t => {
  const calls = [];
  const service = createCredentialService({ maxFlows: 0, maxOwnerFlows: 0, requestJson: async (url, options) => {
    calls.push({ url, ...options });
    return { ip: '8.8.4.4' };
  } });
  t.after(() => service.close());
  // Zero permitted flows proves this route does not allocate OAuth/import state.
  for (let i = 0; i < 12; i++) {
    const result = await service.proxyTest({ owner_id: 42, proxy_url: proxy });
    assert.deepEqual(Object.keys(result).sort(), ['owner_id', 'proxy_check']);
    assert.equal(result.owner_id, '42');
    assert.deepEqual(Object.keys(result.proxy_check).sort(), ['checked_at', 'elapsed_ms', 'ip', 'ok']);
    assert.equal(result.proxy_check.ok, true);
    assert.equal(result.proxy_check.ip, '8.8.4.4');
    assert.ok(Number.isFinite(Date.parse(result.proxy_check.checked_at)));
  }
  assert.equal(calls.length, 12);
  for (const call of calls) {
    assert.equal(call.url, 'https://api.ipify.org/?format=json');
    assert.equal(call.proxy_url, proxy);
    assert.ok(call.timeout > 0 && call.timeout <= 15000);
    assert.equal(call.body, undefined);
    assert.equal(call.headers, undefined);
  }
});

test('proxy-test rejects missing owner, unknown fields, invalid proxy, and invalid returned exit IP', async t => {
  let calls = 0;
  const service = createCredentialService({ requestJson: async () => { calls++; return { ip: '127.0.0.1' }; } });
  t.after(() => service.close());
  await assert.rejects(service.proxyTest({ proxy_url: proxy }), { code: 'invalid_owner' });
  await assert.rejects(service.proxyTest({ owner_id: '42', proxy_url: proxy, url: 'https://claude.ai/' }), { code: 'invalid_request' });
  await assert.rejects(service.proxyTest({ owner_id: '42', proxy_url: 'file:///etc/passwd' }), { code: 'invalid_proxy' });
  assert.equal(calls, 0);
  await assert.rejects(service.proxyTest({ owner_id: '42', proxy_url: proxy }), { code: 'proxy_check_failed' });
  assert.equal(calls, 1);
});

test('proxy-test inherits private and metadata DNS rejection before making any connection', async t => {
  let dials = 0;
  const requestJson = createTransport({ lookup: async () => [{ address: '169.254.169.254', family: 4 }],
    dial: async () => { dials++; throw new Error('must not connect'); } });
  const service = createCredentialService({ requestJson });
  t.after(() => service.close());
  await assert.rejects(service.proxyTest({ owner_id: '42', proxy_url: proxy }), { code: 'proxy_forbidden' });
  assert.equal(dials, 0);
});

test('proxy-test HTTP route requires internal bearer and preserves safe error classifications', async t => {
  let failure, calls = 0;
  const service = createCredentialService({ requestJson: async (_url, options) => {
    calls++;
    assert.equal(options.proxy_url, proxy);
    assert.ok(options.timeout > 0 && options.timeout <= 15000);
    if (failure) throw Object.assign(new Error('mock-user mock-password private upstream body'), failure);
    return { ip: '8.8.4.4' };
  } });
  const server = createHelperServer({ token: 'internal-test-only', service });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => { server.closeAllConnections(); server.close(); });
  const url = `http://127.0.0.1:${server.address().port}/proxy-test`;
  const body = JSON.stringify({ owner_id: '42', proxy_url: proxy });
  const headers = { 'Content-Type': 'application/json', Authorization: 'Bearer internal-test-only' };
  for (const authorization of ['', 'Bearer wrong-internal-token']) {
    const response = await fetch(url, { method: 'POST', headers: { ...headers, Authorization: authorization }, body });
    assert.equal(response.status, 401);
  }
  assert.equal(calls, 0);
  const success = await fetch(url, { method: 'POST', headers, body });
  assert.equal(success.status, 200);
  assert.equal(success.headers.get('cache-control'), 'no-store');
  const result = await success.json();
  assert.deepEqual(Object.keys(result).sort(), ['owner_id', 'proxy_check']);
  assert.ok(!JSON.stringify(result).includes('mock-password'));
  for (const [code, status] of [['proxy_authentication_failed', 502], ['upstream_timeout', 504], ['upstream_network_error', 502]]) {
    failure = { code, status };
    const response = await fetch(url, { method: 'POST', headers, body });
    assert.equal(response.status, status);
    const error = await response.json();
    assert.equal(error.error.code, code);
    assert.ok(!JSON.stringify(error).includes('mock-password'));
    assert.ok(!JSON.stringify(error).includes('mock-user'));
    assert.ok(!JSON.stringify(error).includes('private upstream body'));
  }
});
