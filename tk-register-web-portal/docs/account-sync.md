# Account usage collection

TK Register collects account status and cumulative usage from the **TK Register 0 supplier website**. The collector runs in the existing server process. It does not need an open browser, and it does not submit or edit supplier accounts.

## Administrator setup

Open **Management → Config → Account sync** (`/tk-register/#/config/account_sync`). Enter the supplier website username and password, select **Test login**, then save the configuration. Enable automatic collection when ready.

| Setting | Default | Behavior |
| --- | --- | --- |
| `enabled` | `false` | Enables scheduled collection. Pausing does not remove saved history. |
| `interval_seconds` | `300` | Seconds between planned automatic rounds; accepts whole numbers from 60 to 604800. `300` means five minutes. |
| `timezone` | `Asia/Shanghai` | Timezone for snapshot dates and usage history. Also supports `Asia/Singapore` and `UTC`. |
| `data_source` | Supplier website | Fixed source for this integration. |
| `website_username` | Empty | Supplier website login username, configured by an administrator. |
| `website_password` | Empty | Write-only password. Leave blank to preserve an existing password; changing the username requires a new password. |

**Test login** tests the current form values without saving them or collecting account data. A successful test does not enable the schedule. **Save changes** stores the form values; **Discard** restores the saved values. The collection status section shows the latest attempt, latest successful run, next scheduled collection and any failure message.

Credentials are stored in the TK Register configuration database. The password is encrypted with the existing application encryption key and is never returned to the browser. The browser receives only whether a password is configured. Preserve the encryption key across deployments and backups.

## Scheduling and upgrade behavior

The public configuration field is `interval_seconds`; its database setting is `account_sync_interval_seconds`. The migration converts an existing integer-hour setting to seconds exactly once (`2` becomes `7200`) and preserves a previously saved seconds value. New installations start at `300`. Saving a new interval recomputes the next collection time; history is not deleted, and restarting the service does not reset customized values. The administrator page displays **Next sync** from the persisted schedule.

The worker waits for the next due time rather than rounding execution to a 30-second check. Configuration changes wake or invalidate that wait. Planned rounds share the same distributed synchronization lease as manual and post-operation collection. A round that is still running prevents another full collection from starting. Overlapping planned slots are skipped rather than queued: with a 300-second interval, a round starting at 10:00 and finishing at 10:07 proceeds to the next future slot at 10:10, without replaying 10:05. Recovery after downtime starts at most one current collection, then resumes the schedule; it never sends a burst of historical catch-up requests.

The collector retains the existing five-minute whole-run deadline and seven-minute lease safety bound; the lease is released as soon as work completes. A supplier cooldown delays eligible collection without keeping an HTTP request open. These scheduling rules do not remove pending single-account jobs created by successful browser or API submissions and Auth JSON updates. Those durable jobs keep their separate five-round and deferral behavior described below.

Each round uses the supplier website's account-list endpoint with `page_size=100`, reading successive pages until the declared total is reached. It does not issue one query per local account. With a valid session, a normal round needs approximately `ceil(supplier account count / 100)` GETs (an empty result still needs one). Only a missing or expired session adds login work. Reducing a two-hour interval to 300 seconds raises planned rounds from 12 to 288 per day; successful snapshot writes increase correspondingly.

## Authentication lifecycle

Collection, manual Sync, Auth JSON eligibility/update and post-update reads share a **cached supplier website login**. The server checks the account-query cooldown first, then reuses a valid cached token; it signs in only when no usable session exists. This reuses authentication only: account reads still fetch current supplier data.

The token and its corresponding public key are encrypted together with AES-GCM in Redis. Keys use the `tk-register:{tk-register}:supplier-auth:tk-register-0:` prefix, and cache entries are scoped by a keyed credential fingerprint and configuration revision. Raw usernames, passwords and tokens are not included in Redis key names or diagnostic logs. Changing the saved website credentials invalidates old configuration sessions; a request using an earlier configuration cannot populate the current configuration's cache. The user's personal API token and browser login session are separate and unchanged.

