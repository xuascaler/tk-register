import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createCredentialService, FLOW_TTL_MS, normalizeOwner } from '../src/service.mjs';

const proxy = 'http://test-user:test-password@proxy.example:8080';
function mockService(options = {}) {
  const calls = [];
  let clock = Date.parse('2026-01-01T00:00:00Z');
  const requestJson = async (url, request) => {
    calls.push({ url, ...request });
    assert.equal(request.proxy_url, proxy);
    if (options.handle) {
      const result = await options.handle(url, request);
      if (result !== undefined) return result;
    }
    if (url.includes('api.ipify.org')) return { ip: '8.8.4.4' };
    if (url.includes('/oauth/token')) return { access_token: 'mock-access', refresh_token: 'mock-refresh', expires_in: 3600 };
    if (url.includes('/oauth/profile')) return { email: 'owner@example.test', account_uuid: 'account-1', organization_uuid: 'org-1', organization_name: 'Test' };
    throw new Error('Unexpected URL');
  };
  const service = createCredentialService({ requestJson, now: () => clock, ...options.serviceOptions });
  return { service, calls, advance: ms => { clock += ms; } };
}

test('owner required and normalized, invalid inputs and request endpoint overrides rejected', async t => {
  const { service } = mockService(); t.after(() => service.close());
  assert.equal(normalizeOwner(42), '42'); assert.equal(normalizeOwner(' user-1 '), 'user-1');
  for (const owner of [undefined, null, '', {}, [], true, 1.3, Infinity]) assert.throws(() => normalizeOwner(owner));
  await assert.rejects(service.start({ proxy_url: proxy }), { code: 'invalid_owner' });
  await assert.rejects(service.start({ owner_id: '42', proxy_url: proxy, token_url: 'https://evil.example/' }), { code: 'invalid_request' });
  await assert.rejects(service.start({ owner_id: '42', proxy_url: proxy, mode: 'bad' }), { code: 'invalid_mode' });
});

test('OAuth PKCE remains flow-bound, concurrent and repeated exchange redeems a code only once', async t => {
  const { service, calls } = mockService(); t.after(() => service.close());
  const started = await service.start({ owner_id: 42, proxy_url: proxy });
  const url = new URL(started.auth_url);
  assert.equal(url.hostname, 'claude.ai');
  assert.equal(url.searchParams.get('code_challenge_method'), 'S256');
  assert.equal(started.owner_id, '42');
  const body = { owner_id: '42', flow_id: started.flow_id, code: 'mock-code#returned-state' };
  const [a, b] = await Promise.all([service.exchange(body), service.exchange(body)]);
  const c = await service.exchange(body);
  assert.deepEqual(a, b); assert.deepEqual(a, c);
  const tokens = calls.filter(c => c.url.includes('/oauth/token'));
  assert.equal(tokens.length, 1);
  assert.equal(createHash('sha256').update(tokens[0].body.code_verifier).digest('base64url'), url.searchParams.get('code_challenge'));
  assert.equal(a.auth.proxy_url, proxy); assert.equal(a.auth.email, 'owner@example.test');
  assert.equal(a.expires_at, started.expires_at);
  assert.equal(a.proxy_check.ok, true);
  assert.ok(a.notes.length);
  await assert.rejects(service.exchange({ ...body, owner_id: 'other' }), { code: 'flow_not_found' });
  await assert.rejects(service.verify({ owner_id: 'other', flow_id: started.flow_id }), { code: 'flow_not_found' });
  await assert.rejects(service.exchange({ ...body, code: 'another-code' }), { code: 'code_mismatch' });
});

test('authorization expires after 30 minutes, including cached results', async t => {
  const { service, advance } = mockService(); t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy });
  const body = { owner_id: '42', flow_id: start.flow_id, code: 'one' };
  await service.exchange(body);
  advance(FLOW_TTL_MS);
  await assert.rejects(service.exchange(body), { code: 'flow_not_found' });
});

