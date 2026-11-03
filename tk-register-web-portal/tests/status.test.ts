import assert from 'node:assert/strict';
import test from 'node:test';
import { executionError, statusLabel, syncVisibleStatuses } from '../src/submissions/status.ts';
import type { Execution, Submission, SubmissionDetail } from '../src/submissions/types.ts';

function submission(id: string, changes: Partial<Submission> = {}): Submission {
  return {
    id, vendor_id: '1', vendor_code: 'tk-register-0', vendor_name: 'TK Register 0',
    protocol_version_id: '1', account: `test-${id}@example.com`, plan: 'test',
    status: 'accepted', remote_id: id, remote_alias: null, remote_status: null,
    platform_account_id: null, parameters: {}, created_at: '', updated_at: '',
    last_checked_at: null, status_checked_at: null, ...changes,
  };
}
function detail(item: Submission): SubmissionDetail {
  return { submission: item, executions: [], metrics: null, bills: null };
}
function execution(id: string, status: Execution['status'], operation: Execution['operation'] = 'metrics'): Execution {
  return {
    id, status, operation, request_id: id, protocol_name: 'Status', protocol_version: 1,
    http_status: null, error_code: null, message: null, result: null,
    created_at: '', finished_at: null,
  };
}

test('status labels distinguish unchecked, no returned status and unaccepted accounts', () => {
  assert.equal(statusLabel(submission('1')), 'Not checked');
  assert.equal(statusLabel(submission('1', { last_checked_at: '2026-10-27T00:00:00Z' })), 'Not checked');
  assert.equal(statusLabel(submission('1', { status_checked_at: '2026-10-27T00:00:00Z' })), 'Status unavailable');
  assert.equal(statusLabel(submission('1'), { error: 'Connection failed.' }), 'Status unavailable');
  assert.equal(statusLabel(submission('1', { remote_status: 'online' }), { error: 'Connection failed.' }), 'online');
  assert.equal(statusLabel(submission('1', { remote_status: 'sync_failed' })), 'Upstream sync failed');
  assert.equal(statusLabel(submission('1', { status: 'unknown' })), 'Awaiting confirmation');
  assert.equal(statusLabel(submission('1', { status: 'running' })), 'Awaiting acceptance');
  assert.equal(statusLabel(submission('1', { status: 'failed' })), 'Unavailable');
});

test('HTTP 200 details still expose the newest failed status execution without confusing billing', () => {
  const result = detail(submission('1', { remote_status: 'online' }));
  result.executions = [execution('9', 'succeeded'), execution('11', 'succeeded', 'bills'), {
    ...execution('10', 'failed'), message: 'The status endpoint is unavailable.',
  }];
  assert.equal(executionError(result), 'The status endpoint is unavailable.');
  assert.equal(executionError(result, 'bills'), '');
  result.executions.push(execution('12', 'succeeded'));
  assert.equal(executionError(result), '');
});

test('visible status pass queries only accepted accounts with IDs, at most two concurrently', async () => {
  const abort = new AbortController();
  const items = [submission('1'), submission('2'), submission('3'), submission('4', { status: 'unknown' }), submission('5', { remote_id: null })];
  let running = 0, peak = 0;
  const calls: string[] = [], results: string[] = [];
  await syncVisibleStatuses(items, {
    signal: abort.signal, force: true,
    fetch: async (id, force, signal) => {
      assert.equal(force, true);
      assert.equal(signal, abort.signal);
      calls.push(id);
      peak = Math.max(peak, ++running);
      await new Promise((resolve) => setTimeout(resolve, 5));
      running--;
      return detail(submission(id, { remote_status: 'online' }));
    },
    onChecking: () => {},
    onResult: (id) => results.push(id),
    onError: () => assert.fail('unexpected failure'),
  });
  assert.equal(peak, 2);
  assert.deepEqual(calls, ['1', '2', '3']);
  assert.deepEqual(results.sort(), ['1', '2', '3']);
});

test('changing pages cancels queued work and ignores results from the previous page', async () => {
  const abort = new AbortController();
  const calls: string[] = [];
  const resolve: Array<(value: SubmissionDetail) => void> = [];
  const task = syncVisibleStatuses([submission('1'), submission('2'), submission('3')], {
    signal: abort.signal, force: false,
    fetch: (id, force) => {
      assert.equal(force, false);
      calls.push(id);
      return new Promise((done) => resolve.push(done));
    },
    onChecking: () => {},
    onResult: () => assert.fail('stale result applied'),
    onError: () => assert.fail('stale error applied'),
  });
  abort.abort();
  resolve.forEach((done, i) => done(detail(submission(String(i + 1)))));
  await task;
  assert.deepEqual(calls, ['1', '2']);
});

test('one account query failure does not hide the other visible accounts', async () => {
  const results: string[] = [], errors: string[] = [];
  await syncVisibleStatuses([submission('1'), submission('2'), submission('3')], {
    signal: new AbortController().signal, force: false,
    fetch: async (id) => {
      if (id === '2') throw new Error('Status temporarily unavailable.');
      return detail(submission(id));
    },
    onChecking: () => {},
    onResult: (id) => results.push(id),
    onError: (id) => errors.push(id),
  });
  assert.deepEqual(results.sort(), ['1', '3']);
  assert.deepEqual(errors, ['2']);
});
