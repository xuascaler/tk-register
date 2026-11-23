import { t, useLanguage } from '../i18n';
import { useEffect, useId, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import { ArrowLeft, ArrowRight, BookOpen, Check, ChevronRight, Copy, Info, KeyRound, LockKeyhole } from 'lucide-react';
import {
  API_BASE_PATH, authenticationFields, curlExample, docSections, docTopics, endpoints,
  authJSONUpdateErrors, authJSONUpdateFields, authJSONUpdateResponse, authJSONUpdateResponseFields,
  dailyUsageErrors, dailyUsageFields, dailyUsageResponse, dailyUsageResponseFields,
  dailyUsageSummaryFields, dailyUsageAccountFields, dailyUsageModelFields,
  lookupErrors, lookupFields, lookupResponse, lookupResponseFields, optionalSubmissionFields,
  snapshotFields, submissionErrors, submissionFields,
  type DocSection, type DocTopic, type EndpointTopic, type ErrorDefinition, type FieldDefinition,
} from './data';
import './api-docs.css';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';

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
    {t("Manage API token")} <ArrowRight aria-hidden='true' />
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
      onFeedback({ text: t('{{label}} copied to clipboard.', { label }), error: false });
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
    onClick={() => void copy()} disabled={copying} aria-label={t('Copy {{label}}', { label: label.toLowerCase() })} title={t('Copy {{label}}', { label: label.toLowerCase() })}>
    {copied ? <Check aria-hidden='true' /> : <Copy aria-hidden='true' />}
    {!iconOnly && <span>{copied ? t("Copied") : t("Copy")}</span>}
  </button>;
}

function CodeBlock({ title, text, onFeedback }: { title: string; text: string; onFeedback: CopyFeedback }) {
  return <div className='api-docs-code-frame'>
    <div className='api-docs-code-heading'><span>{title}</span><CopyButton text={text} label={title} onFeedback={onFeedback} /></div>
    <pre tabIndex={0} aria-label={title}><code>{text}</code></pre>
  </div>;
}

function BaseURL({ baseURL, onFeedback }: { baseURL: string; onFeedback: CopyFeedback }) {
  return <section className='api-docs-base-block' aria-label={t("API base URL")}>
    <h2>{t("Base URL")}</h2>
    <div className='api-docs-endpoint'><code>{baseURL}</code><CopyButton text={baseURL} label={t("Base URL")} iconOnly onFeedback={onFeedback} /></div>
  </section>;
}

function FieldTable({ fields, label, showOptional = false }: { fields: FieldDefinition[]; label: string; showOptional?: boolean }) {
  return <table className='api-docs-table'>
    <caption className='sr-only'>{label}</caption>
    <thead><tr><th scope='col'>{t("Field")}</th><th scope='col'>{t("Description")}</th></tr></thead>
    <tbody>{fields.map(field => <tr key={field.name}>
      <th scope='row'><code>{field.name}</code><span className='api-docs-field-meta'>{field.type}
        {field.required ? <span className='api-docs-required'>{t("Required")}</span> : showOptional ? <span>{t("Optional")}</span> : null}
      </span></th>
      <td>{t(field.description)}</td>
    </tr>)}</tbody>
  </table>;
}

function ErrorTable({ errors }: { errors: ErrorDefinition[] }) {
  return <table className='api-docs-table api-docs-error-table'>
    <caption className='sr-only'>{t("Local HTTP errors and how to handle them")}</caption>
    <thead><tr><th scope='col'>{t("Status / code")}</th><th scope='col'>{t("Meaning and action")}</th></tr></thead>
    <tbody>{errors.map(error => <tr key={error.code}>
      <th scope='row'><span className='api-docs-http-status'>{error.status}</span><code>{error.code}</code></th>
      <td>{t(error.description)}</td>
    </tr>)}</tbody>
  </table>;
}

function EndpointLink({ topic, onNavigate }: Navigation & { topic: EndpointTopic }) {
  const endpoint = endpoints[topic];
  return <DocLink topic={topic} onNavigate={onNavigate} className='api-docs-api-row'>
    <span className={`api-docs-method ${endpoint.method === 'GET' ? 'is-get' : ''}`}>{endpoint.method}</span>
    <span className='api-docs-api-copy'><span className='api-docs-api-title'>{t(endpoint.title)}</span>
      <span className='api-docs-api-description'>{t(endpoint.description)}</span>
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
      <div className='api-docs-guide-copy'><h2>{t("Getting started")}</h2><p>{t("Learn how to authenticate requests and work with rate limits.")}</p></div>
      <DocLink topic='getting-started' onNavigate={onNavigate} className='api-docs-button'>{t("Read guide")} <ArrowRight aria-hidden='true' /></DocLink>
    </div>
    <section className='api-docs-reference' aria-labelledby='api-docs-reference-title'>
      <div className='api-docs-section-heading'><h2 id='api-docs-reference-title'>{t("API reference")}</h2><span>{Object.keys(endpoints).length} {t("endpoints")}</span></div>
      <div className='api-docs-api-list'>
        <EndpointLink topic='submit-account' onNavigate={onNavigate} />
        <EndpointLink topic='account-lookup' onNavigate={onNavigate} />
        <EndpointLink topic='daily-usage' onNavigate={onNavigate} />
        <EndpointLink topic='update-auth-json' onNavigate={onNavigate} />
      </div>
    </section>
    <BaseURL baseURL={baseURL} onFeedback={onFeedback} />
    <div className='api-docs-footer'><span><KeyRound aria-hidden='true' />{t("Use your personal token to access these APIs.")}</span><TokenLink onManageToken={onManageToken} /></div>
  </>;
}

