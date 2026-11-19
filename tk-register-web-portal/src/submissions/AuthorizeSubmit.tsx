import { ArrowRight, Check, ChevronDown, CircleAlert, CircleCheck, Copy, Download, ExternalLink, Info, LoaderCircle, LockKeyhole, PlugZap, RefreshCw, Upload } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { APIError, errorMessage, request, type User } from '../api';
import { ErrorNotice, Loading, PasswordField } from '../ui';
import AuthorizationErrorDetails from '../AuthorizationErrorDetails';
import Select from './Select';
import type { SubmissionDetail, Vendor } from './types';
import { MAX_AUTH_BYTES, parseAuthJSON, paymentMethods, plans, registrationLocations, submissionParameters, validateDraft, type AccountDraft } from './validation';
import { authEditorValue, automaticDraft, completionPatch, emailDiffers, flowMetadata, importAccountOptions, parseImportDocument, proxyTestFailure, readFlowMetadata, readProxyCheck, submissionPassword, validateProxy, type CredentialFlow, type CredentialGoal, type CredentialMethod, type ProxyCheck } from './credential-flow';

type Stage = 'prepare' | 'link' | 'ready' | 'missing' | 'mismatch' | 'failed' | 'success' | 'exported';
type ProxyTest = { status: 'idle' | 'testing' } | { status: 'success'; check: ProxyCheck } | { status: 'error'; message: string; code?: string; error?: APIError | null };
type Props = { user: User; onDirty: (dirty: boolean) => void; onCreated: (id: string) => void; onHistory: () => void; onConfig: () => void };
const methodLabels: Record<CredentialMethod, string> = { oauth: 'OAuth', setup: 'Setup Token', json: 'Import JSON', cookie: 'Cookie', token: 'Existing token' };
const paymentLabels: Record<string, string> = { physical_credit_card: 'Physical credit card', virtual_credit_card: 'Virtual credit card', paypal: 'PayPal', apple_pay: 'Apple Pay', google_pay: 'Google Pay', third_party_payment: 'Third-party payment' };

