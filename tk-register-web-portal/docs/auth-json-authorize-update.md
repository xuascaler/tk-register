# Authorize and update an existing account

Open **Accounts → account detail → Authentication → Update Auth JSON**. The dialog keeps the target email, platform Account ID and latest collected status visible. Owners can update their own accounts; administrators use the corresponding Account pool detail. Viewing a descendant's account alone does not grant credential-update permission.

**Authorize & update** reuses the existing credential helper's OAuth, Setup Token, Import JSON, session-cookie and token methods. It does not ask for new-account settings such as plan dates or a generated submission password. **Upload Auth JSON** retains direct replacement using a complete document.

## Browser flow

1. Enter the intended account proxy and click **Test connection**. Generate remains clickable before testing: clicking it shows an inline requirement and focuses the test control, without automatically testing or generating a link. A running test must finish successfully before generation can start.
2. Generate an authorization link, copy it, and authorize the locked account in a browser using that proxy. The link has a bounded lifetime. Paste the resulting authorization code or callback URL. Other supported methods import their credentials through the same helper.
3. **Authorize & update** exchanges or imports the credentials and updates the existing account. The authorized email must match the target after trimming and case normalization. A mismatch is blocked; users cannot adopt a different email and continue.
4. A confirmed update shows **Auth JSON updated**. It creates no submission, changes no owner or plan metadata, and does not start or queue status collection. The last collected status remains visible until the ordinary background collector saves a new observation.

Changing the proxy invalidates its successful test, the prior authorization draft and any pasted code. Expired authorization must be restarted. Missing credential fields can be completed through the helper's existing validation path; existing authorized values and server-controlled proxy verification cannot be overwritten.

## Account-scoped endpoints

All routes below are under `/tk-register/api`. They use the existing cookie session and CSRF protection. For administrators, replace `/accounts/:id` with `/admin/synced-accounts/:id`.

| Route | Purpose |
| --- | --- |
| `GET /accounts/:id/auth-json` | Check current supplier editability and account identity. |
| `POST /accounts/:id/auth-json/flows/proxy-test` | Test a supplied proxy or recheck a flow's existing proxy. |
| `POST /accounts/:id/auth-json/flows/start` | Start OAuth or Setup Token authorization for this account. |
| `POST /accounts/:id/auth-json/flows/import` | Import supported credential JSON, session cookies or tokens. |
| `POST /accounts/:id/auth-json/flows/exchange` | Exchange the supplied code/callback on the bound flow. |
| `GET /accounts/:id/auth-json/flows/:flow_id` | Read the actor's unexpired account-bound draft. |
| `POST /accounts/:id/auth-json/flows/:flow_id/draft` | Complete allowed missing credential fields. |
| `POST /accounts/:id/auth-json` | Update using exactly one of `flow_id` or complete `auth_json`. |

Flow operation payloads follow the existing credential-flow helper contract. The final update uses the existing supplier account-edit PUT and its confirmed supplier record ID; it never invokes account creation. The public personal-token update API continues accepting complete `auth_json` rather than browser flow IDs.

## Identity and execution checks

The backend binds a draft to the actor, target local account ID, target owner and account lifetime. It checks authorization and identity again before writing, including deletion, ownership conflicts and current supplier editability. An update-bound flow cannot be used on a different account or passed to the generic create-submission/export operations.

Credentials stay in the existing encrypted, expiring flow storage and are not added to update audits. Update execution retains the existing account locks, bounded supplier write, metadata preservation and safe errors. A timeout or lost response can leave the write result unconfirmed; no automatic retry may repeat the credential write. Keep the draft and review the account before deciding what to do next.

New successful raw-document and guided updates return `refresh_status: not_requested`. Ordinary scheduled account collection and account-creation follow-up jobs keep their existing behavior. Historical update jobs are not deleted by this change.

## Validation

Run `npm test`, `npm run typecheck`, build the production frontend, then run `npm run test:auth-json-authorize`. The browser regression uses the built assets with every API call mocked; it covers personal and administrator routes on desktop and phone widths. It checks the proxy-test prerequisite, method conversion, email mismatch, manual upload, uncertain-result handling and the absence of submission or synchronization calls. It does not update a real supplier account. The server suite covers target binding, permissions, terminal draft replay and zero queued collections after confirmed credential replacement.
