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

**My account → Accounts** shows saved status, usage, cumulative spending and 7/30/90-day trends. **Config → Account sync** configures supplier website credentials and collection frequency (default two hours, paused until configured). Each run signs in afresh; failed runs preserve the last successful data. See the [Account usage collection guide](docs/account-sync.md) for setup, data semantics and API behavior.

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
| `POST /submissions/:id/bills`             | Query with `{ start_ds, end_ds }` date range. Remote ID is derived server-side.                            |
| `GET /admin/vendors/:id`                  | Administrator-only vendor configuration; token is write-only.                                              |
| `PUT /admin/vendors/:id`                  | Administrator-only update of `{ enabled, timezone, access_token }`.                                        |

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

Complete upstream responses retain their HTTP status and content type without a local envelope. In the top-level JSON `alias` string, `温州润晨` is replaced with `xxxxx`, matching the frontend display. The original alias is retained in storage; account IDs and other business fields are not changed by this masking. If an upstream response echoes the shared vendor credential, that credential is redacted before forwarding. Locally rejected requests use the usual error envelope: 401 for invalid/revoked tokens, 400 for invalid parameters, 409 for an accepted/running duplicate and 429 for the shared per-user submission limit (10 attempts per minute across browser and API). The JSON request limit is 3 MiB. If no complete upstream response is received, connection errors return 502 and timeouts return 504; oversized responses above 256 KiB also return 502. A recorded uncertain attempt remains Unknown and can be manually retried with a new request. Account creation is never retried automatically.

`X-Request-ID` identifies the request/execution. `X-Submission-ID` is included only after a local submission record exists. These are local tracking headers; an `id` in the vendor response remains the vendor's ID. Each new API POST is a new manual attempt, subject to accepted/running duplicate protection.

