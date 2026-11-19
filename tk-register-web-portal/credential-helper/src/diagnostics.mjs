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

export function logDiagnostic(event, error) {
  const state = context.getStore();
  if (!state || !['failure', 'warning'].includes(event)) return;
  const safe = publicError(error);
  const record = {
    event, ...(state.request_id ? { request_id: state.request_id } : {}),
    entry: state.entry, method: state.method, operation: state.operation,
    code: safe.body.error.code, ...safeErrorDetails(error),
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
