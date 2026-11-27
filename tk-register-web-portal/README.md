# TK Register

An email account subsystem at `/tk-register`, styled to match the GoAPI portal. It has its own users and configuration in the `tk_register` database. A TK account does not create a New API user or grant access to GoAPI billing.

## Standalone runtime

This repository uses the standalone HTTPS deployment described in the
[root README](../README.md). The GoAPI integration below is retained as historical
integration documentation; its combined Dockerfile and portal live in the parent
GoAPI repository, not this standalone checkout.

## Legacy GoAPI integrated runtime

```text
HTTPS load balancer → GoAPI portal :8888
  /tk-register and /tk-register/* → 127.0.0.1:3002 (TK Register)
  existing GoAPI API routes       → 127.0.0.1:3000 (New API)
```

All three processes run in the same container. The portal starts, monitors, and shuts down both children. TK has no published port. When enabled, the container health check includes TK database and Redis readiness.

`user-web-portal/Dockerfile` remains the existing GoAPI-only build. Build the combined image from the repository root:

```sh
docker build -f user-web-portal/deployment/Dockerfile.combined \
  --build-arg NEW_API_BASE_IMAGE=<your-current-New-API-backend-image> \
  -t goapi-user-web-portal:tk-register .
```

Use the current backend image to preserve deployed New API changes. The combined build compiles both frontends and runs both Go test suites. Layer the supplied `tk-register-web-portal/deployment/docker-compose.tk-register.yml` over the existing GoAPI compose file. It only changes the existing `user-web-portal` service.

## Private runtime settings

| Variable                          | Purpose                                                                                                        |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `TK_REGISTER_ENABLED=true`        | Start and route to the subsystem. Disabled by default.                                                         |
| `TK_REGISTER_SQL_DRIVER=mysql`    | MySQL in production; SQLite and PostgreSQL are also supported.                                                 |
| `TK_REGISTER_SQL_DSN`             | Separate database DSN targeting `tk_register`; never reuse the GoAPI database.                                 |
| `TK_REGISTER_ENCRYPTION_KEY`      | Stable random secret of at least 32 characters, used to encrypt SMTP credentials and authenticate code hashes. |
| `TK_REGISTER_ADMIN_EMAIL`         | First administrator email, `xu.guoqiang.ascaler@gmail.com`.                                                    |
| `TK_REGISTER_PUBLIC_ORIGIN`       | Exact browser origin, `https://goapi.cloud`. Used for request origin validation.                               |
| `TK_REGISTER_PORT`                | Internal loopback port, default `3002`.                                                                        |
| `TK_REGISTER_ASSETS`              | Built frontend directory, set by the image.                                                                    |
| `TK_REGISTER_REDIS_URL`           | Optional override; normally inherits `REDIS_CONN_STRING` from GoAPI.                                           |
| `TK_REGISTER_TRUSTED_PROXY_CIDRS` | Optional comma-separated trusted load balancer/proxy networks. Without this, IP limits use the direct peer.    |

Keep secrets in the private deployment environment, not Git. Generate the encryption key once, preserve it through rebuilds, and back it up securely with the database. Changing it prevents decryption of saved SMTP credentials.

On first startup, TK copies SMTP settings from the existing GoAPI `options` table using the inherited `SQL_DSN`. It reads that database and writes only its own `settings` table. SMTP credentials are encrypted before storage. A marker prevents future startup from overwriting TK Config changes. If no GoAPI SMTP server is configured, the import remains pending. SMTP uses verified TLS or STARTTLS; there is no insecure plaintext mode.

The administrator must register and verify the configured mailbox. No default password or automatically authenticated administrator is created. Subsequent admin sign-ins require password verification followed by an emailed code.

## Authentication

The subsystem API prefix is `/tk-register/api`.

- Username is the normalized email address, stored once; maximum 50 characters.
- Registration requires a code and password. The UI also requires password confirmation.
- Members can use password or email-code sign-in.
- Admins always need both password and email code, regardless of member sign-in settings.
- Codes use six letters/numbers, default ten-minute lifetime, a sixty-second resend cooldown, and five failed attempts. Successful use atomically consumes the code.
- Sessions use an opaque random cookie, `HttpOnly`, `Secure`, `SameSite=Lax`, scoped to `/tk-register`. Only its hash is used in Redis keys. Authenticated writes also require the CSRF token returned by the session API.
- Session defaults: members seven days; admins eight hours absolute and thirty minutes idle.
- Redis failures fail closed. Codes, attempts, admin challenges, and sessions use the `tk-register:{tk-register}:` prefix. The shared `{tk-register}` hash tag keeps multi-key Lua scripts and transactions in one Redis slot, including operations that combine email and IP limits or a session and its user index. No GoAPI Redis keys are modified.

## Storage

| Table                 | Contents                                                                                                 |
| --------------------- | -------------------------------------------------------------------------------------------------------- |
| `users`               | Unique email, password hash, role, status, verified timestamp, created/updated timestamps.               |
| `settings`            | Key/value, category, type, secret flag, updater and timestamps. SMTP password is encrypted.              |
| `email_verifications` | Unique random request ID, email, purpose, delivery/consumption status and timestamps. No plaintext code. |

Redis stores short-lived code hashes, cooldowns, attempt counters, administrator challenges, sessions and user-session indexes. SQL verification rows are audit records, not reusable credentials. Database constraints enforce unique emails and verification request IDs.

When upgrading from the earlier untagged `tk-register:` key layout, existing verification codes and login sessions become invalid. Users must request a new code or sign in again. Old Redis entries expire naturally; no SQL accounts, passwords, settings or audit records are changed. Do not remove unrelated GoAPI keys.

## Config

Administrators edit a typed Key / Value / Description table grouped into Email delivery, Access, Verification, and Sessions. Numeric `*_seconds` values use seconds with human-readable hints. Unknown keys and invalid values are rejected. Changes across categories save in one database transaction. Blank SMTP password means keep the existing secret; the current value is never sent to the browser.

