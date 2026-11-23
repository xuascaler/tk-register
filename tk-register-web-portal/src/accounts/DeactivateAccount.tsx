import { t, translateMessage, useLanguage } from '../i18n';
import { Check, Copy, Download, LockKeyhole, RefreshCw, UserRound, X } from 'lucide-react';
import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { formatTime, statusLabel, statusTone } from './helpers';
import type { SyncedAccount } from './types';
import AuthorizeAuthJSON from './AuthorizeAuthJSON';
import type { GeneratedAuthJSON } from './deactivate';
import './auth-json.css';

type Eligibility = { available: boolean; account: string; plan: string; reason?: string };

export default function DeactivateAccount({ account, endpoint, onClose, onGenerated }: {
  account: SyncedAccount; endpoint: string; onClose: () => void; onGenerated: () => void;
}) {
  useLanguage();
  const dialog = useRef<HTMLDialogElement>(null);
  const guided = useRef({ busy: false, dirty: false });
  const [guidedState, setGuidedState] = useState(guided.current);
  const [eligibility, setEligibility] = useState<Eligibility | null>(null);
  const [checking, setChecking] = useState(true);
  const [checkError, setCheckError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const [result, setResult] = useState<GeneratedAuthJSON | null>(null);
  const [exportMessage, setExportMessage] = useState('');
  const [showJSON, setShowJSON] = useState(false);
  const heading = useId(), description = useId();
  const path = `${endpoint}/${encodeURIComponent(account.id)}/deactivate`;
  const storageKey = `tk-register:deactivate-flow:${path}`;
  const stateChanged = useCallback((state: { busy: boolean; dirty: boolean }) => { guided.current = state; setGuidedState(state); }, []);
  const generated = useCallback((value: GeneratedAuthJSON) => {
    try { sessionStorage.removeItem(storageKey); } catch { /* Optional storage. */ }
    setResult(value); setExportMessage('');
  }, [storageKey]);

  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const element = dialog.current; element?.showModal();
    return () => { element?.close(); document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  useEffect(() => {
    const controller = new AbortController(); setChecking(true); setCheckError(''); setEligibility(null);
    request<Eligibility>(path, { signal: controller.signal }).then(value => {
      if (controller.signal.aborted) return;
      if (!value || typeof value.available !== 'boolean' || typeof value.account !== 'string' || value.account.trim().toLowerCase() !== account.account.trim().toLowerCase()) throw new Error('Could not confirm the account information. Please try again.');
      setEligibility(value);
    }).catch(cause => { if (!controller.signal.aborted) setCheckError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setChecking(false); });
    return () => controller.abort();
  }, [path, account.account, attempt]);
  useEffect(() => {
    if (result || !guidedState.dirty) return;
    const preventLeave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', preventLeave);
    return () => window.removeEventListener('beforeunload', preventLeave);
  }, [guidedState.dirty, result]);

  function close() {
    if (guided.current.busy) return;
    if (result) { onGenerated(); return; }
    if (guided.current.dirty && !window.confirm(t('Discard this authorization and Auth JSON, and close the editor?'))) return;
    try { sessionStorage.removeItem(storageKey); } catch { /* Optional storage. */ }
    onClose();
  }
  const json = result ? JSON.stringify(result.auth, null, 2) : '';
  async function copyJSON() {
    if (!result) return;
    try { await navigator.clipboard.writeText(json); setExportMessage('Auth JSON copied. Save it somewhere secure.'); }
    catch { setShowJSON(true); setExportMessage('Unable to copy. Select and copy the Auth JSON shown below.'); }
  }
  function downloadJSON() {
    if (!result) return;
    let url = '';
    try {
      url = URL.createObjectURL(new Blob([json], { type: 'application/json' }));
      const anchor = document.createElement('a'); anchor.href = url;
      anchor.download = `auth-${account.account.replace(/[^\w@.-]/g, '_').slice(0, 120)}.json`;
      anchor.click(); setExportMessage('Download requested. You can download the same JSON again without reauthorizing.');
    } catch { setExportMessage('Unable to download. Try again or use Copy JSON.'); }
    finally { if (url) window.setTimeout(() => URL.revokeObjectURL(url), 2000); }
  }
  const available = !!eligibility?.available && !checking && !checkError;
  return <dialog ref={dialog} className='account-auth-dialog account-deactivate-dialog' aria-labelledby={heading} aria-describedby={description} onCancel={event => { event.preventDefault(); close(); }}>
    <div className='account-auth-dialog-heading'><div><h2 id={heading}>{t("Deactivate account")}</h2><p id={description}>{t("Authorize this account to generate a new Auth JSON.")}</p></div><button type='button' className='account-auth-close' aria-label={t("Close dialog")} disabled={guidedState.busy} onClick={close}><X aria-hidden='true' /></button></div>
    <div className='account-auth-dialog-body account-auth-common'>
      <div className='account-auth-identity'><span className='account-auth-identity-icon'><UserRound aria-hidden='true' /></span><div><strong>{account.account}<LockKeyhole className='account-auth-email-lock' aria-label={t("Account email is locked")} /></strong><span>{eligibility?.plan || account.plan} · {account.vendor_name}</span><span>{t("Account ID")} {account.platform_account_id || '—'}</span></div><span className={`usage-status ${statusTone(account.snapshot?.account_status)}`}>{statusLabel(account.snapshot?.account_status)}</span></div>
      {checking ? <Loading label={t("Checking account information…")} /> : checkError ? <div className='account-auth-check'><ErrorNotice>{checkError}</ErrorNotice><button type='button' className='button pale' onClick={() => setAttempt(value => value + 1)}><RefreshCw aria-hidden='true' />{t("Try again")}</button></div> : !eligibility?.available ? <div className='account-auth-unavailable' role='status'><LockKeyhole aria-hidden='true' /><p>{translateMessage(eligibility?.reason || 'Auth JSON generation is unavailable for this account right now.')}</p><button type='button' className='usage-text-button' onClick={() => setAttempt(value => value + 1)}>{t("Check again")}</button></div> : null}
    </div>
    {result ? <><div className='account-auth-dialog-body account-auth-success account-deactivate-success'><span className='account-auth-success-icon'><Check aria-hidden='true' /></span><h3>{t("Auth JSON generated")}</h3><p>{t("Your account will go offline after a while.")}</p><dl><div><dt>{t("Generated")}</dt><dd>{formatTime(result.generated_at)}</dd></div><div><dt>{t("Last collected status")}</dt><dd>{statusLabel(account.snapshot?.account_status)}</dd></div></dl><div className='account-generated-actions'><button type='button' className='button primary' onClick={downloadJSON}><Download aria-hidden='true' />{t("Download JSON")}</button><button type='button' className='button secondary' onClick={() => void copyJSON()}><Copy aria-hidden='true' />{t("Copy JSON")}</button></div><details className='account-generated-json' open={showJSON} onToggle={event => setShowJSON(event.currentTarget.open)}><summary>{t("View Auth JSON")}</summary><pre><code>{json}</code></pre></details><p className='account-generated-message' role='status' aria-live='polite'>{translateMessage(exportMessage || 'Download and save your new credentials.')}</p></div><div className='account-auth-dialog-footer'><button type='button' className='button primary' onClick={close}>{t("Back to account")}</button></div></> : available ? <AuthorizeAuthJSON path={path} accountEmail={account.account} disabled={!available} purpose='deactivate' onCancel={close} onGenerated={generated} onState={stateChanged} /> : <div className='account-auth-dialog-footer'><button type='button' className='button secondary' onClick={close}>{t("Cancel")}</button></div>}
  </dialog>;
}
