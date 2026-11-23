import { t, translateMessage } from '../i18n';
import { AuthFieldError, authFields } from '../submissions/validation';
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
  refresh_status: 'not_requested' | 'pending' | 'succeeded' | 'failed';
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
  if (result.refresh_status === 'not_requested') return 'Auth JSON updated. Account status is updated by scheduled background checks.';
  if (result.refresh_status === 'pending') return result.refresh_message?.trim() || 'Auth JSON updated. Account status and usage will sync in the background.';
  if (result.refresh_status === 'succeeded') return 'Auth JSON updated. Account status and usage refreshed.';
  const prefix = 'Auth JSON updated, but account refresh failed.';
  const details = result.refresh_message?.trim();
  if (details?.startsWith(prefix)) return details;
  return `${prefix} ${details || 'Your last successful data is retained. Use Sync now to try again.'}`;
}

export function confirmedAuthJSONResult(value: unknown): value is AuthJSONUpdateResult {
  if (!value || typeof value !== 'object') return false;
  const result = value as Partial<AuthJSONUpdateResult>;
  return result.updated === true && 'account' in result && (result.account === null || typeof result.account === 'object') && ['not_requested', 'pending', 'succeeded', 'failed'].includes(result.refresh_status || '');
}

// Keep source messages in component state so a language change also updates
// existing warnings. Only interpolate known validation field names; technical
// keys and any additional supplier explanation remain unchanged.
export function localizeAuthJSONMessage(value: string | AuthFieldError): string {
  if (value instanceof AuthFieldError) return value.localizedMessage;
  const field = /^(?:The authorized ([a-z_]+) cannot be replaced\. Complete missing fields only\.|([a-z_]+) has an invalid value\.)$/.exec(value);
  const name = field?.[1] || field?.[2];
  if (name && (authFields as readonly string[]).includes(name)) return t(field?.[1] ? 'The authorized {{field}} cannot be replaced. Complete missing fields only.' : '{{field}} has an invalid value.', { field: name });
  const unavailable = 'The latest page data could not be loaded. Refresh this page to try again.';
  if (value.endsWith(unavailable) && value !== unavailable) return `${localizeAuthJSONMessage(value.slice(0, -unavailable.length).trim())} ${t(unavailable)}`;
  const failed = 'Auth JSON updated, but account refresh failed.';
  if (value.startsWith(`${failed} `)) return `${t(failed)} ${translateMessage(value.slice(failed.length).trim())}`;
  return translateMessage(value);
}
