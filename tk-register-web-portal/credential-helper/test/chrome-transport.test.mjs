import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import { createChromeTransport } from '../src/chrome-transport.mjs';
import { diagnosticContext, logDiagnostic } from '../src/diagnostics.mjs';
import { publicError } from '../src/errors.mjs';

const proxy = 'http://demo:private-password@proxy.example:8080';
const key = 'sk-ant-sid-private-test';
const url = 'https://claude.ai/api/organizations';
function harness(response = { ok: true, data: [{ uuid: 'org-test' }] }, { hang = false } = {}) {
  const calls = []; let killed = 0;
  const transport = createChromeTransport({
    executable: () => '/test/tk-credential-request',
    resolve: async value => { assert.equal(value, proxy); return { address: '8.8.8.8', username: 'demo', password: 'private-password', hostname: 'proxy.example' }; },
    spawn: (command, args, options) => {
      const child = new EventEmitter();
      child.stdin = new PassThrough(); child.stdout = new PassThrough();
      child.kill = () => { killed++; return true; };
      let input = '';
      child.stdin.on('data', chunk => { input += chunk; });
      child.stdin.on('finish', () => {
        calls.push({ command, args, options, input: JSON.parse(input) });
        if (!hang) queueMicrotask(() => { child.stdout.emit('data', Buffer.from(JSON.stringify(response))); child.emit('close', 0); });
      });
      return child;
    },
  });
  return { transport, calls, killed: () => killed };
}

test('Chrome subprocess gets pinned proxy and secrets on stdin only', async () => {
  const h = harness();
  assert.deepEqual(await h.transport(url, { proxy_url: proxy, session_key: key }), [{ uuid: 'org-test' }]);
  const call = h.calls[0];
  assert.equal(call.command, '/test/tk-credential-request'); assert.deepEqual(call.args, []);
  assert.deepEqual(call.options.stdio, ['pipe', 'pipe', 'ignore']);
  assert.equal(call.input.proxy_address, '8.8.8.8'); assert.equal(call.input.proxy_url, proxy);
  assert.equal(call.input.session_key, key); assert.equal(call.input.operation, 'organizations');
  assert(call.input.timeout_ms > 0 && call.input.timeout_ms <= 25000);
});

test('Chrome route mapping allows only the three fixed Cookie authorization endpoints', async () => {
  const h = harness();
  await h.transport('https://claude.ai/v1/oauth/org-test/authorize', { method: 'POST', proxy_url: proxy, session_key: key, body: { organization_uuid: 'org-test' } });
  await h.transport('https://platform.claude.com/v1/oauth/token', { method: 'POST', proxy_url: proxy, body: { code: 'private-code' } });
  assert.equal(h.calls[0].input.operation, 'authorize'); assert.equal(h.calls[0].input.organization_id, 'org-test');
  assert.equal(h.calls[1].input.operation, 'token'); assert.equal(h.calls[1].input.session_key, undefined);
  for (const target of ['http://claude.ai/api/organizations', 'https://evil.example/api/organizations', url + '?redirect=evil', 'https://claude.ai/api/profile', 'https://user@claude.ai/api/organizations']) {
    await assert.rejects(h.transport(target, { proxy_url: proxy, session_key: key }), { code: 'invalid_request' });
  }
  assert.equal(h.calls.length, 2);
});

test('Chrome proxy validation fails before any child or network fallback', async () => {
  let called = false;
  const transport = createChromeTransport({ resolve: async () => { throw Object.assign(new Error('blocked'), { code: 'proxy_forbidden', status: 400 }); }, spawn: () => { called = true; } });
  await assert.rejects(transport(url, { proxy_url: proxy, session_key: key }), { code: 'proxy_forbidden' });
  assert.equal(called, false);
});

