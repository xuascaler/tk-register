# Account usage collection

TK Register collects account status and cumulative usage from the **TK Register 0 supplier website**. The collector runs in the existing server process. It does not need an open browser, and it does not submit or edit supplier accounts.

## Administrator setup

Open **Management → Config → Account sync** (`/tk-register/#/config/account_sync`). Enter the supplier website username and password, select **Test login**, then save the configuration. Enable automatic collection when ready.

| Setting | Default | Behavior |
| --- | --- | --- |
| `enabled` | `false` | Enables scheduled collection. Pausing does not remove saved history. |
| `interval_hours` | `2` | Whole hours between automatic runs; accepted range is 1–168. |
| `timezone` | `Asia/Shanghai` | Timezone for snapshot dates and usage history. Also supports `Asia/Singapore` and `UTC`. |
| `data_source` | Supplier website | Fixed source for this integration. |
| `website_username` | Empty | Supplier website login username, configured by an administrator. |
| `website_password` | Empty | Write-only password. Leave blank to preserve an existing password; changing the username requires a new password. |

**Test login** tests the current form values without saving them or collecting account data. A successful test does not enable the schedule. **Save changes** stores the form values; **Discard** restores the saved values. The collection status section shows the latest attempt, latest successful run, next scheduled collection and any failure message.

Credentials are stored in the TK Register configuration database. The password is encrypted with the existing application encryption key and is never returned to the browser. The browser receives only whether a password is configured. Preserve the encryption key across deployments and backups.

## Authentication lifecycle

Each collection run starts a **fresh username/password login** to the supplier website. A credential obtained during an earlier run is not reused at the next interval. The temporary supplier login token remains in server memory only for that run.

If the supplier reports an expired session during a run, the collector signs in again once and retries the failed read. A second expiry or a login failure ends that attempt with a sanitized error. Supplier passwords, session tokens and account authentication JSON are excluded from the collected account DTO and public error messages.

The supplier origin is fixed to `https://120.233.254.152:8000`. The previously authorized certificate-verification exception is scoped to that supplier; requests are not redirected to another origin.

## User pages

**My account → Accounts** (`/tk-register/#/accounts`) shows only the signed-in user's accounts. It groups retries under one stable local account identity based on owner, vendor and normalized account identifier. A row includes the account email, plan, account ID, status, available quota windows, cumulative amount spent and last successful collection time.

Opening or returning to an Accounts list, reloading the browser, changing page, searching, and filtering read saved database data only. There is no toolbar refresh button and no automatic supplier request from these actions.

Each row has a **Sync** button. Clicking it sends only that account's stable local ID to the existing authenticated collection endpoint. The server searches the supplier by complete account email and verifies the exact match. Only the selected row shows **Syncing…**; other Sync buttons are temporarily disabled while that request is in progress to avoid overlapping requests. The result updates only that row, preserving page membership and other rows. A changed account status does not start a new search or synchronization loop.

Only accounts with an accepted submission can be synchronized. Rows without one show **Awaiting acceptance** with a disabled Sync button. Supplier errors, busy responses and rate limits retain the last successful status, usage, amount and timestamp. A row shows concise failure feedback with details; HTTP 200 is not treated as success unless the per-account result confirms the update. Manual Sync is not retried automatically. If the server provides a retry interval, that row displays a cooldown before another manual attempt. Leaving the list or changing its scope cancels the UI request and ignores late responses; an already-started server operation may still save its result.

Opening an account detail page also reads saved history. **Sync now** explicitly requests a fresh collection there. Manual collection remains available with saved credentials when scheduled collection is paused. The configurable background schedule (two hours by default), detail-page collection and Auth JSON update flow are unchanged. Server authorization, rate limits, bounded concurrency and supplier availability apply to all fresh collection requests.

The account detail page shows cumulative spending, input/output tokens, quota-window usage and reset times. The trend supports **7, 30 or 90 days** and separate spending, input-token and output-token views. **View data** exposes the saved observations in a table. **View submission** opens the associated submission record, with a return action to the account detail.

**Management → Account pool → Accounts** reuses the same saved account list with per-row Sync controls, usage details and trends across all users, adding the owner email in a **User** column and in account details. Administrators can search by account or owner email and explicitly sync an account. These routes require an active administrator session; ordinary members remain limited to their own accounts.

**Submission history** lists saved submission attempts; opening or refreshing a history list reads those stored records. Individual submission details retain their explicit status and billing queries. These queries are separate from scheduled account-usage collection, and account usage pages do not alter the account-submission workflow.

## Automatic Sync after API submission

