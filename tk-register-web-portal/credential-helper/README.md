# Internal credential helper

This Node service adapts the OAuth/PKCE and Auth JSON conversion modules from
`claude-auth-exporter`. It **never submits supplier accounts, writes files, or reads
the exporter's `data` directory**. Go owns application authentication, persisted
drafts, validation, submission, authorization, and audit records.

## Runtime

- Node 20 or later; no third-party dependencies. Node 22 is supported.
- `npm ci --omit=dev --ignore-scripts`, then `npm start`.
- `TK_REGISTER_CREDENTIAL_HELPER_TOKEN` must be a nonempty internal secret shared
  with Go. Missing or malformed configuration prevents startup.
- The executable listens only on **127.0.0.1:3003**. Do not publish that port or
  proxy it through nginx. Every endpoint, including health, requires the exact
  `Authorization: Bearer <internal secret>` header.
- Responses use `Cache-Control: no-store`. Request bodies and upstream responses
  are not logged. Public errors are fixed messages, never raw network errors.
- Flow state exists only in memory and expires 30 minutes after creation. Restarting
  the helper invalidates in-progress flows. Already persisted Go drafts are separate.

## Internal API

All POST bodies are JSON. `owner_id` is required and is normalized to a string;
Go must derive it from the authenticated application user, never trust browser input.
Unknown request fields are rejected. In particular, upstream URLs cannot be supplied.

| Method and path | Request fields | Success |
| --- | --- | --- |
| `GET /healthz` | None | `{ "ok": true }` |
| `POST /proxy-test` | `owner_id`, `proxy_url` | Only `owner_id` and `proxy_check`; no flow or expiry |
| `POST /start` | `owner_id`, `proxy_url`, optional `mode` (`oauth` or `setup`, default `oauth`), `setup_kind` (`full` or `official`, default `full`) | Common fields plus `auth_url` |
| `POST /exchange` | `owner_id`, `flow_id`, `code` (code, `code#state`, or callback URL) | Common fields plus `auth` and `notes` |
| `POST /import` | `owner_id`, `proxy_url`, `kind` (`json`, `cookie`, `token`), kind-specific fields below | Common fields plus `auth` and `notes` |
| `POST /verify` | `owner_id`, `flow_id`, optional `proxy_url` which must equal the bound value | Common fields |

Common fields:

```json
{
  "owner_id": "42",
  "flow_id": "opaque-random-value",
  "expires_at": "2026-01-01T00:30:00.000Z",
  "proxy_check": {
    "ok": true,
    "ip": "8.8.4.4",
    "checked_at": "2026-01-01T00:00:00.000Z",
    "elapsed_ms": 123
  }
}
```

Import-specific fields:

- `json`: `document` is a JSON object, account array, or JSON string containing
  either. `account_index` is an optional nonnegative integer, default 0.
- `cookie`: `session_key` is the `sk-ant-sid…` session key. Cookie authorization
  obtains full OAuth scope using the same proxy and PKCE token endpoint.
- `token`: `access_token` is required; `refresh_token` and `expires_at` are optional.
  `expires_at` accepts an ISO date string or epoch timestamp. No refresh token or
  expiry is fabricated when omitted.

`auth` is the standard Claude object with `access_token`, `account_uuid`,
`claude_device_ids` (array), `disabled` (boolean), `email`, `expired`, `id_token`,
`last_refresh`, `organization_name`, `organization_uuid`, `refresh_token`, `type`,
`proxy_url`, and `verification_status`. `notes` is a string array.

The converter retains original template conventions: generated device IDs,
`type: "claude"`, `verification_status: "verified"`, and conversion/export time
when the refresh time is absent. **That marker is not proof of credential validity.**
Notes disclose generated or missing information. Missing expiry, identity, or refresh
token stays missing so Go can require the user to supply actual values before submit.
The proxy in imported JSON never overrides the proxy bound to the flow.

Errors use non-2xx status and `{ "error": { "code": "…", "message": "…" } }`.
Missing, expired, evicted, or other-owner flows all return `flow_not_found` (404).
Go should map known error codes to its own user messages and avoid recording bodies.

`/proxy-test` is a stateless connection check for the Proxy field. It makes one
request to the fixed IP check endpoint through the supplied proxy, with a
15-second timeout, and does not create a flow, contact Claude, or retain credentials.
Known HTTP 407 or SOCKS authentication rejection returns
`proxy_authentication_failed` (502); connection timeout returns `upstream_timeout`
(504). Messages never contain the proxy address, password, or upstream response.
Use `/verify` for an existing authorization: it also checks the already bound exit IP.

## Retry, timeout, and memory behavior

- PKCE verifier and the original proxy remain bound to a random owner-specific flow.
- The proxy exit IP is checked at start and before exchange/import/verify. A changed
  exit IP returns `proxy_ip_mismatch` and requires a new authorization.
- Concurrent exchange calls for the same owner/flow/code share one operation.
  Successfully exchanged credentials are cached. Retrying after Go persistence
  failure returns the cached result without redeeming the code a second time.
- A token obtained before profile lookup fails is retained, so retry only needs
  proxy verification and profile lookup. A rejected/invalid code can be corrected
  until token redemption succeeds. After that a different code is rejected.
- Repeating the same import is also idempotent within the flow lifetime, including
  a Cookie grant whose profile lookup needs retry. The import key is an in-memory
  digest scoped to owner and proxy; it is not logged or persisted.
- Each operation has a 50-second upstream budget, leaving headroom below Go's
  55-second helper timeout. Proxy checking is capped at 15 seconds; other requests
  are capped at 25 seconds or the remaining operation budget. No failed request
  falls back to a direct connection.
- At most 10 flows per owner and 200 globally are held. On reaching a limit, the
  oldest completed results are evicted first. Running and unfinished authorizations
  are never evicted to make room. A completed result is therefore retryable until
  expiry or bounded-cache eviction; normal consecutive submissions do not exhaust
  the flow limit.

## Proxy and upstream boundaries

HTTP, HTTPS, SOCKS5 and SOCKS5h proxies are supported. Proxy hostname DNS results
must all be public Internet addresses. Loopback, private, link-local, metadata,
reserved, mapped IPv4/IPv6 and transition ranges are rejected. The connection is
made to the **validated IP address**, never a second hostname resolution; TLS uses
the original proxy and upstream hostnames for SNI/certificate verification.

HTTP(S) uses CONNECT. SOCKS targets are resolved through the proxy for both SOCKS
schemes. Only the fixed Claude authorization/profile/token endpoints and the fixed
IP check endpoint are allowed; redirects are rejected. There is no insecure TLS
option, caller-selected upstream URL, direct-connect mode, or supplier submission
endpoint in this service.

## Tests

Run `npm test`. Tests use only local mock proxy servers and fake credentials. They
cover HTTP(S)/SOCKS5(h), proxy authentication, DNS pinning/private-address rejection,
fixed endpoint allowlists, failure without direct fallback, owner isolation, PKCE,
expiry, idempotent redemption, imports, budgeted retries, and error redaction.
The test runner needs permission to bind loopback sockets; no Internet access is
required.
