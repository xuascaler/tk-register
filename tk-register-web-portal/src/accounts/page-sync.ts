import { request } from '../api';
import type { AccountPage, SyncedAccount } from './types';

export type AccountListEndpoint = '/accounts' | '/admin/synced-accounts';
export type PageSyncResult = {
  account_id: string;
  status: 'updated' | 'failed' | 'skipped';
  message?: string;
};
export type PageSyncResponse = {
  accounts: SyncedAccount[];
  results: PageSyncResult[];
  updated_count: number;
  failed_count: number;
  skipped_count: number;
};
export type SyncedAccountPage = {
  page: AccountPage;
  results: Record<string, PageSyncResult>;
  updated: number;
  failed: number;
  skipped: number;
  statusesChanged: boolean;
};

// The visible page stays fixed for this pass, even when a newly collected status
// no longer matches its filter. Never pull more rows into an automatic sync loop.
export function mergePageSync(page: AccountPage, response: PageSyncResponse): SyncedAccountPage {
  const accounts = new Map(response.accounts.map(account => [account.id, account]));
  const results = new Map(response.results.map(result => [result.account_id, result]));
  const view: SyncedAccountPage = {
    page, results: {}, updated: 0, failed: 0, skipped: 0, statusesChanged: false,
  };
  view.page = { ...page, items: page.items.map(previous => {
    let result = results.get(previous.id);
    const account = accounts.get(previous.id);
    const incomplete = !result || (result.status === 'updated' && !account);
    // A successful HTTP response alone is not proof that every row was updated.
    if (incomplete) {
      result = { account_id: previous.id, status: 'failed', message: 'The update result was incomplete. Showing saved data.' };
    }
    if (!result) return previous;
    view.results[previous.id] = result;
    view[result.status === 'updated' ? 'updated' : result.status === 'failed' ? 'failed' : 'skipped']++;
    if (result.status === 'updated') {
      view.statusesChanged ||= previous.snapshot?.account_status !== account?.snapshot?.account_status;
      return account!;
    }
    // The server retains successful snapshots on a failed collection. Its DTO
    // also carries the latest attempt/error; omitted DTOs leave saved data intact.
    return incomplete ? previous : account || previous;
  }) };
  return view;
}

type PageSyncCallbacks = {
  onLoaded: (page: AccountPage) => void;
  onLoadError: (cause: unknown) => void;
  onSyncing: (value: boolean) => void;
  onSynced: (result: SyncedAccountPage) => void;
  onSyncError: (cause: unknown) => void;
};

// One list read followed by one bounded batch. Checking the signal after every
// await also covers transports that finish despite cancellation during navigation.
export async function loadAndSyncAccountPage(
  endpoint: AccountListEndpoint, params: URLSearchParams, signal: AbortSignal, callbacks: PageSyncCallbacks,
) {
  if (signal.aborted) return;
  let page: AccountPage;
  try {
    page = await request<AccountPage>(`${endpoint}?${params}`, { signal });
  } catch (cause) {
    if (!signal.aborted) callbacks.onLoadError(cause);
    return;
  }
  if (signal.aborted) return;
  callbacks.onLoaded(page);
  const ids = [...new Set(page.items.map(account => account.id))];
  if (signal.aborted || !ids.length) return;
  if (ids.length > 20) {
    callbacks.onSyncError(new Error('This page contains too many accounts to update at once. Showing saved data.'));
    return;
  }
  callbacks.onSyncing(true);
  try {
    const result = await request<PageSyncResponse>(`${endpoint}/sync`, {
      method: 'POST', body: { account_ids: ids }, signal,
    });
    if (!signal.aborted) callbacks.onSynced(mergePageSync(page, result));
  } catch (cause) {
    if (!signal.aborted) callbacks.onSyncError(cause);
  } finally {
    if (!signal.aborted) callbacks.onSyncing(false);
  }
}

export function pageSyncSummary(result: SyncedAccountPage) {
  return [
    result.updated ? `${result.updated} updated` : '',
    result.failed ? `${result.failed} could not update` : '',
    result.skipped ? `${result.skipped} skipped` : '',
  ].filter(Boolean).join(' · ');
}