A newly accepted API account submission with a valid vendor account ID schedules a targeted account Sync job for **three seconds after acceptance**. A successful public Auth JSON API update queues the same kind of job for three seconds after its confirmed update. The original API response does not wait for collection. The worker runs in the existing backend process and updates the same account snapshot used by Accounts and the personal-token lookup API. No additional frontend polling or special display is added. Loading a page or calling lookup still reads saved data only.

For a submission, the accepted submission, stable local account and pending job are committed in one database transaction. For an Auth JSON API update, the confirmed audit result, account update metadata and pending job are committed together. The existing `account_post_submit_sync_jobs` table stores both kinds of job:

| Field | Meaning |
| --- | --- |
| `id` | Local job ID. |
| `submission_id` | Foreign key to `submissions.id`, the accepted submission that anchors the account identity; multiple jobs can reference it. |
| `submission_trigger_id` | Nullable unique submission trigger identifier, equal to `submission_id` for a submission-triggered job. Ensures at most one job per accepted API submission. |
| `auth_json_audit_id` | Nullable unique foreign key to `account_auth_json_audits.id`. Set only for an Auth JSON API update job, ensuring at most one job per confirmed update. |
| `account_id` | Foreign key to `synced_accounts.id`, the stable local account. |
| `user_id` | Foreign key to `users.id`, the account owner. |
| `identity_key` | Account lifetime captured when the task is queued. |
| `status` | `pending`, `running`, `succeeded`, `failed` or `skipped`. |
| `attempt_count` | Number of claimed collection attempts, from zero to four; waiting for a busy lease does not increment it. |
| `next_run_at` | Earliest execution time in UTC; initially the successful API operation time plus three seconds. |
| `created_at`, `started_at`, `finished_at` | Job lifecycle timestamps in UTC. |
| `last_error` | Sanitized outcome without credentials or upstream response bodies. |

Exactly one trigger field is set for each job. The migration fills `submission_trigger_id` for existing submission jobs and preserves their state; it does not create jobs for historical operations or revive completed jobs.

The worker checks for due work every second and handles one job at a time. It uses the same Redis lease, `tk-register:{tk-register}:account-sync:lease`, as scheduled and manual synchronization. When that lease is occupied, it does not contact the supplier or consume an attempt; it leaves the job pending and writes `next_run_at = now + 5 seconds` to the database. Execution can therefore occur later than three seconds when other collection work is active.

After acquiring the lease, the worker claims the task and increments its attempt count atomically, signs in with the currently saved supplier website credentials, and queries only the affected account. It validates ownership, vendor, account identity and the current accepted submission before querying and before saving. Auth JSON jobs also verify that a newer confirmed update has not superseded their audit. Changed or ineligible targets are skipped. Saved credentials are required; the periodic `enabled` switch controls the periodic schedule only.

A failed collection returns the job to `pending` with `next_run_at = now + 3 seconds`, until it has used **three retries after the initial attempt (four attempts total)**. Success ends the job immediately; a fourth failed attempt ends it as `failed`. This is a delay after each failure, not a fixed three-second total request timeout. The existing single reauthentication on an expired supplier session remains part of a collection attempt. Failures retain prior successful data. No attempt repeats account creation or the Auth JSON write.

For Auth JSON jobs, the audit's `refresh_status` remains `pending` while queued or retrying, becomes `succeeded` after a saved collection, and becomes `failed` if retries are exhausted or the target is skipped. The credential update outcome stays `updated`. The HTTP response has already returned and does not change later; clients can read saved collection results and timestamps through account lookup.

Pending jobs survive backend restarts. An interrupted `running` job older than two minutes uses the remaining retry budget; it is rescheduled after three seconds if fewer than four attempts were claimed, otherwise it ends as failed. This recovery repeats only a read. Existing manual Sync, the regular two-hour schedule, and the browser Auth JSON editor's immediate refresh retain their existing behavior.

## Values and history

