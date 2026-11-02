import { useEffect, useId, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import { ArrowLeft, ArrowRight, BookOpen, Check, ChevronRight, Copy, Info, KeyRound, LockKeyhole } from 'lucide-react';
import {
  API_BASE_PATH, authenticationFields, curlExample, docSections, docTopics, endpoints,
  authJSONUpdateErrors, authJSONUpdateFields, authJSONUpdateResponse, authJSONUpdateResponseFields,
  lookupErrors, lookupFields, lookupResponse, lookupResponseFields, optionalSubmissionFields,
  snapshotFields, submissionErrors, submissionFields,
  type DocSection, type DocTopic, type EndpointTopic, type ErrorDefinition, type FieldDefinition,
} from './data';
import './api-docs.css';

export type { DocSection, DocTopic } from './data';

export type APIDocumentationProps = {
  topic?: DocTopic;
  section?: DocSection;
  onNavigate: (topic?: DocTopic, section?: DocSection) => void;
  onManageToken: () => void;
};

type Navigation = Pick<APIDocumentationProps, 'onNavigate'>;
type Feedback = { text: string; error: boolean };
type CopyFeedback = (feedback: Feedback) => void;

function documentationHref(topic?: DocTopic, section?: DocSection) {
  return `#/docs${topic ? `/${topic}` : ''}${topic && topic !== 'getting-started' && section && section !== 'parameters' ? `/${section}` : ''}`;
}

function navigateClick(event: MouseEvent<HTMLAnchorElement>, action: () => void) {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  action();
}

function DocLink({ topic, children, className, current, onNavigate }: Navigation & {
  topic?: DocTopic;
  children: ReactNode;
  className?: string;
  current?: boolean;
}) {
  return <a href={documentationHref(topic)} className={className}
    aria-current={current ? 'page' : undefined}
    onClick={event => navigateClick(event, () => onNavigate(topic))}>{children}</a>;
}

function TokenLink({ onManageToken }: Pick<APIDocumentationProps, 'onManageToken'>) {
  return <a className='api-docs-text-link' href='#/account'
    onClick={event => navigateClick(event, onManageToken)}>
    Manage API token <ArrowRight aria-hidden='true' />
  </a>;
}

function CopyButton({ text, label, iconOnly = false, onFeedback }: {
  text: string; label: string; iconOnly?: boolean; onFeedback: CopyFeedback;
}) {
  const [copied, setCopied] = useState(false);
  const [copying, setCopying] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);
  useEffect(() => { setCopied(false); }, [text]);
  async function copy() {
    setCopying(true);
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard unavailable');
      await navigator.clipboard.writeText(text);
      setCopied(true);
      onFeedback({ text: `${label} copied to clipboard.`, error: false });
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 2500);
    } catch {
      setCopied(false);
      onFeedback({ text: 'Could not copy automatically. Select the displayed text and copy it manually.', error: true });
    } finally {
      setCopying(false);
    }
  }
  return <button type='button' className={iconOnly ? 'api-docs-icon-button' : 'api-docs-copy-button'}
    onClick={() => void copy()} disabled={copying} aria-label={`Copy ${label.toLowerCase()}`} title={`Copy ${label.toLowerCase()}`}>
    {copied ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}
    {!iconOnly && <span>{copied ? 'Copied' : 'Copy'}</span>}
  </button>;
}

function CodeBlock({ title, text, onFeedback }: { title: string; text: string; onFeedback: CopyFeedback }) {
  return <div className='api-docs-code-frame'>
    <div className='api-docs-code-heading'><span>{title}</span><CopyButton text={text} label={title} onFeedback={onFeedback} /></div>
    <pre tabIndex={0} aria-label={title}><code>{text}</code></pre>
  </div>;
}

function BaseURL({ baseURL, onFeedback }: { baseURL: string; onFeedback: CopyFeedback }) {
  return <section className='api-docs-base-block' aria-label='API base URL'>
    <h2>Base URL</h2>
    <div className='api-docs-endpoint'><code>{baseURL}</code><CopyButton text={baseURL} label='Base URL' iconOnly onFeedback={onFeedback} /></div>
  </section>;
}

