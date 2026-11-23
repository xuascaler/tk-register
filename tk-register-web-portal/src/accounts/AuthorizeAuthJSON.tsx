import { t, translateMessage, useLanguage, currentLanguage } from '../i18n';
import { Check, ChevronDown, CircleAlert, CircleCheck, Copy, Info, LoaderCircle, PlugZap, RefreshCw, Upload } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import { APIError, errorMessage, request } from '../api';
import { Loading, PasswordField } from '../ui';
import AuthorizationErrorDetails from '../AuthorizationErrorDetails';
import Select from '../submissions/Select';
import { AuthFieldError, MAX_AUTH_BYTES } from '../submissions/validation';
import { authEditorValue, completionPatch, emailDiffers, importAccountOptions, parseImportDocument, proxyTestFailure, readProxyCheck, validateProxy, type CredentialFlow, type CredentialMethod, type ProxyCheck } from '../submissions/credential-flow';
import { authJSONUpdateError, confirmedAuthJSONResult, localizeAuthJSONMessage, type AuthJSONUpdateResult } from './auth-json';
import { generationResultUncertain, readGeneratedAuthJSON, type GeneratedAuthJSON } from './deactivate';
import '../submissions/authorize-submit.css';

type UpdateFlow = Omit<CredentialFlow, 'status'> & { status?: CredentialFlow['status'] | 'updated' | 'unknown' | 'updating' | 'rejected' | 'failed' | 'exported'; update_result?: AuthJSONUpdateResult; generated_at?: string; audit_id?: string };
type ProxyTest = { status: 'idle' | 'testing' } | { status: 'success'; check: ProxyCheck } | { status: 'error'; message: string; error?: APIError | null };
type Stage = 'prepare' | 'link' | 'ready' | 'missing' | 'mismatch' | 'failed' | 'unknown';
type Props = {
  path: string; accountEmail: string; disabled: boolean;
  purpose?: 'update' | 'deactivate'; onCancel: () => void; onSaved?: (result: AuthJSONUpdateResult) => void; onGenerated?: (result: GeneratedAuthJSON) => void;
  onState: (state: { busy: boolean; dirty: boolean }) => void;
};
const methodLabels: Record<CredentialMethod, string> = { oauth: 'OAuth', setup: 'Setup Token', json: 'Import JSON', cookie: 'Cookie', token: 'Existing token' };

