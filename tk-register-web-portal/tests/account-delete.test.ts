import assert from 'node:assert/strict';
import test from 'node:test';
import { setCSRFToken } from '../src/api.ts';
import { checkAccountDeletion, deletionFinished, deletionInProgress, deletionResult, startAccountDeletion } from '../src/accounts/delete-account.ts';
import { statusLabel, statusTone } from '../src/accounts/helpers.ts';

const pending = { operation_id: '71', status: 'pending' };
const response = (data: unknown, status = 200) => new Response(JSON.stringify({ success: true, data }), { status });

for (const endpoint of ['/accounts', '/admin/synced-accounts'] as const) {
  test(`${endpoint} deletion starts once with a request ID, session and CSRF`, async t => {
    let calls = 0;
    const signal = new AbortController().signal;
    setCSRFToken('test-csrf'); t.after(() => setCSRFToken(''));
    t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
      calls++; assert.equal(url, `/tk-register/api${endpoint}/23/delete`); assert.equal(init.method, 'POST');
      assert.deepEqual(JSON.parse(String(init.body)), { request_id: 'fixture-request' });
      assert.equal(init.credentials, 'same-origin'); assert.equal(init.signal, signal);
      assert.equal((init.headers as Record<string, string>)['X-CSRF-Token'], 'test-csrf');
      return response(pending, 202);
    });
    assert.deepEqual(await startAccountDeletion(endpoint, '23', 'fixture-request', signal), { ...pending, message: undefined, retry_after_seconds: undefined });
    assert.equal(calls, 1);
  });
}

test('polling and lost-response recovery are GET requests and never repeat POST', async t => {
  const paths: string[] = [];
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
    paths.push(url); assert.equal(init.method, 'GET'); assert.equal(init.body, undefined); return response(pending);
  });
  const signal = new AbortController().signal;
  await checkAccountDeletion('/accounts', '23', { operationID: '71', requestID: 'unused' }, signal);
  await checkAccountDeletion('/accounts', '23', { requestID: 'original-request' }, signal);
  assert.deepEqual(paths, ['/tk-register/api/accounts/23/delete?operation_id=71', '/tk-register/api/accounts/23/delete?request_id=original-request']);
});

test('a failed or uncertain start is never automatically replayed', async t => {
  for (const kind of ['network', 'http', 'invalid']) {
    let calls = 0;
    const mock = t.mock.method(globalThis, 'fetch', async () => {
      calls++;
      if (kind === 'network') throw new TypeError('Network failure');
      if (kind === 'http') return new Response(JSON.stringify({ success: false, message: 'Try later.' }), { status: 503 });
      return response({ status: 'completed' });
    });
    await assert.rejects(startAccountDeletion('/accounts', '23', 'same-request', new AbortController().signal));
    assert.equal(calls, 1); mock.mock.restore();
  }
});

test('only validated operation responses may finish deletion', () => {
  for (const invalid of [null, {}, { status: 'completed' }, { operation_id: '', status: 'completed' }, { operation_id: '71', status: 'success' }, { operation_id: 'unsafe/path', status: 'completed' }]) {
    assert.throws(() => deletionResult(invalid));
  }
  assert.throws(() => deletionResult({ ...pending, operation_id: '72' }, '71'));
  assert.equal(deletionFinished(deletionResult(pending)), false);
  assert.equal(deletionFinished(deletionResult({ ...pending, status: 'awaiting_verification' })), false);
  assert.equal(deletionFinished(deletionResult({ ...pending, status: 'upstream_confirmed' })), false);
  assert.equal(deletionFinished(deletionResult({ ...pending, status: 'completed' })), true);
  assert.equal(deletionFinished(deletionResult({ ...pending, status: 'failed' })), true);
});

test('deleted and pending accounts block account actions while failed operations allow explicit retry', () => {
  assert.equal(deletionInProgress({}), false);
  assert.equal(deletionInProgress({ deletion_status: '' }), false);
  assert.equal(deletionInProgress({ deletion_status: 'failed' }), false);
  for (const deletion_status of ['pending', 'awaiting_verification', 'upstream_confirmed', 'completed']) assert.equal(deletionInProgress({ deletion_status }), true);
  assert.equal(deletionInProgress({ deleted_at: '2026-11-04T00:00:00Z' }), true);
});

test('upstream sync_failed remains distinct from unknown and is shown as a failure', () => {
  assert.equal(statusLabel('sync_failed'), 'Upstream sync failed');
  assert.equal(statusTone('sync_failed'), 'negative');
  assert.equal(statusLabel('unknown'), 'Unknown');
  assert.equal(statusTone('unknown'), 'neutral');
});

test('cancellation cannot publish a late successful deletion result', async t => {
  const controller = new AbortController();
  let finish!: (response: Response) => void;
  t.mock.method(globalThis, 'fetch', () => new Promise<Response>(resolve => { finish = resolve; }));
  const operation = startAccountDeletion('/accounts', '23', 'fixture-request', controller.signal);
  controller.abort(); finish(response({ ...pending, status: 'completed' }));
  await assert.rejects(operation, error => error instanceof DOMException && error.name === 'AbortError');
});
