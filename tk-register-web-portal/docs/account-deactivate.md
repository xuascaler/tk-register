# Account-scoped Auth JSON generation (Deactivate)

The Authentication card offers Deactivate next to Update Auth JSON. Deactivate
reuses OAuth, Setup Token, credential JSON, Cookie and Existing token inputs to
produce an Auth JSON for the same existing account. The helper performs its
normal profile enrichment. Missing fields are only requested when that real
result remains incomplete; the UI does not fabricate a missing organization.

This operation does not call the supplier submission or update endpoints. It
does not update `synced_accounts.auth_json_updated_at`, saved account status,
submission history, or scheduled collection jobs. Old-token invalidation, if
any, remains behavior of the authorization provider; a generated result is
recorded as generated, never as a confirmed provider-side deactivation.

## Browser API

Cookie authentication and the existing CSRF check apply. The personal prefix is
`/tk-register/api/accounts/{local-account-id}/deactivate`; the administrator
prefix is `/tk-register/api/admin/synced-accounts/{local-account-id}/deactivate`.
The target ID is the existing TK account row ID, not the supplier website ID.

| Method | Suffix | Request / response |
| --- | --- | --- |
| GET | (empty) | Eligibility: `available`, `account`, `plan` |
| POST | `/flows/proxy-test` | Existing `{proxy_url}` or `{flow_id}` test |
| POST | `/flows/start` | Existing OAuth/Setup `proxy_url`, `mode`, `setup_kind` |
| POST | `/flows/import` | Existing JSON/Cookie/Token credential payload |
| POST | `/flows/exchange` | `{flow_id, code}` |
| GET | `/flows/{flow-id}` | Current encrypted draft or generated result |
| POST | `/flows/{flow-id}/draft` | `{auth_patch}` for missing fields only |
| POST | `/flows/{flow-id}/export` | `{}`; complete, verified generated result |

All responses use the normal success envelope. Flow views include `flow_id`,
`auth_url` when relevant, `auth`, `notes`, `proxy_check`, `expires_at`, and
`status`. Completed exports have `status: "exported"`, `generated_at` (UTC
RFC 3339), and `audit_id` (string). Generated credentials remain in the encrypted
Redis draft only until the original, nonrenewing 30-minute flow expiry. Responses
have the API's existing `Cache-Control: no-store` policy.

Owners can use personal routes for their own accepted, available Claude account;
parents' descendant visibility does not grant credential access. Administrators
use the explicit administrator routes. Active accounts are allowed. Deletion,
ownership conflicts, account lifetime changes, disabled vendors and actor
revocation invalidate access. There is no supplier editability or status check.

## Purpose isolation and retry behavior

Generation drafts use their own `account-deactivate-flow` Redis namespace,
encryption authenticated purpose, and leases. They bind actor/auth-version,
account, owner, vendor, normalized email, identity/lifetime, and administrator
scope. Generic submission/export and Auth JSON update routes cannot read or
consume them; older deployments cannot load their ciphertext as generic drafts.

The helper may cache the same input with the same flow ID. A separately initiated
generic flow may retain its own independent draft; it never replaces the
account-bound generation draft. Within the generation purpose, a cache hit only
restores a ready draft with the identical target and scope. A completed export
cannot be reset by exchange, draft patch, or reimport. Repeated export/download
uses the same credentials and audit; no new exchange or token request is made.
Each export rechecks proxy verification and local authorization. A browser can
read an exported flow to recover its completed result while it is still valid.

After slow helper verification the server checks the earlier of helper/draft
expiry, role/auth-version, target identity and ownership again. Under database
locks it inserts one metadata audit per actor/flow, then saves the terminal Redis
draft before returning success. A lost CAS or response can be retried without
duplicating the audit or exchanging credentials again.

## Audit and migration

Startup adds `account_auth_json_generation_audits`. This additive table stores
actor, owner, account and vendor IDs, account lifetime identity, flow ID, method,
purpose, administrator scope, status, request ID, and generation timestamp. It
contains no token, Auth JSON, password, proxy URL, or credential envelope. The
unique `(actor_id, credential_flow_id)` index makes successful metadata
recording idempotent. Existing account data and audit tables are not rewritten.

## Verification

`server/account_deactivate_test.go` exercises every credential method,
eligibility without upstream editability, owner/parent/admin scopes, independent
Redis encryption purposes, target binding, protected field completion, email
mismatch, helper-time role/owner/lifetime/expiry/proxy changes, terminal replay,
and recovery from a lost Redis CAS. It asserts no supplier write/collection,
submission, update audit, account timestamp/status mutation, or sync job. The
generation audit is checked for credential leakage.