A positive supplier `expires_in` of up to seven days is accepted. Local cache lifetime is capped at 24 hours, with an early-expiry margin of the smaller of 60 seconds and 10% of the lifetime. Missing or invalid supplier lifetime uses a 30-minute fallback (29 minutes after the margin). This is a local reuse policy, not a claim about the supplier's actual token lifetime.

A distributed login lease and a second cache check ensure concurrent requests share a newly created session. Waiting is bounded: a request that cannot acquire a usable session promptly receives a short, actionable retry interval. **Test login** always verifies the entered credentials rather than accepting a cached login; it shares the login lease and login cooldown, does not collect accounts, and does not overwrite the normal cached session.

If the supplier reports an expired session during a read, the collector invalidates only the session it used, obtains a shared replacement, and retries that read once. A delayed error for an old session cannot delete a newer cached session. A second expiry or a login failure ends that attempt with a sanitized error. Auth JSON writes are not automatically replayed. Supplier passwords, session tokens and account authentication JSON are excluded from the collected account DTO and public error messages.

The supplier origin is fixed to `https://120.233.254.152:8000`. The previously authorized certificate-verification exception is scoped to that supplier; requests are not redirected to another origin.

### Independent login and query cooldowns

A login HTTP 429 records a shared **login** deadline. It blocks new login attempts, but an already-usable token can still query accounts. An accounts GET HTTP 429 records a separate **accounts-query** deadline, retains the token, and never triggers a login. All requests using this supplier integration honor those deadlines across users and workers; cooldown scope is deliberately shared and does not assert how the supplier internally keys its own rate limit.

A valid `Retry-After` delta or HTTP date determines the deadline, bounded to seven days. A missing or invalid header uses a 60-second fallback. A later response cannot shorten a known longer wait. While an accounts-query deadline is active, no new account query or unnecessary preparatory login is started. When only login is cooling down, cached-session account queries remain available. A Redis/cache failure stops coordinated authentication rather than causing independent login storms.

Manual operations return promptly with a safe explanation and retry duration; they are not held open until a multi-minute cooldown ends. The clicked list row and detail Sync control show a countdown and retain the latest saved snapshot. Countdown completion does not automatically send another request, and a login cooldown on one result does not globally disable other rows that may use a valid session.


## Supplier request diagnostics

Set the server environment variable `TK_REGISTER_SUPPLIER_DIAGNOSTICS=all` while investigating collection failures. The default, `errors`, records failed requests and unsuccessful sync outcomes only. Restart/recreate the application container after changing its environment. Diagnostics do not change login, pagination, retries, synchronization locks or request limits.

`TK_SUPPLIER_REQUEST` records one structured JSON event for each actual supplier website HTTP attempt. It covers public-key requests, login and account-list reads, including pagination and existing read retries. It does not log the Auth JSON PUT payload or change that write's no-retry behavior. In `all` mode, successful attempts are also recorded, allowing requests leading up to a rate limit to be counted.

Each event contains a server-generated `trace_id`, operation `source`, vendor, applicable local actor/account/job IDs, stage, method, allowlisted path, start time, duration and upstream HTTP status. Valid positive `page` and `page_size` query parameters appear as separate numeric fields; the URL query string and account-search keyword are omitted. A valid upstream `Retry-After` is normalized to seconds; absent or invalid values are omitted. Transport failures use a fixed error category rather than raw exception text.

`TK_SUPPLIER_SESSION` adds bounded session events such as `cache_hit`, `cache_miss`, `session_refresh`, `login_lock_wait`, `cooldown_wait` and `cooldown_started`, including the stage and remaining wait seconds. These do not count as HTTP requests and never include the token or credential fingerprint. Successful cache activity is visible in `all` mode.

