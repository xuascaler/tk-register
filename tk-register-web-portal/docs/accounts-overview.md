# Accounts overview

`#/accounts` opens the personal Overview; `#/accounts?tab=list` opens the existing
personal account list. Both tabs share the current Source selection.

Administrators also have Overview and Account list tabs under Account pool →
Accounts. The administrative Overview uses a separate Source selection and shows
all system users. The existing administrative list retains its search, status
filter, pagination, Sync, and Del actions.

Overview defaults to the previous seven complete calendar days in Asia/Shanghai.
Source and model filters initially select all entries. Date shortcuts include
the past day, seven days, thirty days, and All time; custom dates end no later than
yesterday. All time starts at the first successfully collected daily bill for the
selected sources and current account identities. If none exists, it shows an
empty yesterday. Changing filters only reads the database.

## Data and visibility

The session-authenticated `GET /tk-register/api/accounts/overview` endpoint accepts
the existing `sources` filter and either `start_date` / `end_date` or `range=all`.
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
statuses, cumulative spend, model costs, and daily usage.

Account counts, detailed statuses, and cumulative spend follow Source only.
Daily usage and model costs also follow the selected dates and models. Models are
filtered in the browser over already aggregated date/model rows. Currency
conversion uses the existing configured CNY-per-USD exchange rate.

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

No new tables, upstream requests, or collection schedules are introduced.

## Verification

- Backend: `go test ./...` from `server`.
- Frontend: `npm test` and `npm run build`.
- Browser: `node tests/overview.acceptance.mjs`,
  `node tests/admin-overview.acceptance.mjs`, and
  `node tests/daily-usage.acceptance.mjs`.
- The browser tests accept `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` for a locally
  installed Chrome executable.