function GettingStarted({ baseURL, onNavigate, onManageToken, onFeedback }: Navigation & Pick<APIDocumentationProps, 'onManageToken'> & {
  baseURL: string; onFeedback: CopyFeedback;
}) {
  return <div className='api-docs-content api-docs-overview'>
    <section className='api-docs-content-block'>
      <div className='api-docs-section-heading'><h2 className='api-docs-detail-title'>{t("Authentication")}</h2><TokenLink onManageToken={onManageToken} /></div>
      <p>{t("Generate your personal API token in")} <strong>{t("Account settings")}</strong>{t(", then send it in the Authorization header of every request.")}</p>
      <CodeBlock title={t("Request headers")} text={'Authorization: Bearer YOUR_TOKEN\nAccept: application/json\nContent-Type: application/json'} onFeedback={onFeedback} />
      <p>{t("Use")} <code>Content-Type: application/json</code> {t("when sending a POST body. GET account lookups have no body.")}</p>
      <p>{t("A token accesses only its user’s accounts, including tokens owned by administrators. It does not grant access to another user’s accounts, administrator settings, or token management.")}</p>
    </section>
    <BaseURL baseURL={baseURL} onFeedback={onFeedback} />
    <section className='api-docs-content-block'>
      <h2>{t("Access and limits")}</h2>
      <p>{t("New tokens include the three scopes below. Existing tokens gain Auth JSON update access automatically without changing their value. You do not need to regenerate your token or update your scripts.")}</p>
      <p>{t("The existing")} <code>accounts:read</code> {t("scope covers both Account lookup and Daily usage. A valid token with this scope already has access to the new endpoint; no new token is needed.")}</p>
      <p>{t("Requests using different tokens for the same user share that user’s limits.")}</p>
      <table className='api-docs-table api-docs-limits-table'>
        <caption className='sr-only'>{t("API scopes and per-user rate limits")}</caption>
        <thead><tr><th scope='col'>{t("Endpoint")}</th><th scope='col'>{t("Scope and limit")}</th></tr></thead>
        <tbody>
          <tr><th scope='row'><DocLink topic='submit-account' onNavigate={onNavigate}>{t("Submit account")}</DocLink></th><td><code>claude:submit</code><p>{t("100 requests per minute per user, shared with the web submission form.")}</p></td></tr>
          <tr><th scope='row'><DocLink topic='account-lookup' onNavigate={onNavigate}>{t("Account lookup")}</DocLink></th><td><code>accounts:read</code><p>{t("600 requests per minute per user, separate from the submission limit.")}</p></td></tr>
          <tr><th scope='row'><DocLink topic='daily-usage' onNavigate={onNavigate}>{t("Daily usage")}</DocLink></th><td><code>accounts:read</code><p>{t("60 requests per minute per user, separate from account lookups and submissions. Up to 100 account identifiers per request.")}</p></td></tr>
          <tr><th scope='row'><DocLink topic='update-auth-json' onNavigate={onNavigate}>{t("Update Auth JSON")}</DocLink></th><td><code>claude:auth-json:update</code><p>{t("100 requests per minute per user, shared with web Auth JSON updates. Separate from account submissions.")}</p></td></tr>
        </tbody>
      </table>
      <p>{t("A local")} <code>429 RATE_LIMITED</code> {t("response includes")} <code>Retry-After</code> {t("in seconds. Keep")} <code>X-Request-ID</code> {t("when requesting support. Error codes are listed in each endpoint’s guide.")}</p>
    </section>
    <section className='api-docs-content-block'>
      <h2>{t("Choose the response contract")}</h2>
      <p>{t("Submissions return the vendor’s status, content type, and response body, with the vendor name masked in")} <code>alias</code> {t("and any echoed shared credential redacted. Lookups, Daily usage, and Auth JSON updates return TK Register’s")} <code>success</code> / <code>data</code> {t("JSON envelope.")}</p>
      <p>{t("Account lookup reads saved snapshots and does not refresh supplier data. Use the returned collection timestamps when displaying account status or usage.")}</p>
      <p>{t("Daily usage reads saved bills for one completed Beijing calendar day. Account availability is reported separately within a successful batch response; missing bills are not zero-cost bills.")}</p>
      <p>{t("A confirmed Auth JSON update queues five background collections, starting after ten seconds, and returns")} <code>updated: true</code> {t("with")} <code>refresh_status: pending</code>{t(". Each later round waits ten seconds after the previous round completes, regardless of success or failure. The returned account contains saved database data; it does not prove collection has completed. The credential write is never repeated automatically.")}</p>
    </section>
    <div className='api-docs-next-links'>
      <DocLink topic='submit-account' onNavigate={onNavigate}>{t("Submit an account")} <ArrowRight aria-hidden='true' /></DocLink>
      <DocLink topic='account-lookup' onNavigate={onNavigate}>{t("Look up an account")} <ArrowRight aria-hidden='true' /></DocLink>
      <DocLink topic='daily-usage' onNavigate={onNavigate}>{t("Read daily usage")} <ArrowRight aria-hidden='true' /></DocLink>
      <DocLink topic='update-auth-json' onNavigate={onNavigate}>{t("Update Auth JSON")} <ArrowRight aria-hidden='true' /></DocLink>
    </div>
  </div>;
}