`TK_ACCOUNT_SYNC` events record sync summaries and local rejection gates. Summaries include the number of upstream HTTP attempts and account outcomes, including partial failures returned in an HTTP 200 response. Gates distinguish the local user quota (`local_rate_limit`) from an occupied synchronization lease (`sync_lock_busy`); these are different from an upstream HTTP 429. For browser requests, the trace ID matches the response's `X-Request-ID`. Scheduled runs and asynchronous jobs receive their own trace IDs; queued jobs also include their local job ID and attempt.

Example request event (illustrative, not a recorded incident):

```json
{"trace_id":"sync-example-001","source":"manual_sync","vendor":"tk-register-0","stage":"accounts","method":"GET","path":"/supplier-account/api/supplier/accounts","page":2,"page_size":100,"http_status":429,"duration_ms":85,"retry_after_seconds":30}
```

To inspect the standalone deployment:

```sh
docker compose logs --since=15m tk-register | grep -E 'TK_SUPPLIER_REQUEST|TK_SUPPLIER_SESSION|TK_ACCOUNT_SYNC'
```

Use the same `trace_id` to follow the login and reads for one operation. An upstream `http_status: 429` identifies the exact rejecting endpoint; a local gate identifies a rejection before collection. Request timing shows the observed traffic rate, but does not establish the supplier's configured quota unless the supplier explicitly provides it.

Logs exclude credentials, tokens, account emails, request bodies, response bodies and arbitrary response headers. The existing Docker log rotation bounds local storage (the standalone deployment keeps three 10 MB files). Collect the relevant diagnostic window promptly, then return the environment variable to `errors` when investigation is complete. No diagnostic database table is added.

## User pages

**My account → Accounts** (`/tk-register/#/accounts`) shows only the signed-in user's accounts. It groups retries under one stable local account identity based on owner, vendor and normalized account identifier. A row includes the account email, plan, account ID, status, available quota windows, cumulative amount spent and last successful collection time.

Opening or returning to an Accounts list, reloading the browser, changing page, searching, and filtering read saved database data only. There is no toolbar refresh button and no automatic supplier request from these actions.

Each row has a **Sync** button. Clicking it sends only that account's stable local ID to the existing authenticated collection endpoint. The server searches the supplier by complete account email and verifies the exact match. Only the selected row shows **Syncing…**; other Sync buttons are temporarily disabled while that request is in progress to avoid overlapping requests. The result updates only that row, preserving page membership and other rows. A changed account status does not start a new search or synchronization loop.

Only accounts with an accepted submission can be synchronized. Rows without one show **Awaiting acceptance** with a disabled Sync button. Supplier errors, busy responses and rate limits retain the last successful status, usage, amount and timestamp. A row shows concise failure feedback with details; HTTP 200 is not treated as success unless the per-account result confirms the update. Manual Sync is not retried automatically. If the server provides a retry interval, that row displays a cooldown before another manual attempt, including failed rows inside an HTTP 200 batch response. `Login retry` and `Query retry` identify which operation is waiting. Leaving the list or changing its scope cancels the UI request and ignores late responses; an already-started server operation may still save its result.

Opening an account detail page also reads saved history. **Sync now** explicitly requests a fresh collection there. Manual collection remains available with saved credentials when scheduled collection is paused. The configurable background schedule defaults to 300 seconds. Detail-page collection and the browser Auth JSON update flow remain explicit operations. Server authorization, rate limits, bounded concurrency and supplier availability apply to all fresh collection requests.

The account detail page shows cumulative spending, input/output tokens, quota-window usage and reset times. The trend supports **7, 30 or 90 days** and separate spending, input-token and output-token views. It renders smooth curves without permanent sample dots; hovering or focusing the chart reveals the time and value of a saved representative observation. Interpolation is monotone within each valid segment, so it cannot introduce peaks outside the neighboring values. Missing values, long collection gaps and counter resets break the curve. **View data** exposes original observations in a paginated table. **View submission** opens the associated submission record, with a return action to the account detail.

