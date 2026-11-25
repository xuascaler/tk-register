import { APIError, errorMessage } from '../api';
import type { AuthJSONEligibility } from './auth-json';

export type ProxyEligibility = AuthJSONEligibility & { revision: string; proxy_url_masked: string };
export type ProxyTestResult = { test_token: string; expires_in: number; proxy_check: { ok: boolean; ip: string } };
export type ProxyProblem = { kind: 'missing' | 'unknown' | 'conflict' | 'blocked' | 'error'; message: string };
export const proxyUnknownMessage = 'No clear response was received from the supplier. The proxy URL may already have been updated. This does not mean the update failed. Please do not resubmit immediately.';
export const proxyMissingMessage = 'The latest Auth JSON is not saved. Use Update Auth JSON to submit complete credentials containing the new Proxy URL.';
export const proxyChangedMessage = 'Account credentials have changed. Reopen the dialog before continuing.';

// Stable API codes determine the UI state; a lost write response must not
// turn into a retry button. Preflight/test connection failures remain retryable.
export function proxyProblem(cause: unknown, writing = false): ProxyProblem {
  if (cause instanceof APIError) {
    if (cause.code === 'ACCOUNT_CREDENTIALS_MISSING') return { kind: 'missing', message: proxyMissingMessage };
    if (cause.code === 'ACCOUNT_CREDENTIALS_CHANGED' || cause.code === 'ACCOUNT_CHANGED') return { kind: 'conflict', message: proxyChangedMessage };
    if (cause.code === 'AUTH_JSON_NOT_EDITABLE') return { kind: 'blocked', message: 'The supplier does not allow Auth JSON or Proxy URL updates for this account in its current state.' };
    if (cause.code === 'ACCOUNT_CREDENTIALS_UNCONFIRMED' || cause.code === 'AUTH_JSON_UPDATE_UNCONFIRMED' || writing && (cause.status === 0 || (!cause.code && (cause.status >= 500 || cause.status === 200)) || ['INTERNAL_ERROR', 'UNAVAILABLE'].includes(cause.code))) return { kind: 'unknown', message: proxyUnknownMessage };
    if (cause.code === 'PROXY_TEST_REQUIRED') return { kind: 'error', message: 'Test the new proxy connection again before saving.' };
  }
  return { kind: 'error', message: errorMessage(cause) };
}

export function maskedProxyURL(value: string): string {
  try {
    const url = new URL(value);
    if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(url.protocol) || !url.hostname) return '—';
    return `${url.protocol}//${url.username || url.password ? '••••:••••@' : ''}${url.host}`;
  } catch { return '—'; }
}