function Parameters({ topic, onNavigate }: Navigation & { topic: EndpointTopic }) {
  if (topic === 'daily-usage') return <>
    <div className='api-docs-auth-line'><LockKeyhole aria-hidden='true' /><span>{t("Authentication")}</span><code>Authorization: Bearer YOUR_TOKEN</code><DocLink topic='getting-started' onNavigate={onNavigate} className='api-docs-text-link'>{t("View guide")} <ArrowRight aria-hidden='true' /></DocLink></div>
    <section className='api-docs-content-block'><h3>{t("JSON body")}</h3>
      <p>{t("Send exactly these three fields, each once, in one JSON object with")} <code>Content-Type: application/json</code>{t(". The body must be 64 KiB or smaller. Do not add query parameters or unsupported fields.")}</p>
      <FieldTable fields={dailyUsageFields} label={t("Required daily usage request fields")} />
    </section>
    <section className='api-docs-content-block'><h3>{t("Ownership and account order")}</h3>
      <p><code>username</code> {t("must match your personal token’s owner. This endpoint does not include sub-users or search other users’ accounts.")}</p>
      <p>{t("Surrounding whitespace is trimmed and identifiers are matched case-insensitively. Duplicate account identifiers count once in the response, preserving the first occurrence order. The 100-entry input limit is checked before deduplication.")}</p>
      <p>{t("A missing account and an account outside your scope both return")} <code>not_found</code> {t("within the batch. An owned account with no saved bill for the date returns")} <code>no_data</code>{t(". Neither case starts a new collection.")}</p>
    </section>
  </>;
  const lookup = topic === 'account-lookup';
  const update = topic === 'update-auth-json';
  return <>
    <div className='api-docs-auth-line'><LockKeyhole aria-hidden='true' /><span>{t("Authentication")}</span><code>Authorization: Bearer YOUR_TOKEN</code>
      <DocLink topic='getting-started' onNavigate={onNavigate} className='api-docs-text-link'>{t("View guide")} <ArrowRight aria-hidden='true' /></DocLink>
    </div>
    <div className='api-docs-content-block'><h3>{lookup ? t("Query parameters") : t("JSON body")}</h3>
      <p>{lookup ? t("Send exactly these two parameters, each once, in the URL query string. Do not send a request body or any other query parameters.") : t("Send one JSON object with Content-Type: application/json. The request body must be 3 MiB or smaller. Do not add query parameters.")}</p>
      <FieldTable fields={lookup ? lookupFields : update ? authJSONUpdateFields : submissionFields} label={lookup ? t("Required lookup query parameters") : update ? t("Required Auth JSON update fields") : t("Required submission fields")} />
      {!lookup && !update && <p>{t("String limits use UTF-8 bytes. Submission values are not automatically trimmed; nonempty required strings cannot consist only of whitespace. Omit unused optional fields instead of sending")} <code>null</code>.</p>}
      {update && <p>{t("The server finds your account by user, vendor, and exact normalized account name.")} <code>auth_json.email</code> {t("must match that account after trimming and case normalization. Do not send a supplier record ID, password, plan, or any other top-level field.")}</p>}
    </div>
    {lookup ? <section className='api-docs-content-block'>
      <h3>{t("Exact account matching")}</h3>
      <p>{t("Both identifiers are trimmed and matched case-insensitively. Use the TK Register user’s email for")} <code>username</code> {t("and the submitted Claude login for")} <code>account</code>.</p>
      <p>{t("URL-encode both values, especially email addresses containing")} <code>+</code>{t(". The cURL example uses")} <code>--data-urlencode</code> {t("for this. Aliases, partial matches, and another user’s email are not supported.")}</p>
    </section> : <>
      <details className='api-docs-disclosure'><summary>{t("Auth JSON field requirements")} <span>{t("13 fields")}</span></summary>
        <div className='api-docs-disclosure-body'><p>{t("Every key below must be present. All required string values except")} <code>id_token</code> {t("must be nonempty. Each listed string is limited to 16,384 UTF-8 bytes; device IDs have the separate limits shown below.")}</p>
          <FieldTable fields={authenticationFields} label={t("Required fields inside auth_json")} />
          <p>{t("Additional fields inside")} <code>auth_json</code> {t("are allowed and forwarded as part of the authentication object.")}</p>
        </div>
      </details>
      {!update && <details className='api-docs-disclosure'><summary>{t("Optional parameters")} <span>{t("7 fields")}</span></summary>
        <div className='api-docs-disclosure-body'><FieldTable fields={optionalSubmissionFields} label={t("Optional submission fields")} showOptional /></div>
      </details>}
      <p className='api-docs-note'>{update ? t("Only an accepted Claude account belonging to the token owner can be updated. The supplier must currently allow editing. The server verifies the supplier record and preserves its other fields. No new submission is created.") : <>{t("This endpoint is dedicated to Claude. Do not send top-level")} <code>provider</code>, <code>model</code>, <code>account_type</code>{t(", proxy routing fields, or the vendor’s shared Bearer credential. Unsupported top-level fields are rejected.")}</>}</p>
    </>}
  </>;
}

