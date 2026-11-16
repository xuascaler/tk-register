# Account deletion

The backend deletion lifecycle and archive are retained, but account deletion is no longer available in the frontend. User and administrator Accounts lists show only **Sync**. The deletion dialog, progress links, deletion POST requests, progress GET requests and their polling have been removed from the browser code.

## Frontend behavior

The normal account-list response can still report an existing deletion operation. The frontend uses `deletion_status` and `deleted_at` only to disable Sync and Auth JSON updates while the account is unavailable for mutation. It makes no dedicated deletion-progress requests. Existing backend jobs and historical archives are unchanged.

An account with an unfinished or uncertain submission cannot be deleted as a failed local record. A record with only definitively failed submissions can be removed locally without changing a supplier account that may belong to another user.

## Supplier behavior

For an accepted account, the worker reads the supplier's current account configuration and checks the exact account identifier and saved supplier record ID. It never substitutes a different supplier record solely because its email matches.

- If the account exists, replace its supplier-stored password with the fixed deletion password through the supplier’s encrypted password field. Preserve both plan dates, Auth JSON and all other account metadata. A recognized successful supplier update acknowledgement is required before local removal; an HTTP 200 response alone is insufficient. A rejected password change fails deletion, retains the account, and does not create a `deleted_account` archive. An ambiguous response also retains the account pending verification. Existing expired dates do not skip this password update. Supplier locks still apply; Auth JSON editing eligibility does not control a password-only update.
- This updates the password stored on the supplier platform. It does not guarantee that the original provider password changes, that an existing Auth JSON session is revoked, or that supplier status immediately becomes offline.
- If a successful, complete lookup confirms the account is absent, skip the supplier update and complete local deletion.
- A timeout, malformed response, permission error, ambiguous match or incomplete lookup is not evidence that the account is absent.

The supplier request uses the existing configured website credentials, cached login, account mutation lease, shared synchronization lease and supplier cooldowns. No credentials or upstream response bodies are included in the deletion progress response or archive.

## Durable operation and retry rules

`account_deletion_operations` records each explicit deletion attempt. A client-generated `request_id` is immutable: repeating it returns the original operation rather than sending another write. Retrying after a confirmed failure requires a new request ID and preserves the failed attempt.

| State | Meaning |
| --- | --- |
| `pending` | The operation is saved; no supplier write has started. |
| `awaiting_verification` | Persisted immediately before the write, including its operation mode. An uncertain password update retains the account pending administrator verification. |
| `upstream_confirmed` | The supplier condition is confirmed; only the local archive transaction remains. |
| `completed` | Archive and local removal committed together. |
| `failed` | A definite failure retained the account and released its deletion state. |

After `awaiting_verification`, a restart or uncertain result permits **read-only verification**, not automatic replay of the PUT. The supplier does not return plaintext passwords. Masked passwords, dates and status cannot confirm a password change after a lost acknowledgement; the account remains pending for administrator verification. A later complete lookup confirming absence can still complete local removal. A definite vendor refusal, or a transport failure proven to occur before sending the request, can finish as `failed`; the server still does not automatically repeat the write.

Busy leases delay the operation by approximately five seconds. Supplier login or lookup rate limits use the returned retry deadline. Uncertain supplier failures and transient local persistence failures defer the saved operation rather than losing it. Once `upstream_confirmed`, a local database failure retries only the local transaction.

New operations store `upstream_password_updated` in the existing `mode` field. Pre-upgrade pending operations adopt this strategy before writing. Pre-upgrade operations already awaiting a date-update result only verify their original date write without sending a new password or date update. Already completed deletions are not changed retroactively. No database columns or tables are added.

## Archive and retained history

Successful local completion writes `deleted_account` with a unique `(account_id, identity_key)` constraint. An existing archive for that account lifetime is not duplicated. The archive contains the owner, vendor, account identifier, plan, supplier record ID, submission reference, safe account snapshot, original plan dates, deleting actor, mode and timestamp. It does not copy account passwords or Auth JSON.

The same transaction marks the local account deleted, releases its active submission key, cancels pending or running post-operation Sync jobs for that lifetime, and completes the operation. Normal account lists, lookup APIs and mutation endpoints exclude the deleted account. Stale synchronization results cannot overwrite the deleted lifetime.

Submission records, saved usage snapshots and Auth JSON audit records remain stored. Submission history remains available; deleted accounts are removed from ordinary account detail and snapshot routes. Existing submissions cannot recreate the account: the deletion stores the maximum submission ID for the owner/vendor/account group. A later, newly accepted submission can establish a fresh lifetime with no inherited status or usage snapshot.

## Account status mapping

Supplier status `sync_failed` is retained as `sync_failed` in storage and API account data and displayed as **Upstream sync failed**. It is distinct from an unknown status and from a failed submission. The status is available in the account status filter.

## Session endpoints

Paths below are relative to `/tk-register/api/`:

| Scope | Path |
| --- | --- |
| Owner | `accounts/{account_id}/delete` |
| Administrator | `admin/synced-accounts/{account_id}/delete` |

`POST` requires the active browser session, `X-CSRF-Token`, and a JSON object containing only `request_id` (16–64 ASCII letters, digits, hyphens or underscores). It returns HTTP 202 for a pending operation or HTTP 200 when replaying a terminal result. `GET` accepts exactly one of `operation_id` or `request_id` and returns the scoped progress result. Callers cannot supply an upstream URL, supplier account ID or owner ID.

Progress includes `operation_id`, `status`, a safe `message`, and `retry_after_seconds` while pending. The operation must match the account in the route; ordinary users cannot read or delete another owner's account. Administrator access is checked from the current session on every request.
