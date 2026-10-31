import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError, request, setCSRFToken } from '../src/api.ts';

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
