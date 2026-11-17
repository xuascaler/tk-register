// Never expose an upstream response, URL, proxy credential, or native error message.
export function fail(code, message, status = 400, extra = {}) {
  return Object.assign(new Error(message), { code, status, ...extra });
}

const messages = {
  invalid_request: 'The request is invalid.',
  invalid_owner: 'An explicit owner is required.',
  unauthorized: 'Internal authentication is required.',
  not_found: 'The endpoint does not exist.',
  invalid_proxy: 'The proxy URL is invalid.',
  proxy_required: 'A proxy is required.',
  proxy_forbidden: 'The proxy must resolve exclusively to public Internet addresses.',
  proxy_ip_mismatch: 'The proxy exit IP changed. Start a new authorization.',
  proxy_mismatch: 'The proxy does not match this authorization.',
  proxy_check_failed: 'The proxy exit IP could not be verified.',
  flow_not_found: 'The authorization is missing or expired.',
  flow_busy: 'This authorization is already being processed.',
  too_many_sessions: 'Too many pending authorizations. Try again later.',
  invalid_document: 'The credential document is invalid.',
  code_required: 'An authorization code is required.',
  invalid_code: 'The authorization code is invalid.',
  code_mismatch: 'This authorization already used a different code.',
  state_mismatch: 'The authorization state does not match.',
  authorization_declined: 'Authorization was not completed.',
  invalid_mode: 'The authorization mode is invalid.',
  invalid_setup_kind: 'The setup token mode is invalid.',
  session_expired: 'The authorization is missing or expired.',
  session_busy: 'This authorization is already being processed.',
  credential_busy: 'This credential is already being processed.',
  access_token_required: 'An access token is required.',
  invalid_session_key: 'The session key format is invalid.',
  invalid_token_response: 'The authorization response contained no access token.',
  organization_missing: 'The authorization returned no available organization.',
  authorization_response_invalid: 'The authorization returned no usable callback.',
  upstream_timeout: 'The upstream request timed out.',
  upstream_network_error: 'The upstream could not be reached through the proxy.',
  upstream_rejected: 'The upstream rejected the request.',
  upstream_rate_limited: 'The upstream rate limit was reached. Try again later.',
  upstream_invalid_response: 'The upstream response was invalid.',
  authorization_failed: 'Authorization failed. Start a new authorization.',
  internal_error: 'The credential operation could not be completed.',
};

export function publicError(error) {
  const code = Object.hasOwn(messages, error?.code) ? error.code : 'internal_error';
  const status = Number.isInteger(error?.status) && error.status >= 400 && error.status <= 599
    ? error.status : 500;
  return { status, body: { error: { code, message: messages[code] } } };
}