function CurlExample({ topic, baseURL, onFeedback }: { topic: EndpointTopic; baseURL: string; onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>{topic === 'daily-usage' ? t("Read one day of account bills") : topic === 'account-lookup' ? t("Find your submitted account") : topic === 'update-auth-json' ? t("Replace an account’s Auth JSON") : t("Submit a Claude account")}</h3>
      <p>{t("Set your personal token in your shell, then run the request. All account values and credentials below are placeholders; replace them before use")}{topic === 'submit-account' ? t(", including the sample dates") : ''}.</p>
      <CodeBlock title={t("Token environment variable")} text="export TK_REGISTER_API_TOKEN='YOUR_TOKEN'" onFeedback={onFeedback} />
      <CodeBlock title={t("cURL request")} text={curlExample(topic, baseURL)} onFeedback={onFeedback} />
      <p>{topic === 'daily-usage' ? t("Replace username with your TK Register email and accounts with your submitted Claude identifiers. Choose yesterday or an earlier date in Beijing time. The POST reads saved daily bills only; it does not submit accounts or fetch new bills.") : topic === 'account-lookup' ? t("username identifies your TK Register login. account identifies the Claude account you submitted. This request reads saved data only.") : topic === 'update-auth-json' ? t("This changes the account’s credentials at the supplier. username is your TK Register email; account and auth_json.email identify the Claude account. Replace every credential placeholder and sample timestamp. TK Register sends the write once and does not automatically retry it.") : t("The request sends account credentials and can create a real vendor submission. TK Register does not retry the request automatically.")}</p>
    </section>
    <details className='api-docs-disclosure'><summary>{t("Using a self-signed certificate")}</summary><div className='api-docs-disclosure-body'><p>{t("The examples use normal TLS certificate verification. For a deployment whose self-signed certificate you intentionally trust, you can add")} <code>--insecure</code> {t("to cURL. Omit this option when using a trusted certificate.")}</p></div></details>
  </>;
}

function CurrencyDocumentation({ children }: { children: (rate: number) => ReactNode }) {
  const [rate, setRate] = useState<number | null>(null);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setRate(null);
    setError('');
    request<{ exchange_rate_cny_per_usd: number }>('/exchange-rate', { signal: controller.signal })
      .then(data => {
        const value = data.exchange_rate_cny_per_usd;
        if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) throw new Error('Could not load the exchange rate.');
        if (!controller.signal.aborted) setRate(value);
      })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); });
    const refresh = () => setAttempt(value => value + 1);
    window.addEventListener('focus', refresh);
    return () => { controller.abort(); window.removeEventListener('focus', refresh); };
  }, [attempt]);
  if (error) return <><ErrorNotice>{error}</ErrorNotice><button type='button' className='button pale' onClick={() => setAttempt(value => value + 1)}>{t("Try again")}</button></>;
  if (rate === null) return <Loading label={t("Loading response documentation…")} />;
  return children(rate);
}

function LookupResponse({ onFeedback, rate }: { onFeedback: CopyFeedback; rate: number }) {
  return <>
    <section className='api-docs-content-block'><h3>{t("200 · Account found")}</h3>
      <p>{t("Returns the latest saved account data. IDs are strings. A successful lookup means the local account was found; it does not mean the vendor account is active.")}</p>
      <CodeBlock title={t("JSON · Example response")} text={JSON.stringify(lookupResponse(rate), null, 2)} onFeedback={onFeedback} />
    </section>
    <details className='api-docs-disclosure'><summary>{t("Response fields")} <span>data</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={lookupResponseFields} label={t("Fields in response data")} /></div></details>
    <details className='api-docs-disclosure'><summary>{t("Snapshot fields")} <span>data.snapshot</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={snapshotFields(rate)} label={t("Fields in the saved snapshot")} /></div></details>
    <section className='api-docs-content-block'><h3>{t("Freshness and missing data")}</h3>
      <ul className='api-docs-bullets'>
        <li><strong>{t("No collection yet:")}</strong> <code>snapshot</code> {t("is")} <code>null</code>{t(". Show unavailable values as “—”; do not infer zero.")}</li>
        <li><strong>{t("A later collection fails:")}</strong> {t("the current lifetime’s last successful snapshot is retained.")} <code>last_error</code> {t("and")} <code>last_attempt_at</code> {t("describe the latest attempt.")}</li>
        <li><strong>{t("A missing metric:")}</strong> {t("the field is")} <code>null</code>{t(". A measured zero remains")} <code>0</code>{t(", such as")} <code>0%</code> {t("or")} <code>0.00 $</code>.</li>
        <li><strong>{t("Collection time:")}</strong> {t("use")} <code>last_success_at</code> {t("and")} <code>snapshot.collected_at</code> {t("to show when displayed values were collected.")}</li>
      </ul>
      <p>{t("Repeating a lookup does not make the snapshot newer. A new accepted vendor identity or plan can start a new account lifetime; older snapshots are not returned as its latest data.")}</p>
      <p>{t("This endpoint returns one saved snapshot, not a historical time series or billing query. Token totals and spend are cumulative, not the amount used since your previous request.")}</p>
      <p>{t("The response excludes passwords,")} <code>auth_json</code>{t(", personal tokens, and vendor credentials.")}</p>
    </section>
  </>;
}