function FieldTable({ fields, label, showOptional = false }: { fields: FieldDefinition[]; label: string; showOptional?: boolean }) {
  return <table className='api-docs-table'>
    <caption className='sr-only'>{label}</caption>
    <thead><tr><th scope='col'>Field</th><th scope='col'>Description</th></tr></thead>
    <tbody>{fields.map(field => <tr key={field.name}>
      <th scope='row'><code>{field.name}</code><span className='api-docs-field-meta'>{field.type}
        {field.required ? <span className='api-docs-required'>Required</span> : showOptional ? <span>Optional</span> : null}
      </span></th>
      <td>{field.description}</td>
    </tr>)}</tbody>
  </table>;
}

function ErrorTable({ errors }: { errors: ErrorDefinition[] }) {
  return <table className='api-docs-table api-docs-error-table'>
    <caption className='sr-only'>Local HTTP errors and how to handle them</caption>
    <thead><tr><th scope='col'>Status / code</th><th scope='col'>Meaning and action</th></tr></thead>
    <tbody>{errors.map(error => <tr key={error.code}>
      <th scope='row'><span className='api-docs-http-status'>{error.status}</span><code>{error.code}</code></th>
      <td>{error.description}</td>
    </tr>)}</tbody>
  </table>;
}

function EndpointLink({ topic, onNavigate }: Navigation & { topic: EndpointTopic }) {
  const endpoint = endpoints[topic];
  return <DocLink topic={topic} onNavigate={onNavigate} className='api-docs-api-row'>
    <span className={`api-docs-method ${endpoint.method === 'GET' ? 'is-get' : ''}`}>{endpoint.method}</span>
    <span className='api-docs-api-copy'><span className='api-docs-api-title'>{endpoint.title}</span>
      <span className='api-docs-api-description'>{endpoint.description}</span>
      <code className='api-docs-api-path'>{endpoint.path}</code>
    </span>
    <ChevronRight className='api-docs-row-chevron' aria-hidden='true' />
  </DocLink>;
}

function DocumentationIndex({ baseURL, onNavigate, onManageToken, onFeedback }: Navigation & Pick<APIDocumentationProps, 'onManageToken'> & {
  baseURL: string; onFeedback: CopyFeedback;
}) {
  return <>
    <div className='api-docs-guide-strip'>
      <span className='api-docs-guide-icon'><BookOpen aria-hidden='true' /></span>
      <div className='api-docs-guide-copy'><h2>Getting started</h2><p>Learn how to authenticate requests and work with rate limits.</p></div>
      <DocLink topic='getting-started' onNavigate={onNavigate} className='api-docs-button'>Read guide <ArrowRight aria-hidden='true' /></DocLink>
    </div>
    <section className='api-docs-reference' aria-labelledby='api-docs-reference-title'>
      <div className='api-docs-section-heading'><h2 id='api-docs-reference-title'>API reference</h2><span>{Object.keys(endpoints).length} endpoints</span></div>
      <div className='api-docs-api-list'>
        <EndpointLink topic='submit-account' onNavigate={onNavigate} />
        <EndpointLink topic='account-lookup' onNavigate={onNavigate} />
        <EndpointLink topic='update-auth-json' onNavigate={onNavigate} />
      </div>
    </section>
    <BaseURL baseURL={baseURL} onFeedback={onFeedback} />
    <div className='api-docs-footer'><span><KeyRound aria-hidden='true' />Use your personal token to access these APIs.</span><TokenLink onManageToken={onManageToken} /></div>
  </>;
}

