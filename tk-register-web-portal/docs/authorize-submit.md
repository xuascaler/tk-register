# Authorize and submit a Claude account

The **Submit account** page has two tabs. **Upload Auth JSON** retains the manual
submission form. **Authorize & submit** obtains or imports credentials, prepares
standard Claude Auth JSON, and uses the existing submission pipeline. It requires
a signed-in TK Register browser session and CSRF protection. Personal API tokens
continue using the existing submission API; they do not grant access to these
browser authorization drafts.

## Workflow

1. Choose **Auto submit** or **Generate JSON only**. Enter an account proxy. An
   expected account email is optional. Expand **Account settings** to override
   the submission defaults.
2. Select OAuth, Setup Token, or Import JSON. Cookie/sessionKey and existing-token
   methods are available under Other methods. JSON import accepts supported
   credential exports and allows selecting an account from a multi-account file.
3. For OAuth or Setup Token, generate an authorization link. The server tests
   the proxy first. Open the link in a browser using the same proxy and paste the
   authorization code or complete callback URL into TK Register.
4. Choose **Authorize & submit** (or **Import & submit**). The server exchanges
   credentials, reads available identity data, builds Auth JSON, and submits the
   account automatically. Missing required fields pause the operation for
   completion. If the expected and authorized emails differ, confirm the actual
   authorized account before continuing.
5. A successful submission shows its account and submission identifiers, with
   actions to view history or process another account. Existing post-submission
   status/usage synchronization remains unchanged. A supplier failure retains
   the draft for a manual retry; it never automatically repeats account creation.

**Generate JSON only** performs the credential processing and proxy checks but
never calls the supplier account-creation API. Copy/download is available after
processing, including a template with missing fields clearly identified in the
notes. Only submission requires all required credential fields. The generated
JSON contains secrets and should be handled accordingly.

## Defaults

Dates use Asia/Shanghai. Registration date is the same day of the previous
calendar month, clamped to that month's last day. Plan start and recharge dates
are today; plan end is today plus 30 days.

| Setting | Default |
| --- | --- |
| Plan | `claude/cc-max-20x` |
| Registration location | `US` |
| Payment method | `physical_credit_card` |
| Registration device | `Windows` |
| Remaining balance | `100` percent |
| Tag | `速刷` |
| Warranty period and remark | Blank |
| Submission password | Generate 24 random characters when blank |

Generated passwords include upper/lowercase letters, digits, and a special
character. The password is retained through an in-page submission retry. This
value is a supplier submission parameter; it does not change the Claude login
password. Processing the next account clears its proxy, credentials, email,
password and remark while retaining the selected business settings and dates.

## Credential data and failure handling

- The authorization exchange uses PKCE. Its verifier and flow ownership remain
  server-side. Codes and credentials are never written to application logs.
- Drafts expire after 30 minutes. Go stores encrypted drafts in the existing
  namespaced Redis database, scoped to the authenticated user and authentication
  version. Browser storage contains only resume identifiers and non-secret UI
  preferences; credentials, proxy passwords and submission passwords remain in
  memory rather than local/session storage.
- Missing fields can be completed. Existing authorized identity/token fields and
  the server-bound proxy cannot be overwritten through the completion endpoint.
- The selected proxy is tested before authorization and verified again before
  export/submission. A changed exit IP stops the operation. There is no direct
  network fallback. Browser-side Claude authorization must use the same proxy.
- OAuth results are retained if a later step fails, so a consumed authorization
  code is not exchanged twice. A response lost during account submission can be
  recovered using the same submission request key. An explicit Failed or Unknown
  result can be actively retried as a new submission under the existing rules.
- Source exports sometimes lack identity fields or expiry information. Notes
  identify assumptions and generated template values. The `verification_status`
  export-format marker is not proof of successful supplier validation. Missing
  required fields still prevent submission.
- The OAuth client identifier and provider endpoints follow the reference
  `claude-auth-exporter` tool. Availability depends on the provider's authorization
  service and account permissions.

## Runtime

The Go service, Node.js credential helper, and Nginx run in one container. Only
HTTPS `8443` is published. The helper binds `127.0.0.1:3003`; Nginx does not expose
it. Go calls it with an internal Bearer secret and never uses environment HTTP
proxies for this container-local connection. `start.sh` generates the secret at
startup if `TK_REGISTER_CREDENTIAL_HELPER_TOKEN` is absent and exports it to both
processes. Operators can provide a value of at least 32 characters instead.

Provider requests are restricted to the known HTTPS authorization/profile and
proxy-check endpoints. Proxy endpoints must resolve to public IP addresses;
loopback, private, link-local, metadata and other reserved destinations are
rejected. Connections pin the resolved proxy address and verify TLS certificates.

No new SQL table or business-data migration is required. Preserve the deployment's
existing encryption key, SQL/Redis settings, SMTP configuration and TLS material.
A configured vendor credential remains necessary for **Auto submit**.

## Validation

```sh
npm --prefix tk-register-web-portal ci
npm --prefix tk-register-web-portal test
npm --prefix tk-register-web-portal run build
npm --prefix tk-register-web-portal/credential-helper ci
npm --prefix tk-register-web-portal/credential-helper test
(cd tk-register-web-portal/server && go test ./... && go vet ./...)
```

Automated tests use simulated provider/helper/supplier responses, not real Claude
credentials. A real OAuth grant must be completed by the account holder in their
browser; it is not part of the default test suite.
