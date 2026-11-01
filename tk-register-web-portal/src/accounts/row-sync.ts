import { APIError, errorMessage, request } from '../api';
import type { AccountPage, SyncedAccount } from './types';

export type AccountListEndpoint = '/accounts' | '/admin/synced-accounts';
export type AccountRowSyncResult = {
  account: SyncedAccount;
  status: 'updated' | 'failed' | 'skipped';
  message?: string;
  retryAfter?: number;
};
type SyncResponse = {
  accounts: SyncedAccount[];
  results: { account_id: string; status: 'updated' | 'failed' | 'skipped'; message?: string }[];
};

// Opening or changing the list reads saved data only. Supplier reads are explicit.
export function loadAccountPage(endpoint: AccountListEndpoint, params: URLSearchParams, signal: AbortSignal) {
  return request<AccountPage>(`${endpoint}?${params}`, { signal });
}

export async function syncAccountRow(
  endpoint: AccountListEndpoint, previous: SyncedAccount, signal: AbortSignal,
): Promise<AccountRowSyncResult> {
  signal.throwIfAborted();
  if (!previous.current_submission_id) {
    return { account: previous, status: 'skipped', message: 'Available after the account is accepted.' };
  }
  try {
    // The bounded endpoint searches only this account instead of loading the
    // supplier's entire inventory through the older detail sync endpoint.
    const response = await request<SyncResponse>(`${endpoint}/sync`, {
      method: 'POST', body: { account_ids: [previous.id] }, signal,
    });
    signal.throwIfAborted();
    const result = Array.isArray(response?.results) ? response.results.find(item => item?.account_id === previous.id) : undefined;
    const account = Array.isArray(response?.accounts) ? response.accounts.find(item => item?.id === previous.id) : undefined;
    if (!result || !['updated', 'failed', 'skipped'].includes(result.status) || (result.status === 'updated' && !account)) {
      return { account: previous, status: 'failed', message: 'The update result was incomplete. Showing saved data.' };
    }
    if (result.status === 'updated' && account && !account.last_error) {
      return { account, status: 'updated' };
    }
    // An unsuccessful attempt never clears a successful snapshot or changes its
    // timestamp. Keep the original identity too if a submission changed mid-sync.
    return {
      account: previous,
      status: result.status === 'skipped' ? 'skipped' : 'failed',
      message: result.message || account?.last_error || 'The account could not be updated. Showing saved data.',
    };
  } catch (cause) {
    signal.throwIfAborted();
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause;
    return {
      account: previous, status: 'failed', message: errorMessage(cause),
      retryAfter: cause instanceof APIError ? cause.retryAfter : undefined,
    };
  }
}

// Preserve the visible page and filter until its next saved-data read. A status
// change does not pull another row into the page or trigger another supplier call.
export function mergeSyncedAccount(page: AccountPage, result: AccountRowSyncResult): AccountPage {
  return { ...page, items: page.items.map(account => account.id === result.account.id ? result.account : account) };
}