**Management → Account pool → Accounts** reuses the same saved account list with per-row Sync controls, usage details and trends across all users, adding the owner email in a **User** column and in account details. Administrators can search by account or owner email and explicitly sync an account. These routes require an active administrator session; ordinary members remain limited to their own accounts.

**Submission history** lists saved submission attempts; opening or refreshing a history list reads those stored records. Opening an individual submission detail does not issue an automatic status query. Its explicit status and billing controls remain available. These user-selected queries are separate from scheduled account-usage collection and the five post-operation rounds.

<a id="automatic-sync-after-api-submission"></a>

## Automatic Sync after account operations

A newly accepted **browser or API** account submission with a valid vendor account ID schedules a targeted account Sync job for **ten seconds after acceptance**. A confirmed **browser or API Auth JSON update** queues the same kind of job. Each job performs **five collection rounds regardless of success or failure**, waiting ten seconds after a completed round before starting the next. The original operation response does not wait for collection and returns saved account data where applicable; it does not perform an immediate supplier read just to populate that response.

The worker runs in the existing backend process and updates the same account snapshot used by Accounts and the personal-token lookup API. No additional frontend polling or special display is added. Loading an Accounts page or calling lookup still reads saved data only. Submission details no longer automatically query status when opened, and the browser Auth JSON editor no longer immediately collects usage after saving credentials.

For a submission, the accepted submission, stable local account and pending job are committed in one database transaction. For an Auth JSON update, the confirmed audit result, account update metadata and pending job are committed together. The existing `account_post_submit_sync_jobs` table stores both kinds of job:

| Field | Meaning |
| --- | --- |
| `id` | Local job ID. |
| `submission_id` | Foreign key to `submissions.id`, the accepted submission that anchors the account identity; multiple jobs can reference it. |
| `submission_trigger_id` | Nullable unique submission trigger identifier, equal to `submission_id` for a submission-triggered job. Ensures at most one job per accepted browser or API submission. |
| `auth_json_audit_id` | Nullable unique foreign key to `account_auth_json_audits.id`. Set only for an Auth JSON update job, ensuring at most one job per confirmed update. |
| `account_id` | Foreign key to `synced_accounts.id`, the stable local account. |
| `user_id` | Foreign key to `users.id`, the account owner. |
| `identity_key` | Account lifetime captured when the task is queued. |
| `status` | `pending`, `running`, `succeeded`, `failed` or `skipped`. Success in an early round does not finish the job. |
| `attempt_count` | Number of claimed collection rounds, from zero to five; local waits do not consume a round. |
| `success_count`, `failure_count` | Persisted counts of completed successful and failed collection rounds. |
| `next_run_at` | Earliest execution time in UTC; initially the successful operation time plus ten seconds. |
| `created_at`, `started_at`, `finished_at` | Job lifecycle timestamps in UTC. |
| `last_error` | Sanitized outcome without credentials or upstream response bodies. |

Exactly one trigger field is set for each job. Unique trigger constraints prevent duplicate completion notifications from creating duplicate jobs. Upgrades do not backfill historical operations or revive completed jobs.

The worker checks for due work every second and handles one job at a time. It uses the same Redis lease, `tk-register:{tk-register}:account-sync:lease`, as scheduled and manual synchronization. When that lease is occupied, it does not contact the supplier or consume a round; it leaves the job pending and writes `next_run_at = now + 5 seconds` to the database. The first collection therefore starts no earlier than ten seconds after the operation and can start later when other work is active.

After acquiring the lease, the worker claims the task and reserves a round atomically, obtains the shared session using the currently saved supplier website credentials, and queries only the affected account. It validates ownership, vendor, account identity and the current accepted submission before querying and before saving. Auth JSON jobs also verify that a newer confirmed update has not superseded their audit. Changed or ineligible targets are skipped instead of writing results onto a different account. Saved credentials are required; the periodic `enabled` switch controls the periodic schedule only.