## Account usage and scheduled collection

**My account → Accounts** shows saved status, usage, cumulative spending and 7/30/90-day trends. **Config → Account sync** configures supplier website credentials and collection frequency in seconds (default 300 seconds, paused until configured). Runs reuse a valid supplier login session; failed runs preserve the last successful data. Scheduled rounds do not overlap or accumulate missed work. Usage trends render smooth curves from bounded representative observations, while original snapshots remain available through paginated history. See the [Account usage collection guide](docs/account-sync.md) for setup, data semantics and API behavior.

## Vendor account submissions

The workspace includes **Submit account**, **Submission history**, and **Account settings**. A history entry opens its account overview, billing, original submitted parameters, and execution history. The Back action returns to history; bookmarked detail routes restore after sign-in. Layouts support desktop, tablet, and mobile widths.

The first vendor is `tk-register-0`, with its HTTPS origin fixed to `https://120.233.254.152:8000`. Its three supported protocols are:

| Protocol        | Vendor endpoint                                      | Purpose                                                                                 |
| --------------- | ---------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Submit account  | `POST /supplier-account/api/openapi/claude/accounts` | Submit the required account fields and authentication JSON.                             |
| Account metrics | `POST /supplier-account/api/openapi/metrics/query`   | Query one owned account's current status and period activity, using `aggregate: false`. |
| Account bills   | `POST /supplier-account/api/openapi/bill/query`      | Query period totals in CNY, including cache read/write tokens.                          |

Administrators configure the shared vendor Bearer credential in **Config → Vendors**. No token is seeded. Until a token is supplied, the UI explains the missing configuration and the server rejects submissions and queries. Blank token updates retain the existing credential. Saving a new token encrypts it and creates new protocol versions; historical executions keep their original version reference. Disabling the vendor prevents new submissions and queries while retaining history.

Metrics cover at most seven days and billing starts within the past ninety days. The default vendor timezone is `Asia/Shanghai`, configurable by an administrator; confirm it with the vendor before live use. Account status is distinct from submission status: an accepted submission can still await remote review. Metrics `cost` is deprecated and is not treated as a charge. Five-hour/seven-day quota windows, reset times, daily bill lines and lifetime currency conversions are not supplied by this API and are not fabricated.

### Business storage and identifiers

All new tables are in `tk_register`, with foreign-key references and restrictive deletion:

| Table                 | References and stored data                                                                                                                                                                |
| --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `vendors`             | Vendor identity, fixed base URL, enabled state and timezone.                                                                                                                              |
| `vendor_credentials`  | `vendor_id → vendors.id`, `created_by → users.id`; encrypted Bearer credential.                                                                                                           |
| `vendor_protocols`    | `vendor_id → vendors.id`; protocol name, code and push/query operation.                                                                                                                   |
| `protocol_versions`   | `protocol_id → vendor_protocols.id`, nullable `credential_id → vendor_credentials.id`; immutable request/response definitions and version number.                                         |
| `protocol_parameters` | `version_id → protocol_versions.id`; parameter key/type/required/secret/format/order.                                                                                                     |
| `submissions`         | `user_id → users.id`, `vendor_id → vendors.id`, `protocol_version_id → protocol_versions.id`; original parameters, encrypted password/auth JSON, submission state and remote identifiers. |
| `protocol_executions` | `submission_id → submissions.id`, `protocol_version_id → protocol_versions.id`; unique request ID, remote ID snapshot, redacted request/result, response status and timestamps.           |

`submission.id` is the local history record. `execution.id` identifies one push/query attempt. `request_id` is a unique execution trace identifier. `remote_id` is the vendor creation response `id`, and is the only ID used in vendor queries. The metrics `aid` is a separate platform account ID. New API DTOs serialize identifiers as strings to avoid JavaScript integer precision loss.

Every submission and execution is saved before a remote request. A per-user browser client request key is unique, and repeating that key with identical parameters returns the saved result instead of sending again. A new request for an already accepted or currently running account is blocked for the same user and vendor. Lost responses/timeouts become **Unknown**. The user may explicitly submit again after an unknown or failed result; this creates a new history record and leaves the earlier attempt unchanged. The system never automatically retries account creation. Interrupted operations are recovered to Unknown after the execution window. Upgrades release duplicate-account locks held by old Unknown records while keeping accepted/running records protected. Query results can be refreshed manually.

After receiving the supplier response, TK commits execution status, submission status,
account linkage and follow-up sync work in one local transaction. The encrypted
Auth JSON remains in the existing submission history; finalization does not write
a separate current-credentials record.
The account projection is locked/materialized once and passed to sync-job
persistence; it is not upserted again after inserting the sync job. Submit
finalization does not insert, lock or update any current-credentials row.
The local transaction is attempted once, without automatic retries, including on
MySQL `1213` errors. `TK_REGISTER_PERSISTENCE` logs submission/execution/request IDs,
the supplier's returned account ID and alias, upstream result/status, failed step,
elapsed time, rollback outcome and safe database error codes. It omits SQL,
exception text, passwords, Auth JSON and full response bodies. COMMIT errors are
explicitly recorded as `commit_unknown`; a subsequent rollback is not treated as
proof that the supplier response was not saved.

Set `TK_REGISTER_LOG_FILE` to an absolute path to also write backend logs to disk.
Bind that directory to a persistent host directory; only one server process should
write each file. Files use mode `0600`, rotate at 50 MiB, compress backups, and retain
at most 20 backups for up to 30 days (older backups are cleaned during rotation).
Container stderr remains available; disk-write failures emit
`TK_REGISTER_LOG_WRITE_FAILED` there. Tokyo uses host
`/home/ubuntu/Code/tk_register/logs/server.log`, mounted at
`/var/log/tk-register/server.log`. This includes submission persistence diagnostics,
supplier errors and backend worker logs; nginx and the credential helper retain
their existing Docker logs. Old container logs are not retroactively imported.


