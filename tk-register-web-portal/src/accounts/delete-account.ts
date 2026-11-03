import { APIError, request, retryAfterSeconds } from '../api';
import type { AccountListEndpoint } from './row-sync';
import type { SyncedAccount } from './types';

export type AccountDeletionStatus = 'pending' | 'awaiting_verification' | 'upstream_confirmed' | 'completed' | 'failed';
export type AccountDeletionResult = {
  operation_id: string;
  status: AccountDeletionStatus;
  message?: string;
  retry_after_seconds?: number;
};

const statuses: AccountDeletionStatus[] = ['pending', 'awaiting_verification', 'upstream_confirmed', 'completed', 'failed'];
export function deletionInProgress(account: Pick<SyncedAccount, 'deletion_status' | 'deleted_at'>): boolean {
  return Boolean(account.deleted_at || account.deletion_status && account.deletion_status !== 'failed' && account.deletion_status !== '');
}
export function deletionFinished(result: AccountDeletionResult): boolean {
  return result.status === 'completed' || result.status === 'failed';
}
export function deletionResult(value: unknown, expectedOperation?: string): AccountDeletionResult {
  const result = value as Partial<AccountDeletionResult> | null;
  if (!result || typeof result.operation_id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(result.operation_id) ||
    !statuses.includes(result.status as AccountDeletionStatus) || expectedOperation && result.operation_id !== expectedOperation) {
    throw new APIError('The deletion result could not be confirmed. Check its status before trying again.', 0, 'DELETE_UNCONFIRMED');
  }
  return {
    operation_id: result.operation_id, status: result.status as AccountDeletionStatus,
    message: typeof result.message === 'string' ? result.message : undefined,
    retry_after_seconds: retryAfterSeconds(result.retry_after_seconds) || undefined,
  };
}

// Only an explicit confirmation invokes POST. A lost response is reconciled by
// GET using the original request ID; polling never replays a deletion request.
export async function startAccountDeletion(endpoint: AccountListEndpoint, accountID: string, requestID: string, signal: AbortSignal) {
  signal.throwIfAborted();
  const result = await request<unknown>(`${endpoint}/${encodeURIComponent(accountID)}/delete`, {
    method: 'POST', body: { request_id: requestID }, signal,
  });
  signal.throwIfAborted();
  return deletionResult(result);
}
export async function checkAccountDeletion(endpoint: AccountListEndpoint, accountID: string, reference: { operationID?: string; requestID?: string }, signal: AbortSignal) {
  signal.throwIfAborted();
  const params = new URLSearchParams();
  if (reference.operationID) params.set('operation_id', reference.operationID);
  else if (reference.requestID) params.set('request_id', reference.requestID);
  else throw new Error('No deletion request is available to check.');
  const result = await request<unknown>(`${endpoint}/${encodeURIComponent(accountID)}/delete?${params}`, { signal });
  signal.throwIfAborted();
  return deletionResult(result, reference.operationID);
}
