import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError, request } from '../src/api.ts';
import { authorizationErrorCode, authorizationStageLabel, readAuthorizationDiagnostics, safeRequestID } from '../src/authorization-diagnostics.ts';

test('authorization errors retain the actual upstream status and server request ID', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response(JSON.stringify({
    success: false, code: 'AUTHORIZATION_REJECTED', message: 'Claude rejected the organization lookup request.',
    data: { diagnostics: { request_id: 'body-request-123', stage: 'cookie_organizations', upstream_status: 403, upstream_code: 'permission_error', response_kind: 'json' } },
  }), { status: 502, headers: { 'X-Request-ID': 'server-request-456' } }));
  await assert.rejects(request('/credential-flows/import', { method: 'POST', body: {} }), cause => {
    assert(cause instanceof APIError);
    assert.equal(cause.status, 502);
    assert.equal(cause.code, 'AUTHORIZATION_REJECTED');
    assert.equal(cause.message, 'Claude rejected the organization lookup request.');
    assert.equal(cause.requestId, 'server-request-456');
    assert.deepEqual(cause.diagnostics, { request_id: 'server-request-456', stage: 'cookie_organizations', upstream_status: 403, upstream_code: 'permission_error', response_kind: 'json' });
    return true;
  });
});

test('helper HTTP status is never substituted for a missing upstream response', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response(JSON.stringify({
    success: false, code: 'AUTHORIZATION_CONNECTION_FAILED', message: 'The authorization provider could not be reached.',
    data: { diagnostics: { request_id: 'body-request-123', stage: 'cookie_authorize', helper_status: 502 } },
  }), { status: 502 }));
  await assert.rejects(request('/accounts/7/auth-json/flows/import', { method: 'POST', body: {} }), cause => {
    assert(cause instanceof APIError);
    assert.equal(cause.requestId, 'body-request-123');
    assert.deepEqual(cause.diagnostics, { request_id: 'body-request-123', stage: 'cookie_authorize' });
    return true;
  });
});

test('request IDs survive a non-JSON failure without exposing its body', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response('<html>private-response-body</html>', { status: 502, headers: { 'X-Request-ID': 'gateway-request-123' } }));
  await assert.rejects(request('/accounts/7/deactivate/flows/import', { method: 'POST', body: {} }), cause => {
    assert(cause instanceof APIError);
    assert.equal(cause.requestId, 'gateway-request-123');
    assert.deepEqual(cause.diagnostics, { request_id: 'gateway-request-123' });
    assert(!cause.message.includes('private-response-body'));
    return true;
  });
});

test('only bounded allowlisted diagnostic fields can reach the UI', () => {
  const unsafe = 'sk-ant-sid-private-credential';
  assert.deepEqual(readAuthorizationDiagnostics({
    request_id: 'safe-id', stage: 'cookie_authorize', upstream_status: 401,
    upstream_code: 'authentication_error', response_kind: 'json',
    message: unsafe, token: unsafe, url: 'https://private.example', helper_status: 502,
  }), { request_id: 'safe-id', stage: 'cookie_authorize', upstream_status: 401, upstream_code: 'authentication_error', response_kind: 'json' });
  for (const value of [null, undefined, [], '', 3, true, { stage: unsafe, upstream_status: '403', upstream_code: unsafe, response_kind: unsafe, request_id: 'credential@example.com' }, Object.create({ stage: 'cookie_organizations' })]) assert.equal(readAuthorizationDiagnostics(value), undefined);
  for (const status of [0, 99, 600, NaN, Infinity, -1, 403.5, '403']) assert.equal(readAuthorizationDiagnostics({ upstream_status: status }), undefined);
  for (const requestID of ['', 'a'.repeat(129), 'private@example.com', 'https://example.com', 'private id', 'id\nprivate', '<script>']) assert.equal(safeRequestID(requestID), undefined);
  assert.deepEqual(readAuthorizationDiagnostics({ request_id: 'body-id' }, 'invalid header'), { request_id: 'body-id' });
  assert.equal(authorizationStageLabel('cookie_organizations'), 'Read Claude organizations');
  assert.equal(authorizationErrorCode('AUTHORIZATION_REJECTED'), 'AUTHORIZATION_REJECTED');
  assert.equal(authorizationErrorCode('PRIVATE_SECRET_TOKEN'), undefined);
});

test('APIError preserves existing retry fields while sanitizing added diagnostics', () => {
  const error = new APIError('Existing message', 429, 'RATE_LIMITED', 42, 'accounts', { request_id: 'safe-id', upstream_status: 403 });
  assert.equal(error.retryAfter, 42); assert.equal(error.retryStage, 'accounts');
  assert.equal(error.requestId, 'safe-id'); assert.equal(error.diagnostics?.upstream_status, 403);
  const previous = new APIError('Existing message', 502, 'AUTHORIZATION_FAILED');
  assert.equal(previous.requestId, ''); assert.equal(previous.diagnostics, undefined);
});

test('an unexpected HTTP 200 response also retains its request ID', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response('<html>private-body</html>', { status: 200, headers: { 'X-Request-ID': 'unexpected-response-123' } }));
  await assert.rejects(request('/credential-flows/import', { method: 'POST', body: {} }), cause => {
    assert(cause instanceof APIError);
    assert.equal(cause.status, 200);
    assert.equal(cause.requestId, 'unexpected-response-123');
    assert.deepEqual(cause.diagnostics, { request_id: 'unexpected-response-123' });
    assert(!cause.message.includes('private-body'));
    return true;
  });
});