The execution adapter supports the three published HTTP JSON contracts above. Their definitions and parameter metadata are persisted; arbitrary protocols or host overrides cannot be supplied from the browser. At the deployment owner's request, only `submit_account`, `metrics` and `bills` requests for `tk-register-0` at `https://120.233.254.152:8000` skip certificate verification. HTTPS with TLS 1.2 or later remains in use, but the vendor's identity is not verified for those account submissions, status checks and billing queries. Other connections retain certificate verification. Redirects are rejected, and request/response sizes and durations are bounded. Passwords, auth JSON and vendor tokens are not returned in history or written to browser storage. Users can read/query only their own submissions; administrator configuration uses the existing role and CSRF checks.

`POST /tk-register/api/submissions/:id/status` refreshes an accepted account's status using a server-generated last-24-hours metrics range in the vendor's timezone. The endpoint accepts `{ "force": false }` (the default), which can reuse a successful status check for 30 seconds; `{ "force": true }` requests a fresh check. It uses the existing owner, CSRF and query-rate-limit checks and never retries account creation. `status_checked_at` is derived from the latest successful metrics snapshot, separately from billing checks. A successful response without a matching account or status clears the old status rather than presenting it as current.

### Workspace API

All paths below are relative to `/tk-register/api` and require a TK session. Writes require the session's CSRF token.

| Method and path                           | Behavior                                                                                                   |
| ----------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `GET /vendors`                            | Available vendor/protocol descriptors and configuration readiness, never secrets.                          |
| `POST /submissions`                       | Validate, persist and push `{ vendor_id, protocol_version_id, client_request_key, parameters }`.           |
| `GET /submissions?page=1&search=&status=` | Paginated history owned by the current user.                                                               |
| `GET /submissions/:id`                    | Owned submission, execution history, latest metrics and bills.                                             |
| `POST /submissions/:id/metrics`           | Query with `{ begin_time, end_time }` in the configured vendor timezone. Remote ID is derived server-side. |
| `POST /submissions/:id/bills`             | Query the submission's bills for `{ start_ds, end_ds }` using the signed-in user's permitted source.        |
| `GET /admin/vendors/:id`                  | Administrator-only vendor configuration; token is write-only.                                              |
| `PUT /admin/vendors/:id`                  | Administrator-only update of `{ enabled, timezone, access_token }`.                                        |

## Resubmitting deleted accounts

Browser and personal-token submissions share one route decision. The server searches completed `deleted_account` history by vendor and normalized account email across all users. No deletion history uses the existing OpenAPI creation flow. With deletion history, a complete exact-match supplier website lookup selects an update of the original supplier row; a confirmed absent account falls back to OpenAPI creation. Authentication failures, incomplete searches, duplicate matches, timeouts, and a changed supplier row ID never authorize fallback creation.

The update uses the newly submitted password, Auth JSON, plan, dates and supported metadata. Omitted optional fields preserve the supplier's current values. The supplier edit form cannot change tags: omit tags or submit the existing tags; a requested tag change returns `supplier_restore_tags_unsupported`. Supplier locks and credential-edit restrictions still apply.

A confirmed operation creates a new accepted submission owned by its current submitter, even when another user originally deleted it. The original owner's account remains archived and historical records are not transferred. Archived submissions cannot run fresh status or billing queries. A same-owner restoration reuses the local account row with a new lifetime; a different owner gets their own local account row. Both queue the existing five follow-up collections. Old usage snapshots are retained separately; restoring an account does not reset its upstream quota.

Submission details expose `submit_mode`: `create`, `restore_update`, or `create_fallback`. External API responses also include `X-Submission-Mode`. Successful updates return HTTP 200 with the verified existing numeric `id`, supplier `alias` (using the existing public masking), and `submission_mode: "restore_update"`.

Requests for the same supplier/email share the account mutation lease with deletion and Auth JSON edits. Restoration requests also have a durable in-flight/unknown guard, including fallback creation. Repeated browser request keys replay the saved result. An uncertain supplier write remains Unknown and blocks resubmission across restarts; it needs administrator reconciliation, because the supplier does not echo credentials and a metadata read cannot prove that all new credentials were saved. Writes are never automatically retried.

## User API tokens and external APIs

**My account → API documentation** is the in-app reference for every public token API. Open `/tk-register#/docs` for the endpoint directory, authentication and limits; each endpoint has linkable parameter, cURL, response and error sections. Examples use the current website origin. The guides never send live API requests or include the signed-in user's token. Profile, sign-in details and token management remain in **Account settings**.

**Account settings → API token** lets an authenticated user generate, regenerate, copy and revoke their personal token for account submission, saved account lookup, and Auth JSON updates. The full token is returned only when generated or regenerated and is held only in the current page's memory. Reloading shows a masked identifier; lost tokens must be regenerated. Regeneration invalidates the previous token, and revocation prevents new requests with it. Requests already accepted for execution may finish.

Tokens use 32 random bytes with a `tkr_` prefix. Only their SHA-256 digest and display fragments are stored in `user_api_tokens`; `user_id → users.id` is unique. The table also records scope, revision, creation, last-use, expiry and revocation timestamps. New tokens have `claude:submit`, `accounts:read`, and `claude:auth-json:update` scopes. The original account-lookup migration adds `accounts:read` to submission tokens. A separate one-time migration grants existing tokens Auth JSON update access without changing their value or revocation/expiry state; no regeneration is needed. Later restarts do not reapply the grants or overwrite subsequent scope changes. Safe token metadata returns the actual `scopes` array. An administrator's personal token has the same owner-only permissions as a member's token. Tokens never authorize configuration, user administration, browser sessions or token management. Disabled, missing or unverified users cannot authenticate with a token.

