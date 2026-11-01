import assert from 'node:assert/strict';
import test from 'node:test';
import { setCSRFToken } from '../src/api.ts';
import { loadAndSyncAccountPage, mergePageSync, pageSyncSummary, type AccountListEndpoint, type PageSyncResponse, type SyncedAccountPage } from '../src/accounts/page-sync.ts';
import type { AccountPage, AccountSnapshot, SyncedAccount } from '../src/accounts/types.ts';

const savedAt = '2026-11-01T00:00:00Z';
const collectedAt = '2026-11-02T00:00:00Z';
function account(id: string, status = 'active', at = savedAt): SyncedAccount {
  const snapshot: AccountSnapshot = {
    id: `snapshot-${id}-${at}`, account_id: id, snapshot_date: at.slice(0, 10), collected_at: at,
    source_updated_at: null, account_status: status, input_tokens_total: 20, output_tokens_total: 30,
    cost_total: 12.5, currency: 'CNY', usage_5h_percent: 25, reset_5h_at: null,
    usage_7d_percent: 50, reset_7d_at: null, data_source: 'website',
  };
  return {
    id, account: `${id}@example.com`, plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0',
    current_submission_id: id, latest_submission_id: id, submission_status: 'accepted',
    remote_id: `remote-${id}`, platform_account_id: `platform-${id}`,
    last_success_at: at, last_attempt_at: at, last_error: '', snapshot,
  };
}
function page(items: SyncedAccount[], changes: Partial<AccountPage> = {}): AccountPage {
  return { items, total: items.length, page: 1, page_size: 20, ...changes };
}
function response(data: unknown, status = 200) {
  return new Response(JSON.stringify(status === 200 ? { success: true, data } : { success: false, message: String(data) }), { status });
}
function batch(accounts: SyncedAccount[]): PageSyncResponse {
  return { accounts, results: accounts.map(item => ({ account_id: item.id, status: 'updated' })), updated_count: accounts.length, failed_count: 0, skipped_count: 0 };
}
function observe() {
  const events: string[] = [], loaded: AccountPage[] = [], synced: SyncedAccountPage[] = [], errors: unknown[] = [];
  return { events, loaded, synced, errors, callbacks: {
    onLoaded: (value: AccountPage) => { events.push('loaded'); loaded.push(value); },
    onLoadError: (cause: unknown) => { events.push('load error'); errors.push(cause); },
    onSyncing: (value: boolean) => { events.push(value ? 'syncing' : 'finished'); },
    onSynced: (value: SyncedAccountPage) => { events.push('synced'); synced.push(value); },
    onSyncError: (cause: unknown) => { events.push('sync error'); errors.push(cause); },
  } };
}

for (const endpoint of ['/accounts', '/admin/synced-accounts'] as AccountListEndpoint[]) {
  test(`${endpoint} reads the page then sends one authenticated batch for its unique local IDs`, async t => {
    const observed = observe(), controller = new AbortController();
    const params = new URLSearchParams({ page: '2', search: 'example.com', status: 'active' });
    const calls: string[] = [];
    setCSRFToken('test-csrf');
    t.after(() => setCSRFToken(''));
    t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
      calls.push(url);
      assert.equal(init.signal, controller.signal);
      assert.equal(init.credentials, 'same-origin');
      if (init.method === 'GET') return response(page([account('7'), account('8'), account('7')], { total: 30, page: 2 }));
      assert.deepEqual(observed.events, ['loaded', 'syncing']);
      assert.deepEqual(JSON.parse(String(init.body)), { account_ids: ['7', '8'] });
      assert.equal((init.headers as Record<string, string>)['X-CSRF-Token'], 'test-csrf');
      return response(batch([account('7', 'active', collectedAt), account('8', 'banned', collectedAt)]));
    });
    await loadAndSyncAccountPage(endpoint, params, controller.signal, observed.callbacks);
    assert.deepEqual(calls, [`/tk-register/api${endpoint}?${params}`, `/tk-register/api${endpoint}/sync`]);
    assert.deepEqual(observed.events, ['loaded', 'syncing', 'synced', 'finished']);
    assert.equal(observed.synced[0].page.page, 2);
    assert.equal(observed.synced[0].page.total, 30);
    assert.equal(observed.synced[0].statusesChanged, true);
    assert.equal(observed.synced[0].page.items[1].snapshot?.account_status, 'banned');
    // A changed status does not cause another list read or refill the filtered page.
    assert.equal(calls.length, 2);
  });
}

