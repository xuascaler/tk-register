import { AsyncLocalStorage } from 'node:async_hooks';
import { atStage, publicError, safeErrorDetails } from './errors.mjs';

const context = new AsyncLocalStorage();
const entries = new Set(['submit', 'update', 'deactivate']);
const methods = new Set(['oauth', 'setup', 'json', 'cookie', 'token']);
const operations = new Set(['start', 'exchange', 'import', 'verify', 'proxy-test']);

export function diagnosticContext(headers, operation, logger, callback) {
  const id = headers['x-request-id'];
  return context.run({
    ...(typeof id === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(id) ? { request_id: id } : {}),
    entry: entries.has(headers['x-tk-credential-entry']) ? headers['x-tk-credential-entry'] : 'unknown',
    method: methods.has(headers['x-tk-credential-method']) ? headers['x-tk-credential-method'] : 'unknown',
    operation: operations.has(operation) ? operation : 'unknown',
    logger, started: performance.now(),
  }, callback);
}

// Server logs only: no organization IDs, names, capabilities, or credentials.
export function recordCookieOrganization(count, ravenType, selection) {
  const state = context.getStore();
  if (!state || !Number.isSafeInteger(count) || count < 1 || count > 100000
      || !['single', 'team', 'first'].includes(selection)) return;
  state.cookieOrganization = {
    organization_count: count,
    organization_type: ravenType === 'team' ? 'team' : ravenType == null ? 'personal' : 'other',
    organization_selection: selection,
  };
}

// Kept in the request's logging context only, never attached to a public error.
export function recordUpstreamRejection(diagnostic, sensitiveValues = []) {
  const state = context.getStore();
  if (!state || typeof diagnostic?.message !== 'string' || Buffer.byteLength(diagnostic.message) > 8192) return;
  let message = diagnostic.message;
  const variants = new Set();
  for (const value of sensitiveValues) {
    if (typeof value !== 'string' || !value) continue;
    variants.add(value);
    try { variants.add(encodeURIComponent(value)); } catch { /* Malformed Unicode must not disrupt error handling. */ }
    variants.add(JSON.stringify(value).slice(1, -1));
  }
  for (const value of [...variants].sort((a, b) => b.length - a.length)) message = message.split(value).join('[redacted]');
  message = message
    .replace(/[a-z][a-z0-9+.-]*:\/\/[^\s<>"']+/gi, '[redacted-url]')
    .replace(/[A-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi, '[redacted-email]')
    .replace(/\b(?:Bearer\s+|sk-(?:ant-)?)[A-Za-z0-9._~+\/-]+=*/gi, '[redacted-token]')
    .replace(/\b[A-Za-z0-9_-]{24,}(?:\.[A-Za-z0-9_-]+)*/g, '[redacted-id]')
    .replace(/[\p{Cc}\p{Cf}]+/gu, ' ').replace(/\s+/g, ' ').trim();
  if (message) state.upstreamRejection = { upstream_message: [...message].slice(0, 1024).join('') };
}

export function logDiagnostic(event, error) {
  const state = context.getStore();
  if (!state || !['failure', 'warning'].includes(event)) return;
  const safe = publicError(error);
  const record = {
    event, ...(state.request_id ? { request_id: state.request_id } : {}),
    entry: state.entry, method: state.method, operation: state.operation,
    code: safe.body.error.code, ...safeErrorDetails(error), ...state.cookieOrganization, ...state.upstreamRejection,
    ...(event === 'failure' ? { helper_status: safe.status } : {}),
    duration_ms: Math.max(0, Math.min(120000, Math.round(performance.now() - state.started))),
  };
  // Diagnostics must never change an authorization's result.
  try { state.logger(`TK_CREDENTIAL_HELPER ${JSON.stringify(record)}`); } catch { /* Best effort. */ }
}

export async function withStage(stage, operation) {
  try { return await operation(); }
  catch (error) { throw atStage(error, stage); }
}