| Browser method and path | Behavior |
| --- | --- |
| `GET /tk-register/api/account/api-token` | Masked metadata or `token: null`; never returns the saved plaintext. |
| `POST /tk-register/api/account/api-token` | Generate with `{}` when no active token exists. |
| `POST /tk-register/api/account/api-token/rotate` | Regenerate with `{ "token_id": "...", "revision": 1 }`. |
| `DELETE /tk-register/api/account/api-token` | Revoke with the current token ID and revision. |

These management routes require the browser session; writes also require the existing CSRF and origin checks. Stale rotate/revoke requests return 409 rather than changing a newer token.

The external submission endpoint is:

```text
POST https://47.110.71.25:8443/tk-register/api/v1/vendors/tk-register-0/claude/accounts
Authorization: Bearer <your TK Register API token>
Content-Type: application/json
```

Send the account fields directly as one JSON object. No browser cookie or CSRF token is required. Vendor code is resolved from the route and the current submission protocol is selected by the server. The same validation as the webpage applies, including the three supported Claude plans, required authentication JSON fields, dates, mutually exclusive tags, `third_party_payment`, and registration locations US, MY, ID, SG and PH. Authentication JSON can be an object or its JSON string. User IDs, vendor credentials, proxy-routing options and protocol IDs cannot be supplied as top-level account parameters.

The upstream request uses the administrator-configured vendor credential, never the user's API token. API records have `submissions.source = api` and `api_token_id → user_api_tokens.id`; browser records use `source = web`. Both appear in the same owner-scoped history and administrator account pool.

Submission responses preserve the upstream HTTP status. Members and regular administrators receive a JSON response containing the supported submission identity and status fields. Super administrators with model visibility enabled retain the original content type and the complete credential-redacted response. In the top-level JSON `alias` string, `温州润晨` is replaced with `xxxxx`, matching the frontend display. The original alias is retained in storage; account IDs and other business fields are not changed by this masking. If an upstream response echoes the shared vendor credential, that credential is redacted before forwarding. Locally rejected requests use the usual error envelope: 401 for invalid/revoked tokens, 400 for invalid parameters, 409 for an accepted/running duplicate and 429 for the shared per-user submission limit (100 attempts per minute across browser and API). The JSON request limit is 3 MiB. If no complete upstream response is received, connection errors return 502 and timeouts return 504; oversized responses above 256 KiB also return 502. A recorded uncertain attempt remains Unknown and can be manually retried with a new request. Account creation is never retried automatically.

`X-Request-ID` identifies the request/execution. `X-Submission-ID` is included only after a local submission record exists. These are local tracking headers; an `id` in the vendor response remains the vendor's ID. Each new API POST is a new manual attempt, subject to accepted/running duplicate protection.

A newly accepted browser or API submission with a valid vendor ID queues one account Sync job. Its first targeted collection becomes due ten seconds after acceptance; the job performs five collection rounds regardless of each round's success or failure, waiting ten seconds after each completed round before the next. The original response does not wait for collection. A busy shared collection lease defers the pending job five seconds without consuming a round. Known supplier cooldowns and login-lock waits also postpone execution without consuming a round; an actual failed supplier request counts. These rounds only read account status and usage; they never repeat account creation. Each success updates the usual account snapshot, while failures preserve the last successful data. Saved supplier website credentials are required independently of whether scheduled collection is enabled. Unsuccessful submissions and historical accepted records are not backfilled. Opening the submission detail does not issue an additional automatic status query. See the [automatic synchronization details](docs/account-sync.md#automatic-sync-after-account-operations).

The Accounts list and usage details read saved snapshots from scheduled collection, successful browser/API operations and explicit collection; opening or refreshing the list does not query the supplier. Each list row has a Sync button; account details retain Sync now. Submission detail status and billing queries remain separate from this collection flow.

### Saved account lookup

```text
GET /tk-register/api/v1/vendors/tk-register-0/claude/accounts/lookup?username=<owner-email>&account=<submitted-account>
Authorization: Bearer <your TK Register API token>
```

This endpoint requires `accounts:read` and both exact query identifiers, with no request body. It returns an owned account's saved status, cumulative usage, last successful collection time and sanitized latest collection error. It never triggers supplier login or synchronization. A personal administrator token cannot read another user's accounts. Missing or out-of-scope records return the same 404 response; the lookup limit is 600 requests per minute per user. Submission requests use a separate per-user limit.

See the [Account Lookup API reference](docs/account-lookup-api.md) for cURL examples, response fields, null/zero/freshness behavior and error handling. The [Account usage collection guide](docs/account-sync.md) explains the default 300-second collection schedule and administrator configuration.

### Saved daily usage batch API

```http
POST /tk-register/api/v1/vendors/tk-register-0/claude/accounts/daily-usage
Authorization: Bearer <personal token with accounts:read>
Content-Type: application/json
```

Send exactly `username`, `accounts` (1–100 input strings), and `date` (`YYYY-MM-DD`, yesterday or earlier in `Asia/Shanghai`). The username must match the token owner, including administrator and super-administrator tokens. Requests never include sub-users implicitly. Account identifiers are normalized, deduplicated, and returned in first occurrence order; the input limit applies before deduplication. The JSON body limit is 64 KiB, and the separate per-user limit is 60 requests per minute with `Retry-After` on 429.

This endpoint only reads saved `account_daily_usages_modifiled` bills for the token owner's matching accounts. It does not use global model source selection, fetch supplier bills, trigger synchronization, read Raw fallback data, or treat missing days as zero. Account results are `ok`, `no_data`, or `not_found`; inaccessible accounts use the same `not_found` result as missing accounts. A username mismatch rejects the request with 404, while mixed or entirely unavailable account batches return 200 with availability counts and `summary.status` (`complete`, `partial`, or `no_data`).