- Supplier `cpa_usage.cost` is expressed in micro-CNY. The server divides it by **1,000,000** and continues to store the resulting amount as `cost_total` with currency `CNY`. The saved original amount and currency are unchanged.
- Every account snapshot response adds `cost_total_usd` and `exchange_rate_cny_per_usd`. For CNY snapshots, `cost_total_usd` is `cost_total / 7.2`; the rate is always `7.2` CNY per USD. The API retains calculation precision. Missing amounts and missing or unsupported currencies produce `cost_total_usd: null`; a known zero produces `0`.
- Member and administrator account lists, detail totals, spending trends, and observation tables display `cost_total_usd` with two decimal places and a trailing dollar sign (for example, `1,955.08 $`). Latest and historical responses use the same fixed rate. This is not a live or date-specific exchange-rate system, and no stored snapshot migration is required.
- Input and output tokens are cumulative counters. Five-hour and seven-day quota values are normalized to percentages by the server before display.
- An unavailable metric remains SQL `NULL` / JSON `null` and appears as **—**. A known zero remains `0`, `0%` or `0.00 $` as appropriate.
- Failed collection does not replace the latest successful snapshot with zeros. The list shows **Last known** and **Update failed**; detail shows the failure and the timestamp of the retained successful data. An account that has never been successfully collected continues to show **—**.
- Snapshot dates follow the configured timezone; `collected_at` records the actual collection instant. The supplier's account-metadata timestamp is not treated as a usage-update timestamp, so `source_updated_at` stays null unless a reliable usage timestamp is available.
- Trends plot saved cumulative observations. Missing observations, long gaps, currency changes and counter resets are not joined into a continuous line. They do not represent zero consumption or calculated interval usage.
- A new accepted remote identity or plan starts a new displayed account lifetime. Earlier observations remain stored but are not connected to the new lifetime's trend.

## Personal-token account lookup

External clients can read an owned account's saved data using `GET /tk-register/api/v1/vendors/tk-register-0/claude/accounts/lookup?username=<owner-email>&account=<submitted-account>` with a personal Bearer token carrying `accounts:read`. The endpoint returns the latest saved snapshot and collection timestamps without starting a supplier query or collection run. It uses the same null, zero and last-known-data semantics described above.

The username must match the token owner, including when that owner is an administrator. The limit is 600 lookup requests per minute per user. See the [Account Lookup API reference](account-lookup-api.md) for the complete contract and examples. This external endpoint is separate from the browser-session routes below.

## Updating Auth JSON

An accepted Claude account's detail page includes **Authentication → Update Auth JSON**. Members can update their own accounts; administrators can use the same editor in **Account pool → Accounts**. The editor accepts a complete JSON object pasted into the field or uploaded from a JSON file up to 2 MB. It never displays existing credentials. Validation uses the same Claude credential rules as account submission and additionally requires the credential email to match the target account after normalization.

External clients can use `POST /tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json` with a personal token carrying `claude:auth-json:update`. Send `username`, `account`, and the complete `auth_json`. The public API uses the same update, ownership-conflict and concurrency checks; personal tokens only access their owner's accounts, even for administrators. Its post-update collection is asynchronous, using the queue and retry policy described above. Existing tokens receive the new permission automatically and remain usable without regeneration. See the [Auth JSON Update API reference](auth-json-update-api.md).

Opening the editor signs in to the configured supplier website and searches using the complete account email. Only the filtered result pages are read. The backend compares full normalized account emails: one exact match identifies the account; multiple exact matches require an already-confirmed supplier website record ID. A missing or ambiguous match stops the operation. The plan is not an identity condition because the supplier may have changed it. The website record ID is distinct from its visible platform account ID and is stored separately on the local account.

The backend checks the supplier's current lock and raw account status both before editing and when saving. An unlocked account without a positive platform ID can be edited; otherwise the supplier must report `ban`, `offline`, or `login_failed`. These checks do not use the locally normalized `Unknown` status. The account's latest supplier form values are read again before the update, and only `cpa_auth_json` is replaced. Existing passwords, original submission records, tags, and unrelated account details are not changed.

After a confirmed update through the browser editor, the server immediately queries that account again and saves its current status, quota windows, cumulative tokens, amount, currency and collection timestamp as a new snapshot. The UI reloads the saved detail and trend. This does not wait for the scheduled interval and does not claim that saving credentials has restored the account: the supplier's returned status remains authoritative.

Credential saving and subsequent account refresh have separate outcomes. For browser updates, if saving succeeds but the immediate refresh fails, the response still reports `updated: true`, `refresh_status: failed`, and a safe message; the last successful snapshot remains visible. For successful public API updates, the response normally reports `updated: true`, `refresh_status: pending`, and the currently saved account data, which may predate the update or be null if local readback fails. Collection runs later. A timeout or connection loss after a write may have reached the supplier is reported as `AUTH_JSON_UPDATE_UNCONFIRMED`. Writes are never retried automatically. An ordinary subsequent GET or a user-selected **Sync now** cannot repeat the credential update.