export default function AuthorizeAuthJSON({ path, accountEmail, disabled, purpose = 'update', onCancel, onSaved, onGenerated, onState }: Props) {
  useLanguage();
  const base = `${path}/flows`;
  const generation = purpose === 'deactivate';
  const storageKey = `tk-register:${generation ? 'deactivate' : 'auth-json'}-flow:${path}`;
  const prefix = useId();
  const [method, setMethod] = useState<CredentialMethod>('oauth');
  const [setupKind, setSetupKind] = useState('full');
  const [proxy, setProxy] = useState('');
  const [proxyTest, setProxyTest] = useState<ProxyTest>({ status: 'idle' });
  const [proxyPrompt, setProxyPrompt] = useState('');
  const [flow, setFlow] = useState<UpdateFlow | null>(null);
  const flowRef = useRef<UpdateFlow | null>(null);
  const [code, setCode] = useState('');
  const [documentText, setDocumentText] = useState('');
  const [accountIndex, setAccountIndex] = useState('0');
  const [sessionKey, setSessionKey] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [tokenExpiry, setTokenExpiry] = useState('');
  const [authText, setAuthText] = useState('');
  const [stage, setStage] = useState<Stage>('prepare');
  const [busy, setBusy] = useState('');
  const [error, setErrorText] = useState('');
  const [errorDetails, setErrorDetails] = useState<APIError | null>(null);
  const [credentialError, setCredentialError] = useState<string | AuthFieldError>('');
  const [announcement, setAnnouncement] = useState('');
  const [credentialsOpen, setCredentialsOpen] = useState(false);
  const [fileName, setFileName] = useState('');
  const [reading, setReading] = useState(false);
  const [expired, setExpired] = useState(false);
  const alive = useRef(true);
  const pending = useRef<AbortController | null>(null);
  const proxyPending = useRef<AbortController | null>(null);
  const proxyRevision = useRef(0);
  const active = useRef(false);
  const upload = useRef<HTMLInputElement>(null);
  const readingFile = useRef(0);
  const proxyField = useRef<HTMLDivElement>(null);
  const testButton = useRef<HTMLButtonElement>(null);
  const otherMethods = useRef<HTMLDetailsElement>(null);
  const testing = proxyTest.status === 'testing';
  const locked = disabled || !!busy || reading;
  const uncertain = stage === 'unknown';
  const dirty = Boolean(proxy || code || documentText || sessionKey || accessToken || refreshToken || flow);
  const authorizedEmail = typeof flow?.auth?.email === 'string' ? flow.auth.email : '';
  let accountOptions: { value: string; label: string }[] = [];
  try { accountOptions = importAccountOptions(documentText); } catch { /* Validated on import. */ }

  useEffect(() => { alive.current = true; return () => { alive.current = false; readingFile.current++; pending.current?.abort(); proxyPending.current?.abort(); }; }, []);
  useEffect(() => { onState({ busy: !!busy || testing || reading, dirty }); }, [busy, testing, reading, dirty, onState]);
  useEffect(() => () => onState({ busy: false, dirty: false }), [onState]);
  useEffect(() => {
    if (!flow) { setExpired(false); return; }
    const remaining = Date.parse(flow.expires_at) - Date.now();
    setExpired(remaining <= 0);
    const timer = window.setTimeout(() => setExpired(true), Math.max(0, remaining));
    return () => window.clearTimeout(timer);
  }, [flow]);
  useEffect(() => {
    if (disabled) return;
    let saved: { flow_id?: unknown; expires_at?: unknown; method?: unknown; setup_kind?: unknown };
    try { saved = JSON.parse(sessionStorage.getItem(storageKey) || 'null'); } catch { return; }
    if (!saved || typeof saved.flow_id !== 'string' || typeof saved.expires_at !== 'string' || !Number.isFinite(Date.parse(saved.expires_at)) || Date.parse(saved.expires_at) <= Date.now()) return;
    const controller = new AbortController(); pending.current = controller; active.current = true; setBusy('Restoring authorization…');
    request<UpdateFlow>(`${base}/${encodeURIComponent(saved.flow_id)}`, { signal: controller.signal }).then(value => {
      if (!alive.current || controller.signal.aborted) return;
      if (typeof saved.method === 'string' && ['oauth', 'setup', 'json', 'cookie', 'token'].includes(saved.method)) setMethod(saved.method as CredentialMethod);
      setSetupKind(saved.setup_kind === 'official' ? 'official' : 'full');
      acceptFlow(value);
      if (generation && value.status === 'exported') { try { const result = readGeneratedAuthJSON(value, saved.flow_id as string, accountEmail); clearStorage(); onGenerated?.(result); } catch { setStage('unknown'); setError('The saved generation result could not be confirmed. Check the saved result before generating again.'); } }
      else if (value.status === 'updated' && !generation && confirmedAuthJSONResult(value.update_result)) { clearStorage(); onSaved?.(value.update_result); }
      else if (value.status === 'updated') { setStage('unknown'); setError(generation ? 'The saved generation result could not be confirmed. Check the saved result before generating again.' : 'The saved update result could not be confirmed. Check the account before trying again.'); }
      else if (value.status === 'unknown' || value.status === 'updating') { setStage('unknown'); setError(generation ? 'The previous generation result is unconfirmed. Check the saved result before generating again.' : 'The previous update result is unconfirmed. Check the account before trying again.'); }
      else if (value.status === 'rejected' || value.status === 'failed' || value.status === 'exported') { setStage('failed'); setError(generation ? 'This authorization has already finished a generation attempt. Start a new authorization to try again.' : 'This authorization has already finished an update attempt. Start a new authorization to try again.'); }
      else setStage(value.auth ? 'ready' : 'link');
    }).catch(cause => { if (!controller.signal.aborted && alive.current) { clearStorage(); showError(cause); } })
      .finally(() => { if (!controller.signal.aborted && alive.current) { active.current = false; setBusy(''); } });
    return () => controller.abort();
  }, [base, storageKey, disabled]);
  useEffect(() => {
    if (!flow) return;
    // Only flow identity and presentation preferences survive a reload. Tokens,
    // proxy credentials, codes, imported documents and Auth JSON stay in memory.
    try { sessionStorage.setItem(storageKey, JSON.stringify({ flow_id: flow.flow_id, expires_at: flow.expires_at, method, setup_kind: setupKind })); } catch { /* Optional storage. */ }
  }, [flow, method, setupKind, storageKey]);

  function setError(message: string) { setErrorText(message); setErrorDetails(null); }
  function showError(cause: unknown, message = errorMessage(cause)) { setErrorText(message); setErrorDetails(cause instanceof APIError ? cause : null); }
  function clearStorage() { try { sessionStorage.removeItem(storageKey); } catch { /* Optional storage. */ } }
  function acceptFlow(value: UpdateFlow) {
    if (!value?.flow_id || !Number.isFinite(Date.parse(value.expires_at))) throw new Error('The authorization response could not be confirmed.');
    flowRef.current = value; setFlow(value);
    if (value.auth) setAuthText(authEditorValue(value.auth));
  }
  function resetFlow(keepProxyCheck = false) {
    readingFile.current++; setReading(false);
    flowRef.current = null; setFlow(null); setCode(''); setAuthText(''); setCredentialError(''); setError(''); setStage('prepare'); setCredentialsOpen(false); setExpired(false); clearStorage();
    if (!keepProxyCheck) setProxyTest({ status: 'idle' });
    setProxyPrompt('');
  }
  function changeProxy(value: string) {
    proxyRevision.current++; proxyPending.current?.abort(); proxyPending.current = null;
    resetFlow(); setProxy(value);
  }
  function changeMethod(next: CredentialMethod) {
    if (locked || testing) return;
    otherMethods.current?.removeAttribute('open');
    if (next === method) return;
    if (flow && !window.confirm(t('Discard this authorization and choose another method?'))) return;
    resetFlow(true); setMethod(next);
  }
  function requireTestedProxy() {
    if (testing) { setProxyPrompt('Wait for the proxy test to finish, then try again.'); return false; }
    const invalid = flowRef.current ? '' : validateProxy(proxy);
    if (invalid) { setProxyPrompt(invalid); proxyField.current?.querySelector('input')?.focus(); return false; }
    if (proxyTest.status !== 'success') {
      setProxyPrompt(generation ? 'Test this proxy successfully before generating an authorization link or Auth JSON.' : 'Test this proxy successfully before generating an authorization link or updating credentials.');
      testButton.current?.focus(); return false;
    }
    setProxyPrompt(''); return true;
  }
  async function testProxy() {
    if (locked || proxyPending.current || uncertain) return;
    const currentFlow = flowRef.current;
    const invalid = currentFlow ? '' : validateProxy(proxy);
    if (invalid) { setProxyPrompt(invalid); proxyField.current?.querySelector('input')?.focus(); return; }
    const controller = new AbortController(); proxyPending.current = controller;
    const revision = ++proxyRevision.current;
    setProxyPrompt(''); setProxyTest({ status: 'testing' });
    try {
      const result = await request<{ proxy_check: unknown }>(`${base}/proxy-test`, { method: 'POST', body: currentFlow ? { flow_id: currentFlow.flow_id } : { proxy_url: proxy.trim() }, signal: controller.signal });
      if (!alive.current || controller.signal.aborted || revision !== proxyRevision.current) return;
      setProxyTest({ status: 'success', check: readProxyCheck(result.proxy_check) }); setProxyPrompt('');
    } catch (cause) {
      if (!alive.current || controller.signal.aborted || revision !== proxyRevision.current) return;
      setProxyTest({ status: 'error', ...proxyTestFailure(cause), error: cause instanceof APIError ? cause : null });
    } finally { if (proxyPending.current === controller) proxyPending.current = null; }
  }
  async function perform(action: (signal: AbortSignal) => Promise<void>) {
    if (active.current || disabled) return;
    const controller = new AbortController(); pending.current = controller; active.current = true;
    setError(''); setAnnouncement('');
    try { await action(controller.signal); }
    catch (cause) { if (alive.current && !controller.signal.aborted) {
      const failure = authJSONUpdateError(cause);
      showError(cause);
      if (cause instanceof APIError && cause.code === 'AUTH_JSON_EMAIL_MISMATCH') setStage('mismatch');
      else if (generation && cause instanceof APIError && cause.code === 'CREDENTIAL_GENERATION_UNCONFIRMED') { setStage('unknown'); showError(cause, 'The generated result could not be confirmed. Your authorization is retained. Check the saved result before trying again.'); }
      else if (cause instanceof APIError && ['AUTH_JSON_UPDATE_UNCONFIRMED', 'CREDENTIAL_FLOW_UPDATE_FINISHED'].includes(cause.code)) { setStage('unknown'); showError(cause, failure.message); }
      else if (flowRef.current?.auth && stage !== 'missing') setStage('failed');
    } }
    finally { if (pending.current === controller) pending.current = null; active.current = false; if (alive.current) setBusy(''); }
  }
  async function generateLink() {
    if (locked || uncertain || !requireTestedProxy()) return;
    await perform(async signal => {
      setBusy('Generating authorization link…');
      const value = await request<UpdateFlow>(`${base}/start`, { method: 'POST', body: { proxy_url: proxy.trim(), mode: method, setup_kind: setupKind }, signal });
      if (!alive.current || signal.aborted) return;
      acceptFlow(value); setCode(''); setStage('link');
    });
  }
  async function updateFromFlow(value: UpdateFlow, text: string, signal: AbortSignal) {
    if (!value.auth) throw new Error('Authorize or import credentials first.');
    if (Date.parse(value.expires_at) <= Date.now()) throw new Error('This authorization has expired. Start a new authorization.');
    if (emailDiffers(accountEmail, value.auth.email)) { setStage('mismatch'); setError(generation ? 'The authorized email does not match this account.' : 'The authorized email does not match this account. No update was sent.'); return; }
    let auth: Record<string, unknown>, patch: Record<string, unknown>;
    try { ({ auth, patch } = completionPatch(value.auth, text)); }
    catch (cause) { setCredentialError(cause instanceof AuthFieldError ? cause : errorMessage(cause)); setCredentialsOpen(true); setStage('missing'); return; }
    if (emailDiffers(accountEmail, auth.email)) { setStage('mismatch'); setError(generation ? 'The authorized email does not match this account.' : 'The authorized email does not match this account. No update was sent.'); return; }
    if (Object.keys(patch).length) {
      setBusy('Saving missing credential fields…');
      value = await request<UpdateFlow>(`${base}/${encodeURIComponent(value.flow_id)}/draft`, { method: 'POST', body: { auth_patch: patch }, signal });
      if (!alive.current || signal.aborted) return;
      acceptFlow(value);
    }
    if (!alive.current || signal.aborted) return;
    if (Date.parse(value.expires_at) <= Date.now()) throw new Error('This authorization has expired. Start a new authorization.');
    if (generation) {
      setBusy('Generating Auth JSON…');
      const expectedProxy = { ip: readProxyCheck(value.proxy_check).ip, url: auth.proxy_url as string };
      try {
        const result = await request<unknown>(`${base}/${encodeURIComponent(value.flow_id)}/export`, { method: 'POST', body: {}, signal });
        if (!alive.current || signal.aborted) return;
        let confirmed: GeneratedAuthJSON;
        try { confirmed = readGeneratedAuthJSON(result, value.flow_id, accountEmail, expectedProxy); }
        catch { throw new APIError('The generated result could not be confirmed.', 200, 'CREDENTIAL_GENERATION_UNCONFIRMED'); }
        clearStorage(); onGenerated?.(confirmed);
      } catch (cause) {
        if (alive.current && !signal.aborted && generationResultUncertain(cause)) { setStage('unknown'); showError(cause, 'The generated result could not be confirmed. Your authorization is retained. Check the saved result before trying again.'); return; }
        throw cause;
      }
      return;
    }
    setBusy('Updating Auth JSON…');
    try {
      const result = await request<AuthJSONUpdateResult>(path, { method: 'POST', body: { flow_id: value.flow_id }, signal });
      if (!confirmedAuthJSONResult(result)) throw new APIError('The update result could not be confirmed.', 200, 'AUTH_JSON_UPDATE_UNCONFIRMED');
      if (!alive.current || signal.aborted) return;
      clearStorage(); onSaved?.(result);
    } catch (cause) {
      if (alive.current && !signal.aborted) {
        const failure = authJSONUpdateError(cause);
        if (failure.uncertain || cause instanceof APIError && cause.code === 'CREDENTIAL_FLOW_UPDATE_FINISHED') {
          setStage('unknown'); showError(cause, 'We could not confirm whether the update completed. Your input is retained. Check the account before trying again.'); return;
        }
      }
      throw cause;
    }
  }
  async function authorize() {
    if (locked || uncertain || expired || !requireTestedProxy()) return;
    await perform(async signal => {
      let value = flowRef.current;
      if (value?.auth) { await updateFromFlow(value, authText, signal); return; }
      if (method === 'oauth' || method === 'setup') {
        if (!value || !code.trim()) throw new Error('Paste the authorization code or full callback URL.');
        setBusy('Exchanging authorization and reading account information…');
        value = await request<UpdateFlow>(`${base}/exchange`, { method: 'POST', body: { flow_id: value.flow_id, code: code.trim() }, signal });
      } else {
        const body: Record<string, unknown> = { proxy_url: proxy.trim(), kind: method };
        if (method === 'json') { body.document = parseImportDocument(documentText); if (accountOptions.length > 1) body.account_index = Number(accountIndex); }
        if (method === 'cookie') { if (!sessionKey.trim()) throw new Error('Enter the sessionKey.'); body.session_key = sessionKey.trim(); }
        if (method === 'token') { if (!accessToken.trim()) throw new Error('Enter the access token.'); body.access_token = accessToken.trim(); body.refresh_token = refreshToken.trim(); if (tokenExpiry.trim()) body.expires_at = tokenExpiry.trim(); }
        setBusy('Importing credentials…'); value = await request<UpdateFlow>(`${base}/import`, { method: 'POST', body, signal });
      }
      if (!alive.current || signal.aborted) return;
      acceptFlow(value); await updateFromFlow(value, authEditorValue(value.auth || {}), signal);
    });
  }
  async function checkResult() {
    if (!flow || locked) return;
    await perform(async signal => {
      setBusy(generation ? 'Checking saved generation result…' : 'Checking saved update result…');
      const value = await request<UpdateFlow>(`${base}/${encodeURIComponent(flow.flow_id)}`, { signal });
      if (!alive.current || signal.aborted) return;
      acceptFlow(value);
      if (generation && value.status === 'exported') {
        try { const result = readGeneratedAuthJSON(value, flow.flow_id, accountEmail); clearStorage(); onGenerated?.(result); }
        catch { setStage('unknown'); setError('The saved generation result could not be confirmed. Start a new authorization if the saved result remains unavailable.'); }
      }
      else if (generation && value.status === 'ready' && value.auth) { setStage('ready'); setError('No completed export was saved. You can retry generating this same Auth JSON.'); }
      else if (value.status === 'updated' && !generation && confirmedAuthJSONResult(value.update_result)) { clearStorage(); onSaved?.(value.update_result); }
      else if (value.status === 'rejected' || value.status === 'failed') { setStage('failed'); setError(generation ? 'This authorization has already finished a generation attempt. Start a new authorization to try again.' : 'This authorization has already finished an update attempt. Start a new authorization to try again.'); }
      else { setStage('unknown'); setError(generation ? 'The generation result is still unconfirmed. Check the saved result before generating again.' : 'The update result is still unconfirmed. Check the account before starting another update.'); }
    });
  }
  async function readFile(file?: File) {
    if (upload.current) upload.current.value = '';
    if (!file) return;
    const revision = ++readingFile.current;
    if (file.size > MAX_AUTH_BYTES) { setError('Choose a JSON file no larger than 2 MB.'); return; }
    setReading(true);
    try { const text = await file.text(); if (!alive.current || revision !== readingFile.current) return; parseImportDocument(text); setDocumentText(text); setAccountIndex('0'); setFileName(file.name); }
    catch (cause) { if (alive.current && revision === readingFile.current) showError(cause); }
    finally { if (alive.current && revision === readingFile.current) setReading(false); }
  }
  function restart() {
    if (uncertain && !window.confirm(t(generation ? 'The previous Auth JSON may already have been generated. Start a new authorization?' : 'The previous update may have completed. Start a new authorization?'))) return;
    resetFlow(); setDocumentText(''); setSessionKey(''); setAccessToken(''); setRefreshToken(''); setTokenExpiry(''); setFileName('');
  }
  const step = flow?.auth ? 2 : flow ? 1 : 0;
  const hasCodeFlow = !!flow && !flow.auth;
  const canGenerate = !flow && (method === 'oauth' || method === 'setup');
  return <div className='authorize-submit account-auth-authorize' aria-busy={!!busy}>
    <div className='account-auth-authorize-body'>
      <ol className='authorize-progress' aria-label={generation ? t("Auth JSON generation progress") : t("Authentication update progress")}>{['Check proxy', 'Authorize', generation ? 'Generate Auth JSON' : 'Update Auth JSON'].map((label, index) => <li key={label} className={index < step ? 'done' : index === step ? 'active' : ''}><span>{index < step ? <Check aria-hidden='true' /> : index + 1}</span>{t(label)}</li>)}</ol>
      <section className='account-auth-proxy' ref={proxyField}>
        <div className='authorize-proxy-row'><PasswordField label={t("Account proxy")} value={proxy} onChange={event => changeProxy(event.target.value)} disabled={locked || uncertain} autoComplete='off' spellCheck={false} aria-describedby={`${prefix}-proxy-status ${prefix}-proxy-prompt`} placeholder={flow && !proxy ? t("Proxy retained securely by the authorization flow") : 'http://user:password@host:port'} /><button ref={testButton} type='button' className='button secondary authorize-proxy-test' data-required={!!proxyPrompt && !testing} disabled={locked || testing || uncertain} onClick={() => void testProxy()}>{testing ? <LoaderCircle className='spin' aria-hidden='true' /> : <PlugZap aria-hidden='true' />}<span>{testing ? t("Testing…") : t("Test connection")}</span></button></div>
        <div id={`${prefix}-proxy-status`} className='authorize-proxy-status' data-kind={proxyTest.status === 'success' ? 'success' : proxyTest.status === 'error' ? 'error' : 'idle'} role='status' aria-live='polite'>{testing ? <span>{t("Checking proxy connection…")}</span> : proxyTest.status === 'success' ? <><CircleCheck aria-hidden='true' /><span>{t("Connected · Exit IP")} {proxyTest.check.ip}</span></> : proxyTest.status === 'error' ? <><CircleAlert aria-hidden='true' /><span>{translateMessage(proxyTest.message)}</span></> : <span>{t("Test this proxy connection before continuing.")}</span>}</div>
        {proxyTest.status === 'error' && <AuthorizationErrorDetails error={proxyTest.error || null} />}
        {proxyPrompt && <div id={`${prefix}-proxy-prompt`} className='notice account-auth-proxy-prompt' role='alert'><strong>{testing ? t("Connection test in progress") : t("Test connection required")}</strong><p>{translateMessage(proxyPrompt)}</p></div>}
      </section>
      {!flow?.auth && <section className='account-auth-authorization'>
        <h3>{t("Authorization")}</h3>
        <div className='authorize-method-row'><div className='authorize-method-tabs' role='tablist' aria-label={t("Credential source")}>{(['oauth', 'setup', 'json'] as const).map(value => <button type='button' role='tab' key={value} aria-selected={method === value} disabled={locked || testing || uncertain} onClick={() => changeMethod(value)}>{t(methodLabels[value])}</button>)}</div><details ref={otherMethods} className='authorize-other-methods'><summary>{method === 'cookie' || method === 'token' ? t(methodLabels[method]) : t("Other methods")}<ChevronDown aria-hidden='true' /></summary><div>{(['cookie', 'token'] as const).map(value => <button type='button' className='text-link' key={value} disabled={locked || testing || uncertain} onClick={() => changeMethod(value)}>{t(methodLabels[value])}</button>)}</div></details></div>
        {method === 'setup' && <Select id={`${prefix}-setup-kind`} label={t("Authorization type")} value={setupKind} options={[{ value: 'full', label: t("Full access · Recommended") }, { value: 'official', label: t("Inference only · Long-lived token") }]} disabled={locked || !!flow} onChange={setSetupKind} />}
        {method === 'oauth' || method === 'setup' ? <>{flow?.auth_url ? <><div className='field'><label>{t("Authorization link")}</label><div className='authorize-link-row'><div className='authorize-link-display'>{flow.auth_url}</div><button type='button' className='button secondary compact' disabled={locked} onClick={() => void navigator.clipboard.writeText(flow.auth_url!).then(() => setAnnouncement('Authorization link copied.')).catch(() => setError('Unable to copy. Select and copy the authorization link.'))}><Copy aria-hidden='true' />{t("Copy")}</button></div><p className='field-help'>{t('Expires {{time}}.', { time: new Date(flow.expires_at).toLocaleTimeString(currentLanguage()) })}</p></div><div className='field'><label htmlFor={`${prefix}-code`}>{t("Authorization code or callback URL")}</label><textarea id={`${prefix}-code`} value={code} onChange={event => setCode(event.target.value)} disabled={locked || uncertain} rows={3} placeholder={t("Paste code#state or the full callback URL")} autoComplete='off' spellCheck={false} /></div></> : <p className='field-help'>{t('Generate a link, authorize {{account}}, then paste the code or callback URL.', { account: accountEmail })}</p>}<div className='authorize-info'><Info aria-hidden='true' /><span>{t("Use the same proxy in your browser when opening the Claude authorization link.")}</span></div></> : method === 'json' ? <><div className='field'><div className='field-heading'><label htmlFor={`${prefix}-document`}>{t("Source JSON")}</label><button type='button' className='text-link' disabled={locked} onClick={() => upload.current?.click()}><Upload aria-hidden='true' />{reading ? t("Reading…") : t("Upload file")}</button></div><input ref={upload} type='file' className='sr-only' aria-label={t("Upload source JSON file")} accept='.json,application/json' disabled={locked} onChange={event => void readFile(event.target.files?.[0])} /><textarea id={`${prefix}-document`} value={documentText} onChange={event => { readingFile.current++; setDocumentText(event.target.value); setFileName(''); setAccountIndex('0'); }} disabled={locked} rows={5} placeholder={t("Paste Claude CLI, Sub2API or exported credential JSON")} autoComplete='off' spellCheck={false} /><p className='field-help'>{fileName ? `${fileName} · ` : ''}{t("Converted to standard Auth JSON. Maximum 2 MB.")}</p></div>{accountOptions.length > 1 && <Select id={`${prefix}-account-index`} label={t("Account to import")} value={accountIndex} onChange={setAccountIndex} options={accountOptions} disabled={locked} />}</> : method === 'cookie' ? <PasswordField label='sessionKey' value={sessionKey} onChange={event => setSessionKey(event.target.value)} disabled={locked} autoComplete='off' /> : <div className='account-auth-token-fields'><PasswordField label={t("Access Token")} value={accessToken} onChange={event => setAccessToken(event.target.value)} disabled={locked} autoComplete='off' /><PasswordField label={t("Refresh Token")} value={refreshToken} onChange={event => setRefreshToken(event.target.value)} disabled={locked} autoComplete='off' /><div className='field'><label htmlFor={`${prefix}-token-expiry`}>{t("Token expiry")} <span className='muted small'>{t("Optional · RFC 3339")}</span></label><input id={`${prefix}-token-expiry`} value={tokenExpiry} onChange={event => setTokenExpiry(event.target.value)} disabled={locked} placeholder='2026-12-18T00:00:00Z' /></div></div>}
      </section>}
      {busy && <Loading label={translateMessage(busy)} />}
      {stage === 'mismatch' && <div className='notice error account-auth-mismatch' role='alert'><strong>{t("Account email does not match")}</strong><p>{t("Target:")} {accountEmail}</p>{authorizedEmail && <p>{t("Authorized:")} {authorizedEmail}</p>}<p>{generation ? t("Reauthorize with the target account.") : t("No update was sent. Reauthorize with the target account.")}</p></div>}
      {error && stage !== 'mismatch' && <><div className={`notice ${uncertain ? 'account-auth-uncertain' : 'error'}`} role='alert'>{translateMessage(error)}</div><AuthorizationErrorDetails error={errorDetails} /></>}
      {expired && <p className='field-error' role='alert'>{t("This authorization has expired. Generate a new authorization.")}</p>}
      {flow?.auth && !uncertain && stage !== 'mismatch' && <details className='authorize-credentials' open={credentialsOpen} onToggle={event => setCredentialsOpen(event.currentTarget.open)}><summary>{t("Complete missing credential fields")}<ChevronDown aria-hidden='true' /></summary><div className='field'><label htmlFor={`${prefix}-auth-json`}>{t("Standard Auth JSON")}</label><textarea id={`${prefix}-auth-json`} value={authText} onChange={event => { setAuthText(event.target.value); setCredentialError(''); }} disabled={locked} autoComplete='off' spellCheck={false} rows={8} /><p className='field-help'>{t("Complete missing fields only. The authorized email and proxy cannot be replaced.")}</p>{credentialError && <p className='field-error' role='alert'>{localizeAuthJSONMessage(credentialError)}</p>}</div></details>}
      {flow && !busy && <div className='authorize-small-actions'>{uncertain ? <button type='button' className='text-link' onClick={() => void checkResult()}>{generation ? t("Check saved generation result") : t("Check saved update result")}</button> : null}<button type='button' className='text-link' disabled={testing} onClick={restart}><RefreshCw aria-hidden='true' />{t("Start a new authorization")}</button></div>}
      <p className='authorize-announcement' role='status' aria-live='polite'>{translateMessage(announcement)}</p>
    </div>
    <div className='account-auth-dialog-footer'><button type='button' className='button secondary' disabled={!!busy || testing || reading} onClick={onCancel}>{t("Cancel")}</button>{!uncertain && stage !== 'mismatch' && stage !== 'failed' && <button type='button' className='button primary' disabled={locked || expired || (!canGenerate && !testing && !flow?.auth && hasCodeFlow && !code.trim())} onClick={() => void (canGenerate ? generateLink() : authorize())}>{busy ? <><LoaderCircle className='spin' aria-hidden='true' />{generation ? t("Generating…") : t("Updating…")}</> : canGenerate ? t("Generate authorization link") : generation ? t("Generate Auth JSON") : t("Authorize & update")}</button>}</div>
  </div>;
}