Costs are decimal strings (`cost_cny` with eight places, `cost_usd` with two), with the configured exchange rate returned as a string. Missing bill amounts are null. USD summary totals sum the per-account USD amounts; per-model cents are reconciled within each account so account/model totals agree.

**Config → Overview → `api_user_overview_model_costs_enabled`** is a super-administrator-only boolean, default `false`. It controls model details in this API independently of `user_overview_model_costs_enabled` and `model_usage_source`. Choosing Raw for the website must not disable this API setting. When false, responses omit every account's `models` and the top-level `model_totals`, while preserving account and summary totals. When true, unavailable accounts have empty model arrays. The public API documentation describes saved daily bills without exposing internal source or ratio fields.

See the [Daily Usage API reference](docs/daily-usage-api.md) for request examples, complete response fields, precision rules, and errors. The in-app guide is `/tk-register#/docs/daily-usage`.

### Auth JSON updates

```http
POST /tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json
Authorization: Bearer <personal token>
Content-Type: application/json
```

Send `username`, `account`, and a complete `auth_json` object or JSON string. The token requires `claude:auth-json:update`; `username` must match its owner, and `auth_json.email` must match the submitted account after normalization. The API reuses the web editor's ownership, supplier identity, editability, locking, and update checks. Its limit is 100 update requests per minute per user, shared with web updates. It never automatically retries a supplier write.

A confirmed browser or API Auth JSON update returns HTTP 200 with `updated: true` and `refresh_status: not_requested`. It does not collect status or queue follow-up collections. Its `account` contains previously saved data, or is null if local readback fails. The existing scheduled collector updates status and usage independently; saving credentials never marks an account Active. Account creation retains its five-round follow-up policy. Neither operation repeats the supplier write automatically.

The existing **Authentication → Update Auth JSON** dialog supports **Authorize & update** as well as manual **Upload Auth JSON**. Authorization uses the same OAuth, Setup Token, JSON import, session-cookie and token helper as account submission, with the target email and Account ID locked. Test the current proxy before generating an authorization link; clicking Generate without a successful test shows an inline prompt. Changing the proxy discards its prior test and authorization. The backend binds each draft to its actor and target account lifetime, rejects a different authorized email, and finishes through the account update interface without creating a submission. See [guided credential replacement](docs/auth-json-authorize-update.md).

Each executed update has an audit record with source, actor, owner, account, token reference/revision, and separate update/refresh outcomes. Before the supplier write, the audit saves the complete submitted Auth JSON encrypted in `auth_json_ciphertext` (LONGTEXT, nullable for legacy rows). Its payload is immutable; completion updates only operation outcomes and account metadata. Ciphertext is excluded from JSON serialization. Updates do not create another submission. Keep `X-Request-ID` and, when present, `X-Auth-JSON-Audit-ID` for support.

See the [Auth JSON Update API reference](docs/auth-json-update-api.md) for the full validation, cURL, response, audit, and error contract. The in-app guide is at `/tk-register#/docs/update-auth-json`.

The adjacent **Deactivate** action generates Auth JSON for the same existing
account and offers download/copy without submitting it to the supplier or
triggering status collection. It uses independent account-bound authorization
drafts and metadata audits. See [account-scoped credential generation](docs/account-deactivate.md).

All three guided authorization entries share bounded error details and correlated
operational logs, including the Cookie/sessionKey flow. See
[authorization diagnostics](docs/authorization-diagnostics.md) for stages, response
fields, and log privacy.

### Update Proxy URL

The adjacent **Update Proxy URL** dialog changes only the root `proxy_url` in the account's last confirmed Auth JSON. It is available to the account owner and administrators with the same permissions as Update Auth JSON. The complete saved credentials are never returned to the browser; the existing proxy is shown with its username/password hidden. The new URL must pass a connection test before saving, and editing the URL invalidates that test.

The session routes are `GET/POST /accounts/:id/proxy-url` and `POST /accounts/:id/proxy-url-test`, with the corresponding `/admin/synced-accounts/:id/…` routes for administrators. These use the existing session, same-origin and CSRF checks. The connection-test proof expires after 600 seconds and is bound to the actor, account identity, accepted submission, opaque history revision and proxy digest. Save sends only `proxy_url`, `revision`, and `test_token`; the server constructs the full Auth JSON. These routes are not personal-token API endpoints.

Proxy updates read the newest confirmed credentials for the current owner/vendor/account identity and accepted submission from `account_auth_json_audits`, falling back to that accepted `submissions` record only when no later update may have changed its credentials. A successful legacy update without `auth_json_ciphertext` blocks fallback and requires a complete **Update Auth JSON** operation. A newer pending/unknown result also blocks the shortcut, even if followed by an explicitly failed/rejected attempt. A later successful complete update establishes a new confirmed snapshot. Histories from older accepted submissions are never reused.

Startup adds the nullable encrypted payload column and `(account_id, submission_id, id)` index to update history. It neither creates nor reads/writes `account_credentials`; any existing obsolete table is left untouched, with no migration or backfill. Existing history rows remain unchanged. New Submit operations continue saving encrypted Auth JSON only in submission history. All manual/guided web/API updates and proxy updates save their own full encrypted snapshots in update history.

Proxy and Auth JSON updates share the account operation locks and recheck supplier identity, ownership, lock/status restrictions and the accepted submission before sending. An outdated dialog returns `ACCOUNT_CREDENTIALS_CHANGED` and must be reopened. The `revision` is a 64-character opaque history identity derived from the accepted account lifetime and source record, not a numeric counter. The pending candidate is saved in its own encrypted history row before dispatch; only confirmed success makes it the selected version. An uncertain response retains all history and disables further proxy shortcuts. The dialog shows **Unable to confirm the update result**, explains that the update may have succeeded, and offers only **Close**. There is no automatic resubmission or inference of success from status collection; the supplier cannot return complete current Auth JSON.

