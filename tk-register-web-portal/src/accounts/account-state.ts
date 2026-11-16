import type { SyncedAccount } from './types';

// Passive guard for operations already reported by the account-list API.
// It never starts a deletion or requests deletion progress.
export function deletionInProgress(account: Pick<SyncedAccount, 'deletion_status' | 'deleted_at'>): boolean {
  return Boolean(account.deleted_at || account.deletion_status && account.deletion_status !== 'failed' && account.deletion_status !== '');
}