function DailyUsageResponse({ onFeedback, rate }: { onFeedback: CopyFeedback; rate: number }) {
  return <>
    <section className='api-docs-content-block'><h3>{t("200 · Batch result")}</h3>
      <p>{t("A valid batch returns HTTP 200 even when some or all accounts have no available bill. Inspect each account’s")} <code>status</code> {t("and")} <code>summary.status</code>{t(". The example includes a bill, a saved zero-cost bill, an owned account without data, and a missing or inaccessible account.")}</p>
      <CodeBlock title={t("JSON · Totals-only response")} text={JSON.stringify(dailyUsageResponse(rate), null, 2)} onFeedback={onFeedback} />
      <p><code>model_costs_enabled</code> {t("is controlled by an administrator’s API setting and defaults to false. When false, every")} <code>models</code> {t("field and the top-level")} <code>model_totals</code> {t("field are omitted. Account totals remain available.")}</p>
    </section>
    <details className='api-docs-disclosure'><summary>{t("Response with model costs enabled")}</summary><div className='api-docs-disclosure-body'><CodeBlock title={t("JSON · Model-cost response")} text={JSON.stringify(dailyUsageResponse(rate, true), null, 2)} onFeedback={onFeedback} /></div></details>
    <details className='api-docs-disclosure'><summary>{t("Response fields")} <span>data</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={dailyUsageResponseFields} label={t("Fields in daily usage response data")} /></div></details>
    <details className='api-docs-disclosure'><summary>{t("Summary fields")} <span>data.summary</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={dailyUsageSummaryFields} label={t("Daily usage availability and totals")} /></div></details>
    <details className='api-docs-disclosure'><summary>{t("Account fields")} <span>data.accounts[]</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={dailyUsageAccountFields} label={t("Daily usage account result fields")} /></div></details>
    <details className='api-docs-disclosure'><summary>{t("Model fields")} <span>models / model_totals</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={dailyUsageModelFields} label={t("Per-account and combined model-cost fields")} /></div></details>
    <section className='api-docs-content-block'><h3>{t("Availability and amounts")}</h3>
      <ul className='api-docs-bullets'>
        <li><code>complete</code>{t(": every unique requested account has an available bill, including known zero-cost bills.")}</li>
        <li><code>partial</code>{t(": at least one account has a bill and at least one has")} <code>no_data</code> {t("or")} <code>not_found</code>.</li>
        <li><code>no_data</code>{t(": no accounts have available bills. Both summary amount fields are")} <code>null</code>{t(", even when every account is")} <code>not_found</code>.</li>
      </ul>
      <p>{t("Amounts are decimal strings: CNY has eight decimal places and USD has two.")} <code>null</code> {t("means unavailable; a saved zero is")} <code>"0.00000000"</code> / <code>"0.00"</code>{t(". Use decimal arithmetic when processing amounts.")}</p>
      <p>{t("The response reports the configured exchange rate as a string, currently")} <code>{String(rate)}</code> {t("CNY per USD. Each account’s USD total is calculated first; the summary sums these reported account totals. Model USD amounts add up to their account total, and combined model totals sum those account model amounts. Converting the combined CNY total once can differ by a few cents because of per-account rounding.")}</p>
      <p>{t("When model costs are enabled,")} <code>no_data</code> {t("and")} <code>not_found</code> {t("accounts have empty")} <code>models</code> {t("arrays. If no accounts have data,")} <code>model_totals</code> {t("is also empty. Requests never return credentials or start supplier queries.")}</p>
    </section>
  </>;
}