Build the frontend, then run `npm run test:proxy-url` for isolated bilingual desktop/mobile browser coverage with synthetic API responses. Backend tests cover initial submissions, conservative migration, encryption, revision conflicts, permissions, shared locks and uncertain outcomes.

## Administrator users and account pool

**Config → Users** lists TK Register users, roles, account status and registration time. **Add admin** and **Del admin** accept an existing user's email; changing a role does not create or delete an account. Roles are read-only in the table. The current administrator and bootstrap administrator cannot be demoted, and the server prevents removing the last active administrator. A promoted administrator must sign in with a password and email code.

**Create account** opens the email registration form in the Users page. Enter the new user's email, request and verify an email code, and set and confirm a password. The email is the username. New accounts start as members; granting administrator access remains a separate **Add admin** operation. Successful creation refreshes the user list without signing in as the new user or replacing the current administrator's session. The form follows the current registration setting, code lifetime, resend cooldown, email/IP limits and password rules. Turning registration off also disables administrator account creation.

Role updates are serialized and saved in one database transaction with an `admin_role_audits` record (`actor_id` and `target_id` reference `users.id`). Each change increments `users.auth_version`. Sessions and pending administrator login challenges carry that version and are rejected after a role change, even if the user's role is subsequently restored. Existing users migrate with version zero, so the deployment itself does not sign everyone out. `admin_role_guards` contains the singleton row used to serialize concurrent role changes.

**Management → Account pool → Accounts** groups submissions by vendor, submitting user and trimmed/case-normalized account identifier. Different owners' accounts are never merged. Latest submit status uses `created_at DESC, id DESC`; current account status comes from the newest accepted submission with a remote ID. A later failed submission does not replace that accepted account. A newer accepted record becomes the query target even when its first status query has not completed.

**Account pool → Submission history** lists all individual submissions. Account and submission details retain the owner and vendor, original non-secret parameters, remote IDs and status. The account detail includes its complete paginated submission history. Members and regular administrators can query bills for the selected submission from the matching account's saved Modified rows. A range with no matching rows has no bill amount. Super administrators retain the supplier billing query by remote ID. The current account status has its own refresh action.

The lists load at most twenty rows per page. Accounts supports account-status filtering and reads saved usage; each accepted account has an explicit Sync button. Opening, reloading, searching or changing pages does not query the supplier. Submission history has separate submission-state filters and reads saved attempts. Background account collection remains configurable in seconds (300 seconds by default). User accounts, API routes and vendor secrets remain isolated from the GoAPI/New API account system.

All following routes require a current TK administrator session; writes require the session CSRF token and a valid same-origin request. Vendor queries resolve the submitting user and remote ID server-side and apply the query limit to the administrator making the request. The personal `/submissions` routes remain owner-only, including for administrators.

| Method and path | Behavior |
| --- | --- |
| `GET /admin/users?page=1&search=` | Paginated users without authentication secrets. |
| `POST /admin/users/registration-code` | Send a registration code for `{ email }`; returns configured `retry_after` and `expires_in` in seconds. Shares registration code delivery and limits with public signup. |
| `POST /admin/users` | Create a verified member with `{ username, email, password, code }`; username must match email. Returns `{ user }` without a new session or login cookie. |
| `POST /admin/users/role` | Change an existing user's role using `{ email, role: "admin" | "member" }`. |
| `GET /admin/accounts?page=1&search=&status=` | Grouped account pool, owner, latest submission and current accepted account. |
| `GET /admin/accounts/:id?page=1` | Account summary and its paginated history. Any submission ID in the group is a valid stable link. |
| `GET /admin/submissions?page=1&search=&status=&account_id=` | All submissions; optional account-group anchor. |
| `GET /admin/submissions/:id` | Submission details, owner and current accepted account. |
| `POST /admin/submissions/:id/status` | Refresh that submission's status using `{ force }`. |
| `POST /admin/submissions/:id/bills` | Query that submission's bills using `{ start_ds, end_ds }`. |

For isolated browser verification, build the frontend and run `TK_ADMIN_BROWSER_TEST_ADDRESS=127.0.0.1:39129 go test -run '^TestAdminBrowserFixture$' -count=1 -timeout 35m -v .` from `server/`. Its `/__test/admin` and `/__test/member` entry points create test-only sessions against temporary SQLite and Redis. Vendor requests are handled by an in-memory transport. These helpers are excluded from the production binary and never contact real accounts or email services.

## Development and verification

```sh
cd tk-register-web-portal
npm ci
npm test
npm run build
npm run test:navigation
cd server
go test ./...
go vet ./...
```

The navigation browser checks serve the built frontend with local API fixtures.
They cover member and administrator drawers at phone, tablet, and desktop sizes,
including crossing the 820px breakpoint while a drawer is open. The drawer must
remain vertical and scrollable; closing it or entering desktop layout restores
the previous page scroll state and allows the main content to receive input.

Run the real-backend browser fixture and vendor workflow in two terminals:

```sh
# Terminal 1, from tk-register-web-portal/server (after npm run build)
TK_BROWSER_TEST_ADDRESS=127.0.0.1:39124 go test -run '^TestBrowserFixture$' -count=1 -v .

# Terminal 2, from tk-register-web-portal
TK_E2E_URL=http://127.0.0.1:39124 node tests/submissions.integration.mjs
```

The optional `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` selects an installed Chromium browser. This fixture exists only in Go test files. Its vendor transport responds in memory and cannot contact the real vendor, even though protocol validation still checks the real configured origin. Tests verify database persistence/encryption, user isolation, repeated/uncertain submissions, CNY billing, and layouts at 1440/768/390/320 pixels. No vendor key is needed.

