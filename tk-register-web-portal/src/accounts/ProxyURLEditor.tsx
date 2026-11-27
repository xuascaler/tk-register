import { Check, CircleAlert, FilePenLine, LoaderCircle, LockKeyhole, PlugZap, RefreshCw, UserRound, X } from 'lucide-react';
import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { APIError, request } from '../api';
import { t, translateMessage, useLanguage } from '../i18n';
import { Loading, PasswordField } from '../ui';
import { confirmedAuthJSONResult, type AuthJSONUpdateResult } from './auth-json';
import { formatTime, statusLabel, statusTone } from './helpers';
import { maskedProxyURL, proxyProblem, type ProxyEligibility, type ProxyProblem, type ProxyTestResult } from './proxy-url';
import type { SyncedAccount } from './types';
import './proxy-url.css';

export function ProxyURLEditor({ account, endpoint, onClose, onSaved, onAuthJSON }: {
  account: SyncedAccount; endpoint: string; onClose: () => void; onSaved: (result: AuthJSONUpdateResult) => void; onAuthJSON: () => void;
}) {
  useLanguage();
  const dialog = useRef<HTMLDialogElement>(null);
  const alive = useRef(false), active = useRef(false);
  const pending = useRef<AbortController | null>(null);
  const [eligibility, setEligibility] = useState<ProxyEligibility | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [checking, setChecking] = useState(true);
  const [busy, setBusy] = useState<'test' | 'save' | ''>('');
  const [proxy, setProxy] = useState('');
  const [proof, setProof] = useState<(ProxyTestResult & { value: string }) | null>(null);
  const [problem, setProblem] = useState<ProxyProblem | null>(null);
  const [saved, setSaved] = useState<AuthJSONUpdateResult | null>(null);
  const [changed, setChanged] = useState(false);
  const headingID = useId(), descriptionID = useId(), feedbackID = useId();
  const path = `${endpoint}/${encodeURIComponent(account.id)}/proxy-url`;

  useEffect(() => {
    alive.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const element = dialog.current, overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden'; element?.showModal();
    return () => { alive.current = false; pending.current?.abort(); element?.close(); document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setChecking(true); setEligibility(null); setProblem(null); setProxy(''); setProof(null); setChanged(false);
    request<ProxyEligibility>(path, { signal: controller.signal }).then(value => {
      if (controller.signal.aborted) return;
      if (!value || typeof value.editable !== 'boolean' || !/^[0-9a-f]{64}$/.test(value.revision) || typeof value.proxy_url_masked !== 'string') throw new Error('Could not check whether this account can be updated. Please try again.');
      setEligibility(value);
    }).catch(cause => { if (!controller.signal.aborted) setProblem(proxyProblem(cause)); })
      .finally(() => { if (!controller.signal.aborted) setChecking(false); });
    return () => controller.abort();
  }, [path, attempt]);
  useEffect(() => {
    if (busy !== 'save') return;
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', prevent);
    return () => window.removeEventListener('beforeunload', prevent);
  }, [busy]);

  function close() {
    if (active.current) return;
    if (saved) { onSaved(saved); return; }
    onClose();
  }
  async function test() {
    if (active.current || !eligibility?.editable || !proxy.trim()) return;
    active.current = true; setBusy('test'); setProblem(null); setProof(null);
    const controller = new AbortController(); pending.current = controller;
    const value = proxy.trim(); setProxy(value);
    try {
      const result = await request<ProxyTestResult>(`${path}-test`, { method: 'POST', body: { proxy_url: value, revision: eligibility.revision }, signal: controller.signal });
      if (!result?.proxy_check?.ok || !result.test_token || !result.proxy_check.ip) throw new Error('The proxy connection could not be verified. Please test again.');
      if (alive.current) { setProof({ ...result, value }); setChanged(false); }
    } catch (cause) { if (alive.current && !controller.signal.aborted) setProblem(proxyProblem(cause)); }
    finally { active.current = false; if (alive.current) setBusy(''); }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (active.current || !eligibility?.editable || !proof || proof.value !== proxy || problem && problem.kind !== 'error') return;
    active.current = true; setBusy('save'); setProblem(null);
    const controller = new AbortController(); pending.current = controller;
    try {
      const result = await request<AuthJSONUpdateResult>(path, { method: 'POST', body: { proxy_url: proxy, revision: eligibility.revision, test_token: proof.test_token }, signal: controller.signal });
      if (!confirmedAuthJSONResult(result)) throw new APIError('The update result could not be confirmed.', 200, 'AUTH_JSON_UPDATE_UNCONFIRMED');
      if (alive.current) { setProxy(maskedProxyURL(proxy)); setSaved(result); setProof(null); }
    } catch (cause) { if (alive.current && !controller.signal.aborted) { setProblem(proxyProblem(cause, true)); setProof(null); } }
    finally { active.current = false; if (alive.current) setBusy(''); }
  }
  const terminal = !!problem && problem.kind !== 'error';
  const editable = !checking && !!eligibility?.editable && !terminal && !saved;
  const blocked = !checking && eligibility && !eligibility.editable;
  const problemTitle = problem?.kind === 'missing' ? 'Latest Auth JSON is not saved' : problem?.kind === 'conflict' ? 'Account credentials have changed' : problem?.kind === 'unknown' ? 'Unable to confirm the update result' : problem?.kind === 'blocked' ? 'Proxy updates are unavailable' : '';
  return <dialog ref={dialog} className='account-auth-dialog account-proxy-dialog' aria-labelledby={headingID} aria-describedby={descriptionID} onCancel={event => { event.preventDefault(); close(); }}>
    <div className='account-auth-dialog-heading'><div><h2 id={headingID}>{t('Update Proxy URL')}</h2><p id={descriptionID}>{t('Update this account’s proxy using its saved Auth JSON.')}</p></div><button type='button' className='account-auth-close' aria-label={t('Close dialog')} disabled={!!busy} onClick={close}><X aria-hidden='true' /></button></div>
    <form onSubmit={save} noValidate aria-busy={!!busy}>
      <div className='account-auth-dialog-body'>
        <div className='account-auth-identity'><span className='account-auth-identity-icon'><UserRound aria-hidden='true' /></span><div><strong>{account.account}<LockKeyhole className='account-auth-email-lock' aria-label={t('Account email is locked')} /></strong><span>{eligibility?.plan || account.plan} · {account.vendor_name}</span><span>{t('Account ID')} {account.platform_account_id || '—'}</span></div><span className={`usage-status ${statusTone(account.snapshot?.account_status)}`}>{statusLabel(account.snapshot?.account_status)}</span></div>
        {checking && <Loading label={t('Checking the latest account information…')} />}
        {blocked && <div className='account-auth-unavailable' role='status'><LockKeyhole aria-hidden='true' /><p>{translateMessage(eligibility.reason || 'The supplier does not allow Auth JSON or Proxy URL updates for this account in its current state.')}</p></div>}
        {editable && <>
          <div className='account-proxy-current'><span>{t('Current Proxy URL')}</span><code>{eligibility.proxy_url_masked || '—'}</code><p>{t('The proxy username and password are hidden.')}</p></div>
          <div className='account-proxy-row'><PasswordField label={t('New Proxy URL')} value={proxy} onChange={event => { setProxy(event.target.value); setChanged(!!proof || changed); setProof(null); setProblem(null); }} disabled={!!busy} autoComplete='off' spellCheck={false} maxLength={4096} aria-describedby={feedbackID} placeholder={t('Enter the complete proxy URL')} /><button type='button' className='button pale' disabled={!!busy || !proxy.trim()} onClick={() => void test()}>{busy === 'test' ? <LoaderCircle className='spin' aria-hidden='true' /> : <PlugZap aria-hidden='true' />}{busy === 'test' ? t('Testing…') : t('Test connection')}</button></div>
          <p className='field-help'>{t('Supports HTTP, HTTPS, SOCKS5, and SOCKS5H.')}</p>
          <div id={feedbackID} className={`account-proxy-feedback ${proof ? 'connected' : ''}`} role='status'>{proof ? <><Check aria-hidden='true' />{t('Connected · Exit IP')} {proof.proxy_check.ip}</> : t(changed ? 'The proxy URL changed. Test the connection again.' : 'Test the new proxy before saving.')}</div>
        </>}
        {problem && <div className={`notice account-proxy-notice ${problem.kind === 'unknown' || problem.kind === 'conflict' ? 'account-auth-uncertain' : problem.kind === 'blocked' ? 'account-proxy-blocked' : 'error'}`} role='alert'><CircleAlert aria-hidden='true' /><div>{problemTitle && <strong>{t(problemTitle)}</strong>}<p>{translateMessage(problem.message)}</p></div></div>}
        {editable && <p className='account-auth-private'><LockKeyhole aria-hidden='true' />{t('Only the proxy URL will change. Other credentials stay the same.')}</p>}
        {busy === 'save' && <p className='account-auth-progress' role='status'><LoaderCircle className='spin' aria-hidden='true' />{t('Submitting the new proxy URL to the supplier…')}</p>}
        {saved && <div className='account-auth-success' role='status'><span className='account-auth-success-icon'><Check aria-hidden='true' /></span><h3>{t('Proxy URL updated')}</h3><p>{t('The proxy URL was updated. Account status is updated by scheduled background checks.')}</p><dl><div className='account-proxy-result-url'><dt>Proxy URL</dt><dd><code>{proxy}</code></dd></div><div><dt>{t('Updated')}</dt><dd>{formatTime(saved.updated_at)}</dd></div><div><dt>{t('Last collected status')}</dt><dd>{statusLabel(account.snapshot?.account_status)}</dd></div></dl></div>}
      </div>
      <div className='account-auth-dialog-footer'>
        <button type='button' className={`button ${saved ? 'primary' : 'secondary'}`} disabled={!!busy} onClick={close}>{t(saved ? 'Back to account' : problem?.kind === 'unknown' || blocked || problem?.kind === 'blocked' ? 'Close' : 'Cancel')}</button>
        {problem?.kind === 'missing' && <button type='button' className='button primary' onClick={onAuthJSON}><FilePenLine aria-hidden='true' />{t('Update Auth JSON')}</button>}
        {problem?.kind === 'conflict' && <button type='button' className='button primary' onClick={() => setAttempt(v => v + 1)}><RefreshCw aria-hidden='true' />{t('Reopen dialog')}</button>}
        {(blocked || problem?.kind === 'blocked' || !checking && !eligibility && problem?.kind === 'error') && <button type='button' className='button pale' onClick={() => setAttempt(v => v + 1)}><RefreshCw aria-hidden='true' />{t('Check again')}</button>}
        {editable && <button type='submit' className='button primary' disabled={!!busy || !proof || proof.value !== proxy}>{busy === 'save' ? <><LoaderCircle className='spin' aria-hidden='true' />{t('Updating…')}</> : t('Update Proxy URL')}</button>}
      </div>
    </form>
  </dialog>;
}
