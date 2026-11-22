# Daily Usage API

Read saved daily bills for your Claude accounts on one completed Beijing calendar day.

| Property | Value |
| --- | --- |
| Method | `POST` |
| Path | `/tk-register/api/v1/vendors/tk-register-0/claude/accounts/daily-usage` |
| Authentication | Personal TK Register token in `Authorization: Bearer <token>` |
| Required scope | `accounts:read` |
| Content type | `application/json` (a charset parameter is accepted) |
| Request body | Exactly `username`, `accounts`, and `date` |
| Limits | 1–100 input account identifiers; 64 KiB request body |
| Rate limit | 60 requests per minute per authenticated user, separate from account lookups and submissions |
| Response | TK Register `success` / `data` JSON envelope |

This endpoint reads saved bills. It does not submit accounts, contact the supplier, refresh bills, or schedule collection. Repeating a request cannot fill a missing bill.

## Authentication and ownership

Generate or manage your personal token in **Account settings → API token**. Existing valid tokens with `accounts:read` can use this endpoint without regeneration. A browser session or CSRF token is not required.

`username` must identify the token owner. An administrator or super-administrator's personal token also accesses only that user's own accounts. This endpoint does not include sub-users automatically or grant access to their accounts.

A different owner's username rejects the entire request with `404 ACCOUNT_NOT_FOUND`. A missing or inaccessible individual account instead receives `status: "not_found"` in an HTTP 200 batch response. The API does not reveal which of these two cases caused `not_found`.

## Request

| Field | Type | Rules |
| --- | --- | --- |
| `username` | string | Your TK Register login email; valid ASCII email, at most 50 bytes, matching the token owner after normalization. |
| `accounts` | string[] | 1–100 submitted Claude login identifiers or emails. Each is at most 254 UTF-8 bytes after trimming and cannot contain internal whitespace or control characters. |
| `date` | string | Valid `YYYY-MM-DD` date, yesterday or earlier in `Asia/Shanghai`. Today and future dates are rejected. |

All three fields are required and must occur exactly once. Extra fields, duplicate JSON keys, query parameters, and trailing JSON values are rejected. Send one JSON object rather than form data.

Surrounding whitespace is trimmed and account identifiers are matched case-insensitively. Duplicate normalized accounts are removed, keeping the first occurrence order. The 100-entry limit applies **before** deduplication: 101 copies of the same identifier still exceed the limit. `summary.requested_accounts` counts the unique normalized identifiers.

For example, when Beijing time is November 22 at 00:01, November 21 is accepted and November 22 is rejected. The caller's computer timezone does not change this boundary.

### cURL example

Replace the token, deployment URL, owner email, submitted account identifiers, and date before use.

```sh
export TK_REGISTER_API_TOKEN='YOUR_TOKEN'
TK_REGISTER_BASE_URL='https://47.110.71.25:8443'

curl --silent --show-error --include --request POST \
  "${TK_REGISTER_BASE_URL}/tk-register/api/v1/vendors/tk-register-0/claude/accounts/daily-usage" \
  --header "Authorization: Bearer ${TK_REGISTER_API_TOKEN}" \
  --header 'Content-Type: application/json' \
  --data-binary @- <<'JSON'
{
  "username": "member@example.com",
  "accounts": [
    "claude.user@example.com",
    "waiting@example.com",
    "missing@example.com"
  ],
  "date": "2026-11-01"
}
JSON
```

The example uses normal TLS certificate verification. For a deployment with a self-signed certificate, use its trusted CA with `--cacert /path/to/ca.pem`.

## Successful response

HTTP 200 means the batch was processed. It does not mean every requested account has a bill. Inspect both `summary.status` and the individual account statuses.

Model costs are disabled by default. This response omits `models` and `model_totals` entirely:

```json
{
  "success": true,
  "data": {
    "username": "member@example.com",
    "date": "2026-11-01",
    "timezone": "Asia/Shanghai",
    "exchange_rate_cny_per_usd": "7.2",
    "model_costs_enabled": false,
    "summary": {
      "status": "partial",
      "requested_accounts": 3,
      "available_accounts": 1,
      "no_data_accounts": 1,
      "not_found_accounts": 1,
      "cost_cny": "72.00000000",
      "cost_usd": "10.00"
    },
    "accounts": [
      {
        "account": "claude.user@example.com",
        "status": "ok",
        "cost_cny": "72.00000000",
        "cost_usd": "10.00"
      },
      {
        "account": "waiting@example.com",
        "status": "no_data",
        "cost_cny": null,
        "cost_usd": null
      },
      {
        "account": "missing@example.com",
        "status": "not_found",
        "cost_cny": null,
        "cost_usd": null
      }
    ]
  }
}
```

### Response fields

| Field in `data` | Meaning |
| --- | --- |
| `username` | Normalized token-owner email. |
| `date` | Requested billing date. |
| `timezone` | Always `Asia/Shanghai`. |
| `exchange_rate_cny_per_usd` | Configured CNY-per-USD exchange rate, as a decimal string. The example uses `"7.2"`; use the returned value. |
| `model_costs_enabled` | Whether the administrator has enabled API model details. Independent of website model visibility. |
| `summary` | Availability counts and total costs across available accounts. |
| `accounts` | One result per unique requested account, in first occurrence order. |
| `model_totals` | Combined costs by model; only present when `model_costs_enabled` is true. |

