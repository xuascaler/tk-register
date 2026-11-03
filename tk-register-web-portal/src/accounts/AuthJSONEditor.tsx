import { Check, FilePenLine, LoaderCircle, LockKeyhole, RefreshCw, Upload, UserRound, X } from 'lucide-react';
import { useEffect, useId, useRef, useState, type ChangeEvent, type FormEvent } from 'react';
import { APIError, errorMessage, request } from '../api';
import { MAX_AUTH_BYTES, parseAuthJSON } from '../submissions/validation';
import { ErrorNotice, Loading } from '../ui';
import { authJSONUpdateError, type AuthJSONEligibility, type AuthJSONUpdateResult } from './auth-json';
import { formatTime } from './helpers';
import type { SyncedAccount } from './types';
import './auth-json.css';

export function AuthenticationCard({ account, disabled, onEdit, timezone }: {
  account: SyncedAccount; disabled: boolean; onEdit: () => void; timezone: string;
}) {
  return <section className='account-authentication' aria-label='Authentication'>
    <div className='account-auth-copy'><h2>Authentication</h2><p>Update this account’s Auth JSON when its credentials change.</p>
      {account.auth_json_updated_at && <span className='account-auth-updated'>Last updated {formatTime(account.auth_json_updated_at, timezone)}</span>}
      {!account.current_submission_id && <span className='account-auth-updated'>Available after the account submission is accepted.</span>}
    </div>
    <button type='button' className='button pale' disabled={disabled || !account.current_submission_id} onClick={onEdit}><FilePenLine aria-hidden='true' />Update Auth JSON</button>
  </section>;
}