function SubmissionResponse({ onFeedback }: { onFeedback: CopyFeedback }) {
  return <>
    <section className='api-docs-content-block'><h3>{t("Vendor response")}</h3>
      <p>{t("When the vendor returns a complete response of 256 KiB or less, TK Register passes through its HTTP status, content type, and body without adding a JSON envelope. If the shared vendor credential is echoed in that body, it is redacted.")}</p>
      <p>{t("The vendor name in the top-level")} <code>alias</code> {t("string is replaced with")} <code>xxxxx</code>{t(", matching the account history display. This does not change the account ID or other business fields.")}</p>
      <p>{t("There is no fixed TK Register success body for this endpoint. Read the vendor’s response to determine the result. A complete vendor error response, including a duplicate-account error, is passed through in the same way.")}</p>
      <CodeBlock title={t("Response header examples")} text={'X-Request-ID: <request identifier>\nX-Submission-ID: <local submission identifier>'} onFeedback={onFeedback} />
    </section>
    <section className='api-docs-content-block'><h3>{t("Submission history and identifiers")}</h3>
      <p>{t("After validation and duplicate checks pass and execution starts, the attempt is saved in your Submission history.")} <code>X-Submission-ID</code> {t("identifies that local record and is only returned after the record exists. It is different from the vendor’s account ID.")}</p>
      <p><code>X-Request-ID</code> {t("identifies the request for troubleshooting. Keep both headers when available.")}</p>
      <p>{t("An HTTP 2xx response alone does not prove the account was accepted. If the vendor’s complete response has no valid account ID, it is still passed through, but the local attempt is marked")} <code>unknown</code>{t(". An accepted submission also does not mean the account is active.")}</p>
      <p>{t("A newly accepted browser or API submission queues five account-collection rounds. The first starts no earlier than ten seconds after acceptance; the next starts ten seconds after the previous round completes, even when that round succeeded. Your submission response does not wait. These rounds only read account data; they never repeat account creation.")}</p>
      <p>{t("Each successful round updates saved status, usage, and spend in Accounts and account lookup; failures retain the last successful data. A busy lock postpones the job five seconds without consuming a round. Known supplier cooldowns and login-lock waits also defer without consuming a round; an actual failed request counts. Supplier website login must be configured. Manual Sync and the default 300-second schedule remain separate from these five rounds.")}</p>
    </section>
    <section className='api-docs-content-block'><h3>{t("Timeouts and retries")}</h3>
      <p>{t("A timeout or connection failure can leave the remote outcome unconfirmed. The local attempt remains")} <code>unknown</code>{t(", and you may retry manually. Each retry that passes validation and duplicate checks starts a new submission record; the earlier attempt is retained.")}</p>
      <p>{t("The system does not automatically retry account creation. Accepted or currently running submissions for the same user, vendor, and account continue to block duplicates. Check Submission history before deciding whether to submit again.")}</p>
    </section>
  </>;
}

function AuthJSONUpdateResponse({ onNavigate, onFeedback, rate }: Navigation & { onFeedback: CopyFeedback; rate: number }) {
  return <>
    <section className='api-docs-content-block'><h3>{t("200 · Credentials updated")}</h3>
      <p>{t("The supplier confirmed the replacement credentials. The server queues five collections of that account’s status, usage, and spend, starting ten seconds later, and returns without waiting. The example contains saved database data collected five minutes before the credential change, not an immediate post-update query result.")}</p>
      <CodeBlock title={t("JSON · Example response")} text={JSON.stringify(authJSONUpdateResponse(rate), null, 2)} onFeedback={onFeedback} />
    </section>
    <details className='api-docs-disclosure'><summary>{t("Response fields")} <span>data</span></summary><div className='api-docs-disclosure-body'><FieldTable fields={authJSONUpdateResponseFields} label={t("Fields in Auth JSON update response data")} /></div></details>
    <section className='api-docs-content-block'><h3>{t("Handle the two outcomes separately")}</h3>
      <p><code>updated: true</code> {t("confirms the credential change. The normal response has")} <code>refresh_status: pending</code>{t(", meaning collection is queued, not completed. It does not guarantee the supplier account is active. The response does not change after it has been returned.")}</p>
      <p>{t("All five rounds run regardless of earlier success or failure, with ten seconds between completion and the next round. A busy shared synchronization lock defers the job five seconds without consuming a round. Known supplier cooldowns and login-lock waits defer without consuming a round; an actual failed supplier request counts. The next round honors any longer supplier retry deadline. Failed rounds retain the last successful snapshot. These reads never repeat the credential write.")}</p>
      <p>{t("If the job was queued but local readback fails,")} <code>account</code> {t("is null and")} <code>refresh_status</code> {t("remains")} <code>pending</code>{t(". If local persistence or a changed account identity prevents scheduling, the API instead returns HTTP 200 with")} <code>updated: true</code> {t("and")} <code>refresh_status: failed</code>{t(". Neither case undoes the confirmed credential change.")}</p>
      <p>{t("The returned")} <code>account</code> {t("follows the")} <DocLink topic='account-lookup' onNavigate={onNavigate} className='api-docs-text-link'>{t("Account lookup")}</DocLink> {t("response contract, including snapshot currency and USD conversion fields. Use")} <code>last_success_at</code> {t("and")} <code>snapshot.collected_at</code> {t("to distinguish fresh data from retained data.")}</p>
      <p>{t("Read account lookup later for saved collection results and timestamps. Lookup does not start collection. After the five rounds finish, the existing Sync action can request another collection; do not repeat the credential update to refresh data. The browser Auth JSON editor uses the same delayed five-round behavior.")}</p>
    </section>
    <section className='api-docs-content-block'><h3>{t("Audit and request identifiers")}</h3>
      <CodeBlock title={t("Response header examples")} text={'X-Request-ID: <request identifier>\nX-Auth-JSON-Audit-ID: <local update audit identifier>'} onFeedback={onFeedback} />
      <p>{t("An audit record is created after authorization, validation, and lock acquisition, before the supplier write starts. Its ID is returned as")} <code>data.audit_id</code> {t("on success and in")} <code>X-Auth-JSON-Audit-ID</code> {t("whenever that record exists, including failed or unconfirmed updates.")}</p>
      <p>{t("The audit records the actor, owner, vendor, account, API token reference and revision, request ID, update outcome, and refresh outcome. It does not store the replacement Auth JSON or any token plaintext. Existing submission history is unchanged.")}</p>
      <p>{t("For queued updates, the audit’s refresh outcome stays")} <code>pending</code> {t("through the first four rounds, then becomes")} <code>succeeded</code> {t("or")} <code>failed</code> {t("according to the fifth round. An ineligible target also ends the refresh as failed. Earlier successful snapshots remain saved if the final round fails. A failed refresh does not change the credential update outcome.")}</p>
      <p>{t("Errors rejected before an update begins have no update audit ID. Keep the request ID and audit ID, when available, for support.")}</p>
    </section>
  </>;
}