After each of the first four completed rounds, successful or failed, the job returns to `pending` with `next_run_at = completion time + 10 seconds`. The fifth completed round ends the job: its result determines terminal `succeeded` or `failed`. There is no additional three-retry policy layered on top. A round is a bounded account-collection operation, not necessarily one HTTP request: authentication, filtered pagination, and the existing one-time reauthentication/read retry may occur within it. Long requests and queue waits make the total duration longer than fifty seconds; there are no overlapping rounds or bursts of missed rounds.

An actual supplier 429 counts as a failed round, but the next run is postponed until the later of the ten-second delay and the supplier's allowed retry time. A locally detected cooldown or login-lock wait returns the task to `pending`, restores its reserved round, and moves `next_run_at` to the allowed retry time. Waiting alone does not consume one of the five rounds. These updates are persisted, so a wait holds no HTTP response open and survives a restart. Every successful collection saves status and usage; every failure retains the last successful data. No round repeats account creation or an Auth JSON write.

For Auth JSON jobs, the audit's `refresh_status` remains `pending` until all five rounds finish, then reflects the fifth round as `succeeded` or `failed`; an ineligible target also ends the refresh as failed. Earlier successful observations remain saved even if the final round fails. The credential update outcome stays `updated`. The original HTTP response has already returned and does not change later; clients can read saved collection results and timestamps through account lookup.

Pending jobs survive backend restarts. An interrupted `running` job older than two minutes records that round as failed and uses the remaining five-round budget; it is rescheduled ten seconds later if rounds remain, otherwise it ends as failed. Recovery repeats only a read. Manual Sync and the regular configurable schedule are separate collection triggers and do not consume these five rounds. Skipping a missed periodic round never deletes a pending post-submission or post-update job.

## Values and history

- Supplier `cpa_usage.cost` is expressed in micro-CNY. The server divides it by **1,000,000** and continues to store the resulting amount as `cost_total` with currency `CNY`. The saved original amount and currency are unchanged.
- Every account snapshot response adds `cost_total_usd` and `exchange_rate_cny_per_usd`. For CNY snapshots, `cost_total_usd` is `cost_total / 7.2`; the rate is always `7.2` CNY per USD. The API retains calculation precision. Missing amounts and missing or unsupported currencies produce `cost_total_usd: null`; a known zero produces `0`.
- Member and administrator account lists, detail totals, spending trends, and observation tables display `cost_total_usd` with two decimal places and a trailing dollar sign (for example, `1,955.08 $`). Latest and historical responses use the same fixed rate. This is not a live or date-specific exchange-rate system, and no stored snapshot migration is required.
- Input and output tokens are cumulative counters. Five-hour and seven-day quota values are normalized to percentages by the server before display.
- An unavailable metric remains SQL `NULL` / JSON `null` and appears as **—**. A known zero remains `0`, `0%` or `0.00 $` as appropriate.
- Failed collection does not replace the latest successful snapshot with zeros. The list shows **Last known** and **Update failed**; detail shows the failure and the timestamp of the retained successful data. An account that has never been successfully collected continues to show **—**.
- New snapshots also retain the configured collection cadence as internal `collection_interval_seconds` metadata. This keeps earlier valid curves intact when an administrator changes the interval. Legacy snapshots without that metadata use the retired hours setting as the best available historical cadence; their original metrics are not rewritten.
- Snapshot dates follow the configured timezone; `collected_at` records the actual collection instant. The supplier's account-metadata timestamp is not treated as a usage-update timestamp, so `source_updated_at` stays null unless a reliable usage timestamp is available.
- Trends plot saved cumulative observations. Missing observations, long gaps, currency changes and counter resets are not joined into a continuous line. They do not represent zero consumption or calculated interval usage.
- A new accepted remote identity or plan starts a new displayed account lifetime. Earlier observations remain stored but are not connected to the new lifetime's trend.

### Trend response and original observations

