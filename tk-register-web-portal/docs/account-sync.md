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

Opening the list or detail page reads stored database results. The list refresh button reloads those stored results; it does not contact the supplier. **Sync now** on an account detail page explicitly requests a fresh collection. It is available for an account with an accepted submission, subject to server configuration, concurrency and cooldown checks. Manual collection remains available with saved credentials when automatic collection is paused.

The account detail page shows cumulative spending, input/output tokens, quota-window usage and reset times. The trend supports **7, 30 or 90 days** and separate spending, input-token and output-token views. **View data** exposes the saved observations in a table. **View submission** opens the associated submission record, with a return action to the account detail.

**Management → Account pool → Accounts** reuses the same saved account list, usage details and trends across all users, adding the owner email in a **User** column and in account details. Administrators can search by account or owner email and explicitly sync an account. These routes require an active administrator session; ordinary members remain limited to their own accounts.

**Submission history** lists saved submission attempts; opening or refreshing a history list reads those stored records. Individual submission details retain their explicit status and billing queries. These queries are separate from scheduled account-usage collection, and account usage pages do not alter the account-submission workflow.

## Values and history

- Supplier `cpa_usage.cost` is expressed in micro-CNY. The server divides it by **1,000,000**, stores the resulting amount with currency `CNY`, and the frontend displays that amount directly. It does not apply the supplier page's fixed-rate USD conversion.
- Input and output tokens are cumulative counters. Five-hour and seven-day quota values are normalized to percentages by the server before display.
- An unavailable metric remains SQL `NULL` / JSON `null` and appears as **—**. A known zero remains `0`, `0%` or `CNY 0.00` as appropriate.
- Failed collection does not replace the latest successful snapshot with zeros. The list shows **Last known** and **Update failed**; detail shows the failure and the timestamp of the retained successful data. An account that has never been successfully collected continues to show **—**.
- Snapshot dates follow the configured timezone; `collected_at` records the actual collection instant. The supplier's account-metadata timestamp is not treated as a usage-update timestamp, so `source_updated_at` stays null unless a reliable usage timestamp is available.
- Trends plot saved cumulative observations. Missing observations, long gaps, currency changes and counter resets are not joined into a continuous line. They do not represent zero consumption or calculated interval usage.
- A new accepted remote identity or plan starts a new displayed account lifetime. Earlier observations remain stored but are not connected to the new lifetime's trend.

## Personal-token account lookup

External clients can read an owned account's saved data using `GET /tk-register/api/v1/vendors/tk-register-0/claude/accounts/lookup?username=<owner-email>&account=<submitted-account>` with a personal Bearer token carrying `accounts:read`. The endpoint returns the latest saved snapshot and collection timestamps without starting a supplier query or collection run. It uses the same null, zero and last-known-data semantics described above.

The username must match the token owner, including when that owner is an administrator. The limit is 60 lookup requests per minute per user. See the [Account Lookup API reference](account-lookup-api.md) for the complete contract and examples. This external endpoint is separate from the browser-session routes below.

## Browser API contract

The following paths are relative to `/tk-register/api` and use the existing JSON envelope (`data` contains the payload). They require the current browser session. Writes additionally require the existing CSRF and origin checks; the user's personal API token does not authorize these browser-session routes.

| Method and path | Result |
| --- | --- |
| `GET /accounts?page=1&search=&status=` | Owner-scoped saved account list: `items`, `total`, `page`, `page_size`. |
| `GET /accounts/:id?days=7` | Owned account, current-lifetime snapshots, collection interval and timezone. `days` accepts 7, 30 or 90. |
| `POST /accounts/:id/sync` | Explicit fresh collection using `{}`; returns the account with its latest saved result or `last_error`. |
| `GET /admin/synced-accounts?page=1&search=&status=` | Administrator saved account list across users; same metrics plus `user_id` and `user_email`. |
| `GET /admin/synced-accounts/:id?days=7` | Administrator view of a stable synced account and its snapshots. |
| `POST /admin/synced-accounts/:id/sync` | Administrator-triggered fresh collection for that account, with the same configuration, concurrency and cooldown checks. |
| `GET /admin/account-sync` | Administrator configuration without the saved password, plus collection status. |
| `PUT /admin/account-sync` | Save `enabled`, `interval_hours`, `timezone`, `website_username` and optional `website_password`. |
| `POST /admin/account-sync/test-login` | Test `website_username` and optional `website_password`; success returns `authenticated: true`. A blank password uses the saved password only for the same saved username. |

A successful HTTP response to manual sync does not by itself mean the supplier collection succeeded: the returned account's `last_error` must also be checked. Missing configuration, concurrent execution, rate limits and service failures can instead produce an HTTP error. The UI reloads the saved detail after an explicit attempt so retained values and failure timestamps remain visible.

## Verification

Run `npm test` and `npm run build` in `tk-register-web-portal`. Frontend account tests cover missing-versus-zero values, unavailable currencies, gaps, counter resets and chronology without mutating input data. Mock browser checks cover desktop/mobile layout, manual collection failure reporting, testing without saving and clearing the password field after save. They do not contact the supplier or require real credentials.