export default function AuthorizeSubmit({ user, onDirty, onCreated, onHistory, onConfig }: Props) {
  const [draft, setDraft] = useState(automaticDraft);
  const initialDraft = useRef(draft);
  const [vendors, setVendors] = useState<Vendor[] | null>(null);
  const [vendorID, setVendorID] = useState('');
  const [protocolID, setProtocolID] = useState('');
  const [vendorError, setVendorError] = useState('');
  const [vendorAttempt, setVendorAttempt] = useState(0);
  const [method, setMethod] = useState<CredentialMethod>('oauth');
  const [goal, setGoal] = useState<CredentialGoal>('submit');
  const [setupKind, setSetupKind] = useState('full');
  const [proxy, setProxy] = useState('');
  const [proxyTest, setProxyTest] = useState<ProxyTest>({ status: 'idle' });
  const proxyTestRequest = useRef<{ controller: AbortController; revision: number } | null>(null);
  const proxyTestRevision = useRef(0);
  const [declaredEmail, setDeclaredEmail] = useState('');
  const [code, setCode] = useState('');
  const [document, setDocument] = useState('');
  const [accountIndex, setAccountIndex] = useState('0');
  const [sessionKey, setSessionKey] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [tokenExpiry, setTokenExpiry] = useState('');
  const [flow, setFlow] = useState<CredentialFlow | null>(null);
  const flowRef = useRef(flow);
  const [authText, setAuthText] = useState('');
  const [stage, setStage] = useState<Stage>('prepare');
  const [busy, setBusy] = useState('');
  const [error, setErrorText] = useState('');
  const [errorDetails, setErrorDetails] = useState<APIError | null>(null);
  const [credentialError, setCredentialError] = useState('');
  const [announcement, setAnnouncement] = useState('');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [credentialsOpen, setCredentialsOpen] = useState(false);
  const [result, setResult] = useState<SubmissionDetail | null>(null);
  const [settingsErrors, setSettingsErrors] = useState<Partial<Record<keyof AccountDraft, string>>>({});
  const [fileName, setFileName] = useState('');
  const active = useRef(false);
  const alive = useRef(true);
  const password = useRef('');
  const clientKey = useRef('');
  const confirmedEmail = useRef('');
  const upload = useRef<HTMLInputElement>(null);
  const otherMethods = useRef<HTMLDetailsElement>(null);
  const storageKey = `tk-register:credential-flow:${user.id}`;
  const vendor = vendors?.find(item => item.id === vendorID);
  const protocols = vendor?.protocols.filter(item => item.operation === 'push') || [];
  const vendorReady = Boolean(vendor?.enabled && vendor.configured && protocolID);
  const finished = stage === 'success' || stage === 'exported';
  const proxyTesting = proxyTest.status === 'testing';
  const proxyNeedsAuthorization = Boolean(flow && proxyTest.status === 'error' && ['PROXY_CHANGED', 'CREDENTIAL_FLOW_NOT_FOUND'].includes(proxyTest.code || ''));
  const locked = Boolean(busy) || proxyTesting || proxyNeedsAuthorization || finished;
  const hasCredentials = Boolean(flow?.auth);
  const progressStep = finished ? 3 : hasCredentials ? 2 : flow ? 1 : 0;
  const expired = Boolean(flow && Date.parse(flow.expires_at) <= Date.now());
  let accountOptions: { value: string; label: string }[] = [];
  try { accountOptions = importAccountOptions(document); } catch { /* Validated when importing. */ }

  useEffect(() => { alive.current = true; return () => { alive.current = false; proxyTestRevision.current++; proxyTestRequest.current?.controller.abort(); proxyTestRequest.current = null; }; }, []);
  useEffect(() => {
    const controller = new AbortController();
    setVendorError('');
    request<{ vendors: Vendor[] }>('/vendors', { signal: controller.signal }).then(({ vendors: list }) => {
      setVendors(list);
      setVendorID(current => current || list[0]?.id || '');
      setProtocolID(current => current || list[0]?.protocols.find(item => item.operation === 'push')?.version_id || '');
    }).catch(cause => { if (!controller.signal.aborted) setVendorError(errorMessage(cause)); });
    return () => controller.abort();
  }, [vendorAttempt]);
  useEffect(() => {
    let saved;
    try { saved = readFlowMetadata(sessionStorage.getItem(storageKey)); } catch { return; }
    if (!saved) return;
    const controller = new AbortController();
    setBusy('Restoring authorization…');
    active.current = true;
    request<CredentialFlow>(`/credential-flows/${encodeURIComponent(saved.flow_id)}`, { signal: controller.signal }).then(restored => {
      if (controller.signal.aborted) return;
      setMethod(saved.method); setGoal(saved.goal); setSetupKind(saved.setup_kind);
      setVendorID(saved.vendor_id); setProtocolID(saved.protocol_version_id);
      setDraft(current => ({ ...current, ...saved.preferences }));
      clientKey.current = saved.client_request_key || crypto.randomUUID();
      acceptFlow(restored);
      setStage(restored.auth ? 'ready' : 'link');
      setAnnouncement('Authorization restored. Credentials stay in memory. Continue when you are ready.');
    }).catch(cause => { if (!controller.signal.aborted) { showError(cause); try { sessionStorage.removeItem(storageKey); } catch { /* Storage is optional. */ } } })
      .finally(() => { if (!controller.signal.aborted) { active.current = false; setBusy(''); } });
    return () => controller.abort();
  }, [storageKey]);
  useEffect(() => {
    if (!flow || finished) { if (finished) try { sessionStorage.removeItem(storageKey); } catch { /* Storage is optional. */ } return; }
    try { sessionStorage.setItem(storageKey, JSON.stringify(flowMetadata(flow, { method, goal, setup_kind: setupKind, vendor_id: vendorID, protocol_version_id: protocolID, client_request_key: clientKey.current }, draft))); } catch { /* All secrets and the active draft still remain in memory. */ }
  }, [flow, finished, method, goal, setupKind, vendorID, protocolID, draft, storageKey, busy]);
  const dirty = !finished && Boolean(proxy || declaredEmail || code || document || sessionKey || accessToken || refreshToken || flow || JSON.stringify(draft) !== JSON.stringify(initialDraft.current));
  useEffect(() => { onDirty(dirty || Boolean(busy) || proxyTesting); return () => onDirty(false); }, [dirty, busy, proxyTesting, onDirty]);

  function setError(message: string) { setErrorText(message); setErrorDetails(null); }
  function showError(cause: unknown, message = errorMessage(cause)) { setErrorText(message); setErrorDetails(cause instanceof APIError ? cause : null); }
  function invalidateProxyTest() {
    proxyTestRevision.current++;
    proxyTestRequest.current?.controller.abort();
    proxyTestRequest.current = null;
    setProxyTest({ status: 'idle' });
  }
  function updateProxy(value: string) {
    invalidateProxyTest(); setProxy(value); setError('');
  }
  async function testProxy() {
    if (active.current || proxyTestRequest.current || finished) return;
    const currentFlow = flowRef.current;
    const invalid = currentFlow ? '' : validateProxy(proxy);
    if (invalid) { setProxyTest({ status: 'error', message: invalid }); return; }
    const pending = { controller: new AbortController(), revision: ++proxyTestRevision.current };
    proxyTestRequest.current = pending;
    setProxyTest({ status: 'testing' });
    try {
      const value = await request<{ proxy_check: unknown }>('/credential-flows/proxy-test', { method: 'POST', body: currentFlow ? { flow_id: currentFlow.flow_id } : { proxy_url: proxy.trim() }, signal: pending.controller.signal });
      if (!alive.current || pending.controller.signal.aborted || pending.revision !== proxyTestRevision.current) return;
      setProxyTest({ status: 'success', check: readProxyCheck(value?.proxy_check) });
    } catch (cause) {
      if (!alive.current || pending.controller.signal.aborted || pending.revision !== proxyTestRevision.current) return;
      setProxyTest({ status: 'error', ...proxyTestFailure(cause), error: cause instanceof APIError ? cause : null });
    } finally { if (proxyTestRequest.current === pending) proxyTestRequest.current = null; }
  }

  function acceptFlow(value: CredentialFlow) {
    if (!value.flow_id || !Number.isFinite(Date.parse(value.expires_at))) throw new Error('The authorization response could not be confirmed.');
    invalidateProxyTest();
    flowRef.current = value; setFlow(value);
    if (value.auth) setAuthText(authEditorValue(value.auth));
  }
  function ensurePassword() {
    const value = draft.password || password.current || submissionPassword();
    password.current = value;
    if (!draft.password) setDraft(current => ({ ...current, password: value }));
    return value;
  }
  function update(key: keyof AccountDraft, value: string) {
    if (key === 'password') password.current = value;
    setDraft(current => ({ ...current, [key]: value }));
    setSettingsErrors(current => ({ ...current, [key]: undefined }));
    setError('');
  }
  function checkSetup(requireProxy = true) {
    const invalid = requireProxy ? validateProxy(proxy) : '';
    if (invalid) throw new Error(invalid);
    if (declaredEmail.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(declaredEmail.trim())) throw new Error('Enter a valid account email, or leave it blank.');
    if (goal === 'submit' && !vendorReady) throw new Error('Select a configured platform and submission protocol.');
    ensurePassword();
  }
  async function perform(action: () => Promise<void>) {
    if (active.current || proxyTestRequest.current || proxyNeedsAuthorization || finished) return;
    active.current = true; setError(''); setAnnouncement('');
    try { await action(); }
    catch (cause) { if (alive.current) { showError(cause); if (flowRef.current?.auth) setStage('failed'); } }
    finally { active.current = false; if (alive.current) setBusy(''); }
  }
  async function generateLink() {
    await perform(async () => {
      checkSetup(); setBusy('Checking proxy and generating authorization link…');
      const value = await request<CredentialFlow>('/credential-flows/start', { method: 'POST', body: { proxy_url: proxy.trim(), mode: method, setup_kind: setupKind } });
      if (!alive.current) return;
      clientKey.current = crypto.randomUUID(); acceptFlow(value); setCode(''); setStage('link');
    });
  }
  async function complete(value: CredentialFlow, text: string, confirmed = false, retry = false) {
    if (!value.auth) throw new Error('Authorize or import credentials first.');
    if (Date.parse(value.expires_at) <= Date.now()) throw new Error('This authorization has expired. Generate a new link or import the credentials again.');
    let auth: Record<string, unknown>;
    let patch: Record<string, unknown>;
    try { ({ auth, patch } = completionPatch(value.auth, text, goal === 'submit')); }
    catch (cause) { setCredentialError(errorMessage(cause)); setCredentialsOpen(true); setStage('missing'); return; }
    setCredentialError('');
    const submissionDraft = { ...draft, account: String(auth.email), password: ensurePassword(), auth_json: JSON.stringify(auth) };
    const invalid = goal === 'submit' ? validateDraft(submissionDraft) : {};
    delete invalid.auth_json;
    if (Object.keys(invalid).length) { setSettingsErrors(invalid); setSettingsOpen(true); setStage('ready'); setError('Complete the highlighted account settings.'); return; }
    if (emailDiffers(declaredEmail, auth.email) && !confirmed && confirmedEmail.current !== String(auth.email).trim().toLowerCase()) { setStage('mismatch'); return; }
    if (confirmed) confirmedEmail.current = String(auth.email).trim().toLowerCase();
    if (Object.keys(patch).length) {
      setBusy('Saving missing credential fields…');
      value = await request<CredentialFlow>(`/credential-flows/${encodeURIComponent(value.flow_id)}/draft`, { method: 'POST', body: { auth_patch: patch } });
      if (!alive.current) return;
      acceptFlow(value); auth = goal === 'submit' ? parseAuthJSON(JSON.stringify(value.auth)) : value.auth || {};
      submissionDraft.account = String(auth.email); submissionDraft.auth_json = JSON.stringify(auth);
    }
    if (goal === 'export') {
      setBusy('Preparing Auth JSON…');
      const exported = await request<CredentialFlow>('/credential-flows/export', { method: 'POST', body: { flow_id: value.flow_id } });
      if (!alive.current) return;
      acceptFlow(exported); setCredentialsOpen(true); setStage('exported'); return;
    }
    if (!vendorReady) throw new Error('Select a configured platform and submission protocol.');
    if (retry && result && ['failed', 'unknown'].includes(result.submission.status)) clientKey.current = crypto.randomUUID();
    if (!clientKey.current) clientKey.current = crypto.randomUUID();
    setBusy('Submitting account…');
    const submitted = await request<SubmissionDetail>('/credential-flows/submit', { method: 'POST', body: { flow_id: value.flow_id, vendor_id: vendorID, protocol_version_id: protocolID, client_request_key: clientKey.current, parameters: submissionParameters(submissionDraft) } });
    if (!alive.current) return;
    if (typeof submitted?.submission?.id !== 'string' || !submitted.submission.id) throw new Error('The submission result could not be confirmed. Retry to recover this attempt, or check Submission history.');
    setResult(submitted);
    if (submitted.submission.status === 'failed' || submitted.submission.status === 'unknown') { setStage('failed'); setError('The platform could not confirm this submission. Your credentials and settings are retained.'); }
    else { setStage('success'); setAnnouncement('Account submitted. You can view the submission or process another account.'); }
  }
  async function authorize() {
    await perform(async () => {
      let value = flowRef.current;
      if (value?.auth) { await complete(value, authText, false, stage === 'failed'); return; }
      checkSetup(!value);
      if (method === 'oauth' || method === 'setup') {
        if (!value || !code.trim()) throw new Error('Paste the authorization code or full callback URL.');
        setBusy('Exchanging authorization and reading account information…');
        value = await request<CredentialFlow>('/credential-flows/exchange', { method: 'POST', body: { flow_id: value.flow_id, code: code.trim() } });
      } else {
        const body: Record<string, unknown> = { proxy_url: proxy.trim(), kind: method };
        if (method === 'json') { body.document = parseImportDocument(document); if (accountOptions.length > 1) body.account_index = Number(accountIndex); }
        if (method === 'cookie') { if (!sessionKey.trim()) throw new Error('Enter the sessionKey.'); body.session_key = sessionKey.trim(); }
        if (method === 'token') { if (!accessToken.trim()) throw new Error('Enter the access token.'); body.access_token = accessToken.trim(); body.refresh_token = refreshToken.trim(); if (tokenExpiry.trim()) body.expires_at = tokenExpiry.trim(); }
        setBusy('Checking proxy and importing credentials…');
        value = await request<CredentialFlow>('/credential-flows/import', { method: 'POST', body });
        clientKey.current = crypto.randomUUID();
      }
      if (!alive.current) return;
      acceptFlow(value); await complete(value, authEditorValue(value.auth || {}));
    });
  }
  function resetAuthorization() {
    invalidateProxyTest();
    flowRef.current = null; setFlow(null); setCode(''); setAuthText(''); setResult(null); setError(''); setCredentialError(''); setStage('prepare'); setCredentialsOpen(false); clientKey.current = ''; confirmedEmail.current = '';
    try { sessionStorage.removeItem(storageKey); } catch { /* Storage is optional. */ }
  }
  function nextAccount() {
    resetAuthorization(); setProxy(''); setDeclaredEmail(''); setDocument(''); setSessionKey(''); setAccessToken(''); setRefreshToken(''); setTokenExpiry(''); setFileName(''); setAccountIndex('0'); password.current = '';
    setDraft(current => ({ ...current, account: '', password: '', auth_json: '', remark: '' })); setAnnouncement('Ready for another account. Your plan, dates and other account settings are retained.');
  }
  function startNewAuthorization() {
    if (!window.confirm('Start a new authorization and discard these credentials? Your account settings and submission password will be kept.')) return;
    resetAuthorization(); setDocument(''); setSessionKey(''); setAccessToken(''); setRefreshToken(''); setTokenExpiry(''); setFileName(''); setAccountIndex('0');
  }
  function changeMethod(value: CredentialMethod) {
    if (locked) return;
    otherMethods.current?.removeAttribute('open');
    if (value === method) return;
    if (flow && !window.confirm('Start a different authorization method? Your account settings will be kept.')) return;
    resetAuthorization(); setMethod(value);
  }
  async function readFile(file?: File) {
    if (upload.current) upload.current.value = '';
    if (!file) return;
    if (file.size > MAX_AUTH_BYTES) { setError('Choose a JSON file no larger than 2 MB.'); return; }
    try { const text = await file.text(); parseImportDocument(text); if (alive.current) { setDocument(text); setAccountIndex('0'); setFileName(file.name); setError(''); } }
    catch (cause) { showError(cause); }
  }
  async function exportJSON(download: boolean) {
    if (!flow || busy || proxyTestRequest.current || proxyNeedsAuthorization) return;
    setError('');
    try {
      const value = await request<CredentialFlow>('/credential-flows/export', { method: 'POST', body: { flow_id: flow.flow_id } });
      if (!value.auth) throw new Error('No credentials are available for export.');
      const text = JSON.stringify(value.auth, null, 2);
      if (download) {
        const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }));
        const link = window.document.createElement('a'); link.href = url; link.download = 'auth.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
      } else await navigator.clipboard.writeText(text);
      setAnnouncement(download ? 'Auth JSON downloaded.' : 'Auth JSON copied.');
    } catch (cause) { showError(cause); }
  }
  const attrs = (key: keyof AccountDraft) => ({ id: `auto-${key}`, name: key, value: draft[key], disabled: locked, 'aria-invalid': Boolean(settingsErrors[key]), onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => update(key, event.target.value) });
  const fieldError = (key: keyof AccountDraft) => settingsErrors[key] ? <p className='field-error'>{settingsErrors[key]}</p> : null;
  const select = (key: keyof AccountDraft, label: string, options: readonly { value: string; label: string }[]) => <div className='field'><Select id={`auto-${key}`} label={label} value={draft[key]} onChange={value => update(key, value)} options={options} disabled={locked} invalid={Boolean(settingsErrors[key])} />{fieldError(key)}</div>;
  const exitIP = flow?.proxy_check?.ip || flow?.proxy_check?.exit_ip;

  return <div className='authorize-submit'>
    <div className='authorize-toolbar'>
      <ol className='authorize-progress' aria-label='Account submission progress'>{['Check proxy', 'Authorize', goal === 'submit' ? 'Submit account' : 'Generate JSON', 'Result'].map((label, index) => <li key={label} className={index < progressStep ? 'done' : index === progressStep ? 'active' : ''} aria-current={index === progressStep ? 'step' : undefined}><span>{index < progressStep || (finished && index === 3) ? <Check aria-hidden='true' /> : index + 1}</span>{label}</li>)}</ol>
      <Select id='auto-goal' label='After authorization' hiddenLabel value={goal} options={[{ value: 'submit', label: 'Auto submit' }, { value: 'export', label: 'Generate JSON only' }]} disabled={locked || hasCredentials} onChange={value => setGoal(value as CredentialGoal)} />
    </div>
    <div className='authorize-panel panel' aria-busy={Boolean(busy)}>
      <section className='authorize-section'>
        <div className='section-heading'><h2>Account setup</h2><span className='muted small'>{vendor?.name || 'Platform'}</span></div>
        <div className='authorize-account-fields'>
          <div className='authorize-proxy-field'>
            <div className='authorize-proxy-row'><PasswordField label='Account proxy' value={proxy} onChange={event => updateProxy(event.target.value)} disabled={Boolean(busy) || finished || Boolean(flow)} placeholder={flow && !proxy ? 'Proxy retained securely by the authorization flow' : 'http://user:password@host:port'} autoComplete='off' spellCheck={false} aria-describedby='auto-proxy-status' /><button type='button' className='button secondary authorize-proxy-test' disabled={Boolean(busy) || proxyTesting || finished} aria-busy={proxyTesting} onClick={() => void testProxy()}>{proxyTesting ? <LoaderCircle className='spin' aria-hidden='true' /> : <PlugZap aria-hidden='true' />}<span>{proxyTesting ? 'Testing…' : 'Test connection'}</span></button></div>
            <div id='auto-proxy-status' className='authorize-proxy-status' data-kind={proxyTest.status === 'success' || (proxyTest.status === 'idle' && flow?.proxy_check?.ok) ? 'success' : proxyTest.status === 'error' ? 'error' : 'idle'} role='status' aria-live='polite' aria-atomic='true'>
              {proxyTest.status === 'testing' ? <span>Checking proxy connection…</span> : proxyTest.status === 'success' ? <><CircleCheck aria-hidden='true' /><span>Connected · Exit IP {proxyTest.check.ip} · {Math.round(proxyTest.check.elapsed_ms)} ms</span></> : proxyTest.status === 'error' ? <><CircleAlert aria-hidden='true' /><span>{proxyTest.message}</span>{proxyNeedsAuthorization && <button type='button' className='text-link' onClick={() => hasCredentials ? startNewAuthorization() : resetAuthorization()}>Start a new authorization</button>}</> : flow?.proxy_check?.ok ? <><Check aria-hidden='true' /><span>Exit IP bound{exitIP ? ` · ${exitIP}` : ''}</span></> : <span>Check the proxy before authorization.</span>}
            </div>
            {proxyTest.status === 'error' && <AuthorizationErrorDetails error={proxyTest.error || null} />}
          </div>
          <div className='field'><label htmlFor='auto-declared-email'>Account email <span className='muted small'>Optional</span></label><input id='auto-declared-email' type='email' value={declaredEmail} onChange={event => setDeclaredEmail(event.target.value)} disabled={Boolean(busy) || finished || hasCredentials} placeholder='Read from authorization if left blank' autoComplete='off' /><p className='field-help'>We’ll confirm if the authorized email differs.</p></div>
        </div>
        <details className='authorize-settings' open={settingsOpen} onToggle={event => setSettingsOpen(event.currentTarget.open)}>
          <summary><div><span className='authorize-settings-label'>Account settings</span><div className='authorize-default-summary'><span>{draft.plan === plans[0] ? 'Max 20x' : draft.plan === plans[1] ? 'Max 5x' : 'Pro'} · {draft.register_location} · {paymentLabels[draft.payment_method] || 'No payment method'} · {draft.register_device} · {draft.balance}% · {draft.tags}</span><span>{draft.plan_start_time} → {draft.plan_end_time} · Asia/Shanghai</span></div></div><span className='authorize-settings-edit'>Edit<ChevronDown aria-hidden='true' /></span></summary>
          <div className='authorize-settings-body'>
            {vendors && <div className='form-grid'><Select id='auto-vendor' label='Vendor' value={vendorID} options={vendors.map(item => ({ value: item.id, label: item.name }))} disabled={locked || hasCredentials} onChange={value => { setVendorID(value); setProtocolID(vendors.find(item => item.id === value)?.protocols.find(item => item.operation === 'push')?.version_id || ''); }} /><Select id='auto-protocol' label='Protocol' value={protocolID} options={protocols.map(item => ({ value: item.version_id, label: `${item.name} · v${item.version}` }))} disabled={locked || hasCredentials} onChange={setProtocolID} /></div>}
            <div><PasswordField {...attrs('password')} label='Submission password' autoComplete='new-password' placeholder='Leave blank to generate a 24-character password' /><p className='field-help'>Used in the submission. Your Claude login password is not changed.</p>{fieldError('password')}</div>
            <div className='form-grid'>{select('plan', 'Plan', plans.map(value => ({ value, label: value === plans[0] ? 'Claude Max 20x' : value === plans[1] ? 'Claude Max 5x' : 'Claude Pro' })))}{select('register_location', 'Registration location', registrationLocations)}</div>
            <div className='form-grid date-grid'>{([['register_time', 'Registration date'], ['plan_start_time', 'Plan start date'], ['plan_end_time', 'Plan end date']] as const).map(([key, label]) => <div className='field' key={key}><label htmlFor={`auto-${key}`}>{label}</label><input {...attrs(key)} type='date' />{fieldError(key)}</div>)}</div>
            <h3>Additional details</h3>
            <div className='form-grid'>{select('payment_method', 'Payment method', [{ value: '', label: 'Not specified' }, ...paymentMethods.map(value => ({ value, label: paymentLabels[value] }))])}{select('register_device', 'Registration device', ['', 'Mac', 'Windows', 'Linux'].map(value => ({ value, label: value || 'Not specified' })))}<div className='field'><label htmlFor='auto-balance'>Remaining balance (%)</label><input {...attrs('balance')} inputMode='decimal' />{fieldError('balance')}</div><div className='field'><label htmlFor='auto-recharge_time'>Recharge date</label><input {...attrs('recharge_time')} type='date' />{fieldError('recharge_time')}</div>{select('warranty_period', 'Warranty period', ['', '1', '7', '30'].map(value => ({ value, label: value ? `${value} ${value === '1' ? 'day' : 'days'}` : 'Not specified' })))}{select('tags', 'Tag', ['', '速刷', '慢蒸'].map(value => ({ value, label: value || 'Not specified' })))}<div className='field full-width'><label htmlFor='auto-remark'>Remark</label><textarea {...attrs('remark')} rows={2} placeholder='Optional notes' /></div></div>
          </div>
        </details>
      </section>
      {!finished && !hasCredentials && <section className='authorize-section'>
        <div className='section-heading'><div><h2>Authorization</h2><p className='field-help'>{goal === 'submit' ? 'After authorization, your account is submitted automatically.' : 'Generate credentials without submitting the account.'}</p></div></div>
        <div className='authorize-method-row'><div className='authorize-method-tabs' role='tablist' aria-label='Credential source'>{(['oauth', 'setup', 'json'] as const).map(value => <button type='button' role='tab' key={value} aria-selected={method === value} disabled={locked} onClick={() => changeMethod(value)}>{methodLabels[value]}</button>)}</div><details ref={otherMethods} className='authorize-other-methods'><summary>{method === 'cookie' || method === 'token' ? methodLabels[method] : 'Other methods'}<ChevronDown aria-hidden='true' /></summary><div>{(['cookie', 'token'] as const).map(value => <button type='button' className='text-link' key={value} disabled={locked} onClick={() => changeMethod(value)}>{methodLabels[value]}</button>)}</div></details></div>
        {method === 'setup' && <Select id='auto-setup-kind' label='Authorization type' value={setupKind} options={[{ value: 'full', label: 'Full access · Recommended' }, { value: 'official', label: 'Inference only · Long-lived token' }]} disabled={locked || Boolean(flow)} onChange={setSetupKind} />}
        {(method === 'oauth' || method === 'setup') ? <>
          {flow?.auth_url && <><div className='field'><label>Authorization link</label><div className='authorize-link-row'><div className='authorize-link-display'>{flow.auth_url}</div><button type='button' className='button secondary compact' disabled={locked} onClick={() => void navigator.clipboard.writeText(flow.auth_url!).then(() => setAnnouncement('Authorization link copied.')).catch(() => setError('Unable to copy. Select and copy the authorization link.'))}><Copy aria-hidden='true' />Copy</button></div><p className='field-help'>Expires {new Date(flow.expires_at).toLocaleTimeString()}.</p></div><div className='field'><label htmlFor='auto-code'>Authorization code or callback URL</label><textarea id='auto-code' className='authorize-mono' value={code} onChange={event => setCode(event.target.value)} disabled={locked} rows={3} placeholder='Paste code#state or the full callback URL' spellCheck={false} autoComplete='off' /></div></>}
          <div className='authorize-info'><Info aria-hidden='true' /><span>Use the same proxy in your browser when opening the Claude authorization link.</span></div>
          <div className='authorize-actions'>{flow ? <button className='text-link' type='button' disabled={locked} onClick={() => { resetAuthorization(); }}>Generate a new link</button> : <span className='field-help'>Defaults are ready. You can edit them above.</span>}<button type='button' className='button primary' disabled={locked || (goal === 'submit' && !vendorReady)} onClick={() => void (flow ? authorize() : generateLink())}>{flow ? goal === 'submit' ? 'Authorize & submit' : 'Generate Auth JSON' : 'Generate authorization link'}<ArrowRight aria-hidden='true' /></button></div>
        </> : <>
          {method === 'json' ? <><div className='field'><div className='field-heading'><label htmlFor='auto-document'>Source JSON</label><button type='button' className='text-link' disabled={locked} onClick={() => upload.current?.click()}><Upload aria-hidden='true' />Upload file</button></div><input ref={upload} type='file' className='sr-only' aria-label='Upload source JSON file' accept='.json,application/json' onChange={event => void readFile(event.target.files?.[0])} /><textarea id='auto-document' className='authorize-mono' value={document} disabled={locked} onChange={event => { setDocument(event.target.value); setAccountIndex('0'); setFileName(''); }} rows={5} placeholder='Paste Claude CLI, Sub2API or exported credential JSON' spellCheck={false} autoComplete='off' /><p className='field-help'>{fileName ? `${fileName} · ` : ''}Converted to standard Auth JSON. Maximum 2 MB.</p></div>{accountOptions.length > 1 && <Select id='auto-import-account' label='Account to import' value={accountIndex} onChange={setAccountIndex} options={accountOptions} disabled={locked} />}</> : method === 'cookie' ? <PasswordField label='sessionKey' value={sessionKey} onChange={event => setSessionKey(event.target.value)} disabled={locked} autoComplete='off' /> : <div className='form-grid'><PasswordField label='Access Token' value={accessToken} onChange={event => setAccessToken(event.target.value)} disabled={locked} autoComplete='off' /><PasswordField label='Refresh Token' value={refreshToken} onChange={event => setRefreshToken(event.target.value)} disabled={locked} autoComplete='off' /><div className='field full-width'><label htmlFor='auto-token-expiry'>Token expiry <span className='muted small'>Optional · RFC 3339</span></label><input id='auto-token-expiry' value={tokenExpiry} onChange={event => setTokenExpiry(event.target.value)} disabled={locked} placeholder='2026-12-18T00:00:00Z' /></div></div>}
          <div className='authorize-actions'><span className='field-help'>Proxy is checked before processing.</span><button type='button' className='button primary' disabled={locked || (goal === 'submit' && !vendorReady)} onClick={() => void authorize()}>{goal === 'export' ? 'Generate Auth JSON' : method === 'json' ? 'Import & submit' : 'Authorize & submit'}<ArrowRight aria-hidden='true' /></button></div>
        </>}
      </section>}
      {busy && <section className='authorize-result' aria-label='Processing account'><Loading label={busy} /><p className='field-help'>Please keep this page open.</p></section>}
      {!busy && hasCredentials && <section className='authorize-result' aria-label={stage === 'mismatch' ? 'Email confirmation' : stage === 'missing' ? 'Missing credential fields' : 'Account submission result'}>
        {stage === 'success' ? <><div className='authorize-result-heading'><span className='authorize-result-icon'><Check aria-hidden='true' /></span><div><h2>Account submitted</h2><p className='field-help'>Saved to Submission history. Status and usage sync is queued.</p></div></div><div className='authorize-result-grid'><div><span>Account</span><strong>{result?.submission.account}</strong></div><div><span>Account ID</span><strong>{result?.submission.platform_account_id || result?.submission.remote_id || 'Pending'}</strong></div><div><span>Submission</span><strong>#{result?.submission.id} · {result?.submission.status}</strong></div></div><div className='authorize-actions'><button type='button' className='text-link' onClick={() => { onDirty(false); onCreated(result!.submission.id); }}>View submission<ExternalLink aria-hidden='true' /></button><button type='button' className='button primary' onClick={nextAccount}>Process next account<ArrowRight aria-hidden='true' /></button></div></> : stage === 'exported' ? <><h2>Auth JSON is ready</h2><p className='field-help'>{String(flow?.auth?.email || '')} · No account was submitted.</p><div className='authorize-actions'><div className='authorize-small-actions'><button type='button' className='button primary' onClick={() => void exportJSON(true)}><Download aria-hidden='true' />Download auth.json</button><button type='button' className='button secondary' onClick={() => void exportJSON(false)}><Copy aria-hidden='true' />Copy JSON</button></div><button type='button' className='text-link' onClick={nextAccount}>Process another account<ArrowRight aria-hidden='true' /></button></div></> : stage === 'mismatch' ? <><h2>Confirm the authorized account</h2><p className='field-help'>The authorized email is different from the one you entered.</p><div className='authorize-result-grid'><div><span>Entered email</span><strong>{declaredEmail}</strong></div><div><span>Authorized email</span><strong>{(() => { try { return String(JSON.parse(authText).email || ''); } catch { return String(flow?.auth?.email || ''); } })()}</strong></div></div><div className='authorize-actions'><button type='button' className='text-link' onClick={resetAuthorization}>Start over</button><button type='button' className='button primary' onClick={() => void perform(() => complete(flow!, authText, true))}>{goal === 'submit' ? 'Submit authorized account' : 'Generate JSON for this account'}<ArrowRight aria-hidden='true' /></button></div></> : <><h2>{stage === 'missing' ? 'Complete the missing credential fields' : stage === 'failed' ? goal === 'submit' ? 'Submission could not be completed' : 'JSON generation could not be completed' : 'Authorization is ready'}</h2><p className='field-help'>{stage === 'missing' ? 'Your credentials are retained. Complete the standard Auth JSON below to continue.' : 'Your credentials and account settings are retained.'}</p><div className='authorize-actions'><button type='button' className='text-link' onClick={onHistory}>View submission history</button><button type='button' className='button primary' disabled={expired} onClick={() => void authorize()}>{stage === 'failed' ? <><RefreshCw aria-hidden='true' />{goal === 'submit' ? 'Retry submission' : 'Retry JSON generation'}</> : goal === 'submit' ? 'Submit account' : 'Generate Auth JSON'}</button></div></>}
        {expired && !finished && <div><p className='field-error'>This authorization has expired. Start over to authorize again.</p><button type='button' className='text-link' onClick={resetAuthorization}>Start over</button></div>}
        {!expired && !finished && !proxyNeedsAuthorization && stage !== 'mismatch' && <button type='button' className='text-link' onClick={startNewAuthorization}>Start a new authorization</button>}
        {flow?.notes?.length ? <ul className='authorize-notes'>{flow.notes.map((note, index) => <li key={index}>{note}</li>)}</ul> : null}
        <details className='authorize-credentials' open={credentialsOpen} onToggle={event => setCredentialsOpen(event.currentTarget.open)}><summary>Credential details &amp; JSON export<ChevronDown aria-hidden='true' /></summary><div className='field'><label htmlFor='auto-auth-json'>Standard Auth JSON</label><textarea id='auto-auth-json' className='authorize-mono' value={authText} readOnly={finished} disabled={Boolean(busy)} onChange={event => { setAuthText(event.target.value); setCredentialError(''); }} spellCheck={false} autoComplete='off' rows={10} /><p className='field-help'>Complete missing fields only. Existing authorized credentials and proxy verification are protected. Credentials are kept in memory.</p>{credentialError && <p className='field-error' role='alert'>{credentialError}</p>}<div className='authorize-small-actions'><button type='button' className='button secondary compact' disabled={Boolean(busy) || proxyTesting || proxyNeedsAuthorization} onClick={() => void exportJSON(false)}><Copy aria-hidden='true' />Copy JSON</button><button type='button' className='button secondary compact' disabled={Boolean(busy) || proxyTesting || proxyNeedsAuthorization} onClick={() => void exportJSON(true)}><Download aria-hidden='true' />Download auth.json</button></div></div></details>
      </section>}
    </div>
    <ErrorNotice>{error || vendorError}</ErrorNotice>
    {error && <AuthorizationErrorDetails error={errorDetails} />}
    {vendorError && <button type='button' className='text-link' onClick={() => setVendorAttempt(value => value + 1)}>Reload platforms</button>}
    {vendors && !vendorReady && goal === 'submit' && <div className='notice setup-notice'><LockKeyhole aria-hidden='true' /><div><strong>This platform is not ready for submissions.</strong><p>{user.role === 'admin' ? 'Configure the platform credential and enable it in Config.' : 'Your administrator needs to finish setting up this platform.'}</p>{user.role === 'admin' && <button type='button' className='text-link' onClick={onConfig}>Open vendor configuration<ArrowRight aria-hidden='true' /></button>}</div></div>}
    {!vendors && !vendorError && <Loading label='Loading platforms…' />}
    <p className='authorize-announcement' role='status' aria-live='polite'>{announcement}</p>
  </div>;
}