function Errors({ topic, onFeedback }: { topic: EndpointTopic; onFeedback: CopyFeedback }) {
  if (topic === 'daily-usage') return <>
    <section className='api-docs-content-block'><h3>{t("Error responses")}</h3>
      <p>{t("Use the HTTP status and error code; message wording can change. Request-level validation or authentication failures use this error envelope. Individual")} <code>no_data</code> {t("and")} <code>not_found</code> {t("account results use the successful HTTP 200 response instead.")}</p>
      <CodeBlock title={t("JSON · Local error example")} text={JSON.stringify({ success: false, code: 'INVALID_DATE', message: 'Choose yesterday or an earlier date in Asia/Shanghai.' }, null, 2)} onFeedback={onFeedback} />
      <ErrorTable errors={dailyUsageErrors} />
    </section>
    <section className='api-docs-content-block'><h3>{t("Rate limits and retries")}</h3>
      <p>{t("The limit is 60 requests per minute per user, separate from account lookup and submission limits. A")} <code>429 RATE_LIMITED</code> {t("response includes")} <code>Retry-After</code> {t("and JSON")} <code>retry_after</code>{t(", both in seconds. Wait that long before another request.")}</p>
    </section>
  </>;
  const lookup = topic === 'account-lookup';
  const update = topic === 'update-auth-json';
  const example = lookup ? { success: false, code: 'ACCOUNT_NOT_FOUND', message: 'The account could not be found.' } : update ? { success: false, code: 'AUTH_JSON_UPDATE_UNCONFIRMED', message: 'We could not confirm whether the update completed. Check the account before trying again.' } : { success: false, code: 'INVALID_PARAMETERS', message: 'Choose a supported Claude plan.' };
  return <>
    <section className='api-docs-content-block'><h3>{lookup || update ? t("Error responses") : t("Local errors and vendor errors")}</h3>
      <p>{lookup || update ? t("Use the HTTP status and code to handle failed requests. Do not depend on exact message wording.") : t("Local authentication, validation, configuration, and connection errors use the envelope below. Complete vendor error responses use the vendor’s own status and body instead, with shared credentials redacted if echoed.")}</p>
      <CodeBlock title={t("JSON · Local error example")} text={JSON.stringify(example, null, 2)} onFeedback={onFeedback} />
      <ErrorTable errors={lookup ? lookupErrors : update ? authJSONUpdateErrors : submissionErrors} />
      {update && <p>{t("Existing tokens receive")} <code>claude:auth-json:update</code> {t("automatically. If a valid token still receives")} <code>403 INSUFFICIENT_SCOPE</code>{t(", ask an administrator to check its permissions. This feature does not require token regeneration.")}</p>}
    </section>
    <section className='api-docs-content-block'><h3>{t("Rate limits and retries")}</h3>
      <p>{t("A local")} <code>429 RATE_LIMITED</code> {t("response includes a")} <code>Retry-After</code> {t("header and JSON")} <code>retry_after</code> {t("value, both in seconds. Wait for this duration before sending another request.")}{topic === 'submit-account' && t(" A vendor 429 response is passed through and does not guarantee these retry fields.")}</p>
      {update && <p><code>429 SUPPLIER_RATE_LIMITED</code> {t("means the write has not started because supplier access must wait. It includes the same retry fields, plus")} <code>retry_stage</code>: <code>login</code> {t("for a login cooldown,")} <code>accounts</code> {t("for an account-query cooldown, or")} <code>login_lock</code> {t("while another request is logging in. Login and query waits are handled separately; a query cooldown does not discard a valid supplier session.")}</p>}
      <p>{lookup ? t("Lookups can be retried with a delay after transient connection errors or 503 responses because they do not create submissions or query the vendor. A 200 response with last_error describes a saved collection failure; polling repeatedly will not repair it.") : update ? t("Do not automatically retry credential writes. A timeout or connection loss after sending the write can leave the audit result unknown. Check the account before a manual retry. Only a confirmed, locally recorded update queues the five collection rounds. After those rounds, another Sync can refresh data without repeating the write.") : t("Do not automatically retry submissions. A connection failure or timeout does not prove the vendor did not receive your account. Review Submission history before a manual retry.")}</p>
      <p>{t("Keep")} <code>X-Request-ID</code>{lookup ? '' : update ? t(" and, when present, X-Auth-JSON-Audit-ID") : t(" and, when present, X-Submission-ID")} {t("when requesting support.")}</p>
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
    <header className='api-docs-detail-heading'><h2 className='api-docs-detail-title'>{t(endpoint.title)}</h2><p>{t(endpoint.description)}</p></header>
    <div className='api-docs-endpoint'><span className={`api-docs-method ${endpoint.method === 'GET' ? 'is-get' : ''}`}>{endpoint.method}</span>
      <code>{endpoint.path}</code><CopyButton text={baseURL + endpoint.path} label={t("Full endpoint URL")} iconOnly onFeedback={onFeedback} />
    </div>
    <p className='api-docs-base-caption'>{t("Base URL ·")} <code>{baseURL}</code></p>
    <div className='api-docs-meta'><span>{t("Scope")} <code>{endpoint.scope}</code></span><span>{t(endpoint.limit)} {t("per user")}{topic === 'submit-account' || topic === 'update-auth-json' ? t(" · shared with the web form") : ''}</span></div>
    <div className='api-docs-info'><Info aria-hidden='true' /><p>{t(endpoint.note)}</p></div>
    <div className='api-docs-tabs' role='tablist' aria-label={t("Endpoint documentation sections")}>
      {docSections.map((tab, index) => <button key={tab.id} type='button' role='tab'
        id={`${tabPrefix}-tab-${tab.id}`} aria-controls={`${tabPrefix}-panel-${tab.id}`}
        aria-selected={section === tab.id} tabIndex={section === tab.id ? 0 : -1}
        ref={element => { tabs.current[index] = element; }}
        onClick={() => onNavigate(topic, tab.id)} onKeyDown={event => onTabKey(event, index)}>{t(tab.label)}</button>)}
    </div>
    {docSections.map(tab => <div key={tab.id} className='api-docs-content' role='tabpanel'
      id={`${tabPrefix}-panel-${tab.id}`} aria-labelledby={`${tabPrefix}-tab-${tab.id}`} hidden={section !== tab.id} tabIndex={0}>
      {section === tab.id && (tab.id === 'parameters' ? <Parameters topic={topic} onNavigate={onNavigate} />
        : tab.id === 'example' ? <CurlExample topic={topic} baseURL={baseURL} onFeedback={onFeedback} />
        : tab.id === 'response' ? topic === 'submit-account' ? <SubmissionResponse onFeedback={onFeedback} /> : <CurrencyDocumentation>{rate => topic === 'daily-usage' ? <DailyUsageResponse rate={rate} onFeedback={onFeedback} /> : topic === 'account-lookup' ? <LookupResponse rate={rate} onFeedback={onFeedback} /> : <AuthJSONUpdateResponse rate={rate} onNavigate={onNavigate} onFeedback={onFeedback} />}</CurrencyDocumentation>
        : <Errors topic={topic} onFeedback={onFeedback} />)}
    </div>)}
  </>;
}

