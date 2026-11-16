# Accounts overview

`#/accounts` opens the personal Overview; `#/accounts?tab=list` opens the existing
personal account list. Both tabs share the current Source selection.

Administrators also have Overview and Account list tabs under Account pool →
Accounts. The administrative Overview uses a separate Source selection and shows
all system users. The existing administrative list retains its search, status
filter, pagination, and Sync action. Account deletion is not exposed in the frontend.

Personal and administrative Overview default to All time in Asia/Shanghai.
Source and model filters initially select all entries. Date shortcuts include
the past day, seven days, thirty days, and All time; custom dates end no later than
yesterday. All time starts at the first successfully collected daily bill for the
selected sources and current account identities. If none exists, it shows an
empty yesterday. Changing filters only reads the database.

## Data and visibility

The session-authenticated `GET /tk-register/api/accounts/overview` endpoint accepts
the existing `sources` filter and either `period=day|week|month|all_time` or
`start_date` / `end_date`. Legacy `range=all` remains supported. Requests also
accept `models=all` (default), `models=none`, or a comma-separated model selection.
It uses the existing recursive visibility rules. Descendants are grouped under
the viewer's direct member; their underlying user identities are not exposed.
Deleted accounts and previous remote identities are excluded.

The administrator-only `GET /tk-register/api/admin/accounts/overview` accepts the
same date and Source parameters. Its Source options come from
`GET /tk-register/api/admin/accounts/overview/sources`. The current administrator
is labeled `myself`; other users are labeled with their email addresses. Every
row groups accounts by their actual owner, without recursively including that
user's descendants. Users with no accounts remain available in the Source
filter. Each account contributes once to administrative totals. These endpoints
require an administrator browser session and do not accept personal API tokens.

For example, if A created B and B created C, A's personal Overview groups B's and
C's accounts under B. The administrative Overview shows A, B, and C separately,
each with only their own accounts. This distinction applies to account counts,
statuses, period spend, model costs, and daily usage.

Account counts and detailed statuses follow Source only. All Overview monetary
figures—the top Total spent, Source rows, model costs, and Daily usage cost—use
the same daily bills for the selected sources, dates, and models. All time means
all available daily bills in the saved date range, not the supplier lifetime
cumulative amount. The separate Accounts list retains its cumulative spend.

Models are filtered in the browser over already aggregated rows; an explicit
API model selection applies the same filtering to period rows, model costs, and
Source fees. Currency conversion uses the existing configured CNY-per-USD
exchange rate. Raw CNY billing values are unchanged. USD amounts are reconciled
to cents across Source/model cells and daily rows so the displayed Source,
model, and period totals agree, including after model selection. A rounding
remainder is assigned deterministically. Summing reconciled cells can produce
a small rounding difference from independently converting and rounding a
subtotal; all displayed totals consistently use the reconciled cells.

Counts are aggregated separately from daily bill lines to prevent duplicate
counting. Daily coverage uses collection markers, including successfully
collected zero-usage days. Missing data remains missing, not zero. Partial totals
contain only the collected values. Latest successful account data remains
available when a subsequent collection fails.

## Charts

Ranges up to fourteen days use stacked bars. Longer ranges use 3.5 px smooth
monotone curves without overshooting observed points. Missing days break the
curve. Hover or use the arrow keys on the chart to inspect exact daily values.
Counts and totals do not navigate to other pages.

## Persisted preset summaries

The `overview_summaries` table stores one summary per viewer, view
(personal or administrative), and period (`day`, `week`, `month`, `all_time`).
Each summary covers all sources and models and stores the actual date range,
generation time, batch ID, and a versioned JSON payload. Raw CNY decimal amounts
and integer token strings are retained; USD conversion uses the current exchange
rate on read. Model subsets filter the saved per-model rows and their period fees without
recalculating account status counts. Existing version-1 summaries remain usable:
Overview fees are derived from their saved daily bills and model rows rather
than their legacy cumulative-cost fields.

After the scheduled 03:00 Asia/Shanghai daily bill job succeeds, that existing
`daily_usage_jobs` row becomes eligible for a separate summary worker. Only the
current due billing date queues this follow-up; historical catch-up jobs and
previously completed jobs do not trigger generation on deployment or restart.
The worker uses its own Redis coordination key, separate from account sync and
bill-export locks, and reads the local database without contacting the supplier.
It generates personal views for every user and a flat administrative view for
each administrator. Each viewer's four periods publish atomically under the job
claim fence. A stale worker cannot overwrite a newer generation.

Summary failures are recorded on the billing job and are **not retried**. Failed
views retain their previous summaries; other users can still finish. Interrupted
running jobs are marked failed after their lease expires. Pending jobs that were
never claimed can run after restart. The next daily billing job creates its own
normal follow-up. Summary work is bounded to 90 seconds per view and 30 minutes
per batch, with a 35-minute coordination lease.

All-source preset reads use only the saved summary plus the current exchange
rate. Page requests never generate a summary, enqueue work, or fall back to live
aggregation when a summary is missing. New users wait for the next scheduled
batch. Missing summaries return `summary_available: false` and the UI displays
red `—` values and an empty chart. Existing summaries return
`summary_available: true`, `summary_generated_at`, and `summary_period`; the UI
shows the actual saved date range and generation time even when the preset is
older than yesterday.

Custom date ranges and Source subsets retain on-demand database aggregation.
Changing only the model selection can reuse an all-source preset summary.
Account transfers, deletions, and hierarchy changes are reflected in the next
nightly preset generation, not rebuilt during the day. Session validity and
administrator privileges are still checked on every request. Personal and
administrative summaries have separate keys. Account list and manual Sync retain
their current behavior and can therefore be newer than an Overview summary.

The migration adds only the summary table and `overview_*` follow-up fields on
`daily_usage_jobs`; existing billing rows are retained. No initial summary
backfill is performed.

## Verification

- Backend: `go test ./...` from `server`.
- Frontend: `npm test` and `npm run build`.
- Browser: `node tests/overview.acceptance.mjs`,
  `node tests/admin-overview.acceptance.mjs`, and
  `node tests/daily-usage.acceptance.mjs`.
- The browser tests accept `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` for a locally
  installed Chrome executable.