A newly accepted API submission with a valid vendor ID also queues one account Sync job, due three seconds after acceptance. The API response does not wait for collection. A failed collection is retried after three seconds, up to three additional retries (four attempts total). If the shared collection lease is busy, the pending job is deferred five seconds without consuming an attempt. These retries only read account status and usage; they never repeat account creation. Success updates the usual account snapshot; failures preserve the last successful data. Saved supplier website credentials are required, independently of whether the two-hour schedule is enabled. Browser submissions, unsuccessful submissions and historical accepted records are not queued by this feature. See the [automatic synchronization details](docs/account-sync.md#automatic-sync-after-api-submission).

The Accounts list and usage details read saved snapshots from scheduled collection, successful API operations and explicit collection; opening or refreshing the list does not query the supplier. Each list row has a Sync button; account details retain Sync now. Submission detail status and billing queries remain separate from this collection flow.

### Saved account lookup

```text
GET /tk-register/api/v1/vendors/tk-register-0/claude/accounts/lookup?username=<owner-email>&account=<submitted-account>
Authorization: Bearer <your TK Register API token>
```

This endpoint requires `accounts:read` and both exact query identifiers, with no request body. It returns an owned account's saved status, cumulative usage, last successful collection time and sanitized latest collection error. It never triggers supplier login or synchronization. A personal administrator token cannot read another user's accounts. Missing or out-of-scope records return the same 404 response; the lookup limit is 60 requests per minute per user. The existing submission limit is unchanged.

See the [Account Lookup API reference](docs/account-lookup-api.md) for cURL examples, response fields, null/zero/freshness behavior and error handling. The [Account usage collection guide](docs/account-sync.md) explains the default two-hour collection schedule and administrator configuration.

### Auth JSON updates

```http
POST /tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json
Authorization: Bearer <personal token>
Content-Type: application/json
```

Send `username`, `account`, and a complete `auth_json` object or JSON string. The token requires `claude:auth-json:update`; `username` must match its owner, and `auth_json.email` must match the submitted account after normalization. The API reuses the web editor's ownership, supplier identity, editability, locking, and update checks. Its limit is 10 update requests per minute per user, shared with web updates. It never automatically retries a supplier write.

A confirmed API update queues the same asynchronous Sync, initially due three seconds later, with up to three further attempts separated by three seconds on collection failure. The response returns HTTP 200 with `updated: true` and normally `refresh_status: pending` without waiting for collection. Its `account` contains the previously saved data, or is null if local readback fails; use collection timestamps to assess freshness. Audit refresh status stays pending while queued or retrying and becomes succeeded or failed when processing ends. A refresh failure does not undo the confirmed credential update. The browser Auth JSON editor still attempts its refresh immediately. Neither flow repeats the supplier write automatically.

Each executed update has an audit record with source, actor, owner, account, token reference/revision, and separate update/refresh outcomes. It does not store the replacement credentials or create another submission. Keep `X-Request-ID` and, when present, `X-Auth-JSON-Audit-ID` for support.

See the [Auth JSON Update API reference](docs/auth-json-update-api.md) for the full validation, cURL, response, audit, and error contract. The in-app guide is at `/tk-register#/docs/update-auth-json`.

## Administrator users and account pool

**Config → Users** lists TK Register users, roles, account status and registration time. **Add admin** and **Del admin** accept an existing user's email; changing a role does not create or delete an account. Roles are read-only in the table. The current administrator and bootstrap administrator cannot be demoted, and the server prevents removing the last active administrator. A promoted administrator must sign in with a password and email code.

Role updates are serialized and saved in one database transaction with an `admin_role_audits` record (`actor_id` and `target_id` reference `users.id`). Each change increments `users.auth_version`. Sessions and pending administrator login challenges carry that version and are rejected after a role change, even if the user's role is subsequently restored. Existing users migrate with version zero, so the deployment itself does not sign everyone out. `admin_role_guards` contains the singleton row used to serialize concurrent role changes.

**Management → Account pool → Accounts** groups submissions by vendor, submitting user and trimmed/case-normalized account identifier. Different owners' accounts are never merged. Latest submit status uses `created_at DESC, id DESC`; current account status comes from the newest accepted submission with a remote ID. A later failed submission does not replace that accepted account. A newer accepted record becomes the query target even when its first status query has not completed.

**Account pool → Submission history** lists all individual submissions. Account and submission details retain the owner and vendor, original non-secret parameters, remote IDs and status. The account detail includes its complete paginated submission history. Historical activity and bills remain tied to the selected submission's remote ID; the current account status has its own refresh action. Back controls return to the originating account or global history.

The lists load at most twenty rows per page. Accounts supports account-status filtering and reads saved usage; each accepted account has an explicit Sync button. Opening, reloading, searching or changing pages does not query the supplier. Submission history has separate submission-state filters and reads saved attempts. Background account collection remains configurable (two hours by default). User accounts, API routes and vendor secrets remain isolated from the GoAPI/New API account system.

All following routes require a current TK administrator session; writes require the session CSRF token and a valid same-origin request. Vendor queries resolve the submitting user and remote ID server-side and apply the query limit to the administrator making the request. The personal `/submissions` routes remain owner-only, including for administrators.

| Method and path | Behavior |
| --- | --- |
| `GET /admin/users?page=1&search=` | Paginated users without authentication secrets. |
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
cd server
go test ./...
go vet ./...
```

Run the real-backend browser fixture and vendor workflow in two terminals:

```sh
# Terminal 1, from tk-register-web-portal/server (after npm run build)
TK_BROWSER_TEST_ADDRESS=127.0.0.1:39124 go test -run '^TestBrowserFixture$' -count=1 -v .

# Terminal 2, from tk-register-web-portal
TK_E2E_URL=http://127.0.0.1:39124 node tests/submissions.integration.mjs
```

The optional `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` selects an installed Chromium browser. This fixture exists only in Go test files. Its vendor transport responds in memory and cannot contact the real vendor, even though protocol validation still checks the real configured origin. Tests verify database persistence/encryption, user isolation, repeated/uncertain submissions, CNY billing, and layouts at 1440/768/390/320 pixels. No vendor key is needed.

Frontend tests use isolated fixtures. Backend tests use SQLite, temporary Redis and a local SMTP test server, without creating production users or delivering external emails. For local HTTP development, set `TK_REGISTER_PUBLIC_ORIGIN` to the loopback origin; the cookie Secure attribute follows that origin's protocol. Production must use an HTTPS origin.
