import assert from 'node:assert/strict';
import test, { type TestContext } from 'node:test';
import {
  accountListHash,
  accountListHistoryState,
  clearAccountListReturns,
  cloneAccountListReturn,
  normalizeListFilters,
  readAccountListReturn,
  readListFilters,
  rememberAccountList,
  type AccountListFilters,
} from '../src/accounts/list-navigation.ts';

const filters: AccountListFilters = { page: 8, search: 'alice+region@example.com & active?', status: 'active', sources: 'myself,7' };

function browser(t: TestContext) {
  let readsBlocked = false, writesBlocked = false;
  const storage: Record<string, unknown> = Object.create(null);
  Object.defineProperties(storage, {
    getItem: { value(key: string) { if (readsBlocked) throw new Error('Storage disabled'); return storage[key] ?? null; } },
    setItem: { value(key: string, value: string) { if (writesBlocked) throw new Error('Quota exceeded'); storage[key] = value; } },
    removeItem: { value(key: string) { delete storage[key]; } },
  });
  const current = {
    scrollY: 640,
    location: { hash: '#/accounts?tab=list' },
    history: {
      state: { unrelatedState: 'keep' } as Record<string, unknown> | null,
      replaceState(state: Record<string, unknown> | null, _unused: string, hash: string) {
        current.history.state = state;
        current.location.hash = hash;
      },
    },
  };
  for (const [name, value] of Object.entries({ window: current, sessionStorage: storage })) {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, name);
    Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
    t.after(() => descriptor ? Object.defineProperty(globalThis, name, descriptor) : Reflect.deleteProperty(globalThis, name));
  }
  return { current, storage, blockReads() { readsBlocked = true; }, blockWrites() { writesBlocked = true; } };
}

test('list destinations round-trip reserved search characters, page and Source without affecting admin scope', () => {
  const hash = accountListHash(false, filters);
  assert.deepEqual(readListFilters(hash), filters);
  assert.ok(hash.startsWith('#/accounts?tab=list&'));
  assert.ok(!hash.includes('alice+region@example.com & active?'), 'Search text must be encoded as a query value');
  const adminHash = accountListHash(true, filters);
  assert.ok(adminHash.startsWith('#/account-pool/accounts?tab=list&'));
  assert.equal(new URLSearchParams(adminHash.split('?')[1]).has('sources'), false, 'Personal Source IDs must not scope administrator lists');
  assert.deepEqual(readListFilters(adminHash), { ...filters, sources: 'all' });
  assert.equal(accountListHash(false, normalizeListFilters({})), '#/accounts?tab=list');
  assert.equal(readListFilters('#/accounts?tab=list', '7').sources, '7');
  assert.equal(readListFilters('#/accounts?tab=list&sources=all', '7').sources, 'all', 'An explicit all-sources choice must win over shared member context');
});

test('invalid URL pagination and filters cannot become unbounded requests or foreign Source values', () => {
  for (const page of ['-1', '0', '1.5', 'Infinity', 'NaN', '1000001']) {
    assert.equal(readListFilters(`#/accounts?tab=list&page=${page}`).page, 1);
  }
  assert.deepEqual(readListFilters('#/accounts?tab=list&page=8&status=not-a-status&sources=https%3A%2F%2Fother.example'), { page: 8, search: '', status: '', sources: 'all' });
  assert.equal(readListFilters(`#/accounts?tab=list&search=${'x'.repeat(500)}`).search.length, 254);
  for (const sources of ['-1', '0', '7,unknown', 'myself,,7', '7&other=1']) {
    assert.equal(normalizeListFilters({ sources }).sources, 'all');
  }
  for (const sources of [['7'], ['myself', '7'], 7, { toString: () => '7' }]) {
    assert.equal(normalizeListFilters({ sources } as unknown as Partial<AccountListFilters>).sources, 'all', 'Persisted Source values must be strings');
  }
});

test('remembered positions survive a detail route and retain the exact list destination for that history entry', t => {
  const { current } = browser(t);
  const saved = rememberAccountList(1, false, filters, '145');
  assert.ok(saved);
  assert.equal(saved.scrollY, 640);
  assert.equal(saved.accountID, '145');
  assert.equal(current.location.hash, accountListHash(false, filters));
  assert.equal(current.history.state?.unrelatedState, 'keep');
  current.history.state = accountListHistoryState(saved);
  current.location.hash = '#/accounts/145?tab=daily-usage';
  assert.deepEqual(readAccountListReturn(1, false), saved, 'Details and reloads recover the original list through their persisted history reference');
  current.location.hash = saved.hash;
  current.scrollY = 815;
  const updated = rememberAccountList(1, false, filters, '146');
  assert.ok(updated);
  assert.equal(updated.key, saved.key, 'Updating scroll on one list entry should not lose its reference');
  assert.equal(readAccountListReturn(1, false)?.scrollY, 815);
  assert.equal(readAccountListReturn(1, false)?.accountID, '146');
});

