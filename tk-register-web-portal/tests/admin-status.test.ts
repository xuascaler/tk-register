import assert from 'node:assert/strict';
import test from 'node:test';
import { syncCurrentStatuses } from '../src/admin/status.ts';
import type { AdminDetail } from '../src/admin/types.ts';
import type { StatusRefresh } from '../src/submissions/status.ts';
import type { Submission } from '../src/submissions/types.ts';

function submission(id: string, changes: Partial<Submission> = {}): Submission {
  return {
    id, vendor_id: '1', vendor_code: 'tk-register-0', vendor_name: 'TK Register 0',
    protocol_version_id: '1', account: `account-${id}@example.com`, plan: 'test',
    status: 'accepted', remote_id: id, remote_alias: null, remote_status: 'online',
    platform_account_id: null, parameters: {}, created_at: '', updated_at: '',
    last_checked_at: null, status_checked_at: null, ...changes,
  };
}
function response(item: Submission) {
  const data: AdminDetail = {
    submission: item, current_submission: item, user: { id: '2', email: 'owner@example.com' },
    account_id: '1', executions: [], metrics: null, bills: null,
  };
  return new Response(JSON.stringify({ success: true, data }), { status: 200 });
}

test('admin status refresh deduplicates current account IDs and sends force through the admin endpoint', async t => {
  const signal = new AbortController().signal;
  const calls: string[] = [], results: Submission[] = [];
  const states: Array<[string, StatusRefresh]> = [];
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
    calls.push(url);
    assert.equal(init.method, 'POST');
    assert.equal(init.signal, signal);
    assert.equal(init.credentials, 'same-origin');
    assert.deepEqual(JSON.parse(String(init.body)), { force: true });
    // A successful query without a matching remote account must clear a prior known status.
    return response(submission('10', { remote_status: null, status_checked_at: '2026-10-30T00:00:00Z' }));
  });
  await syncCurrentStatuses([
    submission('10'), submission('10'), null,
    submission('11', { status: 'failed' }), submission('12', { remote_id: null }),
  ], signal, true,
  (id, state) => states.push([id, state]),
  (_id, result) => results.push(result));

  assert.deepEqual(calls, ['/tk-register/api/admin/submissions/10/status']);
  assert.equal(results.length, 1);
  assert.equal(results[0].remote_status, null);
  assert.equal(results[0].status_checked_at, '2026-10-30T00:00:00Z');
  assert.deepEqual(states, [['10', { checking: true }], ['10', { error: '' }]]);
});

test('leaving an admin page aborts queued work and never applies stale current account results', async t => {
  const abort = new AbortController();
  const calls: string[] = [], results: string[] = [];
  const states: Array<[string, StatusRefresh]> = [];
  const pending: Array<{ id: string; done: (value: Response) => void }> = [];
  t.mock.method(globalThis, 'fetch', (url: string, init: RequestInit) => {
    const id = url.split('/').at(-2)!;
    calls.push(id);
    assert.equal(init.signal, abort.signal);
    assert.deepEqual(JSON.parse(String(init.body)), { force: false });
    return new Promise<Response>(done => pending.push({ id, done }));
  });
  const pass = syncCurrentStatuses([submission('1'), submission('1'), submission('2'), submission('3')], abort.signal, false,
    (id, state) => states.push([id, state]),
    id => results.push(id));
  abort.abort();
  // Even if an already in-flight transport returns after cancellation, the result is ignored.
  pending.forEach(({ id, done }) => done(response(submission(id))));
  await pass;
  assert.deepEqual(calls, ['1', '2']);
  assert.deepEqual(results, []);
  assert.deepEqual(states, [['1', { checking: true }], ['2', { checking: true }]]);
});

test('a rejected admin status request reports its error while other current accounts still update', async t => {
  const results: string[] = [];
  const states: Record<string, StatusRefresh> = {};
  t.mock.method(globalThis, 'fetch', async (url: string) => {
    const id = url.split('/').at(-2)!;
    if (id === '2') return new Response(JSON.stringify({ success: false, message: 'Vendor temporarily unavailable.' }), { status: 502 });
    return response(submission(id));
  });
  await syncCurrentStatuses([submission('1'), submission('2'), submission('2'), submission('3')], new AbortController().signal, false,
    (id, state) => { states[id] = state; },
    id => results.push(id));
  assert.deepEqual(results.sort(), ['1', '3']);
  assert.deepEqual(states, {
    '1': { error: '' }, '2': { error: 'Vendor temporarily unavailable.' }, '3': { error: '' },
  });
});