test('Chrome error metadata is retained without exposing upstream or child details', async () => {
  const h = harness({ ok: false, error: { code: 'upstream_rejected', status: 502, upstream_status: 403, response_kind: 'html', message: key + proxy, upstream_code: key } });
  await assert.rejects(h.transport(url, { proxy_url: proxy, session_key: key }), error => {
    assert.equal(error.code, 'upstream_rejected'); assert.equal(error.upstream_status, 403); assert.equal(error.response_kind, 'html');
    assert(!JSON.stringify(error).includes(key)); assert(!error.message.includes('private')); return true;
  });
});

test('session freshness rejection survives IPC with fixed public guidance and no retry', async () => {
  const h = harness({ ok: false, error: { code: 'upstream_session_not_fresh', status: 502,
    upstream_status: 403, upstream_code: 'permission_error', response_kind: 'json' },
    diagnostic: { message: key + proxy } });
  await assert.rejects(h.transport('https://claude.ai/v1/oauth/org-test/authorize', {
    method: 'POST', proxy_url: proxy, session_key: key, body: { organization_uuid: 'org-test' },
  }), error => {
    const result = publicError(error);
    assert.equal(result.status, 502);
    assert.equal(result.body.error.code, 'upstream_session_not_fresh');
    assert.equal(result.body.error.upstream_status, 403);
    assert.equal(result.body.error.message, 'Session is not fresh enough to grant elevated access. Sign in again to continue.');
    assert(!JSON.stringify(result).includes(key)); assert(!JSON.stringify(result).includes(proxy));
    return true;
  });
  assert.equal(h.calls.length, 1);
});

test('provider rejection reason is logged with request secrets redacted and never returned publicly', async () => {
  const logs = [];
  const reason = 'OAuth is not enabled for this organization.';
  const secret = 'private-verifier-value';
  const h = harness({ ok: false, error: { code: 'upstream_rejected', status: 502, upstream_status: 403,
    upstream_code: 'permission_error', response_kind: 'json' }, diagnostic: {
      message: `${reason} ${key} ${proxy} private-password ${secret} org-private user@example.com https://unknown.example/token?x=private Bearer sk-secret-example\n\u001b injected`
    } });
  await diagnosticContext({ 'x-request-id': 'test-rejection-reason', 'x-tk-credential-method': 'cookie' }, 'import', line => logs.push(line), async () => {
    await assert.rejects(h.transport('https://claude.ai/v1/oauth/org-private/authorize', {
      method: 'POST', proxy_url: proxy, session_key: key,
      body: { organization_uuid: 'org-private', code_verifier: secret },
    }), error => {
      const publicResult = JSON.stringify(publicError(error));
      assert(!publicResult.includes(reason));
      logDiagnostic('failure', error);
      return error.code === 'upstream_rejected';
    });
  });
  const record = JSON.parse(logs[0].replace('TK_CREDENTIAL_HELPER ', ''));
  assert.equal(record.request_id, 'test-rejection-reason');
  assert(record.upstream_message.startsWith(reason));
  for (const value of [key, proxy, 'private-password', secret, 'org-private', 'user@example.com', 'unknown.example', 'sk-secret-example', '\n', '\u001b']) {
    assert(!record.upstream_message.includes(value), `Diagnostic leaked ${value}`);
  }
});

test('Chrome timeout kills the single process and never retries', async () => {
  const h = harness({}, { hang: true });
  await assert.rejects(h.transport(url, { proxy_url: proxy, session_key: key, timeout: 20 }), { code: 'upstream_timeout' });
  assert.equal(h.killed(), 1); assert.equal(h.calls.length, 1);
});

test('missing Chrome binary and malformed output fail closed', async () => {
  let calls = 0;
  const transport = createChromeTransport({ resolve: async () => ({ address: '8.8.8.8' }), spawn: () => { calls++; throw new Error(proxy + key); } });
  await assert.rejects(transport(url, { proxy_url: proxy, session_key: key }), { code: 'internal_error' });
  assert.equal(calls, 1);
  await assert.rejects(harness({ ok: false, error: { code: key, status: 502 } }).transport(url, { proxy_url: proxy, session_key: key }), { code: 'upstream_invalid_response' });
});
