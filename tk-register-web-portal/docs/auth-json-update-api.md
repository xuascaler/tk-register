# Auth JSON Update API

The Auth JSON Update API replaces the authentication data for an accepted Claude account owned by the caller. It reuses the web editor’s supplier identity checks and update operation. A confirmed update does not start or queue account collection. The existing scheduled collector updates account status and usage independently. The update does not create another submission.

| Property | Value |
| --- | --- |
| Method | `POST` |
| Path | `/tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json` |
| Authentication | Personal TK Register token in `Authorization: Bearer <token>` |
| Required scope | `claude:auth-json:update` |
| Content type | `application/json` |
| Request body limit | 3 MiB; the decoded Auth JSON object is limited to 2 MiB |
| Rate limit | 100 update requests per minute per user, shared with web Auth JSON updates |
| Response format | TK Register JSON envelope, not the supplier response body |

## Authentication and ownership

Generate a token under **My account → Account settings → API token** if you do not already have one. Newly generated tokens include `claude:submit`, `accounts:read`, and `claude:auth-json:update`. Existing tokens receive update access automatically during the upgrade, with their token value unchanged. No regeneration or script changes are required. This upgrade does not reactivate revoked or expired tokens. If a valid token still receives `403 INSUFFICIENT_SCOPE`, ask an administrator to check its permissions.

The token identifies the caller. `username` must match that user's TK Register email, including when the caller is an administrator. An administrator's personal token cannot update other users' accounts. The supplier website username and password are configured by an administrator; callers do not provide them.

The server finds the local account by owner, URL vendor, and normalized account name. Missing owned accounts and mismatched usernames return `404 ACCOUNT_NOT_FOUND`. Accounts must have an accepted submission and a supported Claude plan. Conflicting ownership or ambiguous supplier matches stop the update. The supplier must also currently permit credential editing.

## Request

Send exactly the three fields below, each once, in a JSON object. Duplicate or unsupported top-level keys are rejected. Do not add query parameters, a vendor record ID, `plan`, `password`, or other top-level fields.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `username` | string | Yes | TK Register login email, matching the token owner. Valid ASCII email of at most 50 bytes. |
| `account` | string | Yes | Submitted Claude account email/login. Up to 254 UTF-8 bytes after trimming; no internal whitespace or control characters. |
| `auth_json` | object or JSON string | Yes | Complete replacement credentials, not a partial patch. Its `email` must equal `account` after trimming and case normalization. |

All 13 Auth JSON fields must be present:

| Field | Validation |
| --- | --- |
| `id_token` | String; may be empty. |
| `access_token` | Nonempty string. |
| `refresh_token` | Nonempty string. |
| `last_refresh` | RFC3339 timestamp with a timezone. |
| `email` | Nonempty string matching the requested account after normalization. |
| `account_uuid` | Nonempty string. |
| `organization_uuid` | Nonempty string. |
| `organization_name` | Nonempty string. |
| `claude_device_ids` | Array of 1–32 nonempty strings, each at most 1,024 UTF-8 bytes. |
| `type` | Nonempty string. |
| `expired` | RFC3339 timestamp with a timezone. |
| `proxy_url` | URL with a host and scheme `http`, `https`, `socks5`, or `socks5h`. |
| `verification_status` | Nonempty string. |

String fields are limited to 16,384 UTF-8 bytes, except device IDs as described above. Additional fields inside the authentication object are forwarded. The supplied credentials are not echoed in the response or saved in the update audit.

### cURL example

Set `TK_REGISTER_API_TOKEN` to your personal token. Replace all example account values, credential placeholders, and timestamps before use. This request changes credentials at the supplier.

```sh
curl --silent --show-error --include --request POST \
  'https://47.110.71.25:8443/tk-register/api/v1/vendors/tk-register-0/claude/accounts/auth-json' \
  --header "Authorization: Bearer ${TK_REGISTER_API_TOKEN}" \
  --header 'Content-Type: application/json' \
  --data-binary @- <<'JSON'
{
  "username": "member@example.com",
  "account": "claude.user@example.com",
  "auth_json": {
    "id_token": "",
    "access_token": "YOUR_ACCESS_TOKEN",
    "refresh_token": "YOUR_REFRESH_TOKEN",
    "last_refresh": "2026-11-01T00:00:00Z",
    "email": "claude.user@example.com",
    "account_uuid": "YOUR_ACCOUNT_UUID",
    "organization_uuid": "YOUR_ORGANIZATION_UUID",
    "organization_name": "YOUR_ORGANIZATION",
    "claude_device_ids": ["YOUR_DEVICE_ID"],
    "type": "YOUR_AUTH_TYPE",
    "expired": "2026-12-01T00:00:00Z",
    "proxy_url": "http://user:password@proxy.example.com:8080",
    "verification_status": "YOUR_VERIFICATION_STATUS"
  }
}
JSON
```

