const stages = {
  request_validation: 'Validate authorization input',
  proxy_check: 'Test proxy connection',
  cookie_organizations: 'Read Claude organizations',
  cookie_authorize: 'Authorize using sessionKey',
  oauth_token: 'Exchange authorization for tokens',
  profile: 'Read account information',
  auth_json_conversion: 'Generate Auth JSON',
  helper_transport: 'Connect to authorization service',
} as const;
export type AuthorizationStage = keyof typeof stages;
export type AuthorizationDiagnostics = {
  request_id?: string;
  stage?: AuthorizationStage;
  upstream_status?: number;
  upstream_code?: string;
  response_kind?: 'json' | 'html' | 'other';
};
const upstreamCodes = new Set([
  'invalid_grant', 'invalid_scope', 'authentication_error', 'permission_error',
  'invalid_request', 'rate_limit_error', 'overloaded_error', 'invalid_client',
  'unauthorized_client', 'unsupported_grant_type',
]);
const apiCodes = new Set([
  'AUTHORIZATION_REJECTED', 'INVALID_CREDENTIAL', 'AUTHORIZATION_FAILED', 'AUTHORIZATION_CONNECTION_FAILED', 'AUTHORIZATION_UPSTREAM_REJECTED',
  'AUTHORIZATION_INVALID_RESPONSE', 'AUTHORIZATION_ORGANIZATION_MISSING',
  'AUTHORIZATION_CALLBACK_MISSING', 'AUTHORIZATION_TOKEN_MISSING',
  'INVALID_AUTHORIZATION_CODE', 'INVALID_SESSION_KEY', 'INVALID_DOCUMENT', 'INVALID_REQUEST',
  'INVALID_PROXY', 'PROXY_CHANGED', 'PROXY_AUTHENTICATION_FAILED', 'PROXY_CONNECTION_FAILED',
  'PROXY_TEST_UNAVAILABLE', 'PROXY_TEST_TIMEOUT', 'PROXY_TEST_RATE_LIMITED', 'RATE_LIMITED',
  'CREDENTIAL_RATE_LIMITED', 'CREDENTIAL_HELPER_UNAVAILABLE', 'CREDENTIAL_HELPER_INVALID_RESPONSE',
  'CREDENTIAL_FLOW_NOT_FOUND', 'CREDENTIAL_FLOW_BUSY', 'CREDENTIAL_FLOW_CONFLICT',
  'CREDENTIAL_DRAFT_NOT_READY', 'CREDENTIAL_FIELD_LOCKED', 'AUTH_JSON_EMAIL_MISMATCH',
  'CREDENTIAL_FLOW_UPDATE_FINISHED', 'CREDENTIAL_FLOW_EXPORT_FINISHED',
  'CREDENTIAL_GENERATION_UNCONFIRMED', 'AUTH_JSON_UPDATE_UNCONFIRMED',
]);
export function authorizationStageLabel(stage: AuthorizationStage) { return stages[stage]; }
export function authorizationErrorCode(value: unknown): string | undefined {
  return typeof value === 'string' && apiCodes.has(value) ? value : undefined;
}
export function safeRequestID(value: unknown): string | undefined {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value) ? value : undefined;
}

// Diagnostics are operational metadata only. Ignore unknown keys and values;
// never turn a response body, exception string, URL or token into display text.
export function readAuthorizationDiagnostics(value: unknown, headerRequestID?: unknown): AuthorizationDiagnostics | undefined {
  const object = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
  const field = (key: string): unknown => Object.hasOwn(object, key) ? object[key] : undefined;
  const result: AuthorizationDiagnostics = {};
  const requestID = safeRequestID(headerRequestID) || safeRequestID(field('request_id'));
  if (requestID) result.request_id = requestID;
  const stage = field('stage');
  if (typeof stage === 'string' && Object.hasOwn(stages, stage)) result.stage = stage as AuthorizationStage;
  const status = field('upstream_status');
  if (typeof status === 'number' && Number.isInteger(status) && status >= 100 && status <= 599) result.upstream_status = status;
  const code = field('upstream_code');
  if (typeof code === 'string' && upstreamCodes.has(code)) result.upstream_code = code;
  const kind = field('response_kind');
  if (kind === 'json' || kind === 'html' || kind === 'other') result.response_kind = kind;
  return Object.keys(result).length ? result : undefined;
}