Frontend tests use isolated fixtures. Backend tests use SQLite, temporary Redis and a local SMTP test server, without creating production users or delivering external emails. For local HTTP development, set `TK_REGISTER_PUBLIC_ORIGIN` to the loopback origin; the cookie Secure attribute follows that origin's protocol. Production must use an HTTPS origin.

## Administrator Claude bill exports

Config → **Bill exports** generates one merged Excel workbook for the selected TK Register users. The interface uses the existing admin styles. Search and select up to 20 distinct users, then choose an inclusive date range (up to 60 days, Beijing time). Selections persist across searches and can be removed individually or cleared together. **Include all sub-users** defaults to off; enable it to include every selected user and every level of each descendant tree. Descendants do not count against the 20-user selection limit. Every **Generate export** creates a new job. The page shows only the current job's progress, results and download, with no recent-export list or automatic restoration after a refresh. Keep the page open until the file is ready. Leaving the page does not cancel the backend job; stored files retain their seven-day expiry for existing API clients.

The account set is frozen when the job is created: all current, undeleted `tk-register-0` Claude accounts in the chosen owner scope, across all list pages. The account summary, bill details and review results include **Username**, the actual owner's TK login email saved with that account selection. Changing the form does not alter the current job; generate a new export to apply the changes. Later changes to the user hierarchy, account ownership or owner email do not change the saved selection or Username. Historical ownership is not considered, so an account transferred into the chosen scope before creation is included for the entire selected date range. Accounts deleted before creation are excluded.

An account visible through multiple selected users, including a parent and a descendant, is selected once under its actual owner. Repeated submissions are represented by the stable account record. If separate local records conflict over the same platform account ID or account email, their amounts are excluded and the applicable accounts appear in **Needs review**. Separate valid dates and models remain included in the account/model period totals; hierarchy deduplication does not discard their usage. Selecting users without accounts does not discard other users' accounts; an entirely empty account selection is rejected. The 20,000-account and output-size limits apply to the merged job.

Regular administrators always generate Cache exports from the selected accounts' saved rows in `account_daily_usages_modifiled` within the requested dates, including when a crafted request asks for Raw. They have no source selector. Missing dates contribute no rows. This path works without a supplier login. Regular administrators can list, preview and download only Cache exports.

Super administrators can choose **Cache** or **Raw** in the export form; Raw is the default. Cache uses the same saved-row path without contacting the supplier. Raw uses the **Account sync** website login to request account/model period totals from the supplier's read-only `/supplier-account/api/supplier/bill/claude-export` endpoint. Platform account IDs and emails determine which rows belong to the selected accounts. Existing APIs continue to allow super administrators to access unexpired historical Raw exports. The supplier workbook is processed in memory. The stored workbook and its preview contain the filtered report, with totals calculated from the selected rows in CNY. The sub-user scope option does not change either role's data-source permissions.

The effective source is authorized and frozen as `bill_export_jobs.usage_source` when a job is created. Workers and retries use that saved value; changing the form or global source configuration does not change an existing export. Generate a new job to use another source. The bill-export API uses `raw` and `cache`; Cache maps to the existing internal `modified` value and `account_daily_usages_modifiled` table, without changing stored data or the database schema. Job responses expose `model_usage_source` only to super administrators, with existing Cache jobs also returned as `cache`.

All endpoints below require an active administrator session. POST additionally requires the existing CSRF token and same-origin checks.

| Method and path (relative to `/tk-register/api`) | Purpose |
| --- | --- |
| `POST /admin/bill-exports` | Create a new job with `user_ids` (array of string IDs; 1–20 unique users), `start_date`, `end_date` (`YYYY-MM-DD`), optional boolean `include_sub_users` (defaults to `false`), and optional `model_usage_source` (`raw` or `cache`). The legacy string `user_id` is accepted when `user_ids` is omitted; supplying both is rejected. Source selection applies only to super administrators, defaulting to `raw`; regular administrator requests always use Cache. |
| `GET /admin/bill-exports` | Latest 20 job summaries, retained for API compatibility; the current UI does not call this endpoint. |
| `GET /admin/bill-exports/:id` | Job status and counts, without report/file payloads. |
| `GET /admin/bill-exports/:id/results?view=summary&page=1` | Paginated summary; use `details` or `review` for other sheets (50 rows/page). |
| `GET /admin/bill-exports/:id/download` | Download only the filtered `.xlsx`; expired files return HTTP 410. |

Jobs persist in `bill_export_jobs`, including the `include_sub_users` scope; directly selected user IDs and emails are frozen in `bill_export_users` with a unique `(job_id, user_id)` key. The final account selection and actual owner's `username` are in `bill_export_accounts`. Both selections are written in the job-creation transaction and reused on retries. Responses include a `users` array; a single-user job also includes the legacy `user` object. Older jobs without selected-user rows use their stored target user as a one-user fallback, without rebuilding ownership or rewriting generated files. Multi-user files identify every selected email on all three sheets and use the user count and job ID in the filename. A distributed export gate and token-fenced database leases prevent two workers from publishing the same job. Interrupted reads may safely restart after lease expiry. Supplier login/query cooldowns defer jobs without busy retries. Export work does not hold a SQL transaction during HTTP calls or block the request handler. Reports have bounded source/row/output sizes; unsupported input formats fail explicitly. Filtered JSON is limited to 4 MiB and XLSX to 8 MiB; use a shorter date range if the result exceeds those limits. Creation is limited to five requests/minute per administrator, with at most five active jobs per administrator and 20 globally.

## Members and recursive account visibility

Signed-in members can create a new member in **Account settings → Members**.
Creation uses the existing registration switches, SMTP delivery, verification
expiry and password rules. The browser asks for password confirmation, accepts
verification codes as text, and preserves the creator's current session. The
new user's normalized email is their username, their role is always `member`,
and an existing user cannot be attached to a creator through this workflow.
Administrator creation in Config → Users continues to create independent users.