test('successful redemption survives a later profile network failure and retries only profile', async t => {
  let profiles = 0;
  const { service, calls } = mockService({ handle: async url => {
    if (url.includes('/oauth/profile') && ++profiles === 1) throw Object.assign(new Error('network secret'), { code: 'upstream_network_error', status: 502 });
  } }); t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy });
  const body = { owner_id: '42', flow_id: start.flow_id, code: 'one' };
  await assert.rejects(service.exchange(body), { code: 'upstream_network_error' });
  const result = await service.exchange(body);
  assert.equal(result.auth.access_token, 'mock-access');
  assert.equal(calls.filter(c => c.url.includes('/oauth/token')).length, 1);
  assert.equal(profiles, 2);
});

test('official setup returns incomplete credentials and notes without fabricated identity or expiry', async t => {
  const { service } = mockService({ handle: async url => {
    if (url.includes('/oauth/token')) return { access_token: 'mock-access' };
    if (url.includes('/oauth/profile')) throw Object.assign(new Error('private upstream content'), { code: 'upstream_rejected', upstreamStatus: 403, status: 502 });
  } }); t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy, mode: 'setup', setup_kind: 'official' });
  assert.equal(new URL(start.auth_url).searchParams.get('scope'), 'user:inference');
  const result = await service.exchange({ owner_id: '42', flow_id: start.flow_id, code: 'one' });
  assert.equal(result.auth.refresh_token, ''); assert.equal(result.auth.email, ''); assert.equal(result.auth.expired, '');
  assert.ok(result.notes.length >= 3);
  assert.ok(!JSON.stringify(result.notes).includes('private upstream content'));
});

test('proxy verification stays bound and cannot be overridden after authorization', async t => {
  let ip = '8.8.4.4';
  const { service } = mockService({ handle: async url => url.includes('api.ipify.org') ? { ip } : undefined });
  t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy });
  const body = { owner_id: '42', flow_id: start.flow_id };
  await assert.rejects(service.verify({ ...body, proxy_url: 'http://other.example' }), { code: 'proxy_mismatch' });
  ip = '1.1.1.1';
  await assert.rejects(service.verify(body), { code: 'proxy_ip_mismatch' });
  await assert.rejects(service.exchange({ ...body, code: 'one' }), { code: 'proxy_ip_mismatch' });
});

test('JSON import binds supplied proxy, selects account index and remains owner-scoped', async t => {
  const { service } = mockService(); t.after(() => service.close());
  const body = { owner_id: '42', proxy_url: proxy, kind: 'json', account_index: 1,
    document: JSON.stringify({ accounts: [{ access_token: 'first' }, { access_token: 'second', proxy_url: 'http://127.0.0.1', email: 'second@example.test' }] }) };
  const a = await service.import(body), b = await service.import(body);
  assert.deepEqual(a, b); assert.equal(a.auth.access_token, 'second'); assert.equal(a.auth.proxy_url, proxy);
  assert.equal(a.auth.refresh_token, '');
  await assert.rejects(service.verify({ owner_id: '43', flow_id: a.flow_id }), { code: 'flow_not_found' });
  await assert.rejects(service.import({ ...body, document: 'not-json' }), { code: 'invalid_document' });
});

test('token import does not invent refresh token or expiry', async t => {
  const { service } = mockService(); t.after(() => service.close());
  const result = await service.import({ owner_id: '42', proxy_url: proxy, kind: 'token', access_token: 'mock-token' });
  assert.equal(result.auth.access_token, 'mock-token'); assert.equal(result.auth.refresh_token, ''); assert.equal(result.auth.expired, '');
});

test('pending flow limits apply per owner and globally', async t => {
  const { service } = mockService({ serviceOptions: { maxOwnerFlows: 1, maxFlows: 2 } }); t.after(() => service.close());
  await service.start({ owner_id: '42', proxy_url: proxy });
  await assert.rejects(service.start({ owner_id: '42', proxy_url: proxy }), { code: 'too_many_sessions' });
  await service.start({ owner_id: '43', proxy_url: proxy });
  await assert.rejects(service.start({ owner_id: '44', proxy_url: proxy }), { code: 'too_many_sessions' });
});

test('completed flows are evicted before rejecting another authorization for the same owner', async t => {
  const { service } = mockService({ serviceOptions: { maxOwnerFlows: 1, maxFlows: 2 } }); t.after(() => service.close());
  const first = await service.start({ owner_id: '42', proxy_url: proxy });
  await service.exchange({ owner_id: '42', flow_id: first.flow_id, code: 'one' });
  const second = await service.start({ owner_id: '42', proxy_url: proxy });
  assert.notEqual(first.flow_id, second.flow_id);
  await assert.rejects(service.verify({ owner_id: '42', flow_id: first.flow_id }), { code: 'flow_not_found' });
});

