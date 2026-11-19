import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError } from '../src/api';
import { generationResultUncertain, readGeneratedAuthJSON } from '../src/accounts/deactivate';

const auth = { id_token: '', access_token: 'test-access', refresh_token: 'test-refresh', last_refresh: '2026-11-20T01:00:00Z', email: 'owner@example.com', account_uuid: 'account-uuid', organization_uuid: 'organization-uuid', organization_name: 'Example', claude_device_ids: ['device-uuid'], type: 'claude', expired: '2026-11-20T11:00:00Z', proxy_url: 'http://proxy.example.com:8080', verification_status: 'verified' };
const result = { flow_id: 'generated-flow-123456', status: 'exported', auth, expires_at: '2026-11-20T02:00:00Z', generated_at: '2026-11-20T01:30:00Z', audit_id: '42', notes: [], proxy_check: { ok: true, ip: '203.0.113.24', checked_at: '2026-11-20T01:30:00Z', elapsed_ms: 9 } };

test('generated Auth JSON requires the bound flow and complete identity', () => {
  const value = readGeneratedAuthJSON(result, result.flow_id, ' OWNER@example.com ');
  assert.deepEqual(value.auth, auth);
  assert.notEqual(value.auth, auth);
  assert.throws(() => readGeneratedAuthJSON(result, 'another-flow', auth.email), /could not be confirmed/);
  assert.throws(() => readGeneratedAuthJSON(result, result.flow_id, 'other@example.com'), /does not match/);
});

test('generation responses cannot bypass full credentials or proxy result checks', () => {
  for (const value of [null, { ...result, status: 'ready' }, { ...result, generated_at: 'invalid' }, { ...result, generated_at: 1 }, { ...result, expires_at: null }, { ...result, auth: [] }, { ...result, auth: { ...auth, refresh_token: '' } }, { ...result, auth: { ...auth, organization_name: '' } }, { ...result, proxy_check: { ...result.proxy_check, ok: false } }]) {
    assert.throws(() => readGeneratedAuthJSON(value, result.flow_id, auth.email));
  }
});

test('fresh generation responses retain the verified flow proxy and exit IP', () => {
  const expected = { ip: result.proxy_check.ip, url: auth.proxy_url };
  assert.equal(readGeneratedAuthJSON(result, result.flow_id, auth.email, expected).auth.proxy_url, expected.url);
  assert.throws(() => readGeneratedAuthJSON({ ...result, proxy_check: { ...result.proxy_check, ip: '203.0.113.25' } }, result.flow_id, auth.email, expected), /do not match/);
  assert.throws(() => readGeneratedAuthJSON({ ...result, auth: { ...auth, proxy_url: 'http://another.example.com:8080' } }, result.flow_id, auth.email, expected), /do not match/);
  // A scoped saved-result read has no previous browser proxy state. The server
  // remains authoritative for that read, while identity/full JSON still apply.
  assert.equal(readGeneratedAuthJSON(result, result.flow_id, auth.email).status, 'exported');
});

test('unconfirmed generation responses preserve the draft instead of repeating authorization', () => {
  assert.equal(generationResultUncertain(new APIError('lost response', 0, '')), true);
  assert.equal(generationResultUncertain(new APIError('lost response', 504, 'GATEWAY_TIMEOUT')), true);
  assert.equal(generationResultUncertain(new APIError('bad payload', 200, 'CREDENTIAL_GENERATION_UNCONFIRMED')), true);
  assert.equal(generationResultUncertain(new APIError('wrong account', 400, 'AUTH_JSON_EMAIL_MISMATCH')), false);
  assert.equal(generationResultUncertain(new APIError('expired', 404, 'CREDENTIAL_FLOW_NOT_FOUND')), false);
});