function GettingStarted({ baseURL, onNavigate, onManageToken, onFeedback }: Navigation & Pick<APIDocumentationProps, 'onManageToken'> & {
  baseURL: string; onFeedback: CopyFeedback;
}) {
  return <div className='api-docs-content api-docs-overview'>
    <section className='api-docs-content-block'>
      <div className='api-docs-section-heading'><h2 className='api-docs-detail-title'>Authentication</h2><TokenLink onManageToken={onManageToken} /></div>
      <p>Generate your personal API token in <strong>Account settings</strong>, then send it in the Authorization header of every request.</p>
      <CodeBlock title='Request headers' text={'Authorization: Bearer YOUR_TOKEN\nAccept: application/json\nContent-Type: application/json'} onFeedback={onFeedback} />
      <p>Use <code>Content-Type: application/json</code> when sending a POST body. GET account lookups have no body.</p>
      <p>A token accesses only its user’s accounts, including tokens owned by administrators. It does not grant access to another user’s accounts, administrator settings, or token management.</p>
    </section>
    <BaseURL baseURL={baseURL} onFeedback={onFeedback} />
    <section className='api-docs-content-block'>
      <h2>Access and limits</h2>
      <p>New tokens include the three scopes below. Existing tokens gain Auth JSON update access automatically without changing their value. You do not need to regenerate your token or update your scripts.</p>
      <p>Requests using different tokens for the same user share that user’s limits.</p>
      <table className='api-docs-table api-docs-limits-table'>
        <caption className='sr-only'>API scopes and per-user rate limits</caption>
        <thead><tr><th scope='col'>Endpoint</th><th scope='col'>Scope and limit</th></tr></thead>
        <tbody>
          <tr><th scope='row'><DocLink topic='submit-account' onNavigate={onNavigate}>Submit account</DocLink></th><td><code>claude:submit</code><p>10 requests per minute per user, shared with the web submission form.</p></td></tr>
          <tr><th scope='row'><DocLink topic='account-lookup' onNavigate={onNavigate}>Account lookup</DocLink></th><td><code>accounts:read</code><p>60 requests per minute per user, separate from the submission limit.</p></td></tr>
          <tr><th scope='row'><DocLink topic='update-auth-json' onNavigate={onNavigate}>Update Auth JSON</DocLink></th><td><code>claude:auth-json:update</code><p>10 requests per minute per user, shared with web Auth JSON updates. Separate from account submissions.</p></td></tr>
        </tbody>
      </table>
      <p>A local <code>429 RATE_LIMITED</code> response includes <code>Retry-After</code> in seconds. Keep <code>X-Request-ID</code> when requesting support. Error codes are listed in each endpoint’s guide.</p>
    </section>
    <section className='api-docs-content-block'>
      <h2>Choose the response contract</h2>
      <p>Submissions pass through the vendor’s status, content type, and complete response body. Lookups and Auth JSON updates return TK Register’s <code>success</code> / <code>data</code> JSON envelope.</p>
      <p>Account lookup reads saved snapshots and does not refresh supplier data. Use the returned collection timestamps when displaying account status or usage.</p>
      <p>A confirmed Auth JSON update immediately attempts to refresh that account. Check <code>updated</code> and <code>refresh_status</code> separately: saving credentials can succeed even when the following refresh fails.</p>
    </section>
    <div className='api-docs-next-links'>
      <DocLink topic='submit-account' onNavigate={onNavigate}>Submit an account <ArrowRight aria-hidden='true' /></DocLink>
      <DocLink topic='account-lookup' onNavigate={onNavigate}>Look up an account <ArrowRight aria-hidden='true' /></DocLink>
      <DocLink topic='update-auth-json' onNavigate={onNavigate}>Update Auth JSON <ArrowRight aria-hidden='true' /></DocLink>
    </div>
  </div>;
}