test('personal/admin and different users cannot reuse one another’s saved destinations', t => {
  const { current } = browser(t);
  const personal = rememberAccountList(1, false, filters, '145');
  assert.ok(personal);
  assert.equal(readAccountListReturn(2, false), null);
  assert.equal(readAccountListReturn(1, true), null);
  const admin = rememberAccountList(1, true, { ...filters, sources: 'all', page: 3 }, '45');
  assert.ok(admin);
  assert.notEqual(admin.key, personal.key);
  assert.equal(readAccountListReturn(1, false), null);
  assert.equal(readAccountListReturn(2, true), null);
  current.history.state = accountListHistoryState(personal);
  assert.deepEqual(readAccountListReturn(1, false), personal);
  current.history.state = null;
  assert.equal(readAccountListReturn(1, false), null, 'A direct detail link must not pick an arbitrary older list destination');
  assert.equal(accountListHistoryState(null), null);
});

test('new detail and explicit-return entries do not mutate an older detail return snapshot', t => {
  const { current } = browser(t);
  const originalList = rememberAccountList(1, false, filters, '145');
  assert.ok(originalList);
  const detail = cloneAccountListReturn(originalList);
  assert.ok(detail);
  assert.notEqual(detail.key, originalList.key);
  assert.deepEqual(detail.filters, originalList.filters);
  assert.equal(detail.scrollY, 640);

  // Browser Back restores the original list; scrolling it must not change Forward's detail context.
  current.history.state = accountListHistoryState(originalList);
  current.scrollY = 815;
  rememberAccountList(1, false, filters, '146');
  current.history.state = accountListHistoryState(detail);
  current.location.hash = '#/accounts/145?tab=daily-usage';
  assert.equal(readAccountListReturn(1, false)?.scrollY, 640);
  assert.equal(readAccountListReturn(1, false)?.accountID, '145');

  // Explicit Return creates a new list visit instead of sharing the detail's snapshot.
  const returnedList = cloneAccountListReturn(detail);
  assert.ok(returnedList);
  assert.notEqual(returnedList.key, detail.key);
  current.history.state = accountListHistoryState(returnedList);
  current.location.hash = returnedList.hash;
  current.scrollY = 1200;
  rememberAccountList(1, false, filters, '147');
  assert.equal(readAccountListReturn(1, false)?.scrollY, 1200);
  current.history.state = accountListHistoryState(detail);
  current.location.hash = '#/accounts/145';
  assert.equal(readAccountListReturn(1, false)?.scrollY, 640);
  assert.equal(readAccountListReturn(1, false)?.accountID, '145');
});

test('cloning a return snapshot handles blocked storage without changing its history reference', t => {
  const env = browser(t);
  const saved = rememberAccountList(1, false, filters, '145');
  assert.ok(saved);
  env.blockWrites();
  assert.equal(cloneAccountListReturn(saved), null);
  assert.deepEqual(readAccountListReturn(1, false), saved);
});

test('corrupt or tampered persisted records never authorize a foreign return URL', t => {
  const { storage } = browser(t);
  const saved = rememberAccountList(1, false, filters, '145');
  assert.ok(saved);
  for (const patch of [
    { key: 'different-entry' }, { userID: 2 }, { admin: true },
    { hash: 'https://other.example/accounts' }, { hash: '#/config' }, { hash: '#/account-pool/accounts?tab=list' },
    { scrollY: -1 }, { scrollY: '640' }, { scrollY: null },
    { filters: { ...filters, page: 3 } },
  ]) {
    storage[saved.key] = JSON.stringify({ ...saved, ...patch });
    assert.equal(readAccountListReturn(1, false), null, JSON.stringify(patch));
  }
  for (const value of ['{not json', 'null', '[]', '{}']) {
    storage[saved.key] = value;
    assert.equal(readAccountListReturn(1, false), null);
  }
});

test('blocked storage gracefully falls back without changing the current history entry', t => {
  const env = browser(t);
  const prior = { ...env.current.history.state };
  env.blockWrites();
  assert.equal(rememberAccountList(1, false, filters, '145'), null);
  assert.deepEqual(env.current.history.state, prior);
  assert.equal(env.current.location.hash, '#/accounts?tab=list');
  env.current.history.state = { tkAccountListKey: 'tk-register:account-list:1:member:known-entry' };
  env.blockReads();
  assert.equal(readAccountListReturn(1, false), null);
  assert.doesNotThrow(clearAccountListReturns);
});

test('session cleanup removes saved account-list returns while retaining unrelated browser state', t => {
  const { storage, current } = browser(t);
  const saved = rememberAccountList(1, false, filters, '145');
  assert.ok(saved);
  rememberAccountList(1, true, { ...filters, page: 2 }, '25');
  storage['unrelated-setting'] = 'retain';
  clearAccountListReturns();
  assert.deepEqual(Object.keys(storage), ['unrelated-setting']);
  current.history.state = accountListHistoryState(saved);
  assert.equal(readAccountListReturn(1, false), null, 'A surviving browser-history reference cannot revive data after sign-out');
});