`users.parent_user_id` records the direct parent. Only the member link/unlink workflows change it. The `user_hierarchy`
closure table records `(ancestor_user_id, descendant_user_id, source_user_id,
depth)`, with foreign keys to `users.id`. Every user has a self row (depth zero,
NULL source). The source of a deeper descendant is the ancestor's immediate child
on that branch. User creation and closure insertion share one transaction.
Startup adds self rows for pre-existing users; it does not infer parentage.

Browser Accounts, account usage/history and Submission history include the
viewer's own data plus descendant data. Source filters default to all and accept
`myself` or direct-member IDs; `none` is an explicitly empty selection. Branch
filtering happens before pagination. Response `account_source` exposes only
`myself` or the immediate child, and `can_manage` is true only for the real owner.
Members shows only directly created or linked users. A parent may read saved descendant data
and manually Sync an account, but cannot edit credentials or delete it. Admin
Account pool and personal Bearer API ownership checks keep their existing scope.
Account ownership, submissions and collection history are not copied or moved.

Session-authenticated routes under `/tk-register/api`:

- `GET /members?page=1&search=`: paginated direct members and visible-account counts.
- `GET /members/sources`: `myself` and direct-member choices (no deeper identities).
- `POST /members/registration-code`: send the new user's registration code.
- `POST /members`: verified creation with `email`, `username`, `password`, `code`.
- `GET /accounts?sources=myself,123` and `GET /submissions?sources=123`: selected
  visible branches; absent `sources` means all. Detail and Sync routes recheck
  visibility by local account ID. Mutations still require the true owner.

Member creation and send-code attempts share a per-creator limit of 10 requests
per minute, in addition to existing mailbox/IP delivery and verification limits.
Both POST routes require the session, CSRF token and same-origin validation.
The registration-disabled switch applies to both administrator and member forms.

Validation: `go test ./...` in `server`, `npm test`, `npm run build`.
The opt-in `TestMembersBrowserFixture` uses isolated SQLite, mock mail and a
simulated supplier. Bind it to loopback using
`TK_MEMBERS_BROWSER_TEST_ADDRESS=127.0.0.1:39165`; run
`node tests/members.browser.mjs` against that fixture. No production accounts or
real emails are needed for the browser test.

### Link an existing user

**Account settings → Members → Add sub account** links an existing active,
verified member. The target receives an approval code naming the requesting
parent and describing recursive account visibility. No password is requested.
Codes are single-use, isolated from login/registration, and bound to both user
IDs, authentication versions and the target's membership version. TTL, resend cooldown, email/IP limits and maximum
verification attempts reuse the existing verification settings.

- `POST /members/link-code`: `{ "email": "existing@example.com" }`; returns
  `retry_after` and `expires_in` after successful SMTP delivery and code storage.
- `POST /members/link`: `{ "email": "existing@example.com", "code": "..." }`;
  returns `{ user }` after the relationship commits, without changing sessions.

Both routes require an active browser session, same origin and CSRF; together
they allow 10 requests per actor per minute. Linking is independent of the
new-registration switch. Self-links, ancestors/cycles, administrator or inactive
targets, and users with an existing parent are rejected; this is not a reparenting
endpoint. Every request rechecks eligibility and never accepts a client parent ID.

One transaction updates `users.parent_user_id`, extends `user_hierarchy` from all
parent ancestors to all target descendants, and inserts `member_link_audits`.
Its `actor_id` and non-unique `target_id` reference `users.id`; `action` records
`link` or `unlink` and `created_at` records when it happened. Startup replaces the
legacy unique target index while preserving history. It stores no password/code. Hierarchy mutations and
role changes share the existing `admin_role_guards` singleton transaction lock,
acquired before user rows. Member creation takes the same lock, preventing lost
closure edges during concurrent subtree links. Network/SMTP calls occur outside
the transaction. Source labels expose only the viewer's immediate child.

No account ownership, submissions, snapshots, tokens or login sessions are moved.
Descendant access remains view/manual-Sync only; edit/delete and Bearer APIs keep
their owner/admin checks. The browser refreshes members and Source after success.

### Remove a direct member

**Del sub account** opens a searchable, paginated direct-member picker and an
impact confirmation. This disconnects the member without deleting their user,
accounts or data. Their own descendants, credentials and login sessions remain intact.
The former parent and all its ancestors lose inherited personal visibility;
administrator Account pool access continues to follow role permissions.

- `GET /members` includes `membership_version` as a decimal string.
- `POST /members/unlink`: `{ "member_id": "123", "membership_version": "1" }`.
  Requires an active browser session, same origin and CSRF; limited to 10 attempts
  per actor per minute. Only the actor's current direct members can be removed.
  No email code is required. A stale relationship returns 409 `MEMBER_RELATION_CHANGED`.

Unlinking shares the user-access transaction guard with creation, linking and role
changes. It clears the direct parent and deletes only closure edges crossing the
removed boundary. `users.membership_version` (`uint64` / MySQL `BIGINT UNSIGNED`)
increments on each link/unlink; stale dialogs and old link-code approvals cannot
affect a subsequent relationship. Reattaching requires a fresh approval code.

The same transaction increments `users.visibility_version` for affected ancestors
and upserts `overview_refresh_requests`. Personal summaries in both summary tables
store that version: reads reject mismatches and an in-flight build rechecks it
under a user-row lock before publishing. Stale summaries show an updating state,
with frontend polling while pending. The existing overview worker processes a
bounded membership refresh each pass; failed requests survive restarts and retry
after a minute. Successful matching publication clears the request atomically.
Only affected personal summaries are rebuilt; daily bills, Fable ratios, unrelated
personal summaries and administrator summaries are unchanged.

Run `node tests/member-unlink.browser.mjs` against a fresh `TestMembersBrowserFixture`
to check English/Chinese dialogs, stale relationships, remove/re-add, refreshed
source choices and 390/320-pixel layouts without production data.
