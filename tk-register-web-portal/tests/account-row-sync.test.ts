import assert from 'node:assert/strict';
import test from 'node:test';
import { setCSRFToken } from '../src/api.ts';
import { loadAccountPage, syncAccountRow, mergeSyncedAccount, type AccountListEndpoint } from '../src/accounts/row-sync.ts';
import type { AccountPage, SyncedAccount } from '../src/accounts/types.ts';

const savedAt = '2026-11-01T00:00:00Z';
const collectedAt = '2026-11-02T00:00:00Z';
function account(id: string, at = savedAt): SyncedAccount {
  return {
    id, account: `${id}@example.com`, plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0',
    current_submission_id: id, latest_submission_id: id, submission_status: 'accepted',
    remote_id: `remote-${id}`, platform_account_id: `platform-${id}`,
    last_success_at: at, last_attempt_at: at, last_error: '',
    snapshot: { id: `snapshot-${id}`, account_id: id, snapshot_date: at.slice(0, 10), collected_at: at,
      source_updated_at: null, account_status: 'active', input_tokens_total: 20, output_tokens_total: 30,
      cost_total: 12.5, currency: 'CNY', cost_total_usd: 12.5 / 7.2, exchange_rate_cny_per_usd: 7.2, usage_5h_percent: 25, reset_5h_at: null,
      usage_7d_percent: 50, reset_7d_at: null, data_source: 'website' },
  };
}
function page(): AccountPage { return { items: [account('7'), account('8')], total: 35, page: 2, page_size: 20 }; }
function response(data: unknown, status = 200, headers = {}) {
  return new Response(JSON.stringify(status === 200 ? { success: true, data } : { success: false, message: String(data) }), { status, headers });
}
function success(updated: SyncedAccount) {
  return { accounts: [updated], results: [{ account_id: updated.id, status: 'updated' }], updated_count: 1, failed_count: 0, skipped_count: 0 };
}

for (const endpoint of ['/accounts', '/admin/synced-accounts'] as AccountListEndpoint[]) {
  test(`${endpoint} page loads and filtered reads never trigger supplier synchronization`, async t => {
    const calls: string[] = [];
    t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
      calls.push(url); assert.equal(init.method, 'GET'); return response(page());
    });
    for (const params of [new URLSearchParams(), new URLSearchParams({ page: '2', search: 'example.com', status: 'active' })]) {
      const result = await loadAccountPage(endpoint, params, new AbortController().signal);
      assert.equal(result.items.length, 2);
    }
    assert.equal(calls.length, 2);
    assert.ok(calls.every(url => url.startsWith(`/tk-register/api${endpoint}?`)));
  });

  test(`${endpoint} manual sync sends only the clicked local ID with session and CSRF`, async t => {
    const controller = new AbortController(); const current = page(); const updated = account('7', collectedAt);
    updated.snapshot!.account_status = 'banned'; updated.snapshot!.cost_total = 72; updated.snapshot!.cost_total_usd = 10; let calls = 0;
    setCSRFToken('test-csrf'); t.after(() => setCSRFToken(''));
    t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
      calls++; assert.equal(url, `/tk-register/api${endpoint}/sync`); assert.equal(init.method, 'POST');
      assert.deepEqual(JSON.parse(String(init.body)), { account_ids: ['7'] });
      assert.equal(init.credentials, 'same-origin'); assert.equal(init.signal, controller.signal);
      assert.equal((init.headers as Record<string, string>)['X-CSRF-Token'], 'test-csrf');
      return response(success(updated));
    });
    const result = await syncAccountRow(endpoint, current.items[0], controller.signal);
    assert.equal(result.status, 'updated');
    const merged = mergeSyncedAccount(current, result);
    assert.deepEqual(merged.items.map(row => row.id), ['7', '8']);
    assert.equal(merged.items[0].last_success_at, collectedAt);
    assert.equal(merged.items[0].snapshot?.account_status, 'banned');
    assert.equal(merged.items[0].snapshot?.cost_total_usd, 10);
    assert.equal(merged.items[0].snapshot?.cost_total, 72);
    assert.equal(merged.items[0].snapshot?.exchange_rate_cny_per_usd, 7.2);
    assert.equal(merged.items[1], current.items[1]);
    assert.equal(merged.total, 35); assert.equal(merged.page, 2); assert.equal(calls, 1);
  });
}

test('HTTP 200 supplier failure retains last successful amount and timestamp', async t => {
  const saved = account('7');
  const failed = { ...saved, last_attempt_at: collectedAt, last_error: 'The supplier website is limiting requests.' };
  t.mock.method(globalThis, 'fetch', async () => response({ accounts: [failed], results: [{ account_id: '7', status: 'failed', message: failed.last_error }], updated_count: 0, failed_count: 1, skipped_count: 0 }));
  const result = await syncAccountRow('/accounts', saved, new AbortController().signal);
  assert.equal(result.status, 'failed'); assert.match(result.message || '', /limiting requests/);
  assert.equal(result.account.last_success_at, savedAt);
  assert.deepEqual(result.account.snapshot, saved.snapshot);
});

test('malformed or mismatched successful responses cannot replace the clicked account', async t => {
  const saved = account('7');
  for (const body of [{}, success(account('8', collectedAt)), { accounts: [], results: [{ account_id: '7', status: 'updated' }] }]) {
    const mock = t.mock.method(globalThis, 'fetch', async () => response(body));
    const result = await syncAccountRow('/accounts', saved, new AbortController().signal);
    assert.equal(result.status, 'failed'); assert.equal(result.account, saved);
    mock.mock.restore();
  }
});

test('request rejection and transport failure preserve saved data without automatic retry', async t => {
  const saved = account('7');
  for (const status of [409, 429, 503, 0]) {
    let calls = 0;
    const mock = t.mock.method(globalThis, 'fetch', async () => {
      calls++; if (!status) throw new TypeError('Network error');
      return response('Try again shortly.', status, status === 429 ? { 'Retry-After': '45' } : {});
    });
    const result = await syncAccountRow('/accounts', saved, new AbortController().signal);
    assert.equal(result.status, 'failed'); assert.equal(result.account, saved); assert.equal(calls, 1);
    if (status === 429) assert.equal(result.retryAfter, 45);
    mock.mock.restore();
  }
});

test('navigation cancellation suppresses a late supplier response', async t => {
  const controller = new AbortController();
  let finish!: (value: Response) => void;
  t.mock.method(globalThis, 'fetch', () => new Promise<Response>(resolve => { finish = resolve; }));
  const pending = syncAccountRow('/accounts', account('7'), controller.signal);
  controller.abort(); finish(response(success(account('7', collectedAt))));
  await assert.rejects(pending, error => error instanceof DOMException && error.name === 'AbortError');
});

test('a sync result cannot insert an account into a different page', () => {
  const current = page();
  const merged = mergeSyncedAccount(current, { account: account('99', collectedAt), status: 'updated' });
  assert.deepEqual(merged, current);
});
