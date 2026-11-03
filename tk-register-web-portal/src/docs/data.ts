export type DocTopic = 'getting-started' | 'submit-account' | 'account-lookup' | 'update-auth-json';
export type DocSection = 'parameters' | 'example' | 'response' | 'errors';
export type EndpointTopic = Exclude<DocTopic, 'getting-started'>;

export const API_BASE_PATH = '/tk-register/api/v1';
const accountsPath = '/vendors/tk-register-0/claude/accounts';

export const docTopics: { id: DocTopic; label: string }[] = [
  { id: 'getting-started', label: 'Getting started' },
  { id: 'submit-account', label: 'Submit account' },
  { id: 'account-lookup', label: 'Account lookup' },
  { id: 'update-auth-json', label: 'Update Auth JSON' },
];

export const docSections: { id: DocSection; label: string }[] = [
  { id: 'parameters', label: 'Parameters' },
  { id: 'example', label: 'cURL example' },
  { id: 'response', label: 'Response' },
  { id: 'errors', label: 'Errors' },
];

export const endpoints = {
  'submit-account': {
    title: 'Submit an account',
    method: 'POST',
    path: accountsPath,
    description: 'Submit a Claude account to the vendor and save its submission history.',
    scope: 'claude:submit',
    limit: '10 requests / minute',
    note: 'The account belongs to the token’s user. Validation follows the web submission form. A complete vendor response is passed through without a new JSON envelope.',
  },
  'account-lookup': {
    title: 'Look up an account',
    method: 'GET',
    path: `${accountsPath}/lookup`,
    description: 'Read saved account status, usage windows, token totals, and cumulative spend.',
    scope: 'accounts:read',
    limit: '60 requests / minute',
    note: 'Reads saved data. Collection runs every 2 hours by default when enabled; administrators can change the interval. This request does not start a new collection.',
  },
  'update-auth-json': {
    title: 'Update Auth JSON',
    method: 'POST',
    path: `${accountsPath}/auth-json`,
    description: 'Replace an owned Claude account’s authentication data and queue a status and usage refresh.',
    scope: 'claude:auth-json:update',
    limit: '10 requests / minute',
    note: 'Existing tokens remain usable. A confirmed update queues Sync after three seconds, with up to three retries on collection failure. The API does not wait for refresh or guarantee an active status.',
  },
} as const;

export type FieldDefinition = {
  name: string;
  type: string;
  description: string;
  required?: boolean;
};

export const lookupFields: FieldDefinition[] = [
  { name: 'username', type: 'string', required: true, description: 'Your TK Register login email: a valid ASCII email address of at most 50 bytes. It must match the token’s owner, including when that user is an administrator.' },
  { name: 'account', type: 'string', required: true, description: 'The submitted Claude account login ID or email, not its alias or an account ID. Maximum 254 UTF-8 bytes; no internal Unicode whitespace or control characters.' },
];

export const submissionFields: FieldDefinition[] = [
  { name: 'plan', type: 'string', required: true, description: 'claude/cc-max-20x, claude/cc-max-5x, or claude/cc-pro. The vendor must authorize the selected plan.' },
  { name: 'account', type: 'string', required: true, description: 'Account login ID or email. Maximum 254 UTF-8 bytes; whitespace is not allowed. Accepted or currently running submissions for the same user, vendor, and account block another submission.' },
  { name: 'password', type: 'string', required: true, description: 'The submitted account’s password, not your TK Register password. Nonempty; maximum 1,024 UTF-8 bytes.' },
  { name: 'auth_json', type: 'object | string', required: true, description: 'An authentication JSON object, or a string containing that object’s JSON. Maximum 2 MiB. All 13 fields listed below are required.' },
  { name: 'register_location', type: 'string', required: true, description: 'US (United States), MY (Malaysia), ID (Indonesia), SG (Singapore), or PH (Philippines).' },
  { name: 'register_time', type: 'string', required: true, description: 'Registration date as a valid calendar date in YYYY-MM-DD format.' },
  { name: 'plan_start_time', type: 'string', required: true, description: 'Plan start date in YYYY-MM-DD format.' },
  { name: 'plan_end_time', type: 'string', required: true, description: 'Plan end date in YYYY-MM-DD format, on or after plan_start_time.' },
];

export const authJSONUpdateFields: FieldDefinition[] = [
  ...lookupFields,
  { name: 'auth_json', type: 'object | string', required: true, description: 'The complete replacement authentication object, or its JSON string. Maximum 2 MiB. All 13 fields are required, and its email must match account after trimming and case normalization. Partial updates are not supported.' },
];