Web updates use browser-session ownership/admin and CSRF checks; public updates use scoped personal Bearer tokens. They share per-user update rate limits and Redis leases under `tk-register:`. Conflicting accepted ownership stops the update. A supplier-account lease prevents concurrent updates for the same vendor account; the shared collection lease serializes scheduled, manual and post-update collection. Audit records contain the actor, owner, account, confirmed supplier record ID, request ID, source (`web` or `api`), API token reference and revision when applicable, outcome and refresh outcome, but no credential body. Replacement credentials are forwarded to the supplier without adding another local secret copy; the original encrypted submission remains historical. Public responses return the audit ID in `X-Auth-JSON-Audit-ID` once the record exists and in `data.audit_id` on success.

## Browser API contract

The following paths are relative to `/tk-register/api` and use the existing JSON envelope (`data` contains the payload). They require the current browser session. Writes additionally require the existing CSRF and origin checks; the user's personal API token does not authorize these browser-session routes.

| Method and path | Result |
| --- | --- |
| `GET /accounts?page=1&search=&status=` | Owner-scoped saved account list: `items`, `total`, `page`, `page_size`. |
| `POST /accounts/sync` | `{ "account_ids": ["1", "2"] }`, up to 20 owned local account IDs. Returns `accounts`, per-account `results` (`updated`, `failed`, `skipped`), and `updated_count`, `failed_count`, `skipped_count`. Partial failures do not discard successful observations. |
| `GET /accounts/:id?days=7` | Owned account, current-lifetime snapshots, collection interval and timezone. `days` accepts 7, 30 or 90. |
| `POST /accounts/:id/sync` | Explicit fresh collection using `{}`; returns the account with its latest saved result or `last_error`. |
| `GET /accounts/:id/auth-json` | Fresh supplier eligibility and account identity, without credentials: `account`, `plan`, `editable`, `reason`, `supplier_record_id`, `updated_at`. |
| `POST /accounts/:id/auth-json` | `{ "auth_json": object-or-JSON-string }`; updates credentials once and immediately refreshes the account. Returns `updated`, `refresh_status`, `refresh_message`, `account`, `updated_at`. A confirmed update can return `account: null` if local readback fails; the UI retains its existing data and reports the readback failure. |
| `GET /admin/synced-accounts?page=1&search=&status=` | Administrator saved account list across users; same metrics plus `user_id` and `user_email`. |
| `POST /admin/synced-accounts/sync` | Administrator version of targeted account synchronization; same request and response, with owner information in returned accounts. |
| `GET /admin/synced-accounts/:id?days=7` | Administrator view of a stable synced account and its snapshots. |
| `POST /admin/synced-accounts/:id/sync` | Administrator-triggered fresh collection for that account, with the same configuration, concurrency and cooldown checks. |
| `GET/POST /admin/synced-accounts/:id/auth-json` | Administrator version of the eligibility/update endpoints, with the same upstream and refresh behavior. |
| `GET /admin/account-sync` | Administrator configuration without the saved password, plus collection status. |
| `PUT /admin/account-sync` | Save `enabled`, `interval_hours`, `timezone`, `website_username` and optional `website_password`. |
| `POST /admin/account-sync/test-login` | Test `website_username` and optional `website_password`; success returns `authenticated: true`. A blank password uses the saved password only for the same saved username. |

Accounts-list Sync batch requests allow **200 requests per minute per acting user**, shared across member and administrator list routes. Each batch still accepts at most 20 account IDs. Auth JSON editor eligibility GETs allow **300 requests per minute per acting user**. Auth JSON writes allow **100 requests per minute per user**, shared between browser and public API updates. Detail-page Sync retains its cooldown of one request per minute for each acting user and account.

Account snapshots in list, detail, historical, manual-sync and Auth JSON refresh responses share the original `cost_total` / `currency` fields and the derived `cost_total_usd` / `exchange_rate_cny_per_usd` fields described above. The personal-token lookup uses the same snapshot format.

The list sends a one-ID batch for each manual Sync click. Batch clients must inspect each result; HTTP 200 can contain both successful and failed rows. Batch requests never report retained database data as a fresh supplier success. A successful HTTP response to single-account manual sync does not by itself mean the supplier collection succeeded: the returned account's `last_error` must also be checked. Missing configuration, concurrent execution, rate limits and service failures can instead produce an HTTP error. The UI reloads the saved detail after an explicit attempt so retained values and failure timestamps remain visible.

## Verification

Run `npm test` and `npm run build` in `tk-register-web-portal`. Frontend account tests cover missing-versus-zero values, unavailable currencies, gaps, counter resets and chronology without mutating input data. Mock browser checks cover desktop/mobile layout, manual collection failure reporting, testing without saving and clearing the password field after save. They do not contact the supplier or require real credentials.
