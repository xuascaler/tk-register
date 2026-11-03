import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError } from '../src/api.ts';
import { authJSONResultMessage, authJSONUpdateError, type AuthJSONUpdateResult } from '../src/accounts/auth-json.ts';
import { parseAuthJSON } from '../src/submissions/validation.ts';

const valid = {
  id_token: '', access_token: 'not-a-real-token', refresh_token: 'not-a-real-token',
  last_refresh: '2026-11-01T02:00:00+08:00', email: 'user@example.com',
  account_uuid: 'account', organization_uuid: 'organization', organization_name: 'Example',
  claude_device_ids: ['device'], type: 'claude', expired: '2026-12-01T02:00:00+08:00',
  proxy_url: 'socks5h://proxy.example.com:1080', verification_status: 'verified',
};

test('credential editor uses the submission validator for objects and encoded JSON strings', () => {
  assert.deepEqual(parseAuthJSON(JSON.stringify(valid)), valid);
  assert.deepEqual(parseAuthJSON(JSON.stringify(JSON.stringify(valid))), valid);
  assert.throws(() => parseAuthJSON(JSON.stringify({ ...valid, claude_device_ids: [] })), /device ID/);
  assert.throws(() => parseAuthJSON(JSON.stringify({ ...valid, expired: '2026-12-01' })), /RFC 3339/);
  assert.throws(() => parseAuthJSON(JSON.stringify({ ...valid, proxy_url: 'file:///tmp/auth' })), /proxy_url/);
});

test('network loss and unconfirmed update cannot be labelled as a definitive failed mutation', () => {
  for (const error of [new APIError('offline', 0), new APIError('timeout', 504), new APIError('body invalid', 200), new APIError('safe error', 502, 'AUTH_JSON_UPDATE_UNCONFIRMED')]) {
    const result = authJSONUpdateError(error);
    assert.equal(result.uncertain, true);
    assert.match(result.message, /could not confirm/);
    assert.match(result.message, /input is retained/);
  }
});

test('known validation and eligibility rejections retain their actionable safe explanation', () => {
  const error = new APIError('This account is active.', 409, 'AUTH_JSON_NOT_EDITABLE');
  assert.deepEqual(authJSONUpdateError(error), { uncertain: false, message: 'This account is active.' });
});

test('a login cooldown before a credential write is a known rejection rather than an uncertain update', () => {
  const message = 'Supplier login is limited. Try again in 284 seconds.';
  const error = new APIError(message, 429, 'SUPPLIER_RATE_LIMITED', 284, 'login');
  assert.deepEqual(authJSONUpdateError(error), { uncertain: false, message });
});

test('refresh failure after an accepted update preserves the credential success in the message', () => {
  const result = { updated: true, refresh_status: 'failed', refresh_message: 'The supplier could not be reached.' } as AuthJSONUpdateResult;
  assert.match(authJSONResultMessage(result), /^Auth JSON updated, but account refresh failed\./);
  assert.match(authJSONResultMessage(result), /supplier could not be reached/);
  assert.equal(authJSONResultMessage({ ...result, refresh_status: 'succeeded' }), 'Auth JSON updated. Account status and usage refreshed.');
  const completeMessage = 'Auth JSON updated, but account refresh failed. Your last successful data is retained. Try syncing again.';
  assert.equal(authJSONResultMessage({ ...result, refresh_message: completeMessage }), completeMessage);
});