test('partial results merge by local ID without replacing failed/skipped snapshots or changing page membership', () => {
  const saved = page([account('1'), account('2'), account('3')], { total: 75, page: 3 });
  const failed = { ...saved.items[1], last_attempt_at: collectedAt, last_error: 'Vendor unavailable.' };
  const result = mergePageSync(saved, {
    accounts: [account('99', 'banned', collectedAt), failed, account('1', 'offline', collectedAt), saved.items[2]],
    results: [
      { account_id: '2', status: 'failed', message: 'Vendor unavailable.' },
      { account_id: '3', status: 'skipped', message: 'Account has no accepted submission.' },
      { account_id: '1', status: 'updated' },
      { account_id: '99', status: 'updated' },
    ], updated_count: 2, failed_count: 1, skipped_count: 1,
  });
  assert.deepEqual(result.page.items.map(item => item.id), ['1', '2', '3']);
  assert.equal(result.page.total, 75);
  assert.equal(result.page.page, 3);
  assert.equal(result.page.items[0].last_success_at, collectedAt);
  assert.equal(result.page.items[1].last_success_at, savedAt);
  assert.equal(result.page.items[1].snapshot, saved.items[1].snapshot);
  assert.equal(result.page.items[2], saved.items[2]);
  assert.equal(pageSyncSummary(result), '1 updated · 1 could not update · 1 skipped');
  assert.equal(result.results['99'], undefined);
});

test('incomplete batch results never imply an account was freshly collected', () => {
  const saved = page([account('1'), account('2')]);
  const result = mergePageSync(saved, {
    accounts: [account('1', 'active', collectedAt)], results: [{ account_id: '2', status: 'updated' }],
    updated_count: 2, failed_count: 0, skipped_count: 0,
  });
  assert.deepEqual(result.page.items, saved.items);
  assert.equal(result.updated, 0);
  assert.equal(result.failed, 2);
});

test('leaving during the list read ignores a late result and never starts a vendor sync', async t => {
  const observed = observe(), controller = new AbortController();
  let finish!: (value: Response) => void;
  let calls = 0;
  t.mock.method(globalThis, 'fetch', () => {
    calls++;
    return new Promise<Response>(resolve => { finish = resolve; });
  });
  const pending = loadAndSyncAccountPage('/accounts', new URLSearchParams(), controller.signal, observed.callbacks);
  controller.abort();
  finish(response(page([account('1')])));
  await pending;
  assert.equal(calls, 1);
  assert.deepEqual(observed.events, []);
});

test('an old page batch finishing after a newer page cannot overwrite it or clear its busy state', async t => {
  const oldPage = observe(), newPage = observe(), oldController = new AbortController();
  let finishOld!: (value: Response) => void;
  let startedOld!: () => void;
  const oldBatchStarted = new Promise<void>(resolve => { startedOld = resolve; });
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit) => {
    if (init.method === 'GET') return response(page([account(url.includes('page=2') ? '2' : '1')]));
    if (JSON.parse(String(init.body)).account_ids[0] === '1') {
      startedOld();
      return new Promise<Response>(resolve => { finishOld = resolve; });
    }
    return response(batch([account('2', 'active', collectedAt)]));
  });
  const previous = loadAndSyncAccountPage('/accounts', new URLSearchParams({ page: '1' }), oldController.signal, oldPage.callbacks);
  await oldBatchStarted;
  oldController.abort();
  await loadAndSyncAccountPage('/accounts', new URLSearchParams({ page: '2' }), new AbortController().signal, newPage.callbacks);
  finishOld(response(batch([account('1', 'banned', collectedAt)])));
  await previous;
  assert.deepEqual(oldPage.events, ['loaded', 'syncing']);
  assert.deepEqual(newPage.events, ['loaded', 'syncing', 'synced', 'finished']);
  assert.equal(newPage.synced[0].page.items[0].id, '2');
});

test('batch failure keeps the loaded page and reports collection failure without retries', async t => {
  const observed = observe();
  let calls = 0;
  t.mock.method(globalThis, 'fetch', async (_url: string, init: RequestInit) => {
    calls++;
    return init.method === 'GET' ? response(page([account('1')])) : response('A collection is already running. Try again shortly.', 409);
  });
  await loadAndSyncAccountPage('/accounts', new URLSearchParams(), new AbortController().signal, observed.callbacks);
  assert.equal(calls, 2);
  assert.deepEqual(observed.events, ['loaded', 'syncing', 'sync error', 'finished']);
  assert.equal(observed.loaded[0].items[0].last_success_at, savedAt);
  assert.equal(observed.synced.length, 0);
});

test('failed, empty, and oversized list reads never trigger an incorrect or unbounded batch', async t => {
  for (const list of [response('List unavailable.', 502), response(page([])), response(page(Array.from({ length: 21 }, (_, index) => account(String(index)))))]) {
    const observed = observe();
    let calls = 0;
    const mock = t.mock.method(globalThis, 'fetch', async () => { calls++; return list; });
    await loadAndSyncAccountPage('/accounts', new URLSearchParams(), new AbortController().signal, observed.callbacks);
    assert.equal(calls, 1);
    assert.equal(observed.synced.length, 0);
    assert.equal(observed.events.includes('syncing'), false);
    mock.mock.restore();
  }
});
