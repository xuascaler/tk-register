import { APIError, errorMessage, type RetryStage } from '../api';
import type { SyncedAccount } from './types';

export type AuthJSONEligibility = {
  account: string;
  plan: string;
  editable: boolean;
  reason: string;
  supplier_record_id: string;
  updated_at?: string | null;
};

export type AuthJSONUpdateResult = {
  updated: true;
  refresh_status: 'pending' | 'succeeded' | 'failed';
  refresh_message: string;
  account: SyncedAccount | null;
  updated_at: string;
  refresh_retry_after?: number;
  refresh_retry_stage?: RetryStage;
};

// A lost browser response does not prove the upstream mutation failed. Never
// encourage an automatic retry or report that the original credentials remain.
export function authJSONUpdateError(cause: unknown): { message: string; uncertain: boolean } {
  const uncertain = cause instanceof APIError && (
    cause.code === 'AUTH_JSON_UPDATE_UNCONFIRMED' ||
    cause.status === 0 ||
    (!cause.code && (cause.status >= 500 || cause.status === 200))
  );
  return uncertain
    ? { uncertain: true, message: 'We could not confirm whether the update completed. Your input is retained. Check the account before trying again.' }
    : { uncertain: false, message: errorMessage(cause) };
}

export function authJSONResultMessage(result: AuthJSONUpdateResult) {
  if (result.refresh_status === 'pending') return result.refresh_message?.trim() || 'Auth JSON updated. Account status and usage will sync in the background.';
  if (result.refresh_status === 'succeeded') return 'Auth JSON updated. Account status and usage refreshed.';
  const prefix = 'Auth JSON updated, but account refresh failed.';
  const details = result.refresh_message?.trim();
  if (details?.startsWith(prefix)) return details;
  return `${prefix} ${details || 'Your last successful data is retained. Use Sync now to try again.'}`;
}