function Parameters({ topic, onNavigate }: Navigation & { topic: EndpointTopic }) {
  const lookup = topic === 'account-lookup';
  const update = topic === 'update-auth-json';
  return <>
    <div className='api-docs-auth-line'><LockKeyhole aria-hidden='true' /><span>Authentication</span><code>Authorization: Bearer YOUR_TOKEN</code>
      <DocLink topic='getting-started' onNavigate={onNavigate} className='api-docs-text-link'>View guide <ArrowRight aria-hidden='true' /></DocLink>
    </div>
    <div className='api-docs-content-block'><h3>{lookup ? 'Query parameters' : 'JSON body'}</h3>
      <p>{lookup ? 'Send exactly these two parameters, each once, in the URL query string. Do not send a request body or any other query parameters.' : 'Send one JSON object with Content-Type: application/json. The request body must be 3 MiB or smaller. Do not add query parameters.'}</p>
      <FieldTable fields={lookup ? lookupFields : update ? authJSONUpdateFields : submissionFields} label={lookup ? 'Required lookup query parameters' : update ? 'Required Auth JSON update fields' : 'Required submission fields'} />
      {!lookup && !update && <p>String limits use UTF-8 bytes. Submission values are not automatically trimmed; nonempty required strings cannot consist only of whitespace. Omit unused optional fields instead of sending <code>null</code>.</p>}
      {update && <p>The server finds your account by user, vendor, and exact normalized account name. <code>auth_json.email</code> must match that account after trimming and case normalization. Do not send a supplier record ID, password, plan, or any other top-level field.</p>}
    </div>
    {lookup ? <section className='api-docs-content-block'>
      <h3>Exact account matching</h3>
      <p>Both identifiers are trimmed and matched case-insensitively. Use the TK Register user’s email for <code>username</code> and the submitted Claude login for <code>account</code>.</p>
      <p>URL-encode both values, especially email addresses containing <code>+</code>. The cURL example uses <code>--data-urlencode</code> for this. Aliases, partial matches, and another user’s email are not supported.</p>
    </section> : <>
      <details className='api-docs-disclosure'><summary>Auth JSON field requirements <span>13 fields</span></summary>
        <div className='api-docs-disclosure-body'><p>Every key below must be present. All required string values except <code>id_token</code> must be nonempty. Each listed string is limited to 16,384 UTF-8 bytes; device IDs have the separate limits shown below.</p>
          <FieldTable fields={authenticationFields} label='Required fields inside auth_json' />
          <p>Additional fields inside <code>auth_json</code> are allowed and forwarded as part of the authentication object.</p>
        </div>
      </details>
      {!update && <details className='api-docs-disclosure'><summary>Optional parameters <span>7 fields</span></summary>
        <div className='api-docs-disclosure-body'><FieldTable fields={optionalSubmissionFields} label='Optional submission fields' showOptional /></div>
      </details>}
      <p className='api-docs-note'>{update ? 'Only an accepted Claude account belonging to the token owner can be updated. The supplier must currently allow editing. The server verifies the supplier record and preserves its other fields. No new submission is created.' : <>This endpoint is dedicated to Claude. Do not send top-level <code>provider</code>, <code>model</code>, <code>account_type</code>, proxy routing fields, or the vendor’s shared Bearer credential. Unsupported top-level fields are rejected.</>}</p>
    </>}
  </>;
}