The browser detail response includes `account`, `interval_seconds`, `timezone`, `trend`, `trend_reduced`, `snapshot_count`, `snapshots`, `snapshot_page`, and `snapshot_page_size`. `trend` has `cost_total_usd`, `input_tokens_total`, and `output_tokens_total` entries. Each is an array of separate curve segments; a point contains `snapshot_id`, `collected_at`, and `value`. Segments must not be connected across missing data or counter resets. Each metric returns at most 1,000 representative points. Reduction selects actual saved observations and retains segment endpoints; it does not fabricate amounts. If fragmented segments alone exceed the point budget, older complete segments can be omitted, with `trend_reduced: true`. The page indicates reduced chart detail and offers the paginated original observations. Point reduction changes only the visual representation, not stored snapshots or the latest account totals.

The detail response includes the first observation page, with `snapshot_page=1` and `snapshot_page_size=50`. Page 1 selects the newest observations; items within each page are displayed in chronological order. Additional pages are available from the snapshot endpoints below. `page_size` accepts 1–100 and defaults to 50. The total count refers to the requested 7/30/90-day range and current account lifetime. Empty ranges return an empty table and trend, never zero-valued synthetic observations.

Cumulative snapshots must not be summed to calculate a cumulative total. Any representative or time-bucketed cumulative display uses saved cumulative observations, not the sum of repeated counters. The paginated table remains the source for exact stored values and timestamps.

## Personal-token account lookup

External clients can read an owned account's saved data using `GET /tk-register/api/v1/vendors/tk-register-0/claude/accounts/lookup?username=<owner-email>&account=<submitted-account>` with a personal Bearer token carrying `accounts:read`. The endpoint returns the latest saved snapshot and collection timestamps without starting a supplier query or collection run. It uses the same null, zero and last-known-data semantics described above.

The username must match the token owner, including when that owner is an administrator. The limit is 600 lookup requests per minute per user. See the [Account Lookup API reference](account-lookup-api.md) for the complete contract and examples. This external endpoint is separate from the browser-session routes below.

## Updating Auth JSON

An accepted Claude account's detail page includes **Authentication → Update Auth JSON**. Members can update their own accounts; administrators can use the same editor in **Account pool → Accounts**. The editor accepts a complete JSON object pasted into the field or uploaded from a JSON file up to 2 MB. It never displays existing credentials. Validation uses the same Claude credential rules as account submission and additionally requires the credential email to match the target account after normalization.

External clients can use `POST /tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json` with a personal token carrying `claude:auth-json:update`. Send `username`, `account`, and the complete `auth_json`. The public API uses the same update, ownership-conflict and concurrency checks; personal tokens only access their owner's accounts, even for administrators. Its post-update collection is asynchronous, using the same five-round queue as the browser editor. Existing tokens receive the new permission automatically and remain usable without regeneration. See the [Auth JSON Update API reference](auth-json-update-api.md).

Opening the editor obtains the shared supplier session and searches using the complete account email. Only the filtered result pages are read. The backend compares full normalized account emails: one exact match identifies the account; multiple exact matches require an already-confirmed supplier website record ID. A missing or ambiguous match stops the operation. The plan is not an identity condition because the supplier may have changed it. The website record ID is distinct from its visible platform account ID and is stored separately on the local account.

The backend checks the supplier's current lock and raw account status both before editing and when saving. An unlocked account without a positive platform ID can be edited; otherwise the supplier must report `ban`, `offline`, or `login_failed`. These checks do not use the locally normalized `Unknown` status. The account's latest supplier form values are read again before the update, and only `cpa_auth_json` is replaced. Existing passwords, original submission records, tags, and unrelated account details are not changed.

After a confirmed update through either the browser editor or API, the server queues five targeted collections, with the first due ten seconds later. The browser reloads the currently saved detail and trend; it does not wait for these background rounds or start an immediate supplier query. A later page load reads newly saved observations. This does not wait for the periodic interval and does not claim that saving credentials has restored the account: the supplier's returned status remains authoritative.

