# Daily usage

Account details now contain **Overview** and **Daily usage**. Overview keeps the
cumulative observation chart and adds a daily chart. Daily usage provides the
same daily chart, a period summary and a paginated breakdown. Personal pages
show totals across all models by default; Management retains the model breakdown.

When model details are enabled, models default to all selected. The checkbox menu supports search, multiple
selection, Select all and Clear selection. One filter controls summaries, chart
and table; switching the chart metric never removes table columns. Last 7 days
means seven complete Beijing calendar days. Last 30 days and custom ranges up
to 90 complete days are supported. No request count is inferred from token data.

## Collection

- At 03:00 Asia/Shanghai, the preceding calendar day becomes due. The background
  worker checks every 30 seconds. HTTP handlers do not download supplier bills.
- Each vendor/day is a durable job with a unique key and a fenced, recoverable
  lease. The initial run or an interruption recovers up to the last seven due
  days, newest first, one export at a time. Older uncollected days remain absent.
- The existing `account_sync_enabled` setting pauses or enables this worker.
  The existing supplier website credentials and encrypted token cache are reused.
  The snapshot interval/timezone do not change the fixed Beijing bill schedule.
- A single-day `GET /supplier-account/api/supplier/bill/claude-export` uses equal
  `start_ds` and `end_ds` (YYYYMMDD). The workbook has no date column; a multi-day
  export cannot be used as daily data. Snapshot differences are not a substitute.
- Exports share the distributed `tk-register:{tk-register}:bill-export:lease` with administrator
  exports. A busy lease is checked again after one second without claiming a job
  or consuming an attempt; this avoids repeatedly colliding with the administrator
  worker’s two-second idle cadence. They use the existing bill-export cooldown, independently of account
  queries. Read-only export authentication is retried using the existing client.
- An ordinary failed job retries after 5, 10, 15, 20 and 25 minutes, up to six
  attempts. A supplier cooldown defers to its retry time without consuming a
  failure attempt. A crashed worker can be reclaimed after its four-minute lease.
- The worker has a three-minute budget and the supplier download a 60-second
  timeout. It never overlaps with another export while holding the shared lease.
- The existing workbook safety limits apply (16 MiB compressed, 64 MiB expanded,
  50,000 source rows). Account selection is capped at 20,000 and fails explicitly
  if exceeded, rather than silently collecting one page.

## Storage and identity

Three additive tables are created by the existing startup migration:

| Table | Purpose | Uniqueness / foreign keys |
| --- | --- | --- |
| `daily_usage_jobs` | Vendor/date schedule, attempts, lease and safe error | UNIQUE(vendor_id, usage_date); vendor_id → vendors.id |
| `account_usage_days` | Successful coverage and latest attempt for one account/day | UNIQUE(account_id, identity_key, usage_date); account_id → synced_accounts.id |
| `account_daily_usages` | Aggregated model token counters and raw CNY cost | UNIQUE(day_id, model); day_id → account_usage_days.id |

Rows are matched with the saved platform account ID **and** exact normalized
account email. Submission/remote IDs are not substituted for platform IDs.
Missing IDs, conflicting local identities, or mismatched supplier emails are
reported as unavailable, never as zero. An identity change during collection
cannot attach data to a replacement account lifetime.

Duplicate model lines are summed with exact integer/rational arithmetic. Token
counters are stored and returned as decimal strings; CNY amounts are stored as
DECIMAL(30,8). No passwords, tokens, Auth JSON, supplier remarks or full workbook
are persisted by this feature.

Each account/day is replaced atomically on a successful retry, not accumulated
again. Confirmed exports without matching bill lines record successful zero
coverage with no invented model rows. Failure preserves the last successful
rows and collection timestamp. Historical rows survive account deletion; normal
account authorization continues to exclude removed accounts.

USD is calculated at read time from the existing `exchange_rate_cny_per_usd`
setting, consistent with existing account charts. Each response pins one rate.
The stored CNY value remains unchanged. Dollar values use the `12.50 $` format.

## Authenticated browser endpoints

`GET /tk-register/api/accounts/:id/daily-usage?start_date=YYYY-MM-DD&end_date=YYYY-MM-DD`

The administrator equivalent is
`GET /tk-register/api/admin/synced-accounts/:id/daily-usage`.

The routes reuse session authentication and existing owner/recursive member
visibility. Administrator routes require an administrator. Foreign accounts
return 404. This does not expand personal API-token permissions or change the
existing public account lookup API contract.

Responses include `start_date`, `end_date`, `timezone`, `collection_time`,
`currency` (raw CNY), `exchange_rate_cny_per_usd`, `rows` and `days`.
Each row contains date, model, input/output/cache-read/cache-write token strings,
`cost_cny` and numeric `cost_usd`. Each day identifies `complete`, `pending`,
`missing`, `failed` or `stale`, with the last successful `collected_at` if any.
When Config → Overview disables model details (the default), the personal route
omits `rows` and returns `daily_totals` without any model breakdown. Totals are
aggregated on the server with exact token counters. The administrator Management
route retains the model rows. The personal response includes
`model_costs_enabled` so the UI uses the matching presentation.
Queries are bounded to 90 complete days and 10,000 model rows. Missing days stay
missing on the chart, and partial-period summaries are labelled accordingly.

## Verification

- Backend tests cover day boundary, restart deduplication, lease fencing,
  cooldown deferral, precise aggregation, identity conflicts, retained successes,
  empty days, configured FX conversion and ownership/member/admin visibility.
- Frontend unit tests cover Beijing ranges, all/multiple/empty model selections,
  exact token sums and missing-versus-zero chart data.
- `node tests/daily-usage.acceptance.mjs` exercises Overview/Daily usage, metrics,
  selection/search, date presets, pagination, admin routes and responsive layout.
  Use PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH if the browser is not bundled.