export const authJSONUpdateResponseFields: FieldDefinition[] = [
  { name: 'updated', type: 'boolean', description: 'True only after the supplier confirms the credential update. Independent of the account’s active/inactive state.' },
  { name: 'audit_id', type: 'string', description: 'Local Auth JSON update audit record ID. It is not a submission ID or a supplier account ID.' },
  { name: 'updated_at', type: 'string', description: 'RFC3339 time when the supplier update completed.' },
  { name: 'refresh_status', type: 'string', description: 'Normally pending after Sync is queued. Failed if local persistence or a changed account identity prevents scheduling. The API does not wait for a succeeded collection result.' },
  { name: 'refresh_message', type: 'string', description: 'Safe human-readable explanation of the refresh outcome. Use refresh_status for program logic.' },
  { name: 'account', type: 'object | null', description: 'The saved account object returned by Account lookup. Its snapshot may predate this update or be null if never collected. The whole object is null if local readback fails, without canceling a queued refresh. Check collection timestamps for freshness.' },
];

export const optionalSubmissionFields: FieldDefinition[] = [
  { name: 'payment_method', type: 'string', description: 'physical_credit_card, virtual_credit_card, paypal, apple_pay, google_pay, or third_party_payment.' },
  { name: 'register_device', type: 'string', description: 'Mac, Windows, or Linux.' },
  { name: 'balance', type: 'number | string', description: 'Remaining plan allowance as a percentage from 0 to 100. Numeric strings are accepted; do not include a % sign.' },
  { name: 'recharge_time', type: 'string', description: 'Recharge date in YYYY-MM-DD format.' },
  { name: 'warranty_period', type: 'number | string', description: 'Warranty period in days: 1, 7, or 30. Numeric strings are accepted.' },
  { name: 'tags', type: 'string[]', description: 'At most one tag: 速刷 or 慢蒸. Omit the field or send [] for no tag. The two tags cannot be combined; tags apply only when submitting.' },
  { name: 'remark', type: 'string', description: 'Optional notes about the account; maximum 2,000 UTF-8 bytes.' },
];

export const authenticationFields: FieldDefinition[] = [
  { name: 'id_token', type: 'string', required: true, description: 'The key must be present. An empty string is allowed.' },
  { name: 'access_token', type: 'string', required: true, description: 'A nonempty account access token.' },
  { name: 'refresh_token', type: 'string', required: true, description: 'A nonempty account refresh token.' },
  { name: 'last_refresh', type: 'string', required: true, description: 'An RFC3339 timestamp with a timezone, for example 2026-11-01T00:00:00Z.' },
  { name: 'email', type: 'string', required: true, description: 'A nonempty email value from the account’s authentication data.' },
  { name: 'account_uuid', type: 'string', required: true, description: 'A nonempty account identifier from the authentication data.' },
  { name: 'organization_uuid', type: 'string', required: true, description: 'A nonempty organization identifier.' },
  { name: 'organization_name', type: 'string', required: true, description: 'A nonempty organization name.' },
  { name: 'claude_device_ids', type: 'string[]', required: true, description: 'Between 1 and 32 nonempty device identifiers. Each identifier is limited to 1,024 UTF-8 bytes.' },
  { name: 'type', type: 'string', required: true, description: 'A nonempty authentication type from the account’s credentials.' },
  { name: 'expired', type: 'string', required: true, description: 'An RFC3339 expiration timestamp with a timezone.' },
  { name: 'proxy_url', type: 'string', required: true, description: 'A valid URL with a host and one of these schemes: http://, https://, socks5://, or socks5h://. This is part of auth_json, not a top-level routing parameter.' },
  { name: 'verification_status', type: 'string', required: true, description: 'A nonempty verification status from the account’s authentication data.' },
];

export const lookupResponseFields: FieldDefinition[] = [
  { name: 'id', type: 'string', description: 'Stable local account ID. This is different from a submission ID or a vendor account ID.' },
  { name: 'username', type: 'string', description: 'The normalized TK Register email of the authenticated account owner.' },
  { name: 'account', type: 'string', description: 'The submitted account login ID or email.' },
  { name: 'vendor', type: 'string', description: 'tk-register-0 for this endpoint.' },
  { name: 'plan', type: 'string', description: 'The saved Claude plan.' },
  { name: 'remote_id', type: 'string | null', description: 'Account ID returned by the vendor’s submission API, when available.' },
  { name: 'platform_account_id', type: 'string | null', description: 'Account ID found by website collection. It may differ from remote_id.' },
  { name: 'submission_status', type: 'string', description: 'Latest saved submission state, such as accepted, running, failed, or unknown. This is separate from snapshot.account_status.' },
  { name: 'snapshot', type: 'object | null', description: 'Latest successful saved snapshot for the account’s current lifetime. Null when no snapshot is available.' },
  { name: 'last_success_at', type: 'string | null', description: 'RFC3339 time of the most recent successful collection. A successful collection does not imply a healthy account.' },
  { name: 'last_attempt_at', type: 'string | null', description: 'RFC3339 time of the latest collection attempt, successful or not.' },
  { name: 'last_error', type: 'string | null', description: 'Sanitized collection failure message, or null if no error is recorded.' },
  { name: 'interval_seconds', type: 'integer', description: 'Current configured collection interval in seconds. Default: 300. This value alone does not indicate that collection is enabled.' },
];