Use a trusted server certificate or supply its CA with `--cacert /path/to/ca.pem`. For the existing IP deployment with a self-signed certificate you intentionally trust, explicitly add `--insecure` for the test request.

## Successful response and saved account data

HTTP `200` with `data.updated: true` means the supplier confirmed the credential change. The response has `refresh_status: "not_requested"`: this operation neither queries the supplier again nor schedules follow-up collections. It does not imply that the account is active or that its status, usage, or spend has refreshed.

| Field in `data` | Type | Meaning |
| --- | --- | --- |
| `updated` | boolean | Confirmed credential update; `true` in a successful response. |
| `audit_id` | string | Local update audit ID. Not a submission ID or supplier account ID. |
| `updated_at` | RFC3339 string | Time the supplier update finished. |
| `refresh_status` | string | `not_requested`; no post-update collection is requested. |
| `refresh_message` | string | Safe description for humans. Use status fields for program logic. |
| `account` | object or null | Same account object as [Account lookup](account-lookup-api.md#account-fields), read from saved data. Its snapshot may predate this update, or be null if no collection has succeeded. The whole `account` is null if local readback fails. |

The `account` object includes saved account identifiers, submission status, collection timestamps, and the snapshot. Snapshot fields include `account_status`, usage windows, cumulative token counters, `cost_total`, `currency`, `cost_total_usd`, and `exchange_rate_cny_per_usd`. USD conversion is `cost_total / exchange_rate_cny_per_usd`. The example below uses `7.2`. Neither the account object nor the snapshot contains credentials.

Both browser and API updates retain the last collected account status, quota windows, token totals, spend and collection time. An Offline account remains Offline in the response until a subsequent ordinary collection saves a different status. The regular configurable schedule and explicit **Sync** action remain separate collection triggers. Account creation continues to use its existing five-round follow-up policy; credential replacement does not.

For example, this response contains a snapshot collected five minutes before the credential update:

```json
{
  "success": true,
  "data": {
    "updated": true,
    "audit_id": "2001",
    "updated_at": "2026-11-01T02:05:00+08:00",
    "refresh_status": "not_requested",
    "refresh_message": "Auth JSON updated. Account status is updated by scheduled background checks.",
    "account": {
      "id": "1001",
      "username": "member@example.com",
      "account": "claude.user@example.com",
      "vendor": "tk-register-0",
      "plan": "claude/cc-max-20x",
      "remote_id": "300001",
      "platform_account_id": "15001",
      "submission_status": "accepted",
      "snapshot": {
        "id": "5001",
        "account_id": "1001",
        "platform_account_id": "15001",
        "snapshot_date": "2026-11-01",
        "collected_at": "2026-11-01T02:00:00+08:00",
        "source_updated_at": null,
        "account_status": "offline",
        "input_tokens_total": 120000,
        "output_tokens_total": 240000,
        "cost_total": 123.45,
        "currency": "CNY",
        "cost_total_usd": 17.145833333333332,
        "exchange_rate_cny_per_usd": 7.2,
        "usage_5h_percent": 15,
        "reset_5h_at": "2026-11-01T06:00:00+08:00",
        "usage_7d_percent": 42,
        "reset_7d_at": "2026-11-07T00:00:00+08:00",
        "data_source": "website"
      },
      "last_success_at": "2026-11-01T02:00:00+08:00",
      "last_attempt_at": "2026-11-01T02:00:00+08:00",
      "last_error": null,
      "interval_seconds": 300
    }
  }
}
```

If local persistence or readback fails after a confirmed supplier write, the response still has `updated: true` and `refresh_status: "not_requested"`, with a safe explanation. `account` can be null. A local read failure does not undo the supplier's confirmed credential change or trigger a retry.

Use [Account lookup](account-lookup-api.md) later to read the latest saved result and its `last_success_at`, `snapshot.collected_at`, `last_attempt_at` and `last_error`. Lookup never starts a supplier query. Do not repeat the credential write to obtain fresh data.

## Audit records

After authentication, validation, and lock acquisition, the server creates an `account_auth_json_audits` record before calling the supplier. The record starts as `pending`, then becomes `updated`, `rejected`, `failed`, or `unknown`. New confirmed updates record refresh as `not_requested`. Historical records and already queued jobs retain their original outcomes; this change does not delete them.

API updates record `source=api`, the actor and owner, vendor, stable account ID, account identity, confirmed supplier row ID, request ID, API token ID and revision, timestamps, and outcome. Web updates use `source=web`. Token revision is retained because regeneration can reuse the same token database row. No new submission is created, historical submissions remain unchanged, and neither the replacement Auth JSON nor any token plaintext is stored in this audit.

Responses include `X-Request-ID`. Once an update audit exists, responses also include `X-Auth-JSON-Audit-ID`, even if the supplier update fails or becomes unconfirmed. Successful response bodies include that same ID as `data.audit_id`. Requests rejected before execution starts do not have an update audit ID. Keep these identifiers for troubleshooting.

## Errors and retries

Errors use a safe TK Register envelope, including errors returned by the supplier update operation:

```json
{
  "success": false,
  "code": "AUTH_JSON_UPDATE_UNCONFIRMED",
  "message": "We could not confirm whether the update completed. Check the account before trying again."
}
```

| HTTP | Code | Meaning |
| --- | --- | --- |
| 400 | `INVALID_REQUEST`, `INVALID_PARAMETERS`, `INVALID_AUTH_JSON` | Invalid request or credential format, duplicate/unsupported fields, request body over 3 MiB, or Auth JSON over 2 MiB. |
| 400 | `AUTH_JSON_EMAIL_MISMATCH` | Credential email does not match the target account. |
| 401 | `INVALID_API_TOKEN` | Missing, invalid, revoked, or expired token, or inactive owner. |
| 403 | `INSUFFICIENT_SCOPE` | Token lacks `claude:auth-json:update`; ask an administrator to check its permissions. Existing tokens receive this scope automatically on upgrade. |
| 404 | `ACCOUNT_NOT_FOUND`, `AUTH_JSON_ACCOUNT_NOT_FOUND` | Owned account not found, mismatched username, or supplier record not found. |
| 405 | `METHOD_NOT_ALLOWED` | Use POST. |
| 409 | `ACCOUNT_NOT_ACCEPTED`, `AUTH_JSON_UNSUPPORTED` | No accepted account or unsupported vendor/plan. |
| 409 | `ACCOUNT_OWNERSHIP_CONFLICT`, `AUTH_JSON_ACCOUNT_AMBIGUOUS` | Owner or supplier identity needs administrator resolution. |
| 409 | `AUTH_JSON_UPDATE_RUNNING`, `SYNC_RUNNING` | Another update or collection currently holds the relevant lease. |
| 409 | `AUTH_JSON_NOT_EDITABLE`, `ACCOUNT_CHANGED` | Supplier disallows editing or account identity changed. |
| 415 | `INVALID_CONTENT_TYPE` | Use application/json. |
| 429 | `RATE_LIMITED` | Wait for the `Retry-After` header in seconds. |
| 429 | `SUPPLIER_RATE_LIMITED` | A supplier login or account-query cooldown, or another login in progress, prevented the write from starting. Wait for `Retry-After`; `retry_stage` identifies `login`, `accounts`, or `login_lock`. |
| 502 | `AUTH_JSON_UPDATE_REJECTED` | Supplier explicitly rejected the update. |
| 502 | `AUTH_JSON_LOGIN_FAILED`, `AUTH_JSON_UNAVAILABLE` | Supplier login, connection, or response processing failed; no update confirmed. |
| 503 | `SYNC_NOT_CONFIGURED`, `UNAVAILABLE` | Website login is not configured or a required local service is unavailable. |
| 504 | `AUTH_JSON_UPDATE_UNCONFIRMED` | Write result is uncertain; audit status is `unknown`. |

The write is never retried automatically. A timeout or connection loss after transmission can mean the supplier changed the credentials without returning confirmation. Inspect the account before a manual retry. Each later update that begins execution creates its own audit record; the earlier uncertain record is retained. Credential updates do not queue post-update collection, regardless of their outcome.

The update rate limit is independent of the account submission and lookup limits, but shared with web Auth JSON writes by the same user. Changing tokens does not reset it. `429` responses include both `Retry-After` and a JSON `retry_after` value in seconds.

Supplier waits are separate from this per-user limit. `SUPPLIER_RATE_LIMITED` is returned before sending the Auth JSON write, with `retry_stage` indicating which operation must wait. A login cooldown does not prevent account reads that can use a still-valid cached supplier session. A query cooldown keeps that session cached and does not trigger another login. The API does not automatically replay credential writes.
