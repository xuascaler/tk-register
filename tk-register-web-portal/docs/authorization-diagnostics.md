# Authorization error details and operational logs

Account submission, Authorize & update, and Deactivate share the credential
helper. This includes the Cookie input containing a Claude `sessionKey`.
Credential generation failures occur before the supplier submission/update
request; they are separate from supplier request history and account status.

## Failure stages

| Stage | Operation |
| --- | --- |
| `request_validation` | Validate the authorization input |
| `proxy_check` | Connect through the account proxy and verify its exit IP |
| `cookie_organizations` | Read the organizations available to the sessionKey |
| `cookie_authorize` | Request an authorization callback using the sessionKey |
| `oauth_token` | Exchange an authorization code or refresh token |
| `profile` | Read account and organization identity using the access token |
| `auth_json_conversion` | Convert the available credentials to standard JSON |
| `helper_transport` | Contact the internal credential helper |

Only stages actually reached by the selected method apply. JSON import does not
request a Cookie grant. OAuth and Setup Token use their existing code exchange.
The diagnostic change does not automatically retry any grant or supplier write.

## Browser error response

The ordinary error envelope retains `success`, `code`, and `message`.
Authorization errors can additionally contain `data.diagnostics`:

```json
{
  "success": false,
  "code": "AUTHORIZATION_REJECTED",
  "message": "The authorization provider rejected the request. Please try again or authorize this account again.",
  "data": {
    "diagnostics": {
      "request_id": "example-request-id",
      "stage": "cookie_organizations",
      "upstream_status": 403,
      "response_kind": "html"
    }
  }
}
```

This example illustrates the envelope; the mapped public error code and message
depend on the failure. The browser's Error details section displays the step,
available upstream status and error type, and a selectable/copyable request ID.
Unknown diagnostic fields and values are ignored.

`upstream_status` is the HTTP response from the actual upstream request, not the
HTTP status returned by TK or the internal helper. A TK 502 may wrap a Claude
403. DNS, connection, and timeout failures can have no upstream HTTP status.
There is no guessed status when a response was not received.

403 means that the request was refused. It does not prove that a sessionKey is
expired, nor does an HTML response prove a specific blocking mechanism. Public
messages describe the observed failure rather than infer a cause.

## Log correlation and privacy

TK generates the request ID and returns it as `X-Request-ID`. The same identifier
is forwarded to the internal helper so the Go and helper events can be matched.
The entry (`submit`, `update`, or `deactivate`), credential method and operation
are bounded metadata. The logs distinguish helper HTTP status from upstream
HTTP status and include a duration.

Go events use the `TK_CREDENTIAL_AUTH` prefix and store the request identifier as
`trace_id`. Helper events use `TK_CREDENTIAL_HELPER` and `request_id`. Both fields
contain the same server-generated identifier. In a Docker deployment, filter
these event prefixes and then the exact request ID shown by the page:

```sh
docker logs tk-register 2>&1 | rg 'TK_CREDENTIAL_(AUTH|HELPER)' | rg --fixed-strings -- '<request-id>'
```

Only fixed stage/error enums, validated identifiers, numeric status/duration,
and the bounded response kind (`json`, `html`, or `other`) are recorded. An
upstream error type is retained only if it matches the explicit allowlist.
Request/response bodies, raw upstream messages, stack traces, full URLs,
sessionKeys, access/refresh tokens, authorization codes, PKCE verifiers, proxy
credentials, and Auth JSON are never part of diagnostic events.

A profile rejection after successful token acquisition retains the existing
warning/field-completion behavior. It produces a diagnostic warning without
reclassifying the result as a failed grant or redeeming the code again.

These logs describe future requests. Missing diagnostic data from older
requests cannot be reconstructed. For a new failure, use the request ID shown
on the page to locate the matching structured authorization event.

Events use the application's normal process logging. They do not create a
Submission or store credentials in a new database audit. Keeping events across
container replacement requires the deployment's usual log retention or log
collector; this change does not add a logging service.

## Verification

Tests use synthetic sessionKeys and mocked upstream responses. They cover
Cookie organization lookup, authorization and token failures, proxy
authentication, non-JSON responses, request correlation, field allowlists,
and credential redaction. Browser checks cover all three entry points. No real
user Cookie, authorization code or supplier write is needed for validation.