export const snapshotFields: FieldDefinition[] = [
  { name: 'id', type: 'string', description: 'Snapshot record ID.' },
  { name: 'account_id', type: 'string', description: 'Stable local account ID; matches data.id.' },
  { name: 'platform_account_id', type: 'string | null', description: 'Supplier website account ID associated with this snapshot, when available.' },
  { name: 'snapshot_date', type: 'string', description: 'Collection date in the configured timezone, in YYYY-MM-DD format.' },
  { name: 'collected_at', type: 'string', description: 'Actual collection time as an RFC3339 timestamp.' },
  { name: 'source_updated_at', type: 'string | null', description: 'Reliable upstream update time, when supplied, as an RFC3339 timestamp.' },
  { name: 'account_status', type: 'string | null', description: 'Supplier status at collection time: for example active, offline, banned, pending, limited, or unknown.' },
  { name: 'input_tokens_total', type: 'number | null', description: 'Cumulative input-token counter.' },
  { name: 'output_tokens_total', type: 'number | null', description: 'Cumulative output-token counter.' },
  { name: 'cost_total', type: 'number | null', description: 'Original cumulative spend in the accompanying currency, already normalized from supplier micro-units. Use cost_total_usd for the USD amount.' },
  { name: 'currency', type: 'string | null', description: 'Currency of the original cost_total, such as CNY.' },
  { name: 'cost_total_usd', type: 'number | null', description: 'Cumulative spend in USD: cost_total / 7.2 when currency is CNY. Null when the original amount is unavailable or its currency is not CNY; a known zero remains 0. Not rounded to two decimal places by the API.' },
  { name: 'exchange_rate_cny_per_usd', type: 'number', description: 'Fixed conversion rate: 7.2 CNY per USD. Applies to current and historical snapshots; it is not a live or date-specific exchange rate.' },
  { name: 'usage_5h_percent', type: 'number | null', description: 'Five-hour window usage percentage; 15 means 15%.' },
  { name: 'reset_5h_at', type: 'string | null', description: 'Five-hour window reset time as an RFC3339 timestamp.' },
  { name: 'usage_7d_percent', type: 'number | null', description: 'Seven-day window usage percentage.' },
  { name: 'reset_7d_at', type: 'string | null', description: 'Seven-day window reset time as an RFC3339 timestamp.' },
  { name: 'data_source', type: 'string', description: 'website for this vendor integration.' },
];

export type ErrorDefinition = { status: string; code: string; description: string };
const sharedErrors: ErrorDefinition[] = [
  { status: '401', code: 'INVALID_API_TOKEN', description: 'The token is missing, invalid, revoked, or expired, or the user cannot authenticate. Supply an active personal token.' },
  { status: '403', code: 'INSUFFICIENT_SCOPE', description: 'The token is valid but lacks the scope required by this endpoint.' },
];

export const lookupErrors: ErrorDefinition[] = [
  { status: '400', code: 'INVALID_REQUEST', description: 'Missing, invalid, duplicate, or unsupported query parameters, or a request body. Correct the request before retrying.' },
  ...sharedErrors,
  { status: '404', code: 'ACCOUNT_NOT_FOUND', description: 'No matching owned Claude account. A different owner’s username or account produces the same response.' },
  { status: '405', code: 'METHOD_NOT_ALLOWED', description: 'Use GET for this endpoint.' },
  { status: '429', code: 'RATE_LIMITED', description: 'The per-user limit of 600 lookup requests per minute has been reached. Wait for Retry-After before trying again.' },
  { status: '503', code: 'UNAVAILABLE', description: 'A required database or Redis service is unavailable. Retry with a delay.' },
];

