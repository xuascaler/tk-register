import test from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createCredentialService } from '../src/service.mjs';
import { createHelperServer } from '../src/server.mjs';
import { fail, publicError } from '../src/errors.mjs';

const proxy = 'socks5://diagnostic-user:private-proxy-password@proxy.example:1080';
const cookie = 'sk-ant-sid-private-cookie-test-only';
const body = { owner_id: '42', kind: 'cookie', proxy_url: proxy, session_key: cookie };
const forbidden = [cookie, 'private-proxy-password', 'private-access', 'private-refresh', 'secret-upstream-message', 'private-org', 'proxy.example'];

function credentialService(handle = () => undefined) {
  const calls = [];
  const service = createCredentialService({ requestJson: async (url, request) => {
    calls.push(url);
    const result = await handle(url, request);
    if (result !== undefined) return result;
    if (url.includes('api.ipify.org')) return { ip: '8.8.4.4' };
    if (url.endsWith('/api/organizations')) return [{ uuid: 'private-org', name: 'Private organization', capabilities: ['chat'] }];
    if (url.endsWith('/authorize')) return { redirect_uri: `https://platform.claude.com/oauth/code/callback?code=private-code&state=${request.body.state}` };
    if (url.endsWith('/oauth/token')) return { access_token: 'private-access', refresh_token: 'private-refresh', expires_in: 3600 };
    if (url.endsWith('/oauth/profile')) return { email: 'owner@example.test' };
    throw new Error('secret-upstream-message');
  } });
  return { service, calls };
}

async function helper(t, service) {
  const logs = [];
  const server = createHelperServer({ token: 'test-only-bearer', service, logger: line => logs.push(line) });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => { server.closeAllConnections(); server.close(); });
  const base = `http://127.0.0.1:${server.address().port}`;
  const post = (value = body, metadata = {}) => fetch(base + '/import', {
    method: 'POST', headers: { Authorization: 'Bearer test-only-bearer', 'Content-Type': 'application/json',
      'X-Request-ID': 'request-cookie-123', 'X-TK-Credential-Entry': 'submit', 'X-TK-Credential-Method': 'cookie', ...metadata },
    body: JSON.stringify(value),
  });
  return { logs, post };
}

function safeOutput(value) {
  const text = JSON.stringify(value);
  for (const secret of forbidden) assert(!text.includes(secret), `Unexpected sensitive value: ${secret}`);
}

test('error diagnostics copy only fixed fields, enums and actual bounded integer statuses', () => {
  const original = fail('upstream_rejected', 'secret-upstream-message', 502, {
    stage: 'cookie_organizations', upstreamStatus: 403, upstreamCode: 'permission_error', responseKind: 'html',
    credential: cookie, url: proxy, stack: 'secret-upstream-message',
  });
  assert.deepEqual(publicError(original), { status: 502, body: { error: {
    code: 'upstream_rejected', message: 'The upstream rejected the request.', stage: 'cookie_organizations',
    upstream_status: 403, upstream_code: 'permission_error', response_kind: 'html',
  } } });
  for (const status of ['403', 403.5, 99, 600, NaN, Infinity, null]) {
    const result = publicError(fail('upstream_rejected', cookie, 502, {
      stage: proxy, upstreamStatus: status, upstreamCode: cookie, responseKind: cookie,
    }));
    assert.deepEqual(result.body.error, { code: 'upstream_rejected', message: 'The upstream rejected the request.' });
    safeOutput(result);
  }
  safeOutput(publicError(original));
});

for (const [stage, suffix] of [['cookie_organizations', '/api/organizations'], ['cookie_authorize', '/authorize'], ['oauth_token', '/oauth/token']]) {
  for (const status of [401, 403]) {
    test(`Cookie ${stage} HTTP ${status} keeps its exact failure stage without diagnosing expiry`, async t => {
      const { service } = credentialService(url => {
        if (url.endsWith(suffix)) throw fail('upstream_rejected', 'secret-upstream-message', 502,
          { upstreamStatus: status, upstreamCode: 'authentication_error', responseKind: 'json' });
      });
      t.after(() => service.close());
      await assert.rejects(service.import(body), error => {
        const result = publicError(error);
        assert.equal(result.status, 502);
        assert.equal(result.body.error.stage, stage);
        assert.equal(result.body.error.upstream_status, status);
        assert.equal(result.body.error.upstream_code, 'authentication_error');
        assert(!/expired|invalid session/i.test(result.body.error.message));
        safeOutput(result); return true;
      });
    });
  }
}

for (const [stage, suffix, result, code] of [
  ['cookie_organizations', '/api/organizations', [], 'organization_missing'],
  ['cookie_authorize', '/authorize', {}, 'authorization_response_invalid'],
  ['oauth_token', '/oauth/token', {}, 'invalid_token_response'],
]) {
  test(`Cookie semantic ${code} is attributed without inventing an upstream HTTP status`, async t => {
    const { service } = credentialService(url => url.endsWith(suffix) ? result : undefined);
    t.after(() => service.close());
    await assert.rejects(service.import(body), error => {
      const value = publicError(error).body.error;
      assert.equal(value.code, code); assert.equal(value.stage, stage);
      assert.equal(value.upstream_status, undefined); safeOutput(value); return true;
    });
  });
}