function CurlExample({ topic, baseURL, onFeedback }: { topic: EndpointTopic; baseURL: string; onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>{topic === 'account-lookup' ? 'Find your submitted account' : topic === 'update-auth-json' ? 'Replace an account’s Auth JSON' : 'Submit a Claude account'}</h3>
      <p>Set your personal token in your shell, then run the request. All account values and credentials below are placeholders; replace them before use{topic === 'submit-account' ? ', including the sample dates' : ''}.</p>
      <CodeBlock title='Token environment variable' text="export TK_REGISTER_API_TOKEN='YOUR_TOKEN'" onFeedback={onFeedback} />
      <CodeBlock title='cURL request' text={curlExample(topic, baseURL)} onFeedback={onFeedback} />
      <p>{topic === 'account-lookup' ? 'username identifies your TK Register login. account identifies the Claude account you submitted. This request reads saved data only.' : topic === 'update-auth-json' ? 'This changes the account’s credentials at the supplier. username is your TK Register email; account and auth_json.email identify the Claude account. Replace every credential placeholder and sample timestamp. TK Register sends the write once and does not automatically retry it.' : 'The request sends account credentials and can create a real vendor submission. TK Register does not retry the request automatically.'}</p>
    </section>
    <details className='api-docs-disclosure'><summary>Using a self-signed certificate</summary><div className='api-docs-disclosure-body'><p>The examples use normal TLS certificate verification. For a deployment whose self-signed certificate you intentionally trust, you can add <code>--insecure</code> to cURL. Omit this option when using a trusted certificate.</p></div></details>
  </>;
}

function LookupResponse({ onFeedback }: { onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>200 · Account found</h3>
      <p>Returns the latest saved account data. IDs are strings. A successful lookup means the local account was found; it does not mean the vendor account is active.</p>
      <CodeBlock title='JSON · Example response' text={JSON.stringify(lookupResponse, null, 2)} onFeedback={onFeedback} />
    </section>
    <details className='api-docs-disclosure'><summary>Response fields <span>data</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={lookupResponseFields} label='Fields in response data' /></div></details>
    <details className='api-docs-disclosure'><summary>Snapshot fields <span>data.snapshot</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={snapshotFields} label='Fields in the saved snapshot' /></div></details>
    <section className='api-docs-content-block'><h3>Freshness and missing data</h3>
      <ul className='api-docs-bullets'>
        <li><strong>No collection yet:</strong> <code>snapshot</code> is <code>null</code>. Show unavailable values as “—”; do not infer zero.</li>
        <li><strong>A later collection fails:</strong> the current lifetime’s last successful snapshot is retained. <code>last_error</code> and <code>last_attempt_at</code> describe the latest attempt.</li>
        <li><strong>A missing metric:</strong> the field is <code>null</code>. A measured zero remains <code>0</code>, such as <code>0%</code> or <code>0.00 $</code>.</li>
        <li><strong>Collection time:</strong> use <code>last_success_at</code> and <code>snapshot.collected_at</code> to show when displayed values were collected.</li>
      </ul>
      <p>Repeating a lookup does not make the snapshot newer. A new accepted vendor identity or plan can start a new account lifetime; older snapshots are not returned as its latest data.</p>
      <p>This endpoint returns one saved snapshot, not a historical time series or billing query. Token totals and spend are cumulative, not the amount used since your previous request.</p>
      <p>The response excludes passwords, <code>auth_json</code>, personal tokens, and vendor credentials.</p>
    </section>
  </>;
}

function SubmissionResponse({ onFeedback }: { onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>Vendor response</h3>
      <p>When the vendor returns a complete response of 256 KiB or less, TK Register passes through its HTTP status, content type, and body without adding a JSON envelope. If the shared vendor credential is echoed in that body, it is redacted.</p>
      <p>There is no fixed TK Register success body for this endpoint. Read the vendor’s response to determine the result. A complete vendor error response, including a duplicate-account error, is passed through in the same way.</p>
      <CodeBlock title='Response header examples' text={'X-Request-ID: <request identifier>\nX-Submission-ID: <local submission identifier>'} onFeedback={onFeedback} />
    </section>
    <section className='api-docs-content-block'><h3>Submission history and identifiers</h3>
      <p>After validation and duplicate checks pass and execution starts, the attempt is saved in your Submission history. <code>X-Submission-ID</code> identifies that local record and is only returned after the record exists. It is different from the vendor’s account ID.</p>
      <p><code>X-Request-ID</code> identifies the request for troubleshooting. Keep both headers when available.</p>
      <p>An HTTP 2xx response alone does not prove the account was accepted. If the vendor’s complete response has no valid account ID, it is still passed through, but the local attempt is marked <code>unknown</code>. An accepted submission also does not mean the account is active.</p>
    </section>
    <section className='api-docs-content-block'><h3>Timeouts and retries</h3>
      <p>A timeout or connection failure can leave the remote outcome unconfirmed. The local attempt remains <code>unknown</code>, and you may retry manually. Each retry that passes validation and duplicate checks starts a new submission record; the earlier attempt is retained.</p>
      <p>The system does not automatically retry account creation. Accepted or currently running submissions for the same user, vendor, and account continue to block duplicates. Check Submission history before deciding whether to submit again.</p>
    </section>
  </>;
}

function AuthJSONUpdateResponse({ onNavigate, onFeedback }: Navigation & { onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>200 · Credentials updated</h3>
      <p>The supplier confirmed the replacement credentials. The server immediately attempts to collect that account’s status, usage, and spend, without waiting for the scheduled collection interval.</p>
      <CodeBlock title='JSON · Example response' text={JSON.stringify(authJSONUpdateResponse, null, 2)} onFeedback={onFeedback} />
    </section>
    <details className='api-docs-disclosure'><summary>Response fields <span>data</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={authJSONUpdateResponseFields} label='Fields in Auth JSON update response data' /></div></details>
    <section className='api-docs-content-block'><h3>Handle the two outcomes separately</h3>
      <p><code>updated: true</code> confirms the credential change. <code>refresh_status: succeeded</code> confirms that the refreshed account data was saved. Neither result guarantees the supplier account is active.</p>
      <p>When saving succeeds but refresh fails, the API still returns HTTP 200 with <code>updated: true</code> and <code>refresh_status: failed</code>. The last successful snapshot is retained. <code>account</code> can be null if local readback failed. Sync the account again to refresh its data; do not repeat the credential update just because refresh failed.</p>
      <p>The returned <code>account</code> follows the <DocLink topic='account-lookup' onNavigate={onNavigate} className='api-docs-text-link'>Account lookup</DocLink> response contract, including snapshot currency and USD conversion fields. Use <code>last_success_at</code> and <code>snapshot.collected_at</code> to distinguish fresh data from retained data.</p>
    </section>
    <section className='api-docs-content-block'><h3>Audit and request identifiers</h3>
      <CodeBlock title='Response header examples' text={'X-Request-ID: <request identifier>\nX-Auth-JSON-Audit-ID: <local update audit identifier>'} onFeedback={onFeedback} />
      <p>An audit record is created after authorization, validation, and lock acquisition, before the supplier write starts. Its ID is returned as <code>data.audit_id</code> on success and in <code>X-Auth-JSON-Audit-ID</code> whenever that record exists, including failed or unconfirmed updates.</p>
      <p>The audit records the actor, owner, vendor, account, API token reference and revision, request ID, update outcome, and refresh outcome. It does not store the replacement Auth JSON or any token plaintext. Existing submission history is unchanged.</p>
      <p>Errors rejected before an update begins have no update audit ID. Keep the request ID and audit ID, when available, for support.</p>
    </section>
  </>;
}

function Errors({ topic, onFeedback }: { topic: EndpointTopic; onFeedback: CopyFeedback }) {
  const lookup = topic === 'account-lookup';
  const update = topic === 'update-auth-json';
  const example = lookup ? { success: false, code: 'ACCOUNT_NOT_FOUND', message: 'The account could not be found.' } : update ? { success: false, code: 'AUTH_JSON_UPDATE_UNCONFIRMED', message: 'We could not confirm whether the update completed. Check the account before trying again.' } : { success: false, code: 'INVALID_PARAMETERS', message: 'Choose a supported Claude plan.' };
  return <>
    <section className='api-docs-content-block'><h3>{lookup || update ? 'Error responses' : 'Local errors and vendor errors'}</h3>
      <p>{lookup || update ? 'Use the HTTP status and code to handle failed requests. Do not depend on exact message wording.' : 'Local authentication, validation, configuration, and connection errors use the envelope below. Complete vendor error responses use the vendor’s own status and body instead, with shared credentials redacted if echoed.'}</p>
      <CodeBlock title='JSON · Local error example' text={JSON.stringify(example, null, 2)} onFeedback={onFeedback} />
      <ErrorTable errors={lookup ? lookupErrors : update ? authJSONUpdateErrors : submissionErrors} />
      {update && <p>Existing tokens receive <code>claude:auth-json:update</code> automatically. If a valid token still receives <code>403 INSUFFICIENT_SCOPE</code>, ask an administrator to check its permissions. This feature does not require token regeneration.</p>}
    </section>
    <section className='api-docs-content-block'><h3>Rate limits and retries</h3>
      <p>A local <code>429 RATE_LIMITED</code> response includes a <code>Retry-After</code> header and JSON <code>retry_after</code> value, both in seconds. Wait for this duration before sending another request.{topic === 'submit-account' && ' A vendor 429 response is passed through and does not guarantee these retry fields.'}</p>
      <p>{lookup ? 'Lookups can be retried with a delay after transient connection errors or 503 responses because they do not create submissions or query the vendor. A 200 response with last_error describes a saved collection failure; polling repeatedly will not repair it.' : update ? 'Do not automatically retry credential writes. A timeout or connection loss after sending the write can leave the audit result unknown. Check the account before a manual retry. A successful credential update followed by a failed refresh only needs another Sync.' : 'Do not automatically retry submissions. A connection failure or timeout does not prove the vendor did not receive your account. Review Submission history before a manual retry.'}</p>
      <p>Keep <code>X-Request-ID</code>{lookup ? '' : update ? ' and, when present, X-Auth-JSON-Audit-ID' : ' and, when present, X-Submission-ID'} when requesting support.</p>
    </section>
  </>;
}

function EndpointDocumentation({ topic, section, baseURL, onNavigate, onFeedback }: Navigation & {
  topic: EndpointTopic; section: DocSection; baseURL: string; onFeedback: CopyFeedback;
}) {
  const endpoint = endpoints[topic];
  const tabPrefix = useId();
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  function onTabKey(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let next: number;
    if (event.key === 'ArrowRight') next = (index + 1) % docSections.length;
    else if (event.key === 'ArrowLeft') next = (index + docSections.length - 1) % docSections.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = docSections.length - 1;
    else return;
    event.preventDefault();
    tabs.current[next]?.focus();
    onNavigate(topic, docSections[next].id);
  }
  return <>
    <header className='api-docs-detail-heading'><h2 className='api-docs-detail-title'>{endpoint.title}</h2><p>{endpoint.description}</p></header>
    <div className='api-docs-endpoint'><span className={`api-docs-method ${endpoint.method === 'GET' ? 'is-get' : ''}`}>{endpoint.method}</span>
      <code>{endpoint.path}</code><CopyButton text={baseURL + endpoint.path} label='Full endpoint URL' iconOnly onFeedback={onFeedback} />
    </div>
    <p className='api-docs-base-caption'>Base URL · <code>{baseURL}</code></p>
    <div className='api-docs-meta'><span>Scope <code>{endpoint.scope}</code></span><span>{endpoint.limit} per user{topic !== 'account-lookup' ? ' · shared with the web form' : ''}</span></div>
    <div className='api-docs-info'><Info aria-hidden='true' /><p>{endpoint.note}</p></div>
    <div className='api-docs-tabs' role='tablist' aria-label='Endpoint documentation sections'>
      {docSections.map((tab, index) => <button key={tab.id} type='button' role='tab'
        id={`${tabPrefix}-tab-${tab.id}`} aria-controls={`${tabPrefix}-panel-${tab.id}`}
        aria-selected={section === tab.id} tabIndex={section === tab.id ? 0 : -1}
        ref={element => { tabs.current[index] = element; }}
        onClick={() => onNavigate(topic, tab.id)} onKeyDown={event => onTabKey(event, index)}>{tab.label}</button>)}
    </div>
    {docSections.map(tab => <div key={tab.id} className='api-docs-content' role='tabpanel'
      id={`${tabPrefix}-panel-${tab.id}`} aria-labelledby={`${tabPrefix}-tab-${tab.id}`} hidden={section !== tab.id} tabIndex={0}>
      {section === tab.id && (tab.id === 'parameters' ? <Parameters topic={topic} onNavigate={onNavigate} />
        : tab.id === 'example' ? <CurlExample topic={topic} baseURL={baseURL} onFeedback={onFeedback} />
        : tab.id === 'response' ? topic === 'account-lookup' ? <LookupResponse onFeedback={onFeedback} /> : topic === 'update-auth-json' ? <AuthJSONUpdateResponse onNavigate={onNavigate} onFeedback={onFeedback} /> : <SubmissionResponse onFeedback={onFeedback} />
        : <Errors topic={topic} onFeedback={onFeedback} />)}
    </div>)}
  </>;
}

export default function APIDocumentation({ topic, section = 'parameters', onNavigate, onManageToken }: APIDocumentationProps) {
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const baseURL = `${window.location.origin}${API_BASE_PATH}`;
  useEffect(() => { setFeedback(null); }, [topic, section]);
  useEffect(() => {
    if (!feedback) return;
    const timeout = setTimeout(() => setFeedback(null), 6000);
    return () => clearTimeout(timeout);
  }, [feedback]);
  return <div className='api-docs'>
    {topic && <DocLink onNavigate={onNavigate} className='api-docs-text-link api-docs-back'><ArrowLeft aria-hidden='true' />API documentation</DocLink>}
    <header className='api-docs-page-heading'><div><h1>API documentation</h1><p>{topic ? 'Connect your application to TK Register.' : 'Explore every API, with parameters and ready-to-use examples.'}</p></div><span className='api-docs-version'>{topic ? 'v1' : 'REST API · v1'}</span></header>
    {!topic ? <DocumentationIndex baseURL={baseURL} onNavigate={onNavigate} onManageToken={onManageToken} onFeedback={setFeedback} /> : <>
      <nav className='api-docs-topic-nav' aria-label='Documentation topics'>{docTopics.map(item => <DocLink key={item.id} topic={item.id} current={topic === item.id} onNavigate={onNavigate}>{item.label}</DocLink>)}</nav>
      {topic === 'getting-started' ? <GettingStarted baseURL={baseURL} onNavigate={onNavigate} onManageToken={onManageToken} onFeedback={setFeedback} /> : <EndpointDocumentation key={topic} topic={topic} section={section} baseURL={baseURL} onNavigate={onNavigate} onFeedback={setFeedback} />}
    </>}
    <div className={`api-docs-feedback${feedback?.error ? ' is-error' : ''}`} role={feedback?.error ? 'alert' : 'status'} aria-live={feedback?.error ? 'assertive' : 'polite'} aria-atomic='true'>{feedback?.text || ''}</div>
  </div>;
}