export const submissionErrors: ErrorDefinition[] = [
  { status: '400', code: 'INVALID_REQUEST', description: 'Send one JSON object in the request body, with no query parameters.' },
  { status: '400', code: 'INVALID_PARAMETERS', description: 'A required field is missing, a value is invalid, or an unsupported top-level parameter was supplied.' },
  ...sharedErrors,
  { status: '405', code: 'METHOD_NOT_ALLOWED', description: 'Use POST for this endpoint.' },
  { status: '409', code: 'ACCOUNT_ALREADY_SUBMITTED', description: 'The same user, vendor, and account already have an accepted or currently running submission. Check Submission history.' },
  { status: '409', code: 'VENDOR_DISABLED / VENDOR_NOT_CONFIGURED', description: 'The vendor is disabled or its shared credential is not configured. Contact an administrator.' },
  { status: '409', code: 'PROTOCOL_VERSION_CHANGED', description: 'The vendor configuration changed while preparing the request. Check Submission history before submitting again.' },
  { status: '413', code: 'REQUEST_TOO_LARGE', description: 'The JSON request body exceeds 3 MiB.' },
  { status: '415', code: 'INVALID_CONTENT_TYPE', description: 'Send Content-Type: application/json.' },
  { status: '429', code: 'RATE_LIMITED', description: 'The per-user limit of 100 submission requests per minute has been reached. This limit is shared with the web form.' },
  { status: '502', code: 'UPSTREAM_CONNECTION_ERROR', description: 'The connection failed and the remote outcome is unconfirmed. A manual retry is allowed; no automatic retry is performed.' },
  { status: '502', code: 'UPSTREAM_RESPONSE_INVALID', description: 'The vendor response was incomplete or exceeded 256 KiB. The remote outcome may be unknown.' },
  { status: '503', code: 'UNAVAILABLE', description: 'A required local service is unavailable. Check Submission history before retrying if the outcome is uncertain.' },
  { status: '504', code: 'UPSTREAM_TIMEOUT', description: 'The vendor request timed out. The account may already exist remotely; a manual retry is allowed.' },
];

export const authJSONUpdateErrors: ErrorDefinition[] = [
  { status: '400', code: 'INVALID_REQUEST / INVALID_PARAMETERS / INVALID_AUTH_JSON', description: 'Send only username, account, and a complete valid auth_json, each once. The request body must fit within 3 MiB and auth_json within 2 MiB. Correct missing fields, timestamps, device IDs, or proxy URL before retrying.' },
  { status: '400', code: 'AUTH_JSON_EMAIL_MISMATCH', description: 'auth_json.email does not match the requested account after normalization. Supply credentials for that account.' },
  ...sharedErrors,
  { status: '404', code: 'ACCOUNT_NOT_FOUND / AUTH_JSON_ACCOUNT_NOT_FOUND', description: 'No owned local account matches, the username is not the token owner, or the supplier has no exact match for this account.' },
  { status: '405', code: 'METHOD_NOT_ALLOWED', description: 'Use POST for this endpoint.' },
  { status: '409', code: 'ACCOUNT_NOT_ACCEPTED / AUTH_JSON_UNSUPPORTED', description: 'The account has no accepted submission, or its vendor or plan does not support this operation.' },
  { status: '409', code: 'ACCOUNT_OWNERSHIP_CONFLICT / AUTH_JSON_ACCOUNT_AMBIGUOUS', description: 'Account ownership or supplier identity is ambiguous. An administrator must resolve it before credentials can be changed.' },
  { status: '409', code: 'AUTH_JSON_UPDATE_RUNNING / SYNC_RUNNING', description: 'Another update or collection is running. Wait for it to finish before starting a new request.' },
  { status: '409', code: 'AUTH_JSON_NOT_EDITABLE / ACCOUNT_CHANGED', description: 'The supplier does not allow editing in the current state, or the local account changed during the operation. Check the account before trying again.' },
  { status: '415', code: 'INVALID_CONTENT_TYPE', description: 'Send Content-Type: application/json.' },
  { status: '429', code: 'RATE_LIMITED', description: 'The update limit of 100 requests per minute per user has been reached. Shared with web updates; wait for Retry-After.' },
  { status: '429', code: 'SUPPLIER_RATE_LIMITED', description: 'A supplier cooldown or another login in progress prevented the write from starting. Wait for Retry-After. retry_stage identifies login, accounts, or login_lock.' },
  { status: '502', code: 'AUTH_JSON_UPDATE_REJECTED', description: 'The supplier explicitly rejected the replacement credentials. The error response does not include its credential-bearing body.' },
  { status: '502', code: 'AUTH_JSON_LOGIN_FAILED / AUTH_JSON_UNAVAILABLE', description: 'Supplier login, connection, or response processing failed. No update was confirmed. Contact an administrator if the issue persists.' },
  { status: '503', code: 'SYNC_NOT_CONFIGURED / UNAVAILABLE', description: 'Supplier website login has not been configured, or a required local service is unavailable.' },
  { status: '504', code: 'AUTH_JSON_UPDATE_UNCONFIRMED', description: 'The write may have reached the supplier, but its result could not be confirmed. The audit outcome is unknown. Check the account before a manual retry; do not retry automatically.' },
];