export function AuthJSONEditor({ account, endpoint, onClose, onSaved }: {
  account: SyncedAccount; endpoint: string; onClose: () => void; onSaved: (result: AuthJSONUpdateResult) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const pending = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const submitting = useRef(false);
  const readingFile = useRef(0);
  const [eligibility, setEligibility] = useState<AuthJSONEligibility | null>(null);
  const [checking, setChecking] = useState(true);
  const [checkError, setCheckError] = useState('');
  const [checkAttempt, setCheckAttempt] = useState(0);
  const [raw, setRaw] = useState('');
  const [fileName, setFileName] = useState('');
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState('');
  const [uncertain, setUncertain] = useState(false);
  const [validFormat, setValidFormat] = useState(false);
  const inputID = useId(), headingID = useId(), descriptionID = useId(), errorID = useId(), helpID = useId();
  const path = `${endpoint}/${encodeURIComponent(account.id)}/auth-json`;

  useEffect(() => {
    mounted.current = true;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const element = dialog.current;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    element?.showModal();
    return () => {
      mounted.current = false;
      readingFile.current += 1;
      pending.current?.abort();
      if (textarea.current) textarea.current.value = '';
      if (fileInput.current) fileInput.current.value = '';
      element?.close();
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setChecking(true); setCheckError(''); setEligibility(null);
    request<AuthJSONEligibility>(path, { signal: controller.signal })
      .then(result => {
        if (controller.signal.aborted || !mounted.current) return;
        if (!result || typeof result.editable !== 'boolean') throw new Error('Could not check whether this account can be updated. Please try again.');
        setEligibility(result);
      })
      .catch(cause => { if (!controller.signal.aborted && mounted.current) setCheckError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted && mounted.current) setChecking(false); });
    return () => controller.abort();
  }, [path, checkAttempt]);

  useEffect(() => { if (!checking && eligibility?.editable) textarea.current?.focus(); }, [checking, eligibility]);
  useEffect(() => {
    if (!raw && !busy) return;
    const preventLeave = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', preventLeave);
    return () => window.removeEventListener('beforeunload', preventLeave);
  }, [raw, busy]);

  function close() {
    if (submitting.current) return;
    if (raw.trim() && !window.confirm('Discard this Auth JSON and close the editor?')) return;
    setRaw(''); setFileName(''); setError('');
    onClose();
  }
  function checkFormat(value: string) {
    if (!value.trim()) { setValidFormat(false); return; }
    try { parseAuthJSON(value); setValidFormat(true); setError(''); }
    catch (cause) { setValidFormat(false); setError(errorMessage(cause)); }
  }
  async function upload(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file || submitting.current) return;
    setError(''); setValidFormat(false);
    if (file.size > MAX_AUTH_BYTES) { setError('Auth JSON must be 2 MB or smaller.'); return; }
    const readID = ++readingFile.current;
    setUploading(true);
    try {
      const text = await file.text();
      if (!mounted.current || readingFile.current !== readID) return;
      setRaw(text); setFileName(file.name); setUncertain(false); checkFormat(text);
      textarea.current?.focus();
    } catch { if (mounted.current && readingFile.current === readID) setError('This file could not be read. Choose another JSON file.'); }
    finally { if (mounted.current && readingFile.current === readID) setUploading(false); }
  }
  async function save(event: FormEvent) {
    event.preventDefault();
    if (submitting.current || checking || uploading || !eligibility?.editable) return;
    let auth: Record<string, unknown>;
    try { auth = parseAuthJSON(raw); }
    catch (cause) { setError(errorMessage(cause)); setUncertain(false); textarea.current?.focus(); return; }
    const controller = new AbortController();
    pending.current = controller;
    submitting.current = true; setBusy(true); setError(''); setUncertain(false);
    try {
      const result = await request<AuthJSONUpdateResult>(path, { method: 'POST', body: { auth_json: auth }, signal: controller.signal });
      if (!result || result.updated !== true || !('account' in result) || (result.account !== null && typeof result.account !== 'object') || !['pending', 'succeeded', 'failed'].includes(result.refresh_status)) {
        throw new APIError('The update result could not be confirmed.', 200, 'AUTH_JSON_UPDATE_UNCONFIRMED');
      }
      if (!controller.signal.aborted && mounted.current) {
        setRaw(''); setFileName('');
        onSaved(result);
      }
    } catch (cause) {
      if (!controller.signal.aborted && mounted.current) {
        const failure = authJSONUpdateError(cause);
        setError(failure.message); setUncertain(failure.uncertain);
        if (cause instanceof APIError && cause.code === 'AUTH_JSON_NOT_EDITABLE') {
          setEligibility(current => current && { ...current, editable: false, reason: failure.message });
        }
      }
    } finally {
      submitting.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  const editable = !!eligibility?.editable && !checking && !checkError;
  return <dialog ref={dialog} className='account-auth-dialog' aria-labelledby={headingID} aria-describedby={descriptionID} onCancel={event => { event.preventDefault(); close(); }}>
    <div className='account-auth-dialog-heading'><div><h2 id={headingID}>Update Auth JSON</h2><p id={descriptionID}>Replace the credentials for this account.</p></div><button type='button' className='account-auth-close' aria-label='Close dialog' disabled={busy} onClick={close}><X aria-hidden='true' /></button></div>
    <form onSubmit={save} noValidate aria-busy={busy}>
      <div className='account-auth-dialog-body'>
        <div className='account-auth-identity'><span className='account-auth-identity-icon'><UserRound aria-hidden='true' /></span><div><strong>{account.account}</strong><span>{eligibility?.plan || account.plan} · {account.vendor_name}</span></div></div>
        {checking ? <Loading label='Checking the latest account information…' /> : checkError ? <div className='account-auth-check'><ErrorNotice>{checkError}</ErrorNotice><button type='button' className='button pale' onClick={() => setCheckAttempt(v => v + 1)}><RefreshCw aria-hidden='true' />Try again</button></div> : !eligibility?.editable ? <div className='account-auth-unavailable' role='status'><LockKeyhole aria-hidden='true' /><p>{eligibility?.reason || 'The vendor does not allow Auth JSON updates for this account right now.'}</p><button type='button' className='usage-text-button' onClick={() => setCheckAttempt(v => v + 1)}>Check again</button></div> : null}
        <div className='account-auth-field-heading'><label htmlFor={inputID}>New Auth JSON <span aria-hidden='true'>*</span></label><button type='button' className='button secondary account-auth-upload' disabled={!editable || busy || uploading} onClick={() => fileInput.current?.click()}><Upload aria-hidden='true' />{uploading ? 'Reading…' : 'Upload JSON'}</button><input ref={fileInput} type='file' accept='.json,application/json' hidden onChange={upload} /></div>
        <textarea ref={textarea} id={inputID} value={raw} disabled={!editable || busy || uploading} spellCheck={false} autoComplete='off' autoCapitalize='none' placeholder='Paste the complete authentication JSON here…' aria-invalid={!!error && !uncertain} aria-describedby={`${helpID} ${errorID}`} onChange={event => { setRaw(event.target.value); setFileName(''); setValidFormat(false); if (!uncertain) setError(''); }} onBlur={() => { if (!busy && !uncertain) checkFormat(raw); }} />
        <div className='account-auth-field-help' id={helpID}><span>{fileName || 'Paste a JSON object or upload a .json file, up to 2 MB.'}</span>{validFormat && <span className='account-auth-valid'><Check aria-hidden='true' />Valid format</span>}</div>
        <div id={errorID}>{error && <div className={`notice ${uncertain ? 'account-auth-uncertain' : 'error'}`} role='alert'>{error}</div>}</div>
        <p className='account-auth-private'><LockKeyhole aria-hidden='true' />Existing credentials are never shown. Your password and other account details stay unchanged.</p>
        {busy && <p className='account-auth-progress' role='status'><LoaderCircle className='spin' aria-hidden='true' />Updating credentials…</p>}
      </div>
      <div className='account-auth-dialog-footer'><button type='button' className='button secondary' disabled={busy} onClick={close}>Cancel</button><button type='submit' className='button primary' disabled={busy || uploading || !editable || !raw.trim()}>{busy ? <><LoaderCircle className='spin' aria-hidden='true' />Updating…</> : 'Update Auth JSON'}</button></div>
    </form>
  </dialog>;
}
