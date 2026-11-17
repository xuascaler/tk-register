import { useCallback, useEffect, useRef, useState } from 'react';

export type AccountListFilters = { page: number; search: string; status: string; sources: string };
export type AccountListReturn = { key: string; userID: number; admin: boolean; hash: string; filters: AccountListFilters; scrollY: number; accountID?: string };
const prefix = 'tk-register:account-list:';
const historyKey = 'tkAccountListKey';
const statuses = new Set(['', 'active', 'offline', 'banned', 'sync_failed', 'login_failed', 're_login_failed', 'bound_failed', 'cooking_failed', 'pending', 'unknown']);

export function normalizeListFilters(value: Partial<AccountListFilters>): AccountListFilters {
  const page = Number(value.page);
  const sources = typeof value.sources === 'string' && value.sources ? value.sources : 'all';
  return {
    page: Number.isSafeInteger(page) && page > 0 && page <= 1_000_000 ? page : 1,
    search: typeof value.search === 'string' ? value.search.trim().slice(0, 254) : '',
    status: statuses.has(value.status || '') ? value.status || '' : '',
    sources: /^(all|none|(?:myself|[1-9]\d*)(?:,(?:myself|[1-9]\d*))*)$/.test(sources) && sources.length <= 4000 ? sources : 'all',
  };
}

export function readListFilters(hash: string, fallbackSources = 'all'): AccountListFilters {
  const params = new URLSearchParams(hash.split('?')[1]);
  return normalizeListFilters({ page: Number(params.get('page')), search: params.get('search') || '', status: params.get('status') || '', sources: params.get('sources') ?? fallbackSources });
}

export function accountListHash(admin: boolean, filters: AccountListFilters) {
  const state = normalizeListFilters(filters);
  const params = new URLSearchParams({ tab: 'list' });
  if (state.page > 1) params.set('page', String(state.page));
  if (state.search) params.set('search', state.search);
  if (state.status) params.set('status', state.status);
  if (state.sources !== 'all' && !admin) params.set('sources', state.sources);
  return `#/${admin ? 'account-pool/accounts' : 'accounts'}?${params}`;
}

export function readAccountListReturn(userID: number, admin: boolean): AccountListReturn | null {
  const key = window.history.state?.[historyKey];
  if (typeof key !== 'string' || !key.startsWith(`${prefix}${userID}:${admin ? 'admin' : 'member'}:`)) return null;
  try {
    const saved = JSON.parse(sessionStorage.getItem(key) || 'null') as AccountListReturn | null;
    if (!saved || saved.key !== key || saved.userID !== userID || saved.admin !== admin || !Number.isFinite(saved.scrollY) || saved.scrollY < 0) return null;
    const filters = normalizeListFilters(saved.filters || {});
    if (accountListHash(admin, filters) !== saved.hash) return null;
    return { ...saved, filters };
  } catch { return null; }
}

export function accountListHistoryState(saved: AccountListReturn | null) {
  return saved ? { [historyKey]: saved.key } : null;
}

function storeAccountListReturn(saved: AccountListReturn) {
  sessionStorage.setItem(saved.key, JSON.stringify(saved));
  // Keep a bounded set of entries in this browser tab, including older Back/Forward destinations.
  const keys = Object.keys(sessionStorage).filter(item => item.startsWith(prefix));
  for (const stale of keys.slice(0, Math.max(0, keys.length - 40))) if (stale !== saved.key) sessionStorage.removeItem(stale);
}

export function cloneAccountListReturn(saved: AccountListReturn): AccountListReturn | null {
  try {
    const cloned = { ...saved, key: `${prefix}${saved.userID}:${saved.admin ? 'admin' : 'member'}:${crypto.randomUUID()}`, filters: { ...saved.filters } };
    storeAccountListReturn(cloned);
    return cloned;
  } catch { return null; }
}

export function rememberAccountList(userID: number, admin: boolean, filters: AccountListFilters, accountID?: string): AccountListReturn | null {
  const current = readAccountListReturn(userID, admin);
  const hash = accountListHash(admin, filters);
  const key = current?.hash === hash ? current.key : `${prefix}${userID}:${admin ? 'admin' : 'member'}:${crypto.randomUUID()}`;
  const saved = { key, userID, admin, hash, filters: normalizeListFilters(filters), scrollY: Math.max(0, window.scrollY), accountID };
  try {
    storeAccountListReturn(saved);
    window.history.replaceState({ ...window.history.state, ...accountListHistoryState(saved) }, '', hash);
    return saved;
  } catch { return null; }
}

export function clearAccountListReturns() {
  try { for (const key of Object.keys(sessionStorage)) if (key.startsWith(prefix)) sessionStorage.removeItem(key); } catch { /* Storage can be disabled. */ }
}

export function useAccountListNavigation(userID: number, admin: boolean, fallbackSources: string) {
  const initial = () => {
    const saved = readAccountListReturn(userID, admin);
    return saved?.hash === window.location.hash ? saved : null;
  };
  const restoring = useRef<AccountListReturn | null>(initial());
  const [filters, setFilters] = useState(() => restoring.current?.filters || readListFilters(window.location.hash, admin ? 'all' : fallbackSources));
  const frames = useRef<number[]>([]);
  const restoringPosition = useRef(Boolean(restoring.current));
  useEffect(() => {
    const followHistory = () => {
      const saved = readAccountListReturn(userID, admin);
      restoring.current = saved?.hash === window.location.hash ? saved : null;
      restoringPosition.current = Boolean(restoring.current);
      setFilters(restoring.current?.filters || readListFilters(window.location.hash));
    };
    window.addEventListener('hashchange', followHistory);
    return () => { window.removeEventListener('hashchange', followHistory); frames.current.forEach(cancelAnimationFrame); };
  }, [userID, admin]);

  const change = useCallback((patch: Partial<AccountListFilters>, options: { replace?: boolean; keepScroll?: boolean } = {}) => {
    const next = normalizeListFilters({ ...filters, ...patch });
    if (admin) next.sources = 'all';
    const hash = accountListHash(admin, next);
    if (hash === accountListHash(admin, filters)) return;
    if (!options.keepScroll) rememberAccountList(userID, admin, filters);
    if (!options.keepScroll) { restoring.current = null; restoringPosition.current = false; }
    window.history[options.replace ? 'replaceState' : 'pushState'](null, '', hash);
    setFilters(next);
  }, [filters, userID, admin]);

  const remember = useCallback((accountID?: string) => rememberAccountList(userID, admin, filters, accountID), [filters, userID, admin]);
  const restoreScroll = useCallback(() => {
    const saved = restoring.current;
    if (!saved) return;
    restoring.current = null;
    frames.current.push(requestAnimationFrame(() => {
      frames.current.push(requestAnimationFrame(() => {
        window.scrollTo({ top: saved.scrollY, behavior: 'instant' });
        restoringPosition.current = false;
      }));
    }));
  }, []);
  // Save scrolling as well as explicit account clicks, so browser Back/Forward and list reloads restore position.
  useEffect(() => {
    const save = () => {
      // A delayed scrollend from the old screen must not rewrite a detail URL
      // or save old filters into a newer pagination/history entry.
      if (!restoringPosition.current && window.location.hash === accountListHash(admin, filters)) rememberAccountList(userID, admin, filters);
    };
    window.addEventListener('scrollend', save);
    return () => window.removeEventListener('scrollend', save);
  }, [filters, userID, admin]);
  return { filters, change, remember, restoreScroll };
}