test('array imports select the requested record', async t => {
  const { service } = mockService(); t.after(() => service.close());
  const result = await service.import({ owner_id: '42', proxy_url: proxy, kind: 'json',
    document: [{ access_token: 'one' }, { access_token: 'two' }], account_index: 1 });
  assert.equal(result.auth.access_token, 'two');
});

test('a rejected authorization code may be corrected before successful redemption', async t => {
  const { service } = mockService({ handle: async (url, request) => {
    if (url.includes('/oauth/token') && request.body.code === 'typo') throw Object.assign(new Error('invalid grant'), { code: 'upstream_rejected', status: 502 });
  } }); t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy });
  await assert.rejects(service.exchange({ owner_id: '42', flow_id: start.flow_id, code: 'typo' }), { code: 'upstream_rejected' });
  const result = await service.exchange({ owner_id: '42', flow_id: start.flow_id, code: 'correct-code' });
  assert.equal(result.auth.access_token, 'mock-access');
});

test('cookie import is idempotent and keeps redeemed credentials if profile needs a retry', async t => {
  let redirect, profileCalls = 0;
  const { service, calls } = mockService({ handle: async (url, request) => {
    if (url.endsWith('/api/organizations')) return [{ uuid: 'org-1', name: 'Test Org', capabilities: ['chat'] }];
    if (url.endsWith('/v1/oauth/org-1/authorize')) {
      redirect = 'https://platform.claude.com/oauth/code/callback?code=cookie-code&state=' + request.body.state;
      return { redirect_uri: redirect };
    }
    if (url.includes('/oauth/profile') && ++profileCalls === 1) throw Object.assign(new Error('network'), { code: 'upstream_network_error', status: 502 });
  } }); t.after(() => service.close());
  const body = { owner_id: '42', proxy_url: proxy, kind: 'cookie', session_key: 'sk-ant-sid-test-only' };
  await assert.rejects(service.import(body), { code: 'upstream_network_error' });
  const result = await service.import(body), again = await service.import(body);
  assert.deepEqual(result, again);
  assert.equal(result.auth.access_token, 'mock-access');
  assert.equal(calls.filter(c => c.url.includes('/oauth/token')).length, 1);
});

test('operation deadline reduces later request timeouts below the Go helper deadline', async t => {
  let clock = 1000000;
  const timeouts = [];
  const service = createCredentialService({ now: () => clock, requestJson: async (url, request) => {
    timeouts.push({ url, timeout: request.timeout });
    if (url.includes('api.ipify.org')) { clock += 10000; return { ip: '8.8.4.4' }; }
    if (url.includes('/oauth/token')) { clock += 24000; return { access_token: 'mock', refresh_token: 'mock-refresh', expires_in: 3600 }; }
    if (url.includes('/oauth/profile')) return { email: 'test@example.test' };
  } }); t.after(() => service.close());
  const start = await service.start({ owner_id: '42', proxy_url: proxy });
  await service.exchange({ owner_id: '42', flow_id: start.flow_id, code: 'one' });
  assert.equal(timeouts.find(t => t.url.includes('/oauth/token')).timeout, 25000);
  assert.equal(timeouts.find(t => t.url.includes('/oauth/profile')).timeout, 16000);
});

test('Cookie PKCE callback state must match the authorization request', async t => {
  const { service, calls } = mockService({ handle: async url => {
    if (url.endsWith('/api/organizations')) return [{ uuid: 'org-1' }];
    if (url.endsWith('/v1/oauth/org-1/authorize')) return { redirect_uri: 'https://platform.claude.com/oauth/code/callback?code=code&state=wrong' };
  } }); t.after(() => service.close());
  await assert.rejects(service.import({ owner_id: '42', proxy_url: proxy, kind: 'cookie', session_key: 'sk-ant-sid-test-only' }), { code: 'state_mismatch' });
  assert.equal(calls.filter(c => c.url.includes('/oauth/token')).length, 0);
});