`summary` contains:

| Field | Meaning |
| --- | --- |
| `status` | `complete` when every requested account has a bill; `partial` when some have bills; `no_data` when none have bills. |
| `requested_accounts` | Unique account count after normalization and deduplication. |
| `available_accounts` | Number of `ok` results, including saved zero-cost bills. |
| `no_data_accounts` | Number of owned accounts without a bill on the requested date. |
| `not_found_accounts` | Number of missing or inaccessible account identifiers. |
| `cost_cny` | Sum of available account costs, with eight decimal places, or null when none are available. |
| `cost_usd` | Sum of available accounts' reported USD costs, with two decimal places, or null when none are available. |

Each account contains its normalized `account`, a `status`, nullable `cost_cny` / `cost_usd`, and an optional `models` array. Account statuses are `ok`, `no_data`, and `not_found`; they describe bill availability, not whether the Claude account is active or banned.

### Missing data and zero

- A saved zero-cost bill uses `status: "ok"`, `cost_cny: "0.00000000"`, and `cost_usd: "0.00"`.
- `no_data` and `not_found` use null amounts, never zero.
- If no bills are available, `summary.status` is `no_data` and both summary amounts are null. This also applies when every account is `not_found`; the counts retain that distinction.
- Missing bills are excluded from sums. A partial total represents only accounts with available bills.

### Optional model costs

An administrator can enable per-model costs for this API. Clients cannot enable them by adding request fields. When `model_costs_enabled` is true, every account has a `models` array and `data` has `model_totals`. The model object has the same fields in both arrays:

```json
{
  "model": "claude-opus-example",
  "cost_cny": "54.00000000",
  "cost_usd": "7.50"
}
```

For example, a `"72.00000000"` CNY / `"10.00"` USD account can have these model costs:

```json
{
  "account": "claude.user@example.com",
  "status": "ok",
  "cost_cny": "72.00000000",
  "cost_usd": "10.00",
  "models": [
    { "model": "claude-fable-example", "cost_cny": "18.00000000", "cost_usd": "2.50" },
    { "model": "claude-opus-example", "cost_cny": "54.00000000", "cost_usd": "7.50" }
  ]
}
```

`no_data` and `not_found` accounts have `models: []`. If no accounts have available bills, `model_totals` is also `[]`. When model costs are disabled, all these model fields are omitted rather than returned as empty arrays. Account and summary totals remain available.

### Decimal precision and totals

All costs and the exchange rate are JSON strings. Use decimal arithmetic, not binary floating-point accumulation, for money. CNY costs have eight decimal places; USD costs have two.

Each account's USD total is calculated first using the returned exchange rate. Summary USD is the sum of those reported account totals. A model's combined USD cost is the sum of its reported USD amounts across accounts, and each account's model USD amounts add up to that account's USD total. Rounding is reconciled within each account. Converting the combined CNY total once may differ by a few cents from the returned USD summary because account totals are rounded individually.

## Errors and retries

Request errors use an envelope such as:

```json
{
  "success": false,
  "code": "INVALID_DATE",
  "message": "Choose yesterday or an earlier date in Asia/Shanghai."
}
```

Use HTTP status and `code`; do not depend on exact message text.

| HTTP | Code | Meaning |
| --- | --- | --- |
| 400 | `INVALID_REQUEST` | Invalid JSON, missing or duplicate fields, extra fields/query parameters, invalid identifiers, empty accounts, or more than 100 input entries. |
| 400 | `INVALID_DATE` | Invalid date, today, or a future date in Beijing time. |
| 401 | `INVALID_API_TOKEN` | Missing, invalid, revoked, or expired token, or a user who cannot authenticate. |
| 403 | `INSUFFICIENT_SCOPE` | Valid token without `accounts:read`. |
| 404 | `ACCOUNT_NOT_FOUND` | `username` is not the token owner. Individual unavailable accounts use HTTP 200 results instead. |
| 405 | `METHOD_NOT_ALLOWED` | Use POST; the response includes `Allow: POST`. |
| 413 | `REQUEST_TOO_LARGE` | Body exceeds 64 KiB. |
| 415 | `INVALID_CONTENT_TYPE` | Send `Content-Type: application/json`. |
| 429 | `RATE_LIMITED` | Daily-usage request limit reached. Wait for the returned retry duration. |
| 503 | `UNAVAILABLE` | A required local database or rate-limit service is unavailable. |
| 504 | `REQUEST_TIMEOUT` | Saved-bill read timed out. |

The per-user limit is 60 requests per minute, independent of account-lookup and submission limits. `429 RATE_LIMITED` includes a `Retry-After` header and JSON `retry_after`, both in seconds. Wait for that duration before retrying. Transient connection errors and 503/504 responses can be retried with a delay because this endpoint only reads saved bills. Repeated requests do not refresh missing bills. Keep `X-Request-ID` when contacting support.

The same guide is available in **API documentation → Daily usage** at `/tk-register#/docs/daily-usage`. For cumulative snapshots, use the separate [Account Lookup API](account-lookup-api.md).