Credential saving and subsequent account refresh have separate outcomes. Successful browser and API updates normally report `updated: true`, `refresh_status: pending`, and the currently saved account data, which may predate the update or be null if local readback fails. Collection runs later. A failure to queue collection does not undo a confirmed credential write; the response still reports `updated: true` with `refresh_status: failed` and an explanation. A timeout or connection loss after a write may have reached the supplier is reported as `AUTH_JSON_UPDATE_UNCONFIRMED`. Writes are never retried automatically. An ordinary subsequent GET or a user-selected **Sync now** cannot repeat the credential update.

Web updates use browser-session ownership/admin and CSRF checks; public updates use scoped personal Bearer tokens. They share per-user update rate limits and Redis leases under `tk-register:`. Conflicting accepted ownership stops the update. A supplier-account lease prevents concurrent updates for the same vendor account; the shared collection lease serializes scheduled, manual and post-update collection. Audit records contain the actor, owner, account, confirmed supplier record ID, request ID, source (`web` or `api`), API token reference and revision when applicable, outcome and refresh outcome, but no credential body. Replacement credentials are forwarded to the supplier without adding another local secret copy; the original encrypted submission remains historical. Public responses return the audit ID in `X-Auth-JSON-Audit-ID` once the record exists and in `data.audit_id` on success.

## Browser API contract

The following paths are relative to `/tk-register/api` and use the existing JSON envelope (`data` contains the payload). They require the current browser session. Writes additionally require the existing CSRF and origin checks; the user's personal API token does not authorize these browser-session routes.

| Method and path | Result |
| --- | --- |
| `GET /accounts?page=1&search=&status=` | Owner-scoped saved account list: `items`, `total`, `page`, `page_size`. |
| `POST /accounts/sync` | `{ "account_ids": ["1", "2"] }`, up to 20 owned local account IDs. Returns `accounts`, per-account `results` (`updated`, `failed`, `skipped`), and `updated_count`, `failed_count`, `skipped_count`. Partial failures do not discard successful observations. |
| `GET /accounts/:id?days=7` | Owned account, bounded segmented trend data, the first page of current-lifetime observations, collection interval in seconds and timezone. `days` accepts 7, 30 or 90. |
| `GET /accounts/:id/snapshots?days=7&page=1&page_size=50` | Paginated original observations for the owned account's current lifetime. Returns `items`, `total`, `page`, `page_size`. |
| `POST /accounts/:id/sync` | Explicit fresh collection using `{}`; returns the account with its latest saved result or `last_error`. |
| `GET /accounts/:id/auth-json` | Fresh supplier eligibility and account identity, without credentials: `account`, `plan`, `editable`, `reason`, `supplier_record_id`, `updated_at`. |
| `POST /accounts/:id/auth-json` | `{ "auth_json": object-or-JSON-string }`; updates credentials once and queues five delayed account collections. Returns `updated`, `refresh_status`, `refresh_message`, saved `account`, and `updated_at`. A confirmed update normally returns `refresh_status: pending`; `account` can be null if local readback fails, without canceling a queued refresh. |
| `GET /admin/synced-accounts?page=1&search=&status=` | Administrator saved account list across users; same metrics plus `user_id` and `user_email`. |
| `POST /admin/synced-accounts/sync` | Administrator version of targeted account synchronization; same request and response, with owner information in returned accounts. |
| `GET /admin/synced-accounts/:id?days=7` | Administrator account detail with the same bounded trend and snapshot-page contract. |
| `GET /admin/synced-accounts/:id/snapshots?days=7&page=1&page_size=50` | Administrator view of paginated original observations for the account's current lifetime. |
| `POST /admin/synced-accounts/:id/sync` | Administrator-triggered fresh collection for that account, with the same configuration, concurrency and cooldown checks. |
| `GET/POST /admin/synced-accounts/:id/auth-json` | Administrator version of the eligibility/update endpoints, with the same upstream and refresh behavior. |
| `GET /admin/account-sync` | Administrator configuration without the saved password, plus collection status. |
| `PUT /admin/account-sync` | Save `enabled`, `interval_seconds`, `timezone`, `website_username` and optional `website_password`. |
| `POST /admin/account-sync/test-login` | Test `website_username` and optional `website_password`; success returns `authenticated: true`. A blank password uses the saved password only for the same saved username. |