test('proxy authentication in Cookie import records proxy_check without a fake upstream status', async t => {
  const { service } = credentialService(url => {
    if (url.includes('api.ipify.org')) throw fail('proxy_authentication_failed', proxy, 502);
  });
  t.after(() => service.close());
  const { post, logs } = await helper(t, service);
  const response = await post();
  assert.equal(response.status, 502);
  const result = await response.json();
  assert.equal(result.error.stage, 'proxy_check');
  assert.equal(result.error.code, 'proxy_authentication_failed');
  assert.equal(result.error.upstream_status, undefined);
  assert.equal(logs.length, 1); safeOutput([result, logs]);
});

test('three entry headers correlate Cookie failure logs while response and logs contain no credentials', async t => {
  const { service } = credentialService(url => {
    if (url.endsWith('/api/organizations')) throw fail('upstream_rejected', 'secret-upstream-message', 502,
      { upstreamStatus: 403, responseKind: 'html' });
  });
  const { post, logs } = await helper(t, service);
  for (const entry of ['submit', 'update', 'deactivate']) {
    const response = await post(body, { 'X-Request-ID': `request-${entry}`, 'X-TK-Credential-Entry': entry });
    const result = await response.json();
    assert.equal(result.error.upstream_status, 403); safeOutput(result);
    const log = JSON.parse(logs.at(-1).replace('TK_CREDENTIAL_HELPER ', ''));
    assert.deepEqual({ event: log.event, request_id: log.request_id, entry: log.entry, method: log.method,
      operation: log.operation, stage: log.stage, helper_status: log.helper_status, upstream_status: log.upstream_status },
    { event: 'failure', request_id: `request-${entry}`, entry, method: 'cookie', operation: 'import',
      stage: 'cookie_organizations', helper_status: 502, upstream_status: 403 });
    assert(Number.isInteger(log.duration_ms));
  }
  assert.equal(logs.length, 3); safeOutput(logs);
});

test('unexpected exceptions and invalid diagnostic headers never leak arbitrary strings', async t => {
  const service = { close() {}, async import() { throw Object.assign(new Error(cookie), {
    stage: proxy, upstreamStatus: '403', upstreamCode: cookie, responseKind: cookie,
  }); } };
  const { post, logs } = await helper(t, service);
  const response = await post(body, { 'X-Request-ID': 'secret.upstream.message', 'X-TK-Credential-Entry': cookie, 'X-TK-Credential-Method': cookie });
  assert.equal(response.status, 500);
  assert.deepEqual((await response.json()).error, { code: 'internal_error', message: 'The credential operation could not be completed.', stage: 'request_validation' });
  const record = JSON.parse(logs[0].replace('TK_CREDENTIAL_HELPER ', ''));
  assert.equal(record.entry, 'unknown'); assert.equal(record.method, 'unknown'); assert.equal(record.request_id, undefined);
  safeOutput(logs);
});

test('profile warnings retain credentials, emit current request metadata and do not redeem tokens again', async t => {
  let profiles = 0;
  const { service, calls } = credentialService(url => {
    if (url.endsWith('/oauth/profile') && ++profiles === 1) throw fail('upstream_network_error', cookie, 502);
    if (url.endsWith('/oauth/profile')) throw fail('upstream_rejected', 'secret-upstream-message', 502,
      { upstreamStatus: 403, upstreamCode: 'permission_error', responseKind: 'json' });
  });
  const { post, logs } = await helper(t, service);
  const first = await post(body, { 'X-Request-ID': 'request-profile-first' });
  assert.equal(first.status, 502);
  assert.equal((await first.json()).error.stage, 'profile');
  const second = await post(body, { 'X-Request-ID': 'request-profile-retry' });
  assert.equal(second.status, 200);
  const result = await second.json();
  assert.equal(result.auth.access_token, 'private-access');
  assert.equal(result.auth.refresh_token, 'private-refresh');
  assert(result.notes.some(value => value.includes('Credentials were obtained')));
  const third = await post(body, { 'X-Request-ID': 'request-profile-cached' });
  assert.equal(third.status, 200);
  assert.equal(calls.filter(url => url.endsWith('/oauth/token')).length, 1);
  const records = logs.map(value => JSON.parse(value.replace('TK_CREDENTIAL_HELPER ', '')));
  assert.deepEqual(records.map(value => [value.event, value.request_id, value.stage]), [
    ['failure', 'request-profile-first', 'profile'], ['warning', 'request-profile-retry', 'profile'],
  ]);
  assert.equal(records[1].upstream_status, 403); assert.equal(records[1].helper_status, undefined);
  safeOutput(logs);
});