export const sampleSubmission = {
  plan: 'claude/cc-max-20x',
  account: 'claude.user@example.com',
  password: 'YOUR_ACCOUNT_PASSWORD',
  auth_json: {
    id_token: '',
    access_token: 'YOUR_ACCESS_TOKEN',
    refresh_token: 'YOUR_REFRESH_TOKEN',
    last_refresh: '2026-11-01T00:00:00Z',
    email: 'claude.user@example.com',
    account_uuid: 'YOUR_ACCOUNT_UUID',
    organization_uuid: 'YOUR_ORGANIZATION_UUID',
    organization_name: 'YOUR_ORGANIZATION',
    claude_device_ids: ['YOUR_DEVICE_ID'],
    type: 'YOUR_AUTH_TYPE',
    expired: '2026-12-01T00:00:00Z',
    proxy_url: 'http://user:password@proxy.example.com:8080',
    verification_status: 'YOUR_VERIFICATION_STATUS',
  },
  register_location: 'US',
  register_time: '2026-11-01',
  plan_start_time: '2026-11-01',
  plan_end_time: '2026-12-01',
  balance: 100,
  tags: ['速刷'],
  remark: 'Submitted via API',
};

export const sampleAuthJSONUpdate = {
  username: 'member@example.com',
  account: sampleSubmission.account,
  auth_json: sampleSubmission.auth_json,
};

export function curlExample(topic: EndpointTopic, baseURL: string) {
  const url = baseURL + endpoints[topic].path;
  if (topic === 'account-lookup') {
    return [
      'curl --silent --show-error --include --get \\',
      `  '${url}' \\`,
      '  -H "Authorization: Bearer ${TK_REGISTER_API_TOKEN}" \\',
      "  -H 'Accept: application/json' \\",
      "  --data-urlencode 'username=member@example.com' \\",
      "  --data-urlencode 'account=claude.user@example.com'",
    ].join('\n');
  }
  return [
    'curl --silent --show-error --include --request POST \\',
    `  '${url}' \\`,
    '  -H "Authorization: Bearer ${TK_REGISTER_API_TOKEN}" \\',
    "  -H 'Content-Type: application/json' \\",
    "  --data-binary @- <<'JSON'",
    JSON.stringify(topic === 'update-auth-json' ? sampleAuthJSONUpdate : sampleSubmission, null, 2),
    'JSON',
  ].join('\n');
}

export const lookupResponse = {
  success: true,
  data: {
    id: '1001',
    username: 'member@example.com',
    account: 'claude.user@example.com',
    vendor: 'tk-register-0',
    plan: 'claude/cc-max-20x',
    remote_id: '300001',
    platform_account_id: '15001',
    submission_status: 'accepted',
    snapshot: {
      id: '5001',
      account_id: '1001',
      platform_account_id: '15001',
      snapshot_date: '2026-11-01',
      collected_at: '2026-11-01T02:00:00+08:00',
      source_updated_at: null,
      account_status: 'active',
      input_tokens_total: 120000,
      output_tokens_total: 240000,
      cost_total: 123.45,
      currency: 'CNY',
      cost_total_usd: 17.145833333333332,
      exchange_rate_cny_per_usd: 7.2,
      usage_5h_percent: 15,
      reset_5h_at: '2026-11-01T06:00:00+08:00',
      usage_7d_percent: 42,
      reset_7d_at: '2026-11-07T00:00:00+08:00',
      data_source: 'website',
    },
    last_success_at: '2026-11-01T02:00:00+08:00',
    last_attempt_at: '2026-11-01T02:00:00+08:00',
    last_error: null,
    interval_seconds: 300,
  },
};

export const authJSONUpdateResponse = {
  success: true,
  data: {
    updated: true,
    audit_id: '2001',
    updated_at: '2026-11-01T02:05:00+08:00',
    refresh_status: 'pending',
    refresh_message: 'Auth JSON updated. Account refresh is scheduled after three seconds; failed refreshes are retried up to three times.',
    account: {
      ...lookupResponse.data,
      snapshot: { ...lookupResponse.data.snapshot, account_status: 'offline' },
    },
  },
};
