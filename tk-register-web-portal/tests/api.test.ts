import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError, request, retryAfterSeconds, setCSRFToken } from '../src/api.ts';

test('authenticated mutations send same-origin cookies and an in-memory CSRF token', async () => {
  const original = globalThis.fetch;
  let init: RequestInit | undefined;
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/tk-register/api/admin/settings');
    init = options;
    return new Response(
      JSON.stringify({ success: true, data: { settings: [] } }),
      { status: 200 },
    );
  };
  try {
    setCSRFToken('test-csrf');
    await request('/admin/settings', {
      method: 'PUT',
      body: { settings: { registration_enabled: 'false' } },
    });
    assert.equal(init?.credentials, 'same-origin');
    assert.equal(init?.cache, 'no-store');
    assert.equal(
      (init?.headers as Record<string, string>)['X-CSRF-Token'],
      'test-csrf',
    );
    assert.deepEqual(JSON.parse(String(init?.body)), {
      settings: { registration_enabled: 'false' },
    });
  } finally {
    globalThis.fetch = original;
    setCSRFToken('');
  }
});

test('supplier cooldown errors retain the operation stage and bounded retry duration', async t => {
  t.mock.method(globalThis, 'fetch', async () => new Response(JSON.stringify({
    success: false, code: 'SUPPLIER_RATE_LIMITED', message: 'Account queries are limited. Try again in 20 seconds.',
    retry_after: 20, retry_stage: 'accounts',
  }), { status: 429, headers: { 'Retry-After': '-1' } }));
  await assert.rejects(request('/accounts/7/sync', { method: 'POST', body: {} }), error =>
    error instanceof APIError && error.retryAfter === 20 && error.retryStage === 'accounts');
  for (const value of [undefined, null, '', 'Infinity', '1e3', '0.5', -1, 604801, true, [], {}, NaN]) {
    assert.equal(retryAfterSeconds(value), 0);
  }
  assert.equal(retryAfterSeconds('284'), 284);
  assert.equal(retryAfterSeconds(604800), 604800);
});
test('rate limits expose server retry duration and invalid responses are actionable', async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      new Response(
        JSON.stringify({
          success: false,
          message: 'Wait before requesting another code.',
          code: 'RATE_LIMITED',
        }),
        { status: 429, headers: { 'Retry-After': '42' } },
      );
    await assert.rejects(
      request('/auth/code', { method: 'POST', body: {} }),
      (error) =>
        error instanceof APIError &&
        error.retryAfter === 42 &&
        error.code === 'RATE_LIMITED',
    );
    globalThis.fetch = async () =>
      new Response('<html>unavailable</html>', { status: 502 });
    await assert.rejects(
      request('/auth/status'),
      (error) => error instanceof APIError && !error.message.includes('<html>'),
    );
  } finally {
    globalThis.fetch = original;
  }
});