export default function APIDocumentation({ topic, section = 'parameters', onNavigate, onManageToken }: APIDocumentationProps) {
  useLanguage();
  const [feedback, setFeedback] = useState<Feedback | null>(null);
  const baseURL = `${window.location.origin}${API_BASE_PATH}`;
  useEffect(() => { setFeedback(null); }, [topic, section]);
  useEffect(() => {
    if (!feedback) return;
    const timeout = setTimeout(() => setFeedback(null), 6000);
    return () => clearTimeout(timeout);
  }, [feedback]);
  return <div className='api-docs'>
    {topic && <DocLink onNavigate={onNavigate} className='api-docs-text-link api-docs-back'><ArrowLeft aria-hidden='true' />{t("API documentation")}</DocLink>}
    <header className='api-docs-page-heading'><div><h1>{t("API documentation")}</h1><p>{topic ? t("Connect your application to TK Register.") : t("Explore every API, with parameters and ready-to-use examples.")}</p></div><span className='api-docs-version'>{topic ? 'v1' : 'REST API · v1'}</span></header>
    {!topic ? <DocumentationIndex baseURL={baseURL} onNavigate={onNavigate} onManageToken={onManageToken} onFeedback={setFeedback} /> : <>
      <nav className='api-docs-topic-nav' aria-label={t("Documentation topics")}>{docTopics.map(item => <DocLink key={item.id} topic={item.id} current={topic === item.id} onNavigate={onNavigate}>{t(item.label)}</DocLink>)}</nav>
      {topic === 'getting-started' ? <GettingStarted baseURL={baseURL} onNavigate={onNavigate} onManageToken={onManageToken} onFeedback={setFeedback} /> : <EndpointDocumentation key={topic} topic={topic} section={section} baseURL={baseURL} onNavigate={onNavigate} onFeedback={setFeedback} />}
    </>}
    <div className={`api-docs-feedback${feedback?.error ? ' is-error' : ''}`} role={feedback?.error ? 'alert' : 'status'} aria-live={feedback?.error ? 'assertive' : 'polite'} aria-atomic='true'>{feedback?.text ? t(feedback.text) : ''}</div>
  </div>;
}