Accounts-list Sync batch requests allow **200 requests per minute per acting user**, shared across member and administrator list routes. Each batch still accepts at most 20 account IDs. Auth JSON editor eligibility GETs allow **300 requests per minute per acting user**. Auth JSON writes allow **100 requests per minute per user**, shared between browser and public API updates. Detail-page Sync retains its cooldown of one request per minute for each acting user and account.

Account snapshots in list, detail, historical, manual-sync and Auth JSON refresh responses share the original `cost_total` / `currency` fields and the derived `cost_total_usd` / `exchange_rate_cny_per_usd` fields described above. The personal-token lookup uses the same snapshot format.

The list sends a one-ID batch for each manual Sync click. Batch clients must inspect each result; HTTP 200 can contain both successful and failed rows. A waiting row includes `retry_after` (positive integer seconds) and `retry_stage` (`login`, `accounts`, or `login_lock`). A rejected detail Sync, login test or pre-write Auth JSON operation returns HTTP 429 with `Retry-After`, JSON `retry_after` and `retry_stage`. These supplier waits are distinct from the existing per-user application quota. Batch requests never report retained database data as a fresh supplier success. A successful HTTP response to single-account manual sync does not by itself mean the supplier collection succeeded: the returned account's `last_error` must also be checked. Missing configuration, concurrent execution, rate limits and service failures can instead produce an HTTP error. The UI reloads the saved detail after an explicit attempt so retained values and failure timestamps remain visible.

## Verification

Run `npm test` and `npm run build` in `tk-register-web-portal`. Frontend account tests cover missing-versus-zero values, unavailable currencies, gaps, counter resets and chronology without mutating input data. Mock browser checks cover desktop/mobile layout, manual collection failure reporting, testing without saving and clearing the password field after save. They do not contact the supplier or require real credentials.

## Account status and error details

`account_snapshots` adds nullable `upstream_status_raw` (VARCHAR(128)) and `upstream_error_message` (TEXT). The latter stores a bounded, sanitized supplier explanation, not raw response bodies or credentials. `synced_accounts.last_error` changes from VARCHAR(255) to TEXT so safe collection diagnostics are not replaced by a generic message. No table is added. Migration is additive for snapshots; existing rows remain NULL and are not backfilled.

Confirmed supplier states `login_failed`, `re_login_failed`, `bound_failed`, and `cooking_failed` retain distinct canonical meanings. `sync_failed` is displayed as **Sync error**. An unrecognized state remains **Unknown**, with its original value available in the detail popover.

Explicit manual Sync from the list or account detail page uses the bounded targeted collector. After an exact account match, banned and offline accounts perform one additional GET of `/supplier-account/api/supplier/accounts/{supplier_record_id}/claude-status-reason`, using the website record ID and the existing cached session, pacing, retry and cooldown mechanisms. This response is parsed as `{found, content}` and sanitized before persistence. Scheduled and post-write collection do not request reasons. An unchanged supplier state carries forward the last reported reason; a changed state or recovery clears it. An explicit successful lookup with `found: false` also clears it. A failed supplementary lookup preserves successfully collected metrics and returns a separate warning to the initiating browser.

Status badges and collection-failure messages open an overlay on hover or click; click pins it. Keyboard activation, Escape, outside-click dismissal and copy are supported. The overlay lives outside the table scroll container and does not expand rows. Supplier account failure and TK Register collection failure remain separate: a successful collection may report a banned account, while a failed collection retains the last successful account data. Account lookup responses include the new snapshot fields without requesting upstream data.
